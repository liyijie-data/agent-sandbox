package model

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type Image struct {
	ID       string `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	ClientID string `gorm:"column:client_id;type:uuid;not null;uniqueIndex:images_client_image_uq"`
	ImageID  string `gorm:"column:image_id;type:text;not null;uniqueIndex:images_client_image_uq"`
	Digest   string `gorm:"column:digest;type:text;not null"`

	Repository      *string `gorm:"column:repository;type:text"`
	ContractVersion string  `gorm:"column:contract_version;type:text;not null"`
	Capabilities    string  `gorm:"column:capabilities;type:jsonb;not null"`
	Entrypoint      string  `gorm:"column:entrypoint;type:jsonb;not null"`
	StateFormat     string  `gorm:"column:state_format;type:text;not null;default:''"`
	Status          string  `gorm:"column:status;type:text;not null;default:validating"`
	ValidationRef   string  `gorm:"column:validation_ref;type:text;not null;default:''"`

	WarmPoolReplicas int       `gorm:"column:warm_pool_replicas;type:int;not null;default:0"`
	CreatedAt        time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
}

func (Image) TableName() string { return "images" }

type ImageDAO struct{ db *gorm.DB }

func (d ImageDAO) Create(c context.Context, r *Image) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d ImageDAO) Get(c context.Context, id string) (*Image, error) {
	var r Image
	e := d.db.WithContext(c).Where("id = ?", id).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d ImageDAO) GetByClientAndImageID(c context.Context, clientID, imageID string) (*Image, error) {
	var r Image
	e := d.db.WithContext(c).Where("client_id = ? AND image_id = ?", clientID, imageID).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}

type ImagePatch struct {
	ContractVersion *string
	Capabilities    *string
	Entrypoint      *string
	StateFormat     *string
	Status          *string
	ValidationRef   *string

	WarmPoolReplicas *int

	Repository *string
}

func (d ImageDAO) Update(c context.Context, id string, v ImagePatch) (bool, error) {
	x := d.db.WithContext(c).Model(&Image{}).Where("id = ?", id).Updates(v)
	return x.RowsAffected == 1, daoError(x.Error)
}

type ImageCondition struct {
	Status *string
}

func (d ImageDAO) UpdateIf(c context.Context, id string, where ImageCondition, v ImagePatch) (bool, error) {
	q := d.db.WithContext(c).Model(&Image{}).Where("id = ?", id)
	if where.Status != nil {
		q = q.Where("status = ?", *where.Status)
	}
	x := q.Updates(v)
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d ImageDAO) List(c context.Context, limit, offset int) ([]Image, error) {
	var rows []Image
	e := d.db.WithContext(c).Order("created_at ASC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, daoError(e)
}

func (d ImageDAO) ListByClient(c context.Context, clientID string) ([]Image, error) {
	var rows []Image
	e := d.db.WithContext(c).Where("client_id = ?", clientID).Order("created_at").Find(&rows).Error
	return rows, daoError(e)
}

func (d ImageDAO) ListEnabledByClient(c context.Context, clientID string) ([]Image, error) {
	var rows []Image
	e := d.db.WithContext(c).Where("client_id = ? AND status = 'enabled'", clientID).Order("created_at").Find(&rows).Error
	return rows, daoError(e)
}

func (d ImageDAO) ListEnabled(c context.Context) ([]Image, error) {
	var rows []Image
	e := d.db.WithContext(c).Where("status = 'enabled'").Order("created_at").Find(&rows).Error
	return rows, daoError(e)
}

func (d ImageDAO) Delete(c context.Context, id string) (bool, error) {
	x := d.db.WithContext(c).Delete(&Image{}, "id = ?", id)
	return x.RowsAffected == 1, daoError(x.Error)
}
