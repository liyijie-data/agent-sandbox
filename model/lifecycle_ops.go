package model

import (
	"context"
	"time"

	"gorm.io/gorm"
)

func (d RunDAO) AddExecSeconds(ctx context.Context, runID string, secs int64, now time.Time) error {
	if secs <= 0 {
		return nil
	}
	x := d.db.WithContext(ctx).
		Model(&Run{}).
		Where("id = ?", runID).
		Updates(map[string]any{
			"cumulative_exec_seconds": gorm.Expr("cumulative_exec_seconds + ?", secs),
			"updated_at":              now,
		})
	return daoError(x.Error)
}

func (d RunDAO) TakeSteerSlot(ctx context.Context, runID string, contentLen int64, allowedStatuses []string, maxCount int, maxBytes int64, now time.Time) (int64, bool, error) {
	x := d.db.WithContext(ctx).
		Model(&Run{}).
		Where("id = ?", runID).
		Where("status IN ?", allowedStatuses).
		Where("steer_count < ?", maxCount).
		Where("steer_content_bytes + ? <= ?", contentLen, maxBytes).
		Where("total_lifecycle_deadline > ?", now).
		Updates(map[string]any{
			"next_steer_seq":      gorm.Expr("next_steer_seq + 1"),
			"steer_count":         gorm.Expr("steer_count + 1"),
			"steer_content_bytes": gorm.Expr("steer_content_bytes + ?", contentLen),
			"updated_at":          now,
		})
	if x.Error != nil {
		return 0, false, daoError(x.Error)
	}
	if x.RowsAffected != 1 {
		return 0, false, nil
	}

	r, err := d.Get(ctx, runID)
	if err != nil {
		return 0, false, err
	}
	return r.NextSteerSeq - 1, true, nil
}

func (d RunContentDAO) ExpireByRun(ctx context.Context, runID string, exp time.Time) error {
	x := d.db.WithContext(ctx).
		Model(&RunContent{}).
		Where("run_id = ?", runID).
		Where("expires_at IS NULL OR expires_at > ?", exp).
		Update("expires_at", exp)
	return daoError(x.Error)
}

func (d RunContentDAO) DeleteExpiredByRun(ctx context.Context, runID string, now time.Time) (int64, error) {
	x := d.db.WithContext(ctx).
		Where("run_id = ?", runID).
		Where("expires_at IS NOT NULL AND expires_at <= ?", now).
		Delete(&RunContent{})
	return x.RowsAffected, daoError(x.Error)
}

func (d RunContentDAO) CountExpiredByRun(ctx context.Context, runID string, now time.Time) (int64, error) {
	var n int64
	e := d.db.WithContext(ctx).Model(&RunContent{}).
		Where("run_id = ?", runID).
		Where("expires_at IS NOT NULL AND expires_at <= ?", now).
		Count(&n).Error
	return n, daoError(e)
}

func (d RunContentDAO) CountLiveByRun(ctx context.Context, runID string, now time.Time) (int64, error) {
	var n int64
	e := d.db.WithContext(ctx).Model(&RunContent{}).
		Where("run_id = ?", runID).
		Where("expires_at IS NULL OR expires_at > ?", now).
		Count(&n).Error
	return n, daoError(e)
}

func (d RunStageDAO) Claim(ctx context.Context, runID string, stageNo int, owner, executionID string, now, leaseUntil time.Time) (*RunStage, bool, error) {
	x := d.db.WithContext(ctx).
		Model(&RunStage{}).
		Where("run_id = ? AND stage_no = ?", runID, stageNo).
		Where("launch_started = ?", false).
		Where("owner = ? OR lease_expires_at IS NULL OR lease_expires_at <= ?", "", now).
		Updates(map[string]any{
			"fence":            gorm.Expr("fence + 1"),
			"owner":            owner,
			"lease_expires_at": leaseUntil,
			"phase":            "preparing",
			"attempt":          gorm.Expr("attempt + 1"),
			"execution_id":     executionID,
			"started_at":       now,
			"updated_at":       now,
		})
	if x.Error != nil {
		return nil, false, daoError(x.Error)
	}
	if x.RowsAffected != 1 {
		return nil, false, nil
	}
	st, err := d.Get(ctx, runID, stageNo)
	if err != nil {
		return nil, false, err
	}
	return st, true, nil
}

func (d RunStageDAO) Heartbeat(ctx context.Context, runID string, stageNo int, fence int64, owner string, now, leaseUntil time.Time) (bool, error) {
	x := d.db.WithContext(ctx).
		Model(&RunStage{}).
		Where("run_id = ? AND stage_no = ? AND fence = ? AND owner = ?", runID, stageNo, fence, owner).
		Where("lease_expires_at IS NULL OR lease_expires_at > ?", now).
		Updates(map[string]any{"lease_expires_at": leaseUntil, "updated_at": now})
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RunStageDAO) MarkLaunch(ctx context.Context, runID string, stageNo int, fence int64, owner string, now time.Time) (bool, error) {
	x := d.db.WithContext(ctx).
		Model(&RunStage{}).
		Where("run_id = ? AND stage_no = ? AND fence = ? AND owner = ?", runID, stageNo, fence, owner).
		Where("launch_started = ?", false).
		Where("lease_expires_at IS NULL OR lease_expires_at > ?", now).
		Updates(map[string]any{"launch_started": true, "updated_at": now})
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RunStageDAO) NextFence(ctx context.Context, runID string) (int64, error) {
	var n int64
	e := d.db.WithContext(ctx).
		Model(&RunStage{}).
		Where("run_id = ?", runID).
		Select("COALESCE(MAX(fence), 0) + 1").
		Scan(&n).Error
	return n, daoError(e)
}

func (d RunDAO) ListSchedulableRuns(ctx context.Context, now time.Time, maxAttempts int, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var ids []string
	e := d.db.WithContext(ctx).
		Table("runs r").
		Joins("JOIN run_stages s ON s.run_id = r.id AND s.stage_no = r.current_stage").
		Where("r.status IN ?", []string{"queued", "preparing"}).
		Where("r.total_lifecycle_deadline > ?", now).
		Where("s.launch_started = ?", false).
		Where("s.owner = ? OR s.lease_expires_at IS NULL OR s.lease_expires_at <= ?", "", now).
		Where("s.attempt < ?", maxAttempts).
		Where("s.budget_remaining_secs > 0").
		Order("r.created_at").
		Limit(limit).
		Pluck("r.id", &ids).Error
	return ids, daoError(e)
}

func (d RunDAO) ListStalledRuns(ctx context.Context, maxAttempts int, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var ids []string
	e := d.db.WithContext(ctx).
		Table("runs r").
		Joins("JOIN run_stages s ON s.run_id = r.id AND s.stage_no = r.current_stage").
		Where("r.status IN ?", []string{"queued", "preparing"}).
		Where("s.launch_started = ?", false).
		Where("s.attempt >= ? OR s.budget_remaining_secs <= 0", maxAttempts).
		Order("r.created_at").
		Limit(limit).
		Pluck("r.id", &ids).Error
	return ids, daoError(e)
}

func (d RunDAO) ListDueRuns(ctx context.Context, now time.Time, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var ids []string
	e := d.db.WithContext(ctx).
		Table("runs r").
		Joins("JOIN run_stages s ON s.run_id = r.id AND s.stage_no = r.current_stage").
		Where("r.status IN ?", []string{"queued", "preparing", "running", "awaiting_input"}).
		Where("r.total_lifecycle_deadline <= ?", now).
		Order("r.created_at").
		Limit(limit).
		Pluck("r.id", &ids).Error
	return ids, daoError(e)
}

func (d RunDAO) ListStalePlatformExecuting(ctx context.Context, activePlatformRevID string, now time.Time, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var ids []string
	e := d.db.WithContext(ctx).
		Table("runs r").
		Joins("JOIN run_stages s ON s.run_id = r.id AND s.stage_no = r.current_stage").
		Joins("JOIN runtime_profiles p ON p.id = s.runtime_profile_id").
		Where("r.status IN ?", []string{"preparing", "running"}).
		Where("r.total_lifecycle_deadline > ?", now).
		Where("s.runtime_profile_id IS NOT NULL").
		Where("p.platform_revision_id <> ?", activePlatformRevID).
		Order("r.created_at").
		Limit(limit).
		Pluck("r.id", &ids).Error
	return ids, daoError(e)
}

func (d RunDAO) ListPlatformRebasing(ctx context.Context, now time.Time, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var ids []string
	e := d.db.WithContext(ctx).
		Table("runs r").
		Joins("JOIN run_stages s ON s.run_id = r.id AND s.stage_no = r.current_stage").
		Where("r.status = ?", "platform_rebasing").
		Where("s.owner = ? OR s.lease_expires_at IS NULL OR s.lease_expires_at <= ?", "", now).
		Order("r.created_at").
		Limit(limit).
		Pluck("r.id", &ids).Error
	return ids, daoError(e)
}

type ResumeRead struct {
	CheckpointID string
	RunID        string
	StageNo      int
	Fence        int64
	ObjectRef    string
	SHA256       string
	SizeBytes    int64
	StateFormat  string
	InputID      string
	InputState   string
	Ciphertext   []byte
	KeyVersion   int
}

func (d CheckpointDAO) LatestResume(ctx context.Context, runID, answerKind string) (*ResumeRead, error) {
	var r ResumeRead
	e := d.db.WithContext(ctx).
		Table("checkpoints c").
		Select("c.id AS checkpoint_id, c.run_id, c.stage_no, c.fence, c.object_ref, c.sha256, c.size_bytes, c.state_format, i.input_id, i.state AS input_state, rc.ciphertext, rc.key_version").
		Joins("JOIN run_inputs i ON i.checkpoint_id = c.id").
		Joins("JOIN run_contents rc ON rc.id = i.answer_content_id").
		Where("c.run_id = ? AND c.verified AND c.referenced", runID).
		Where("rc.kind = ?", answerKind).
		Order("c.stage_no DESC, c.created_at DESC").
		Limit(1).
		Scan(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}

func (d RunInputDAO) LatestPending(ctx context.Context, runID, state string) (*RunInput, error) {
	var r RunInput
	e := d.db.WithContext(ctx).Where("run_id = ? AND state = ?", runID, state).
		Order("created_at DESC").First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}

func (d RunStageDAO) GetByExecution(ctx context.Context, executionID string) (*RunStage, error) {
	var r RunStage
	e := d.db.WithContext(ctx).Where("execution_id = ? AND execution_id <> ''", executionID).
		Order("started_at DESC").First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
