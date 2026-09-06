package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

type Checkpoint struct {
	ID          string    `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	RunID       string    `gorm:"column:run_id;type:uuid;not null"`
	StageNo     int       `gorm:"column:stage_no;type:int;not null"`
	Fence       int64     `gorm:"column:fence;type:bigint;not null"`
	ObjectRef   string    `gorm:"column:object_ref;type:text;not null"`
	SHA256      string    `gorm:"column:sha256;type:text;not null"`
	SizeBytes   int64     `gorm:"column:size_bytes;type:bigint;not null"`
	StateFormat string    `gorm:"column:state_format;type:text;not null;default:''"`
	Verified    bool      `gorm:"column:verified;not null;default:false"`
	Referenced  bool      `gorm:"column:referenced;not null;default:true"`
	CreatedAt   time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
}

func (Checkpoint) TableName() string { return "checkpoints" }

type CheckpointDAO struct{ db *gorm.DB }

func (d CheckpointDAO) Create(c context.Context, r *Checkpoint) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d CheckpointDAO) Get(c context.Context, id string) (*Checkpoint, error) {
	var r Checkpoint
	e := d.db.WithContext(c).Where("id = ?", id).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d CheckpointDAO) ListByStage(c context.Context, runID string, no int, limit, offset int) ([]Checkpoint, error) {
	var r []Checkpoint
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	e := d.db.WithContext(c).Where("run_id = ? AND stage_no = ?", runID, no).Order("created_at").Limit(limit).Offset(offset).Find(&r).Error
	return r, daoError(e)
}

type CheckpointPatch struct {
	Verified   *bool
	Referenced *bool
}

func (d CheckpointDAO) Update(c context.Context, id string, v CheckpointPatch) (bool, error) {
	x := d.db.WithContext(c).Model(&Checkpoint{}).Where("id = ?", id).Updates(v)
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d CheckpointDAO) Delete(c context.Context, id string) (bool, error) {
	x := d.db.WithContext(c).Delete(&Checkpoint{}, "id = ?", id)
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d CheckpointDAO) CountRefsByObject(c context.Context, objectRef string) (int, error) {
	var n int64
	e := d.db.WithContext(c).Model(&Checkpoint{}).Where("object_ref = ?", objectRef).Count(&n).Error
	return int(n), daoError(e)
}
