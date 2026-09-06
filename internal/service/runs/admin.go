package runs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"agent-platform/model"
)

type CreatedClient struct {
	ClientID   string
	Name       string
	APIKey     string
	APIKeyHash string
}

func (s *Service) CreateClientWithKey(ctx context.Context, name string) (*CreatedClient, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("runs: generate api key: %w", err)
	}
	apiKey := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(apiKey))
	hash := hex.EncodeToString(sum[:])

	var out *CreatedClient
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		c := &model.Client{Name: name}
		if err := d.Clients.Create(ctx, c); err != nil {
			return err
		}
		if err := d.APIKeys.Create(ctx, &model.APIKey{ClientID: c.ID, KeyHash: hash, Active: true}); err != nil {
			return err
		}
		out = &CreatedClient{ClientID: c.ID, Name: c.Name, APIKey: apiKey, APIKeyHash: hash}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type AdminClientRow struct {
	ClientID         string
	Name             string
	ConcurrencyQuota int
	CreatedAt        time.Time
}

func adminClientRow(c model.Client) AdminClientRow {
	return AdminClientRow{ClientID: c.ID, Name: c.Name, ConcurrencyQuota: c.ConcurrencyQuota, CreatedAt: c.CreatedAt}
}

func (s *Service) ListClients(ctx context.Context, limit, offset int) ([]AdminClientRow, error) {
	rows, err := s.store.DAOs().Clients.List(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]AdminClientRow, 0, len(rows))
	for _, c := range rows {
		out = append(out, adminClientRow(c))
	}
	return out, nil
}

type AdminAPIKeyRow struct {
	ID        string
	Label     string
	Active    bool
	CreatedAt time.Time
}

type ClientDetail struct {
	Client AdminClientRow
	Keys   []AdminAPIKeyRow
}

func (s *Service) GetClientDetail(ctx context.Context, clientID string) (*ClientDetail, error) {
	c, err := s.store.DAOs().Clients.Get(ctx, clientID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	keys, err := s.store.DAOs().APIKeys.ListByClient(ctx, clientID)
	if err != nil {
		return nil, err
	}
	d := &ClientDetail{Client: adminClientRow(*c)}
	for _, k := range keys {
		d.Keys = append(d.Keys, AdminAPIKeyRow{ID: k.ID, Label: k.Label, Active: k.Active, CreatedAt: k.CreatedAt})
	}
	return d, nil
}

type AdminImageRow struct {
	RegistrationID   string
	ImageID          string
	Digest           string
	Repository       string
	Status           string
	WarmPoolReplicas int
	ValidationRef    string
	CreatedAt        time.Time
}

func (s *Service) ListClientImages(ctx context.Context, clientID string) ([]AdminImageRow, error) {
	if _, err := s.store.DAOs().Clients.Get(ctx, clientID); err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := s.store.DAOs().Images.ListByClient(ctx, clientID)
	if err != nil {
		return nil, err
	}
	out := make([]AdminImageRow, 0, len(rows))
	for _, img := range rows {
		out = append(out, AdminImageRow{
			RegistrationID: img.ID, ImageID: img.ImageID, Digest: img.Digest, Repository: func() string {
				if img.Repository != nil {
					return *img.Repository
				}
				return ""
			}(),
			Status: img.Status, WarmPoolReplicas: img.WarmPoolReplicas,
			ValidationRef: img.ValidationRef, CreatedAt: img.CreatedAt,
		})
	}
	return out, nil
}

type AdminRunRow struct {
	RunID        string
	ReqID        string
	Status       string
	CurrentStage int
	CreatedAt    time.Time
	TerminalAt   *time.Time
}

func (s *Service) ListClientRuns(ctx context.Context, clientID string, limit, offset int) ([]AdminRunRow, error) {
	if _, err := s.store.DAOs().Clients.Get(ctx, clientID); err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := s.store.DAOs().Runs.List(ctx, clientID, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]AdminRunRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, AdminRunRow{
			RunID: r.ID, ReqID: r.ReqID, Status: r.Status,
			CurrentStage: r.CurrentStage, CreatedAt: r.CreatedAt, TerminalAt: r.TerminalAt,
		})
	}
	return out, nil
}

type AdminNetworkStatus struct {
	ActiveRevision    *int64
	ActiveRevisionID  *string
	DesiredRevisionID *string
	RolloutStatus     string
	ErrorSummary      string
	UpdatedAt         *time.Time
}

func (s *Service) PlatformNetworkStatus(ctx context.Context) (*AdminNetworkStatus, error) {
	head, err := s.store.DAOs().PlatformNetwork.GetHead(ctx)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return &AdminNetworkStatus{}, nil
		}
		return nil, err
	}
	out := &AdminNetworkStatus{
		ActiveRevisionID:  head.ActiveRevisionID,
		DesiredRevisionID: head.DesiredRevisionID,
		RolloutStatus:     head.RolloutStatus,
		ErrorSummary:      head.ErrorSummary,
		UpdatedAt:         &head.UpdatedAt,
	}
	if head.ActiveRevisionID != nil {
		base, berr := s.store.DAOs().PlatformNetwork.Get(ctx, *head.ActiveRevisionID)
		if berr == nil {
			out.ActiveRevision = &base.Revision
		} else if !errors.Is(berr, model.ErrNotFound) {
			return nil, berr
		}
	}
	return out, nil
}

type AdminWarmPoolStatus struct {
	Budget      int
	MaxPerImage int
	DefaultPool int
	Configured  bool
}

func (s *Service) PlatformWarmPoolStatus(ctx context.Context) (*AdminWarmPoolStatus, error) {
	settings, err := s.store.DAOs().Platform.Get(ctx)
	configured := err == nil
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	eff := model.EffectivePlatformSettings(settings, model.WarmPoolBudget{
		Budget: s.warmPool.Budget, MaxPerImage: s.warmPool.MaxPerImage, DefaultPool: s.warmPool.DefaultPool,
	})
	return &AdminWarmPoolStatus{
		Budget: eff.Budget, MaxPerImage: eff.MaxPerImage, DefaultPool: eff.DefaultPool, Configured: configured,
	}, nil
}
