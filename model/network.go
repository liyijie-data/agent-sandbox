package model

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"gorm.io/gorm"
)

type PlatformNetworkBaseline struct {
	ID         string    `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	Revision   int64     `gorm:"column:revision;type:bigint"`
	ConfigHash []byte    `gorm:"column:config_hash;type:bytea"`
	Config     string    `gorm:"column:config;type:jsonb"`
	CreatedAt  time.Time `gorm:"column:created_at;type:timestamptz"`
}

func (PlatformNetworkBaseline) TableName() string { return "platform_network_baselines" }

type PlatformNetworkHead struct {
	ID                bool      `gorm:"column:id;primaryKey"`
	ActiveRevisionID  *string   `gorm:"column:active_revision_id;type:uuid"`
	DesiredRevisionID *string   `gorm:"column:desired_revision_id;type:uuid"`
	UpdatedAt         time.Time `gorm:"column:updated_at;type:timestamptz"`
	RolloutStatus     string    `gorm:"column:rollout_status;type:text"`
	ErrorSummary      string    `gorm:"column:error_summary;type:text"`
}

func (PlatformNetworkHead) TableName() string { return "platform_network_head" }

type NetworkConfigRevision struct {
	ID                  string    `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	Scope               string    `gorm:"column:scope;type:text"`
	ClientID            string    `gorm:"column:client_id;type:uuid"`
	ImageRegistrationID *string   `gorm:"column:image_registration_id;type:uuid"`
	Revision            int64     `gorm:"column:revision;type:bigint"`
	Config              string    `gorm:"column:config;type:jsonb"`
	ConfigHash          []byte    `gorm:"column:config_hash;type:bytea"`
	CreatedAt           time.Time `gorm:"column:created_at;type:timestamptz"`
}

func (NetworkConfigRevision) TableName() string { return "network_config_revisions" }

type NetworkConfigHead struct {
	ClientID          string    `gorm:"column:client_id;type:uuid;primaryKey"`
	ActiveRevisionID  *string   `gorm:"column:active_revision_id;type:uuid"`
	DesiredRevisionID *string   `gorm:"column:desired_revision_id;type:uuid"`
	UpdatedAt         time.Time `gorm:"column:updated_at;type:timestamptz"`
	RolloutStatus     string    `gorm:"column:rollout_status;type:text"`
	ErrorSummary      string    `gorm:"column:error_summary;type:text"`
}

func (NetworkConfigHead) TableName() string { return "client_network_heads" }

type ImageNetworkHead struct {
	ImageRegistrationID string    `gorm:"column:image_registration_id;type:uuid;primaryKey"`
	ActiveRevisionID    *string   `gorm:"column:active_revision_id;type:uuid"`
	DesiredRevisionID   *string   `gorm:"column:desired_revision_id;type:uuid"`
	UpdatedAt           time.Time `gorm:"column:updated_at;type:timestamptz"`
	RolloutStatus       string    `gorm:"column:rollout_status;type:text"`
	ErrorSummary        string    `gorm:"column:error_summary;type:text"`
}

func (ImageNetworkHead) TableName() string { return "image_network_heads" }

type RuntimeProfile struct {
	ID                  string    `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	ImageRegistrationID string    `gorm:"column:image_registration_id;type:uuid"`
	PlatformRevisionID  string    `gorm:"column:platform_revision_id;type:uuid"`
	NetworkRevisionID   *string   `gorm:"column:network_revision_id;type:uuid"`
	ImageRef            string    `gorm:"column:image_ref;type:text"`
	TemplateName        string    `gorm:"column:template_name;type:text"`
	WarmPoolName        string    `gorm:"column:warm_pool_name;type:text"`
	Status              string    `gorm:"column:status;type:text"`
	CreatedAt           time.Time `gorm:"column:created_at;type:timestamptz"`
}

func (RuntimeProfile) TableName() string { return "runtime_profiles" }

type RolloutJob struct {
	ID                  string     `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	Scope               string     `gorm:"column:scope;type:text"`
	ClientID            *string    `gorm:"column:client_id;type:uuid"`
	ImageRegistrationID *string    `gorm:"column:image_registration_id;type:uuid"`
	RequestID           string     `gorm:"column:request_id;type:text"`
	PlatformRevisionID  *string    `gorm:"column:platform_revision_id;type:uuid"`
	NetworkRevisionID   *string    `gorm:"column:network_revision_id;type:uuid"`
	Status              string     `gorm:"column:status;type:text"`
	Attempts            int        `gorm:"column:attempts;type:int"`
	ErrorSummary        string     `gorm:"column:error_summary;type:text"`
	CreatedAt           time.Time  `gorm:"column:created_at;type:timestamptz"`
	UpdatedAt           time.Time  `gorm:"column:updated_at;type:timestamptz"`
	CompletedAt         *time.Time `gorm:"column:completed_at;type:timestamptz"`
}

func (RolloutJob) TableName() string { return "rollout_jobs" }

type RolloutJobImage struct {
	ID                  string    `gorm:"column:id;type:uuid;primaryKey;default:platform_uuid_v4()"`
	RolloutJobID        string    `gorm:"column:rollout_job_id;type:uuid"`
	ImageRegistrationID string    `gorm:"column:image_registration_id;type:uuid"`
	RuntimeProfileID    *string   `gorm:"column:runtime_profile_id;type:uuid"`
	Status              string    `gorm:"column:status;type:text"`
	Attempts            int       `gorm:"column:attempts;type:int"`
	ErrorSummary        string    `gorm:"column:error_summary;type:text"`
	UpdatedAt           time.Time `gorm:"column:updated_at;type:timestamptz"`
}

func (RolloutJobImage) TableName() string { return "rollout_job_images" }

type PlatformNetworkDAO struct{ db *gorm.DB }

func (d PlatformNetworkDAO) Create(c context.Context, v *PlatformNetworkBaseline) error {
	return daoError(d.db.WithContext(c).Create(v).Error)
}
func (d PlatformNetworkDAO) Get(c context.Context, id string) (*PlatformNetworkBaseline, error) {
	var v PlatformNetworkBaseline
	e := d.db.WithContext(c).First(&v, "id = ?", id).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &v, daoError(e)
}
func (d PlatformNetworkDAO) GetHead(c context.Context) (*PlatformNetworkHead, error) {
	var v PlatformNetworkHead
	e := d.db.WithContext(c).First(&v, "id = true").Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &v, daoError(e)
}

func (d PlatformNetworkDAO) List(c context.Context) ([]PlatformNetworkBaseline, error) {
	var v []PlatformNetworkBaseline
	e := d.db.WithContext(c).Order("revision").Find(&v).Error
	return v, daoError(e)
}

func (d PlatformNetworkDAO) Fail(c context.Context, errSummary string) error {
	x := d.db.WithContext(c).Model(&PlatformNetworkHead{}).Where("id = true").
		Updates(map[string]any{"rollout_status": "failed", "error_summary": errSummary, "updated_at": time.Now()})
	return daoError(x.Error)
}
func (d PlatformNetworkDAO) SetDesired(c context.Context, desired string, expected *string) (bool, error) {
	q := d.db.WithContext(c).Model(&PlatformNetworkHead{}).Where("id = true")
	if expected == nil {
		q = q.Where("active_revision_id IS NULL")
	} else {
		q = q.Where("active_revision_id = ?", *expected)
	}
	x := q.Updates(map[string]any{"desired_revision_id": desired, "updated_at": time.Now()})
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d PlatformNetworkDAO) Promote(c context.Context, expectedActive, expectedDesired *string) (bool, error) {
	q := d.db.WithContext(c).Model(&PlatformNetworkHead{}).Where("id = true")
	if expectedActive == nil {
		q = q.Where("active_revision_id IS NULL")
	} else {
		q = q.Where("active_revision_id = ?", *expectedActive)
	}
	if expectedDesired == nil {
		q = q.Where("desired_revision_id IS NULL")
	} else {
		q = q.Where("desired_revision_id = ?", *expectedDesired)
	}

	x := q.Updates(map[string]any{"active_revision_id": expectedDesired, "rollout_status": "ready", "error_summary": "", "updated_at": time.Now()})
	return x.RowsAffected == 1, daoError(x.Error)
}

type NetworkConfigRevisionDAO struct{ db *gorm.DB }

func (d NetworkConfigRevisionDAO) Create(c context.Context, v *NetworkConfigRevision) error {
	return daoError(d.db.WithContext(c).Create(v).Error)
}
func (d NetworkConfigRevisionDAO) Get(c context.Context, id string) (*NetworkConfigRevision, error) {
	var v NetworkConfigRevision
	e := d.db.WithContext(c).First(&v, "id = ?", id).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &v, daoError(e)
}
func (d NetworkConfigRevisionDAO) List(c context.Context, scope, clientID string) ([]NetworkConfigRevision, error) {
	var v []NetworkConfigRevision
	e := d.db.WithContext(c).Where("scope = ? AND client_id = ?", scope, clientID).Order("revision").Find(&v).Error
	return v, daoError(e)
}

func (d NetworkConfigRevisionDAO) ListByImage(c context.Context, imageID string) ([]NetworkConfigRevision, error) {
	var v []NetworkConfigRevision
	e := d.db.WithContext(c).Where("scope = ? AND image_registration_id = ?", "image", imageID).Order("revision").Find(&v).Error
	return v, daoError(e)
}

type NetworkConfigHeadDAO struct{ db *gorm.DB }

func (d NetworkConfigHeadDAO) SetDesiredClient(c context.Context, v *NetworkConfigHead, expected *string) (bool, error) {
	q := d.db.WithContext(c).Model(&NetworkConfigHead{}).Where("client_id = ?", v.ClientID)
	if expected == nil {
		q = q.Where("active_revision_id IS NULL")
	} else {
		q = q.Where("active_revision_id = ?", *expected)
	}
	x := q.Updates(map[string]any{"desired_revision_id": v.DesiredRevisionID, "rollout_status": "applying", "error_summary": "", "updated_at": v.UpdatedAt})
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d NetworkConfigHeadDAO) CreateClient(c context.Context, v *NetworkConfigHead) error {
	return daoError(d.db.WithContext(c).Create(v).Error)
}
func (d NetworkConfigHeadDAO) CreateImage(c context.Context, v *ImageNetworkHead) error {
	return daoError(d.db.WithContext(c).Table("image_network_heads").Create(v).Error)
}
func (d NetworkConfigHeadDAO) SetDesired(c context.Context, id, desired string, expected *string) (bool, error) {
	v := &NetworkConfigHead{ClientID: id, DesiredRevisionID: &desired}
	return d.SetDesiredClient(c, v, expected)
}
func (d NetworkConfigHeadDAO) PromoteClient(c context.Context, id string, expectedActive, expectedDesired *string) (bool, error) {
	q := d.db.WithContext(c).Model(&NetworkConfigHead{}).Where("client_id = ?", id)
	if expectedActive == nil {
		q = q.Where("active_revision_id IS NULL")
	} else {
		q = q.Where("active_revision_id = ?", *expectedActive)
	}
	if expectedDesired == nil {
		q = q.Where("desired_revision_id IS NULL")
	} else {
		q = q.Where("desired_revision_id = ?", *expectedDesired)
	}

	x := q.Updates(map[string]any{"active_revision_id": expectedDesired, "rollout_status": "ready", "error_summary": "", "updated_at": time.Now()})
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d NetworkConfigHeadDAO) PromoteImage(c context.Context, id string, expectedActive, expectedDesired *string) (bool, error) {
	q := d.db.WithContext(c).Model(&ImageNetworkHead{}).Where("image_registration_id = ?", id)
	if expectedActive == nil {
		q = q.Where("active_revision_id IS NULL")
	} else {
		q = q.Where("active_revision_id = ?", *expectedActive)
	}
	if expectedDesired == nil {
		q = q.Where("desired_revision_id IS NULL")
	} else {
		q = q.Where("desired_revision_id = ?", *expectedDesired)
	}

	x := q.Updates(map[string]any{"active_revision_id": expectedDesired, "rollout_status": "ready", "error_summary": "", "updated_at": time.Now()})
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d NetworkConfigHeadDAO) GetClient(c context.Context, id string) (*NetworkConfigHead, error) {
	var v NetworkConfigHead
	e := d.db.WithContext(c).First(&v, "client_id = ?", id).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &v, daoError(e)
}
func (d NetworkConfigHeadDAO) GetImage(c context.Context, id string) (*ImageNetworkHead, error) {
	var v ImageNetworkHead
	e := d.db.WithContext(c).First(&v, "image_registration_id = ?", id).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &v, daoError(e)
}
func (d NetworkConfigHeadDAO) UpdateImage(c context.Context, v *ImageNetworkHead, expected *string) (bool, error) {
	q := d.db.WithContext(c).Model(&ImageNetworkHead{}).Where("image_registration_id = ?", v.ImageRegistrationID)
	if expected == nil {
		q = q.Where("active_revision_id IS NULL")
	} else {
		q = q.Where("active_revision_id = ?", *expected)
	}
	x := q.Updates(map[string]any{"desired_revision_id": v.DesiredRevisionID, "rollout_status": "applying", "error_summary": "", "updated_at": v.UpdatedAt})
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d NetworkConfigHeadDAO) ClearImage(c context.Context, imageID string) (bool, error) {
	x := d.db.WithContext(c).Model(&ImageNetworkHead{}).Where("image_registration_id = ?", imageID).
		Updates(map[string]any{"active_revision_id": nil, "desired_revision_id": nil, "rollout_status": "ready", "error_summary": "", "updated_at": time.Now()})
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d NetworkConfigHeadDAO) ClearImageAll(c context.Context, clientID string) error {
	x := d.db.WithContext(c).Model(&ImageNetworkHead{}).
		Where("image_registration_id IN (SELECT id FROM images WHERE client_id = ?)", clientID).
		Updates(map[string]any{"active_revision_id": nil, "desired_revision_id": nil, "rollout_status": "ready", "error_summary": "", "updated_at": time.Now()})
	return daoError(x.Error)
}

func (d NetworkConfigHeadDAO) FailClient(c context.Context, id, errSummary string) error {
	x := d.db.WithContext(c).Model(&NetworkConfigHead{}).Where("client_id = ?", id).
		Updates(map[string]any{"rollout_status": "failed", "error_summary": errSummary, "updated_at": time.Now()})
	return daoError(x.Error)
}

func (d NetworkConfigHeadDAO) FailImage(c context.Context, id, errSummary string) error {
	x := d.db.WithContext(c).Model(&ImageNetworkHead{}).Where("image_registration_id = ?", id).
		Updates(map[string]any{"rollout_status": "failed", "error_summary": errSummary, "updated_at": time.Now()})
	return daoError(x.Error)
}

type RuntimeProfileDAO struct{ db *gorm.DB }

var imageRefPattern = regexp.MustCompile(`^.+@sha256:[0-9a-f]{64}$`)

func (d RuntimeProfileDAO) Create(c context.Context, v *RuntimeProfile) error {
	if !imageRefPattern.MatchString(v.ImageRef) {
		return fmt.Errorf("model: invalid immutable image ref")
	}
	var img Image
	if e := d.db.WithContext(c).First(&img, "id = ?", v.ImageRegistrationID).Error; e != nil {
		return daoError(e)
	}
	if img.Repository == nil || *img.Repository == "" || v.ImageRef != *img.Repository+"@"+img.Digest {
		return fmt.Errorf("model: image ref does not match registration")
	}
	return daoError(d.db.WithContext(c).Create(v).Error)
}
func (d RuntimeProfileDAO) ListByImage(c context.Context, id string) ([]RuntimeProfile, error) {
	var v []RuntimeProfile
	e := d.db.WithContext(c).Where("image_registration_id = ?", id).Order("created_at").Find(&v).Error
	return v, daoError(e)
}

func (d RuntimeProfileDAO) ListByPlatformRevision(c context.Context, platformRevID string) ([]RuntimeProfile, error) {
	var v []RuntimeProfile
	e := d.db.WithContext(c).Where("platform_revision_id = ?", platformRevID).Order("created_at").Find(&v).Error
	return v, daoError(e)
}

func (d RuntimeProfileDAO) ListAll(c context.Context) ([]RuntimeProfile, error) {
	var v []RuntimeProfile
	e := d.db.WithContext(c).Order("created_at").Find(&v).Error
	return v, daoError(e)
}
func (d RuntimeProfileDAO) Get(c context.Context, id string) (*RuntimeProfile, error) {
	var v RuntimeProfile
	e := d.db.WithContext(c).First(&v, "id = ?", id).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &v, daoError(e)
}

func (d RuntimeProfileDAO) GetByTriple(c context.Context, imageRegID, platformRevID string, netRevID *string) (*RuntimeProfile, error) {
	var v RuntimeProfile
	q := d.db.WithContext(c).Where("image_registration_id = ? AND platform_revision_id = ?", imageRegID, platformRevID)
	if netRevID == nil {
		q = q.Where("network_revision_id IS NULL")
	} else {
		q = q.Where("network_revision_id = ?", *netRevID)
	}
	e := q.First(&v).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &v, daoError(e)
}

func (d RuntimeProfileDAO) UpdateStatus(c context.Context, id, from, to string) (bool, error) {
	x := d.db.WithContext(c).Model(&RuntimeProfile{}).Where("id = ? AND status = ?", id, from).
		Updates(map[string]any{"status": to})
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RuntimeProfileDAO) UpdateNames(c context.Context, id, templateName, warmPoolName string) (bool, error) {
	x := d.db.WithContext(c).Model(&RuntimeProfile{}).Where("id = ?", id).
		Updates(map[string]any{"template_name": templateName, "warm_pool_name": warmPoolName})
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RuntimeProfileDAO) Delete(c context.Context, id string) (bool, error) {
	x := d.db.WithContext(c).Delete(&RuntimeProfile{}, "id = ?", id)
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RuntimeProfileDAO) MarkConvergedStatus(c context.Context, id, status string) (bool, error) {
	x := d.db.WithContext(c).Model(&RuntimeProfile{}).
		Where("id = ? AND status <> ?", id, status).
		Updates(map[string]any{"status": status})
	return x.RowsAffected == 1, daoError(x.Error)
}

type RolloutJobDAO struct{ db *gorm.DB }

func (d RolloutJobDAO) Create(c context.Context, v *RolloutJob) error {
	return daoError(d.db.WithContext(c).Create(v).Error)
}
func (d RolloutJobDAO) CreateImage(c context.Context, v *RolloutJobImage) error {
	return daoError(d.db.WithContext(c).Create(v).Error)
}
func (d RolloutJobDAO) Get(c context.Context, id string) (*RolloutJob, error) {
	var v RolloutJob
	e := d.db.WithContext(c).First(&v, "id = ?", id).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &v, daoError(e)
}
func (d RolloutJobDAO) GetByRequest(c context.Context, scope, requestID, clientID, imageID string) (*RolloutJob, error) {
	var v RolloutJob
	q := d.db.WithContext(c).Where("scope = ? AND request_id = ?", scope, requestID)
	if clientID != "" {
		q = q.Where("client_id = ?", clientID)
	}
	if imageID != "" {
		q = q.Where("image_registration_id = ?", imageID)
	}
	e := q.First(&v).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &v, daoError(e)
}
func (d RolloutJobDAO) ListImages(c context.Context, id string) ([]RolloutJobImage, error) {
	var v []RolloutJobImage
	e := d.db.WithContext(c).Where("rollout_job_id = ?", id).Find(&v).Error
	return v, daoError(e)
}

func (d RolloutJobDAO) CountProfileRefs(c context.Context, profileID string) (int, error) {
	var n int64
	e := d.db.WithContext(c).Model(&RolloutJobImage{}).
		Joins("JOIN rollout_jobs ON rollout_jobs.id = rollout_job_images.rollout_job_id").
		Where("rollout_job_images.runtime_profile_id = ? AND rollout_jobs.status <> ?", profileID, "completed").
		Count(&n).Error
	return int(n), daoError(e)
}
func (d RolloutJobDAO) UpdateImageStatus(c context.Context, id, from, to string, attempts int, errSummary string) (bool, error) {
	x := d.db.WithContext(c).Model(&RolloutJobImage{}).Where("id = ? AND status = ?", id, from).Updates(map[string]any{"status": to, "attempts": attempts, "error_summary": errSummary, "updated_at": time.Now()})
	return x.RowsAffected == 1, daoError(x.Error)
}
func (d RolloutJobDAO) UpdateStatus(c context.Context, id, from, to string) (bool, error) {
	x := d.db.WithContext(c).Model(&RolloutJob{}).Where("id = ? AND status = ?", id, from).Updates(map[string]any{"status": to, "updated_at": time.Now()})
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RolloutJobDAO) ListActive(c context.Context) ([]RolloutJob, error) {
	var v []RolloutJob
	e := d.db.WithContext(c).Where("status IN (?)", []string{"pending", "running", "failed"}).Order("created_at").Find(&v).Error
	return v, daoError(e)
}

func (d RolloutJobDAO) MarkRunning(c context.Context, id, from string) (bool, error) {
	x := d.db.WithContext(c).Model(&RolloutJob{}).Where("id = ? AND status = ?", id, from).
		Updates(map[string]any{"status": "running", "updated_at": time.Now()})
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RolloutJobDAO) Fail(c context.Context, id, from string, errSummary string) (bool, error) {
	x := d.db.WithContext(c).Model(&RolloutJob{}).Where("id = ? AND status = ?", id, from).
		Updates(map[string]any{"status": "failed", "attempts": gorm.Expr("attempts + 1"), "error_summary": errSummary, "updated_at": time.Now()})
	return x.RowsAffected == 1, daoError(x.Error)
}

func (d RolloutJobDAO) Complete(c context.Context, id, from string) (bool, error) {
	x := d.db.WithContext(c).Model(&RolloutJob{}).Where("id = ? AND status = ?", id, from).
		Updates(map[string]any{"status": "completed", "completed_at": time.Now(), "updated_at": time.Now()})
	return x.RowsAffected == 1, daoError(x.Error)
}
