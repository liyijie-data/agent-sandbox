package images

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/model"
)

const (
	StatusValidating = "validating"
	StatusEnabled    = "enabled"
	StatusDisabled   = "disabled"
	StatusRevoked    = "revoked"
)

type Service struct {
	store       *model.Store
	verifier    Verifier
	coordinator RevokeCoordinator
	log         *slog.Logger
}

func New(store *model.Store, verifier Verifier, log *slog.Logger) *Service {
	if verifier == nil {
		verifier = FailClosedVerifier{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, verifier: verifier, log: log}
}

func (s *Service) WithRevokeCoordinator(c RevokeCoordinator) *Service {
	s.coordinator = c
	return s
}

type RegisterInput struct {
	ClientID   string
	ImageID    string
	Digest     string
	Repository string
	Manifest   *contracts.Manifest

	WarmPoolReplicas int
}

type Registration struct {
	ID              string
	ClientID        string
	ImageID         string
	Digest          string
	Repository      string
	ContractVersion string
	Capabilities    []string
	Entrypoint      []string
	StateFormat     string
	Status          string
	ValidationRef   string

	WarmPoolReplicas int
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (*Registration, bool, error) {
	if err := contracts.ValidateImageDigest(in.Digest); err != nil {
		return nil, false, ErrInvalidDigest
	}
	in.Repository = strings.TrimSpace(in.Repository)
	if in.Repository == "" || strings.ContainsAny(in.Repository, "@ \t\r\n") {
		return nil, false, ErrConflict
	}
	if _, err := s.store.DAOs().Clients.Get(ctx, in.ClientID); err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, false, ErrNotFound
		}
		return nil, false, err
	}
	if err := s.verifier.Verify(ctx, in.Digest); err != nil {
		return nil, false, ErrSignatureInvalid
	}

	existing, err := s.store.DAOs().Images.GetByClientAndImageID(ctx, in.ClientID, in.ImageID)
	switch {
	case err == nil:
		return s.reconcile(ctx, existing, in)
	case !errors.Is(err, model.ErrNotFound):
		return nil, false, err
	}

	capsJSON, err := json.Marshal(in.Manifest.Capabilities)
	if err != nil {
		return nil, false, err
	}
	entryJSON, err := json.Marshal(in.Manifest.Entrypoint)
	if err != nil {
		return nil, false, err
	}
	row := &model.Image{
		ClientID:         in.ClientID,
		ImageID:          in.ImageID,
		Digest:           in.Digest,
		Repository:       &in.Repository,
		ContractVersion:  in.Manifest.ContractVersion,
		Capabilities:     string(capsJSON),
		Entrypoint:       string(entryJSON),
		StateFormat:      in.Manifest.StateFormat,
		Status:           StatusValidating,
		WarmPoolReplicas: in.WarmPoolReplicas,
	}
	if err := s.store.DAOs().Images.Create(ctx, row); err != nil {
		if model.IsDuplicate(err) {

			winner, ge := s.store.DAOs().Images.GetByClientAndImageID(ctx, in.ClientID, in.ImageID)
			if ge != nil {
				return nil, false, ge
			}
			return s.reconcile(ctx, winner, in)
		}
		return nil, false, err
	}
	return registrationFrom(row), true, nil
}

func (s *Service) reconcile(ctx context.Context, existing *model.Image, in RegisterInput) (*Registration, bool, error) {
	if existing.Digest != in.Digest || existing.Repository == nil || *existing.Repository != in.Repository ||
		existing.ContractVersion != in.Manifest.ContractVersion ||
		existing.StateFormat != in.Manifest.StateFormat ||
		!jsonEqual(existing.Capabilities, in.Manifest.Capabilities) ||
		!jsonEqual(existing.Entrypoint, in.Manifest.Entrypoint) {
		return nil, false, ErrConflict
	}
	if existing.WarmPoolReplicas != in.WarmPoolReplicas {
		v := in.WarmPoolReplicas
		if ok, err := s.store.DAOs().Images.UpdateIf(ctx, existing.ID, model.ImageCondition{},
			model.ImagePatch{WarmPoolReplicas: &v}); err != nil {
			return nil, false, err
		} else if !ok {
			return nil, false, ErrStateConflict
		}
		existing.WarmPoolReplicas = in.WarmPoolReplicas
	}
	return registrationFrom(existing), false, nil
}

func (s *Service) GetRegistration(ctx context.Context, registrationID string) (*Registration, error) {
	row, err := s.store.DAOs().Images.Get(ctx, registrationID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return registrationFrom(row), nil
}

type Evidence struct {
	Digest        string
	ValidationRef string
}

func (s *Service) Enable(ctx context.Context, clientID, imageID string, ev Evidence) (*Registration, error) {
	row, err := s.requireImage(ctx, clientID, imageID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(ev.ValidationRef) == "" || ev.Digest != row.Digest {
		return nil, ErrVerificationRequired
	}
	if row.Status == StatusRevoked {
		return nil, ErrStateConflict
	}
	if row.Status == StatusEnabled && row.ValidationRef == ev.ValidationRef {
		return registrationFrom(row), nil
	}
	status := StatusEnabled
	ok, err := s.store.DAOs().Images.UpdateIf(ctx, row.ID, model.ImageCondition{Status: &row.Status},
		model.ImagePatch{Status: &status, ValidationRef: &ev.ValidationRef})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrStateConflict
	}
	out := registrationFrom(row)
	out.Status = status
	out.ValidationRef = ev.ValidationRef
	return out, nil
}

func (s *Service) Disable(ctx context.Context, clientID, imageID string) (*Registration, error) {
	row, err := s.requireImage(ctx, clientID, imageID)
	if err != nil {
		return nil, err
	}
	if row.Status == StatusRevoked {
		return nil, ErrStateConflict
	}
	if row.Status == StatusDisabled {
		return registrationFrom(row), nil
	}
	return s.transition(ctx, row, StatusDisabled)
}

func (s *Service) Revoke(ctx context.Context, clientID, imageID string) (*Registration, error) {
	row, err := s.requireImage(ctx, clientID, imageID)
	if err != nil {
		return nil, err
	}
	if row.Status == StatusRevoked {
		return registrationFrom(row), nil
	}
	out, err := s.transition(ctx, row, StatusRevoked)
	if err != nil {
		return nil, err
	}
	if s.coordinator != nil {
		if cerr := s.coordinator.CancelImageRuns(ctx, clientID, row.ID); cerr != nil {
			s.log.Error("images: revoke run cancellation failed",
				"client_id", clientID, "image_id", imageID, "error", cerr)
		}
	}
	return out, nil
}

func (s *Service) requireImage(ctx context.Context, clientID, imageID string) (*model.Image, error) {
	row, err := s.store.DAOs().Images.GetByClientAndImageID(ctx, clientID, imageID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return row, nil
}

func (s *Service) IsEnabledFor(ctx context.Context, clientID, imageID string) (bool, error) {
	row, err := s.requireImage(ctx, clientID, imageID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return row.Status == StatusEnabled, nil
}

func (s *Service) GetEnabledRegistration(ctx context.Context, clientID, imageID string) (*Registration, error) {
	row, err := s.requireImage(ctx, clientID, imageID)
	if err != nil {
		return nil, err
	}
	if row.Status != StatusEnabled {
		return nil, ErrNotFound
	}
	return registrationFrom(row), nil
}

func (s *Service) transition(ctx context.Context, row *model.Image, status string) (*Registration, error) {
	ok, err := s.store.DAOs().Images.UpdateIf(ctx, row.ID,
		model.ImageCondition{Status: &row.Status}, model.ImagePatch{Status: &status})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrStateConflict
	}
	out := registrationFrom(row)
	out.Status = status
	return out, nil
}

func registrationFrom(row *model.Image) *Registration {
	var caps, entry []string
	_ = json.Unmarshal([]byte(row.Capabilities), &caps)
	_ = json.Unmarshal([]byte(row.Entrypoint), &entry)
	return &Registration{
		ID:       row.ID,
		ClientID: row.ClientID,
		ImageID:  row.ImageID,
		Digest:   row.Digest,
		Repository: func() string {
			if row.Repository != nil {
				return *row.Repository
			}
			return ""
		}(),
		ContractVersion:  row.ContractVersion,
		Capabilities:     caps,
		Entrypoint:       entry,
		StateFormat:      row.StateFormat,
		Status:           row.Status,
		ValidationRef:    row.ValidationRef,
		WarmPoolReplicas: row.WarmPoolReplicas,
	}
}

func jsonEqual(stored string, want []string) bool {
	var got []string
	if err := json.Unmarshal([]byte(stored), &got); err != nil {
		return false
	}
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
