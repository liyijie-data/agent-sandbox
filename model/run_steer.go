package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

type RunSteer struct {
	ID                string     `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	ClientID          string     `gorm:"column:client_id;type:uuid;not null;uniqueIndex:run_steers_client_run_steer_uq"`
	RunID             string     `gorm:"column:run_id;type:uuid;not null;uniqueIndex:run_steers_client_run_steer_uq;uniqueIndex:run_steers_run_seq_uq;index:run_steers_pending_idx"`
	SteerID           string     `gorm:"column:steer_id;type:text;not null;uniqueIndex:run_steers_client_run_steer_uq"`
	Seq               int64      `gorm:"column:seq;type:bigint;not null;uniqueIndex:run_steers_run_seq_uq;index:run_steers_pending_idx"`
	ContentID         *string    `gorm:"column:content_id;type:uuid"`
	ContentHash       []byte     `gorm:"column:content_hash;type:bytea"`
	Status            string     `gorm:"column:status;type:text;not null;default:pending;index:run_steers_pending_idx"`
	ReasonCode        *string    `gorm:"column:reason_code;type:text"`
	AcceptedAt        time.Time  `gorm:"column:accepted_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
	IncorporatedAt    *time.Time `gorm:"column:incorporated_at;type:timestamptz"`
	IncorporatedStage *int       `gorm:"column:incorporated_stage;type:int"`
}

func (RunSteer) TableName() string { return "run_steers" }

type RunSteerDAO struct{ db *gorm.DB }

func (d RunSteerDAO) Create(c context.Context, r *RunSteer) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d RunSteerDAO) Get(c context.Context, clientID, runID, steerID string) (*RunSteer, error) {
	var r RunSteer
	e := d.db.WithContext(c).Where("client_id = ? AND run_id = ? AND steer_id = ?", clientID, runID, steerID).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d RunSteerDAO) ListByRun(c context.Context, runID string, limit, offset int) ([]RunSteer, error) {
	var r []RunSteer
	e := d.db.WithContext(c).Where("run_id = ?", runID).Order("seq").Limit(limit).Offset(offset).Find(&r).Error
	return r, daoError(e)
}

type RunSteerPatch struct {
	Status            *string
	ReasonCode        *string
	IncorporatedAt    *time.Time
	IncorporatedStage *int
}
type RunSteerCondition struct{ Status *string }

func (d RunSteerDAO) UpdateIf(c context.Context, id string, w RunSteerCondition, v RunSteerPatch) (bool, error) {
	q := d.db.WithContext(c).Model(&RunSteer{}).Where("id = ?", id)
	if w.Status != nil {
		q = q.Where("status = ?", *w.Status)
	}
	x := q.Updates(v)
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d RunSteerDAO) Delete(c context.Context, id string) (bool, error) {
	x := d.db.WithContext(c).Delete(&RunSteer{}, "id = ?", id)
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RunSteerDAO) ListAfter(c context.Context, runID string, afterSeq int64, status string, limit int) ([]RunSteer, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var r []RunSteer
	e := d.db.WithContext(c).Where("run_id = ? AND seq > ? AND status = ?", runID, afterSeq, status).
		Order("seq").Limit(limit).Find(&r).Error
	return r, daoError(e)
}

func (d RunSteerDAO) MarkIncorporatedUpTo(c context.Context, runID string, throughSeq int64, stageNo int, pendingStatus, incorporatedStatus string, now time.Time) (int64, error) {
	x := d.db.WithContext(c).
		Model(&RunSteer{}).
		Where("run_id = ? AND status = ? AND seq <= ?", runID, pendingStatus, throughSeq).
		Updates(map[string]any{
			"status":             incorporatedStatus,
			"incorporated_at":    now,
			"incorporated_stage": stageNo,
		})
	return x.RowsAffected, daoError(x.Error)
}

func (d RunSteerDAO) CloseUpTo(c context.Context, runID, pendingStatus string, throughSeq int64, closedStatus, reasonCode string) (int64, error) {
	x := d.db.WithContext(c).
		Model(&RunSteer{}).
		Where("run_id = ? AND status = ? AND seq <= ?", runID, pendingStatus, throughSeq).
		Updates(map[string]any{"status": closedStatus, "reason_code": reasonCode})
	return x.RowsAffected, daoError(x.Error)
}

func (d RunSteerDAO) CloseAll(c context.Context, runID, pendingStatus, closedStatus, reasonCode string) (int64, error) {
	x := d.db.WithContext(c).
		Model(&RunSteer{}).
		Where("run_id = ? AND status = ?", runID, pendingStatus).
		Updates(map[string]any{"status": closedStatus, "reason_code": reasonCode})
	return x.RowsAffected, daoError(x.Error)
}

type SteerBatch struct {
	ID          string     `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	RunID       string     `gorm:"column:run_id;type:uuid;not null"`
	BatchID     string     `gorm:"column:batch_id;type:uuid;not null;uniqueIndex:steer_batches_batch_id_uq"`
	ExecutionID string     `gorm:"column:execution_id;type:text;not null"`
	StageNo     int        `gorm:"column:stage_no;type:int;not null"`
	Fence       int64      `gorm:"column:fence;type:bigint;not null"`
	StartSeq    int64      `gorm:"column:start_seq;type:bigint;not null"`
	ThroughSeq  int64      `gorm:"column:through_seq;type:bigint;not null"`
	IssuedAt    time.Time  `gorm:"column:issued_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
	AckedAt     *time.Time `gorm:"column:acked_at;type:timestamptz"`
	Status      string     `gorm:"column:status;type:text;not null;default:issued"`
}

func (SteerBatch) TableName() string { return "steer_batches" }

type SteerBatchDAO struct{ db *gorm.DB }

func (d SteerBatchDAO) Create(c context.Context, r *SteerBatch) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d SteerBatchDAO) Get(c context.Context, id string) (*SteerBatch, error) {
	var r SteerBatch
	e := d.db.WithContext(c).Where("batch_id = ?", id).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d SteerBatchDAO) ListByRun(c context.Context, id string, limit, offset int) ([]SteerBatch, error) {
	var r []SteerBatch
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	e := d.db.WithContext(c).Where("run_id = ?", id).Order("issued_at").Limit(limit).Offset(offset).Find(&r).Error
	return r, daoError(e)
}

type SteerBatchPatch struct {
	AckedAt *time.Time
	Status  *string
}
type SteerBatchCondition struct {
	Status      *string
	ExecutionID *string
	Fence       *int64
}

func (d SteerBatchDAO) UpdateIf(c context.Context, id string, w SteerBatchCondition, v SteerBatchPatch) (bool, error) {
	q := d.db.WithContext(c).Model(&SteerBatch{}).Where("batch_id = ?", id)
	if w.Status != nil {
		q = q.Where("status = ?", *w.Status)
	}
	if w.ExecutionID != nil {
		q = q.Where("execution_id = ?", *w.ExecutionID)
	}
	if w.Fence != nil {
		q = q.Where("fence = ?", *w.Fence)
	}
	x := q.Updates(v)
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d SteerBatchDAO) Delete(c context.Context, id string) (bool, error) {
	x := d.db.WithContext(c).Delete(&SteerBatch{}, "batch_id = ?", id)
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d SteerBatchDAO) GetIssuedByExecution(c context.Context, runID, executionID string, stageNo int, fence int64, status string) (*SteerBatch, error) {
	var r SteerBatch
	e := d.db.WithContext(c).Where("run_id = ? AND execution_id = ? AND stage_no = ? AND fence = ? AND status = ?",
		runID, executionID, stageNo, fence, status).Order("start_seq").First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}

func (d SteerBatchDAO) MaxIssuedThroughSeq(c context.Context, runID, status string) (int64, error) {
	var n int64
	e := d.db.WithContext(c).Model(&SteerBatch{}).
		Where("run_id = ? AND status = ?", runID, status).
		Select("COALESCE(MAX(through_seq), 0)").Scan(&n).Error
	return n, daoError(e)
}

func (d SteerBatchDAO) CountByRunAndStatus(c context.Context, runID, status string) (int64, error) {
	var n int64
	e := d.db.WithContext(c).Model(&SteerBatch{}).
		Where("run_id = ? AND status = ?", runID, status).
		Count(&n).Error
	return n, daoError(e)
}
