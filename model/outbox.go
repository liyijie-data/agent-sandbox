package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

type OutboxEvent struct {
	ID           int64      `gorm:"column:id;type:bigint;primaryKey;autoIncrement"`
	EventID      string     `gorm:"column:event_id;type:uuid;not null;uniqueIndex:outbox_event_id_uq"`
	RunID        *string    `gorm:"column:run_id;type:uuid"`
	StageNo      int        `gorm:"column:stage_no;type:int;not null;default:0"`
	Fence        int64      `gorm:"column:fence;type:bigint;not null;default:0"`
	Topic        string     `gorm:"column:topic;type:text;not null"`
	Status       string     `gorm:"column:status;type:text;not null;default:pending;index:outbox_ready_idx"`
	PayloadRef   *string    `gorm:"column:payload_ref;type:uuid"`
	RetryAt      *time.Time `gorm:"column:retry_at;type:timestamptz;index:outbox_ready_idx"`
	ClaimToken   *string    `gorm:"column:claim_token;type:uuid"`
	ClaimedUntil *time.Time `gorm:"column:claimed_until;type:timestamptz;index:outbox_claim_expiry_idx"`
	ExpiresAt    *time.Time `gorm:"column:expires_at;type:timestamptz"`
	CreatedAt    time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
}

func (OutboxEvent) TableName() string { return "outbox" }

type OutboxDAO struct{ db *gorm.DB }

func (d OutboxDAO) Create(c context.Context, r *OutboxEvent) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d OutboxDAO) Get(c context.Context, id string) (*OutboxEvent, error) {
	var r OutboxEvent
	e := d.db.WithContext(c).Where("event_id = ?", id).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d OutboxDAO) List(c context.Context, status string, limit, offset int) ([]OutboxEvent, error) {
	var r []OutboxEvent
	e := d.db.WithContext(c).Where("status = ?", status).Order("id").Limit(limit).Offset(offset).Find(&r).Error
	return r, daoError(e)
}

func (d OutboxDAO) ListReadyPending(c context.Context, now time.Time, limit int) ([]OutboxEvent, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var r []OutboxEvent
	e := d.db.WithContext(c).
		Where("status IN ?", []string{"pending", "claimed"}).
		Where("retry_at IS NULL OR retry_at <= ?", now).
		Where("claimed_until IS NULL OR claimed_until <= ?", now).
		Order("id").Limit(limit).Find(&r).Error
	return r, daoError(e)
}

type OutboxPatch struct {
	Status       *string
	RetryAt      *time.Time
	ClaimToken   *string
	ClaimedUntil *time.Time
	ExpiresAt    *time.Time
}
type OutboxCondition struct {
	Status     *string
	StatusIn   []string
	ClaimToken *string
}

func (d OutboxDAO) UpdateIf(c context.Context, id string, w OutboxCondition, v OutboxPatch) (bool, error) {
	q := d.db.WithContext(c).Model(&OutboxEvent{}).Where("event_id = ?", id)
	if w.Status != nil {
		q = q.Where("status = ?", *w.Status)
	}
	if len(w.StatusIn) > 0 {
		q = q.Where("status IN ?", w.StatusIn)
	}
	if w.ClaimToken != nil {
		q = q.Where("claim_token = ?", *w.ClaimToken)
	}
	x := q.Updates(v)
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d OutboxDAO) Delete(c context.Context, id string) (bool, error) {
	x := d.db.WithContext(c).Delete(&OutboxEvent{}, "event_id = ?", id)
	return x.RowsAffected == 1, daoError(x.Error)
}
