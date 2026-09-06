package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

type RunStage struct {
	ID                 string     `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	RunID              string     `gorm:"column:run_id;type:uuid;not null;uniqueIndex:run_stages_run_stage_uq;uniqueIndex:run_stages_run_fence_idx"`
	RuntimeProfileID   *string    `gorm:"column:runtime_profile_id;type:uuid"`
	StageNo            int        `gorm:"column:stage_no;type:int;not null;uniqueIndex:run_stages_run_stage_uq"`
	Fence              int64      `gorm:"column:fence;type:bigint;not null;uniqueIndex:run_stages_run_fence_idx"`
	Owner              string     `gorm:"column:owner;type:text;not null;default:''"`
	LeaseExpiresAt     *time.Time `gorm:"column:lease_expires_at;type:timestamptz"`
	Phase              string     `gorm:"column:phase;type:text;not null;default:preparing"`
	Attempt            int        `gorm:"column:attempt;type:int;not null;default:0"`
	LaunchStarted      bool       `gorm:"column:launch_started;not null;default:false"`
	ExecutionID        string     `gorm:"column:execution_id;type:text;not null;default:''"`
	StartedAt          *time.Time `gorm:"column:started_at;type:timestamptz"`
	BudgetRemainingSec int64      `gorm:"column:budget_remaining_secs;type:bigint;not null;default:0"`
	PodRef             string     `gorm:"column:pod_ref;type:text;not null;default:''"`
	CreatedAt          time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
}

func (RunStage) TableName() string { return "run_stages" }

type RunStageDAO struct{ db *gorm.DB }

func (d RunStageDAO) Create(c context.Context, r *RunStage) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d RunStageDAO) Get(c context.Context, runID string, no int) (*RunStage, error) {
	var r RunStage
	e := d.db.WithContext(c).Where("run_id = ? AND stage_no = ?", runID, no).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d RunStageDAO) ListByRun(c context.Context, id string, limit, offset int) ([]RunStage, error) {
	var r []RunStage
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	e := d.db.WithContext(c).Where("run_id = ?", id).Order("stage_no").Limit(limit).Offset(offset).Find(&r).Error
	return r, daoError(e)
}

type RunStagePatch struct {
	Fence              *int64
	Owner              *string
	LeaseExpiresAt     *time.Time
	Phase              *string
	Attempt            *int
	LaunchStarted      *bool
	ExecutionID        *string
	StartedAt          *time.Time
	BudgetRemainingSec *int64
	PodRef             *string

	RuntimeProfileID *string
	UpdatedAt        *time.Time
}
type RunStageCondition struct {
	Fence         *int64
	Owner         *string
	ExecutionID   *string
	LaunchStarted *bool
}

func (d RunStageDAO) UpdateIf(c context.Context, runID string, no int, where RunStageCondition, v RunStagePatch) (bool, error) {
	q := d.db.WithContext(c).Model(&RunStage{}).Where("run_id = ? AND stage_no = ?", runID, no)
	if where.Fence != nil {
		q = q.Where("fence = ?", *where.Fence)
	}
	if where.Owner != nil {
		q = q.Where("owner = ?", *where.Owner)
	}
	if where.ExecutionID != nil {
		q = q.Where("execution_id = ?", *where.ExecutionID)
	}
	if where.LaunchStarted != nil {
		q = q.Where("launch_started = ?", *where.LaunchStarted)
	}
	x := q.Updates(v)
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d RunStageDAO) Delete(c context.Context, runID string, no int) (bool, error) {
	x := d.db.WithContext(c).Delete(&RunStage{}, "run_id = ? AND stage_no = ?", runID, no)
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RunStageDAO) ResetAfterRebase(ctx context.Context, runID string, no int) (bool, error) {
	x := d.db.WithContext(ctx).Model(&RunStage{}).Where("run_id = ? AND stage_no = ?", runID, no).
		Updates(map[string]any{
			"launch_started":     false,
			"owner":              "",
			"lease_expires_at":   nil,
			"execution_id":       "",
			"runtime_profile_id": nil,
			"attempt":            0,
			"updated_at":         time.Now(),
		})
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RunStageDAO) CountRuntimeProfileRefs(c context.Context, profileID string) (int, error) {
	var n int64
	e := d.db.WithContext(c).Model(&RunStage{}).Where("runtime_profile_id = ?", profileID).Count(&n).Error
	return int(n), daoError(e)
}
