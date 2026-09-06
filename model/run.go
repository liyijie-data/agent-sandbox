package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

type Run struct {
	ID                     string     `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	ClientID               string     `gorm:"column:client_id;type:uuid;not null;uniqueIndex:runs_client_req_uq;index:runs_client_status_idx"`
	ImageRegistrationID    *string    `gorm:"column:image_registration_id;type:uuid"`
	NetworkRevisionID      *string    `gorm:"column:network_revision_id;type:uuid"`
	ReqID                  string     `gorm:"column:req_id;type:text;not null;uniqueIndex:runs_client_req_uq"`
	ReqFingerprint         []byte     `gorm:"column:req_fingerprint;type:bytea;not null"`
	Status                 string     `gorm:"column:status;type:text;not null;default:queued;index:runs_client_status_idx"`
	CurrentStage           int        `gorm:"column:current_stage;type:int;not null;default:1"`
	TotalLifecycleDeadline time.Time  `gorm:"column:total_lifecycle_deadline;type:timestamptz;not null"`
	CumulativeExecSeconds  int64      `gorm:"column:cumulative_exec_seconds;type:bigint;not null;default:0"`
	ModelRequestCount      int64      `gorm:"column:model_request_count;type:bigint;not null;default:0"`
	NextSteerSeq           int64      `gorm:"column:next_steer_seq;type:bigint;not null;default:1"`
	IncorporatedThrough    int64      `gorm:"column:incorporated_through_seq;type:bigint;not null;default:0"`
	SteerCount             int        `gorm:"column:steer_count;type:int;not null;default:0"`
	SteerContentBytes      int64      `gorm:"column:steer_content_bytes;type:bigint;not null;default:0"`
	TerminalAt             *time.Time `gorm:"column:terminal_at;type:timestamptz"`
	MetadataExpiresAt      *time.Time `gorm:"column:metadata_expires_at;type:timestamptz"`
	CleanupStatus          string     `gorm:"column:cleanup_status;type:text;not null;default:not_due"`
	CreatedAt              time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt              time.Time  `gorm:"column:updated_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
}

func (Run) TableName() string { return "runs" }

type RunDAO struct{ db *gorm.DB }

func (d RunDAO) Create(c context.Context, r *Run) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d RunDAO) Get(c context.Context, id string) (*Run, error) {
	var r Run
	e := d.db.WithContext(c).Where("id = ?", id).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d RunDAO) GetByClientAndRequest(c context.Context, clientID, reqID string) (*Run, error) {
	var r Run
	e := d.db.WithContext(c).Where("client_id = ? AND req_id = ?", clientID, reqID).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d RunDAO) List(c context.Context, clientID string, limit, offset int) ([]Run, error) {
	var r []Run
	e := d.db.WithContext(c).Where("client_id = ?", clientID).Order("created_at DESC").Limit(limit).Offset(offset).Find(&r).Error
	return r, daoError(e)
}

func (d RunDAO) ListQueuedByClient(c context.Context, clientID string) ([]Run, error) {
	var r []Run
	e := d.db.WithContext(c).Where("client_id = ? AND status = ?", clientID, "queued").
		Order("created_at ASC, id ASC").Find(&r).Error
	return r, daoError(e)
}

func (d RunDAO) CountActiveExecutions(c context.Context, clientID string, now time.Time) (int, error) {
	var n int64
	e := d.db.WithContext(c).
		Table("runs r").
		Joins("JOIN run_stages s ON s.run_id = r.id AND s.stage_no = r.current_stage").
		Where("r.client_id = ?", clientID).
		Where("r.status IN ?", []string{"preparing", "running", "awaiting_input", "cancel_requested"}).
		Where("s.owner <> '' AND s.lease_expires_at IS NOT NULL AND s.lease_expires_at > ?", now).
		Count(&n).Error
	return int(n), daoError(e)
}

func (d RunDAO) ListNonTerminalByImage(c context.Context, imageRegID string, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var ids []string
	e := d.db.WithContext(c).Model(&Run{}).
		Where("image_registration_id = ?", imageRegID).
		Where("status NOT IN ?", []string{"succeeded", "failed", "cancelled", "expired"}).
		Order("created_at").
		Limit(limit).
		Pluck("id", &ids).Error
	return ids, daoError(e)
}

func (d RunDAO) ChargeModelRequest(c context.Context, runID string, now time.Time) (count int64, ok bool, err error) {
	x := d.db.WithContext(c).
		Model(&Run{}).
		Where("id = ?", runID).
		Updates(map[string]any{
			"model_request_count": gorm.Expr("model_request_count + 1"),
			"updated_at":          now,
		})
	if x.Error != nil {
		return 0, false, daoError(x.Error)
	}
	if x.RowsAffected != 1 {
		return 0, false, nil
	}
	r, err := d.Get(c, runID)
	if err != nil {
		return 0, false, err
	}
	return r.ModelRequestCount, true, nil
}

type RunPatch struct {
	Status                Optional[string]
	CurrentStage          Optional[int]
	CumulativeExecSeconds Optional[int64]
	ModelRequestCount     Optional[int64]
	NextSteerSeq          Optional[int64]
	IncorporatedThrough   Optional[int64]
	SteerCount            Optional[int]
	SteerContentBytes     Optional[int64]

	ImageRegistrationID Optional[*string]
	TerminalAt          Optional[*time.Time]
	MetadataExpiresAt   Optional[*time.Time]
	CleanupStatus       Optional[string]
	UpdatedAt           Optional[time.Time]
}
type RunCondition struct {
	Status   *string
	ClientID *string

	StatusIn []string

	CurrentStage *int
}

func (d RunDAO) Update(c context.Context, id string, v RunPatch) (bool, error) {
	x := d.db.WithContext(c).Model(&Run{}).Where("id = ?", id).Updates(runPatchValues(v))
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d RunDAO) UpdateIf(c context.Context, id string, where RunCondition, v RunPatch) (bool, error) {
	q := d.db.WithContext(c).Model(&Run{}).Where("id = ?", id)
	if where.Status != nil {
		q = q.Where("status = ?", *where.Status)
	}
	if where.ClientID != nil {
		q = q.Where("client_id = ?", *where.ClientID)
	}
	if len(where.StatusIn) > 0 {
		q = q.Where("status IN ?", where.StatusIn)
	}
	if where.CurrentStage != nil {
		q = q.Where("current_stage = ?", *where.CurrentStage)
	}
	x := q.Updates(runPatchValues(v))
	return x.RowsAffected == 1, daoError(x.Error)
}

func runPatchValues(p RunPatch) map[string]any {
	v := map[string]any{}
	if p.Status.Set {
		v["status"] = p.Status.Value
	}
	if p.CurrentStage.Set {
		v["current_stage"] = p.CurrentStage.Value
	}
	if p.CumulativeExecSeconds.Set {
		v["cumulative_exec_seconds"] = p.CumulativeExecSeconds.Value
	}
	if p.ModelRequestCount.Set {
		v["model_request_count"] = p.ModelRequestCount.Value
	}
	if p.ImageRegistrationID.Set {
		v["image_registration_id"] = p.ImageRegistrationID.Value
	}
	if p.NextSteerSeq.Set {
		v["next_steer_seq"] = p.NextSteerSeq.Value
	}
	if p.IncorporatedThrough.Set {
		v["incorporated_through_seq"] = p.IncorporatedThrough.Value
	}
	if p.SteerCount.Set {
		v["steer_count"] = p.SteerCount.Value
	}
	if p.SteerContentBytes.Set {
		v["steer_content_bytes"] = p.SteerContentBytes.Value
	}
	if p.TerminalAt.Set {
		v["terminal_at"] = p.TerminalAt.Value
	}
	if p.MetadataExpiresAt.Set {
		v["metadata_expires_at"] = p.MetadataExpiresAt.Value
	}
	if p.CleanupStatus.Set {
		v["cleanup_status"] = p.CleanupStatus.Value
	}
	if p.UpdatedAt.Set {
		v["updated_at"] = p.UpdatedAt.Value
	}
	return v
}
func (d RunDAO) Delete(c context.Context, id string) (bool, error) {
	x := d.db.WithContext(c).Delete(&Run{}, "id = ?", id)
	return x.RowsAffected == 1, daoError(x.Error)
}

type RunDetailRead struct {
	Run   Run
	Stage *RunStage
	Input *RunInput
}

func (d RunDAO) ReadDetail(c context.Context, id string) (*RunDetailRead, error) {
	r, e := d.Get(c, id)
	if e != nil {
		return nil, daoError(e)
	}
	var st RunStage
	se := d.db.WithContext(c).Where("run_id = ? AND stage_no = ?", id, r.CurrentStage).First(&st).Error
	if errorsIsNotFound(se) {
		return &RunDetailRead{Run: *r}, nil
	}
	if se != nil {
		return nil, se
	}
	var in RunInput
	ie := d.db.WithContext(c).Where("run_id = ? AND state = ?", id, "pending").Order("created_at DESC").First(&in).Error
	if errorsIsNotFound(ie) {
		return &RunDetailRead{Run: *r, Stage: &st}, nil
	}
	if ie != nil {
		return nil, ie
	}
	return &RunDetailRead{Run: *r, Stage: &st, Input: &in}, nil
}
