package model

import (
	"context"
	"gorm.io/gorm"
	"time"
)

type RunInput struct {
	ID              string     `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	RunID           string     `gorm:"column:run_id;type:uuid;not null;uniqueIndex:run_inputs_run_input_uq"`
	StageNo         int        `gorm:"column:stage_no;type:int;not null"`
	InputID         string     `gorm:"column:input_id;type:text;not null;uniqueIndex:run_inputs_run_input_uq"`
	Kind            string     `gorm:"column:kind;type:text;not null"`
	State           string     `gorm:"column:state;type:text;not null;default:pending"`
	AnswerHMAC      []byte     `gorm:"column:answer_hmac;type:bytea"`
	CheckpointID    *string    `gorm:"column:checkpoint_id;type:uuid"`
	AnswerContentID *string    `gorm:"column:answer_content_id;type:uuid"`
	InputDeadline   time.Time  `gorm:"column:input_deadline;type:timestamptz;not null;default:infinity"`
	Options         string     `gorm:"column:options;type:jsonb;not null;default:'[]'"`
	CreatedAt       time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
	AnsweredAt      *time.Time `gorm:"column:answered_at;type:timestamptz"`
}

func (RunInput) TableName() string { return "run_inputs" }

type RunInputDAO struct{ db *gorm.DB }

func (d RunInputDAO) Create(c context.Context, r *RunInput) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d RunInputDAO) Get(c context.Context, runID, inputID string) (*RunInput, error) {
	var r RunInput
	e := d.db.WithContext(c).Where("run_id = ? AND input_id = ?", runID, inputID).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d RunInputDAO) ListByRun(c context.Context, id string, limit, offset int) ([]RunInput, error) {
	var r []RunInput
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	e := d.db.WithContext(c).Where("run_id = ?", id).Order("created_at").Limit(limit).Offset(offset).Find(&r).Error
	return r, daoError(e)
}

type RunInputPatch struct {
	State           *string
	AnswerHMAC      *[]byte
	CheckpointID    *string
	AnswerContentID *string
	InputDeadline   *time.Time
	Options         *string
	AnsweredAt      *time.Time
}
type RunInputCondition struct{ State *string }

func (d RunInputDAO) UpdateIf(c context.Context, runID, inputID string, w RunInputCondition, v RunInputPatch) (bool, error) {
	q := d.db.WithContext(c).Model(&RunInput{}).Where("run_id = ? AND input_id = ?", runID, inputID)
	if w.State != nil {
		q = q.Where("state = ?", *w.State)
	}
	x := q.Updates(v)
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d RunInputDAO) Delete(c context.Context, runID, inputID string) (bool, error) {
	x := d.db.WithContext(c).Delete(&RunInput{}, "run_id = ? AND input_id = ?", runID, inputID)
	return x.RowsAffected == 1, daoError(x.Error)
}
