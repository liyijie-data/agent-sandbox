package model

import (
	"context"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PlatformSettings struct {
	ID          bool      `gorm:"column:id;type:boolean;primaryKey;default:true"`
	Budget      *int      `gorm:"column:budget;type:int"`
	MaxPerImage *int      `gorm:"column:max_per_image;type:int"`
	DefaultPool *int      `gorm:"column:default_pool;type:int"`
	UpdatedAt   time.Time `gorm:"column:updated_at;type:timestamptz;not null;default:CURRENT_TIMESTAMP"`
}

func (PlatformSettings) TableName() string { return "platform_settings" }

type PlatformSettingsDAO struct{ db *gorm.DB }

func (d PlatformSettingsDAO) Get(ctx context.Context) (*PlatformSettings, error) {
	var r PlatformSettings
	e := d.db.WithContext(ctx).Where("id = true").First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}

func (d PlatformSettingsDAO) Seed(ctx context.Context, budget, maxPerImage, defaultPool int) error {
	x := d.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&PlatformSettings{
		ID: true, Budget: intPtr(budget), MaxPerImage: intPtr(maxPerImage), DefaultPool: intPtr(0),
	})
	return daoError(x.Error)
}

func (d PlatformSettingsDAO) LockForUpdate(ctx context.Context) (*PlatformSettings, error) {
	var r PlatformSettings
	e := d.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = true").First(&r).Error
	if errorsIsNotFound(e) {
		return nil, ErrNotFound
	}
	return &r, daoError(e)
}

func (d PlatformSettingsDAO) Set(ctx context.Context, budget, maxPerImage, defaultPool *int) error {
	x := d.db.WithContext(ctx).Model(&PlatformSettings{}).Where("id = true").
		Updates(map[string]any{
			"budget": budget, "max_per_image": maxPerImage, "default_pool": defaultPool,
			"updated_at": time.Now(),
		})
	if x.RowsAffected != 1 && x.Error == nil {

		return ErrNotFound
	}
	return daoError(x.Error)
}

func intPtr(i int) *int { return &i }

const WarmPoolMaxReplicas = 512

type WarmPoolBudget struct {
	Budget      int
	MaxPerImage int
	DefaultPool int
}

func EffectivePlatformSettings(s *PlatformSettings, def WarmPoolBudget) WarmPoolBudget {
	out := def
	out.DefaultPool = 0
	if s == nil {
		return out
	}
	if s.Budget != nil {
		out.Budget = *s.Budget
	}
	if s.MaxPerImage != nil {
		out.MaxPerImage = *s.MaxPerImage
	}
	return out
}

func warmPoolImages(images []Image) []Image {
	out := make([]Image, 0, len(images))
	for _, img := range images {
		if img.Status != "enabled" || img.Repository == nil || *img.Repository == "" {
			continue
		}
		out = append(out, img)
	}
	return out
}

func WarmPoolWantedTotal(images []Image, b WarmPoolBudget) int {
	total := 0
	for _, img := range warmPoolImages(images) {
		wanted := img.WarmPoolReplicas
		if wanted > b.MaxPerImage {
			wanted = b.MaxPerImage
		}
		total += wanted
	}
	return total
}

func WarmPoolExplicitExceedsMax(images []Image, maxPerImage int) bool {
	for _, img := range warmPoolImages(images) {
		if img.WarmPoolReplicas > maxPerImage {
			return true
		}
	}
	return false
}

func WarmPoolAllocate(images []Image, b WarmPoolBudget) map[string]int32 {
	imgs := warmPoolImages(images)
	sort.SliceStable(imgs, func(i, j int) bool {
		if imgs[i].CreatedAt.Equal(imgs[j].CreatedAt) {
			return imgs[i].ID < imgs[j].ID
		}
		return imgs[i].CreatedAt.Before(imgs[j].CreatedAt)
	})
	out := make(map[string]int32, len(imgs))
	remaining := b.Budget
	for _, img := range imgs {
		wanted := img.WarmPoolReplicas
		if wanted > b.MaxPerImage {
			wanted = b.MaxPerImage
		}
		if wanted > WarmPoolMaxReplicas {
			wanted = WarmPoolMaxReplicas
		}
		if wanted > remaining {
			wanted = remaining
		}
		if wanted < 0 {
			wanted = 0
		}
		out[img.ID] = int32(wanted)
		remaining -= wanted
	}
	return out
}
