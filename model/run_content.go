package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

type RunContent struct {
	ID         string     `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	RunID      string     `gorm:"column:run_id;type:uuid;not null;index:run_contents_run_kind_idx"`
	StageNo    int        `gorm:"column:stage_no;type:int;not null;default:0"`
	Kind       string     `gorm:"column:kind;type:text;not null;index:run_contents_run_kind_idx"`
	Ciphertext []byte     `gorm:"column:ciphertext;type:bytea;not null"`
	KeyVersion int        `gorm:"column:key_version;type:int;not null"`
	ExpiresAt  *time.Time `gorm:"column:expires_at;type:timestamptz"`
	CreatedAt  time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
}

func (RunContent) TableName() string { return "run_contents" }

type RunContentDAO struct{ db *gorm.DB }

func (d RunContentDAO) Create(c context.Context, r *RunContent) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d RunContentDAO) Get(c context.Context, id string) (*RunContent, error) {
	var r RunContent
	e := d.db.WithContext(c).Where("id = ?", id).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d RunContentDAO) ListByRunAndKind(c context.Context, id, kind string, limit, offset int) ([]RunContent, error) {
	var r []RunContent
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	e := d.db.WithContext(c).Where("run_id = ? AND kind = ?", id, kind).Order("created_at").Limit(limit).Offset(offset).Find(&r).Error
	return r, daoError(e)
}

func (d RunContentDAO) ListByRun(c context.Context, id string, limit, offset int) ([]RunContent, error) {
	var r []RunContent
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	e := d.db.WithContext(c).Where("run_id = ?", id).Order("created_at").Limit(limit).Offset(offset).Find(&r).Error
	return r, daoError(e)
}

type RunContentPatch struct{ ExpiresAt *time.Time }

func (d RunContentDAO) Update(c context.Context, id string, v RunContentPatch) (bool, error) {
	x := d.db.WithContext(c).Model(&RunContent{}).Where("id = ?", id).Updates(v)
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d RunContentDAO) Delete(c context.Context, id string) (bool, error) {
	x := d.db.WithContext(c).Delete(&RunContent{}, "id = ?", id)
	return x.RowsAffected == 1, daoError(x.Error)
}
