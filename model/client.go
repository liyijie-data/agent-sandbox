package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Client struct {
	ID   string `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	Name string `gorm:"column:name;type:text;not null"`

	ConcurrencyQuota int       `gorm:"column:concurrency_quota;type:int;not null;default:0"`
	CreatedAt        time.Time `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
}

func (Client) TableName() string { return "clients" }

type ClientDAO struct{ db *gorm.DB }

func (d ClientDAO) Create(ctx context.Context, row *Client) error {
	return daoError(d.db.WithContext(ctx).Create(row).Error)
}
func (d ClientDAO) Get(ctx context.Context, id string) (*Client, error) {
	var r Client
	e := d.db.WithContext(ctx).Where("id = ?", id).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d ClientDAO) List(ctx context.Context, limit, offset int) ([]Client, error) {
	var r []Client
	e := d.db.WithContext(ctx).Order("created_at").Limit(limit).Offset(offset).Find(&r).Error
	return r, daoError(e)
}

func (d ClientDAO) GetForUpdate(ctx context.Context, id string) (*Client, error) {
	var r Client
	e := d.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}

func (d ClientDAO) SetConcurrencyQuota(ctx context.Context, id string, quota int) (bool, error) {
	x := d.db.WithContext(ctx).Model(&Client{}).Where("id = ?", id).
		Updates(map[string]any{"concurrency_quota": quota})
	return x.RowsAffected == 1, daoError(x.Error)
}

type ClientPatch struct{ Name *string }

func (d ClientDAO) Update(ctx context.Context, id string, changes ClientPatch) (bool, error) {
	r := d.db.WithContext(ctx).Model(&Client{}).Where("id = ?", id).Updates(changes)
	return r.RowsAffected == 1, daoError(r.Error)
}
func (d ClientDAO) Delete(ctx context.Context, id string) (bool, error) {
	r := d.db.WithContext(ctx).Delete(&Client{}, "id = ?", id)
	return r.RowsAffected == 1, daoError(r.Error)
}

type APIKey struct {
	ID        string     `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	ClientID  string     `gorm:"column:client_id;type:uuid;not null;index:api_keys_client_idx"`
	KeyHash   string     `gorm:"column:key_hash;type:text;not null"`
	Label     string     `gorm:"column:label;type:text;not null;default:''"`
	Active    bool       `gorm:"column:active;not null;default:true"`
	CreatedAt time.Time  `gorm:"column:created_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
	RevokedAt *time.Time `gorm:"column:revoked_at"`
}

func (APIKey) TableName() string { return "api_keys" }

type APIKeyDAO struct{ db *gorm.DB }

func (d APIKeyDAO) Create(c context.Context, r *APIKey) error {
	return daoError(d.db.WithContext(c).Create(r).Error)
}
func (d APIKeyDAO) Get(c context.Context, id string) (*APIKey, error) {
	var r APIKey
	e := d.db.WithContext(c).Where("id = ?", id).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}
func (d APIKeyDAO) FindByClientAndHash(c context.Context, clientID, hash string) (*APIKey, error) {
	var r APIKey
	e := d.db.WithContext(c).Where("client_id = ? AND key_hash = ?", clientID, hash).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}

func (d APIKeyDAO) FindByHash(c context.Context, hash string) (*APIKey, error) {
	var r APIKey
	e := d.db.WithContext(c).Where("key_hash = ? AND active = true", hash).First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}

func (d APIKeyDAO) ListByClient(c context.Context, clientID string) ([]APIKey, error) {
	var rows []APIKey
	e := d.db.WithContext(c).Where("client_id = ?", clientID).Order("created_at").Find(&rows).Error
	return rows, daoError(e)
}

type APIKeyPatch struct {
	Label     Optional[string]
	Active    Optional[bool]
	RevokedAt Optional[*time.Time]
}

func (d APIKeyDAO) Update(c context.Context, id string, v APIKeyPatch) (bool, error) {
	values := map[string]any{}
	if v.Label.Set {
		values["label"] = v.Label.Value
	}
	if v.Active.Set {
		values["active"] = v.Active.Value
	}
	if v.RevokedAt.Set {
		values["revoked_at"] = v.RevokedAt.Value
	}
	x := d.db.WithContext(c).Model(&APIKey{}).Where("id = ?", id).Updates(values)
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d APIKeyDAO) Delete(c context.Context, id string) (bool, error) {
	x := d.db.WithContext(c).Delete(&APIKey{}, "id = ?", id)
	return x.RowsAffected == 1, daoError(x.Error)
}

func errorsIsNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
