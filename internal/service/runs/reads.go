package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/storage"
	"agent-platform/model"
)

func (s *Service) RunClientID(ctx context.Context, runID string) (string, error) {
	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil {
		return "", ErrNotFound
	}
	return r.ClientID, nil
}

func (s *Service) AuthenticateClientKey(ctx context.Context, clientID, rawKey string) (bool, error) {
	sum := sha256.Sum256([]byte(rawKey))
	hexHash := hex.EncodeToString(sum[:])
	k, err := s.store.DAOs().APIKeys.FindByClientAndHash(ctx, clientID, hexHash)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if !storage.ConstantTimeEqual([]byte(k.KeyHash), []byte(hexHash)) || !k.Active {
		return false, nil
	}
	return true, nil
}

func (s *Service) AuthenticateClient(ctx context.Context, rawKey string) (string, error) {
	if rawKey == "" {
		return "", ErrNotFound
	}
	sum := sha256.Sum256([]byte(rawKey))
	hexHash := hex.EncodeToString(sum[:])
	k, err := s.store.DAOs().APIKeys.FindByHash(ctx, hexHash)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	return k.ClientID, nil
}

type PendingInput struct {
	InputID   string
	Kind      string
	Prompt    string
	Options   []contracts.Choice
	WaitAt    time.Time
	ExpiresAt time.Time
	Readable  bool
}

func (s *Service) PendingInput(ctx context.Context, runID string) (*PendingInput, error) {
	inp, err := s.store.DAOs().Inputs.LatestPending(ctx, runID, "pending")
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	pi := &PendingInput{InputID: inp.InputID, Kind: inp.Kind, WaitAt: inp.CreatedAt, ExpiresAt: inp.InputDeadline}
	if len(inp.Options) > 0 {
		var opts []contracts.Choice
		if err := json.Unmarshal([]byte(inp.Options), &opts); err != nil {
			return nil, fmt.Errorf("runs: decode pending options: %w", err)
		}
		pi.Options = opts
	}
	if inp.AnswerContentID != nil {
		ct, err := s.store.DAOs().Contents.Get(ctx, *inp.AnswerContentID)
		if err != nil {
			return nil, err
		}
		if ct.ExpiresAt != nil && !ct.ExpiresAt.After(s.clock.Now()) {
			return pi, nil
		}
		plain, err := s.cipher.Decrypt(ContentQuestion, ct.Ciphertext, ct.KeyVersion)
		if err != nil {
			return nil, fmt.Errorf("runs: decrypt pending prompt: %w", err)
		}
		pi.Prompt = string(plain)
		pi.Readable = true
	}
	return pi, nil
}

type InputMeta struct {
	Kind    string
	Options []string
}

func (s *Service) InputMeta(ctx context.Context, runID, inputID string) (*InputMeta, error) {
	inp, err := s.store.DAOs().Inputs.Get(ctx, runID, inputID)
	if err != nil {
		return nil, ErrNotFound
	}
	m := &InputMeta{Kind: inp.Kind}
	if len(inp.Options) > 0 {
		var opts []contracts.Choice
		if err := json.Unmarshal([]byte(inp.Options), &opts); err != nil {
			return nil, fmt.Errorf("runs: decode input options: %w", err)
		}
		for _, o := range opts {
			if o.Value != "" {
				m.Options = append(m.Options, o.Value)
			}
		}
	}
	return m, nil
}

type ExecutionIdentity struct {
	RunID   string
	StageNo int
	Fence   int64
	Status  string
}

func (s *Service) ExecutionIdentity(ctx context.Context, executionID string) (*ExecutionIdentity, error) {
	st, err := s.store.DAOs().Stages.GetByExecution(ctx, executionID)
	if err != nil {
		return nil, ErrNotFound
	}
	r, err := s.store.DAOs().Runs.Get(ctx, st.RunID)
	if err != nil {
		return nil, ErrNotFound
	}
	return &ExecutionIdentity{RunID: st.RunID, StageNo: st.StageNo, Fence: st.Fence, Status: r.Status}, nil
}

func (s *Service) StatusOf(ctx context.Context, runID string) (contracts.RunStatus, error) {
	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil {
		return "", ErrNotFound
	}
	return contracts.RunStatus(r.Status), nil
}

func (s *Service) InputDeadlineOf(ctx context.Context, runID string) (time.Time, error) {
	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil {
		return time.Time{}, ErrNotFound
	}
	return s.policy.InputDeadline(s.clock.Now(), r.TotalLifecycleDeadline), nil
}

func (s *Service) LoadFrozenConfig(ctx context.Context, runID string) ([]byte, error) {
	rows, err := s.store.DAOs().Contents.ListByRunAndKind(ctx, runID, ContentConfig, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	plain, err := s.cipher.Decrypt(ContentConfig, rows[0].Ciphertext, rows[0].KeyVersion)
	if err != nil {
		return nil, fmt.Errorf("runs: decrypt frozen config: %w", err)
	}
	return plain, nil
}

func (s *Service) LoadFrozenImageFacts(ctx context.Context, runID string) (entrypoint []string, imageRef string, err error) {
	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil {
		return nil, "", ErrNotFound
	}
	if r.ImageRegistrationID == nil {
		return nil, "", ErrNotFound
	}
	reg, err := s.store.DAOs().Images.Get(ctx, *r.ImageRegistrationID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	if reg.Repository == nil || *reg.Repository == "" {
		return nil, "", ErrNotFound
	}
	var entry []string
	if len(reg.Entrypoint) > 0 {
		if err := json.Unmarshal([]byte(reg.Entrypoint), &entry); err != nil {
			return nil, "", fmt.Errorf("runs: decode frozen entrypoint: %w", err)
		}
	}
	return entry, *reg.Repository + "@" + reg.Digest, nil
}

func (s *Service) FrozenModel(ctx context.Context, runID string) (contracts.ModelSpec, error) {
	plain, err := s.LoadFrozenConfig(ctx, runID)
	if err != nil {
		return contracts.ModelSpec{}, err
	}
	if len(plain) == 0 {
		return contracts.ModelSpec{}, ErrNotFound
	}
	var cfg contracts.CreateRunRequest
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return contracts.ModelSpec{}, fmt.Errorf("runs: decode frozen config for model: %w", err)
	}
	if cfg.Model.Name == "" || cfg.Model.BaseURL == "" || cfg.Model.Access.APIKey == "" {
		return contracts.ModelSpec{}, ErrNotFound
	}
	if refresh, err := s.LoadAccessRefresh(ctx, runID); err != nil {
		return contracts.ModelSpec{}, err
	} else if refresh != nil && refresh.Model != nil {
		cfg.Model.Access = *refresh.Model
	}
	return cfg.Model, nil
}

func (s *Service) FrozenModelName(ctx context.Context, runID string) (string, error) {
	model, err := s.FrozenModel(ctx, runID)
	if err != nil {
		return "", err
	}
	return model.Name, nil
}

func (s *Service) LoadRunResult(ctx context.Context, runID string) (*contracts.RuntimeResult, error) {
	rows, err := s.store.DAOs().Contents.ListByRunAndKind(ctx, runID, ContentResult, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if rows[0].ExpiresAt != nil && !rows[0].ExpiresAt.After(s.clock.Now()) {
		return nil, nil
	}
	plain, err := s.cipher.Decrypt(ContentResult, rows[0].Ciphertext, rows[0].KeyVersion)
	if err != nil {
		return nil, fmt.Errorf("runs: decrypt run result: %w", err)
	}
	var res contracts.RuntimeResult
	if err := json.Unmarshal(plain, &res); err != nil {
		return nil, fmt.Errorf("runs: decode run result: %w", err)
	}
	return &res, nil
}

func (s *Service) ContentAvailability(ctx context.Context, runID string) (available bool, expiresAt *time.Time, err error) {
	live, err := s.store.DAOs().Contents.CountLiveByRun(ctx, runID, s.clock.Now())
	if err != nil {
		return false, nil, err
	}
	if live == 0 {
		return false, nil, nil
	}
	rows, err := s.store.DAOs().Contents.ListByRun(ctx, runID, 100, 0)
	if err != nil {
		return false, nil, err
	}
	var earliest *time.Time
	for i := range rows {
		if rows[i].ExpiresAt == nil {
			continue
		}
		if earliest == nil || rows[i].ExpiresAt.Before(*earliest) {
			e := *rows[i].ExpiresAt
			earliest = &e
		}
	}
	return true, earliest, nil
}
