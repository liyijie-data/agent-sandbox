package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

type CleanupJob struct {
	ID           string     `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	ResourceKind string     `gorm:"column:resource_kind;type:text;not null"`
	InternalRef  string     `gorm:"column:internal_ref;type:text;not null;default:''"`
	DueAt        time.Time  `gorm:"column:due_at;type:timestamptz;not null;index:cleanup_jobs_due_idx"`
	Status       string     `gorm:"column:status;type:text;not null;default:pending;index:cleanup_jobs_due_idx"`
	Attempts     int        `gorm:"column:attempts;type:int;not null;default:0"`
	LastSafeCode string     `gorm:"column:last_safe_code;type:text;not null;default:''"`
	ClaimedUntil *time.Time `gorm:"column:claimed_until;type:timestamptz"`
	CreatedAt    time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
	CompletedAt  *time.Time `gorm:"column:completed_at;type:timestamptz"`
}

func (CleanupJob) TableName() string { return "cleanup_jobs" }

type CleanupJobDAO struct{ db *gorm.DB }

func (d CleanupJobDAO) Create(c context.Context, r *CleanupJob) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d CleanupJobDAO) Get(c context.Context, id string) (*CleanupJob, error) {
	var r CleanupJob
	e := d.db.WithContext(c).Where("id = ?", id).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d CleanupJobDAO) ListDue(c context.Context, at time.Time, limit int) ([]CleanupJob, error) {
	var r []CleanupJob
	e := d.db.WithContext(c).Where("due_at <= ?", at).Order("due_at").Limit(limit).Find(&r).Error
	return r, daoError(e)
}

func (d CleanupJobDAO) ListByKindAndRef(c context.Context, kind, ref string) ([]CleanupJob, error) {
	var r []CleanupJob
	e := d.db.WithContext(c).Where("resource_kind = ? AND internal_ref = ?", kind, ref).Find(&r).Error
	return r, daoError(e)
}

func (d CleanupJobDAO) DeleteByKindAndRef(c context.Context, kind, ref string) (int64, error) {
	x := d.db.WithContext(c).Where("resource_kind = ? AND internal_ref = ?", kind, ref).Delete(&CleanupJob{})
	return x.RowsAffected, daoError(x.Error)
}

type CleanupJobPatch struct {
	DueAt        *time.Time
	Status       *string
	Attempts     *int
	LastSafeCode *string
	ClaimedUntil *time.Time
	CompletedAt  *time.Time
}
type CleanupJobCondition struct{ Status *string }

func (d CleanupJobDAO) UpdateIf(c context.Context, id string, w CleanupJobCondition, v CleanupJobPatch) (bool, error) {
	q := d.db.WithContext(c).Model(&CleanupJob{}).Where("id = ?", id)
	if w.Status != nil {
		q = q.Where("status = ?", *w.Status)
	}
	x := q.Updates(v)
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d CleanupJobDAO) Delete(c context.Context, id string) (bool, error) {
	x := d.db.WithContext(c).Delete(&CleanupJob{}, "id = ?", id)
	return x.RowsAffected == 1, daoError(x.Error)
}
