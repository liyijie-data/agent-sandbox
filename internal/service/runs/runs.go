package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/lifecycle"
	"agent-platform/internal/storage"
	"agent-platform/model"
)

const (
	ContentMessages = "run.messages"
	ContentAnswer   = "run.answer"
	ContentQuestion = "run.question"
	ContentResult   = "run.result"
	ContentConfig   = "run.config"
)

var (
	ErrNotFound         = errors.New("runs: not found")
	ErrReqIDConflict    = errors.New("runs: req_id conflict")
	ErrRunStateConflict = errors.New("runs: run state conflict")
	ErrAnswerConflict   = errors.New("runs: answer conflict")
	ErrInputExpired     = errors.New("runs: input expired")
)

var errAbort = errors.New("runs: race lost")

type Service struct {
	store  *model.Store
	policy lifecycle.Policy
	clock  lifecycle.Clock
	cipher *storage.ContentCipher

	warmPool warmPoolDefaults
}

func New(store *model.Store, cipher *storage.ContentCipher, policy lifecycle.Policy, clock lifecycle.Clock) *Service {
	if clock == nil {
		clock = lifecycle.SystemClock
	}
	return &Service{store: store, policy: policy, clock: clock, cipher: cipher,
		warmPool: defaultWarmPoolFallback}
}

type RunRecord struct {
	ID            string
	ClientID      string
	ReqID         string
	Status        contracts.RunStatus
	CurrentStage  int
	TotalDeadline time.Time
	CreatedAt     time.Time
	TerminalAt    *time.Time
	CleanupStatus string
	MetadataExpAt *time.Time
}

type CreateRunInput struct {
	ClientID    string
	ReqID       string
	Fingerprint []byte
	Messages    []byte
	Config      []byte

	ImageRegistrationID *string
	NetworkRevisionID   *string
}

type CreateRunResult struct {
	Run        *RunRecord
	Idempotent bool
}

func (s *Service) FingerprintCreate(normalized []byte) []byte {
	return s.cipher.Fingerprint("run.create", normalized)
}

func (s *Service) CreateRun(ctx context.Context, in CreateRunInput) (*CreateRunResult, error) {
	now := s.clock.Now()
	total := s.policy.TotalLifecycleDeadline(now)

	var created *model.Run
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		r := &model.Run{
			ClientID: in.ClientID, ReqID: in.ReqID, ReqFingerprint: in.Fingerprint,
			ImageRegistrationID: in.ImageRegistrationID, NetworkRevisionID: in.NetworkRevisionID,
			TotalLifecycleDeadline: total, Status: string(contracts.RunStatusQueued), CurrentStage: 1,
		}
		if err := d.Runs.Create(ctx, r); err != nil {
			if model.IsDuplicate(err) {
				return errAbort
			}
			return err
		}
		if err := d.Stages.Create(ctx, &model.RunStage{
			RunID: r.ID, StageNo: 1, Fence: 1, Phase: "preparing",
			BudgetRemainingSec: int64(s.policy.ExecutionBudget.Seconds()),
		}); err != nil {
			return err
		}
		msgID, err := s.putContent(ctx, d, r.ID, 1, ContentMessages, in.Messages, nil)
		if err != nil {
			return err
		}

		if len(in.Config) > 0 {
			cfgID, cerr := s.putContent(ctx, d, r.ID, 1, ContentConfig, in.Config, nil)
			if cerr != nil {
				return cerr
			}
			_ = cfgID
		}
		if err := s.enqueueOutbox(ctx, d, r.ID, 1, contracts.EventRunQueued, now, msgID); err != nil {
			return err
		}
		created = r
		return nil
	})
	if errors.Is(err, errAbort) {
		existing, e := s.store.DAOs().Runs.GetByClientAndRequest(ctx, in.ClientID, in.ReqID)
		if e != nil {
			return nil, ErrNotFound
		}
		if len(existing.ReqFingerprint) != len(in.Fingerprint) ||
			!storage.ConstantTimeEqual(existing.ReqFingerprint, in.Fingerprint) {
			return nil, ErrReqIDConflict
		}
		return &CreateRunResult{Run: recordFrom(existing), Idempotent: true}, nil
	}
	if err != nil {
		return nil, err
	}
	return &CreateRunResult{Run: &RunRecord{
		ID: created.ID, ClientID: created.ClientID, ReqID: created.ReqID,
		Status: contracts.RunStatusQueued, CurrentStage: 1, TotalDeadline: total, CreatedAt: now,
	}, Idempotent: false}, nil
}

func (s *Service) GetRun(ctx context.Context, clientID, runID string) (*RunRecord, error) {
	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil {
		return nil, ErrNotFound
	}
	if r.ClientID != clientID {
		return nil, ErrNotFound
	}
	rec := recordFrom(r)
	rec.MetadataExpAt = r.MetadataExpiresAt
	return rec, nil
}

func (s *Service) StageBudgetRemaining(ctx context.Context, runID string, stageNo int) (int64, error) {
	st, err := s.store.DAOs().Stages.Get(ctx, runID, stageNo)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return st.BudgetRemainingSec, nil
}

type StageResult struct {
	RunID         string
	StageNo       int
	Fence         int64
	Owner         string
	Phase         string
	Attempt       int
	LaunchStarted bool
	ExecutionID   string

	PoolName string

	ImageRef string
}

func (s *Service) ClaimStage(ctx context.Context, clientID, runID string, stageNo int, owner, executionID string) (*StageResult, error) {
	now := s.clock.Now()
	var res *StageResult
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		r, err := d.Runs.Get(ctx, runID)
		if err != nil {
			return err
		}
		if r.ClientID != clientID || lifecycle.IsTerminalStatus(contracts.RunStatus(r.Status)) {
			return errAbort
		}
		if !r.TotalLifecycleDeadline.After(now) {
			return errAbort
		}

		client, err := d.Clients.GetForUpdate(ctx, r.ClientID)
		if err != nil {
			return err
		}
		if client.ConcurrencyQuota > 0 {
			active, cerr := d.Runs.CountActiveExecutions(ctx, r.ClientID, now)
			if cerr != nil {
				return cerr
			}
			if active >= client.ConcurrencyQuota {
				return errAbort
			}
		}

		profile, err := s.freezeProfileForClaim(ctx, d, r, now)
		if err != nil {
			return err
		}
		st, ok, err := d.Stages.Claim(ctx, runID, stageNo, owner, executionID, now, now.Add(s.policy.QueueBudget))
		if err != nil {
			return err
		}
		if !ok {
			return errAbort
		}

		if ok, uerr := d.Runs.UpdateIf(ctx, runID,
			model.RunCondition{StatusIn: []string{string(contracts.RunStatusQueued), string(contracts.RunStatusPreparing)}},
			model.RunPatch{Status: model.Optional[string]{Set: true, Value: string(contracts.RunStatusPreparing)}}); uerr != nil {
			return uerr
		} else if !ok {
			return errAbort
		}
		if profile != nil {

			if ok, uerr := d.Stages.UpdateIf(ctx, runID, stageNo, model.RunStageCondition{},
				model.RunStagePatch{RuntimeProfileID: &profile.ID}); uerr != nil {
				return uerr
			} else if !ok {
				return errAbort
			}
			st.RuntimeProfileID = &profile.ID
		}
		res = &StageResult{RunID: st.RunID, StageNo: st.StageNo, Fence: st.Fence, Owner: st.Owner,
			Phase: st.Phase, Attempt: st.Attempt, LaunchStarted: st.LaunchStarted, ExecutionID: st.ExecutionID}
		if profile != nil {
			res.PoolName = profile.WarmPoolName
			res.ImageRef = profile.ImageRef
		}
		return nil
	})
	if errors.Is(err, errAbort) {
		return nil, ErrRunStateConflict
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Service) freezeProfileForClaim(ctx context.Context, d model.DAOs, r *model.Run, now time.Time) (*model.RuntimeProfile, error) {
	if r.ImageRegistrationID == nil {
		return nil, nil
	}
	head, err := d.PlatformNetwork.GetHead(ctx)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, errAbort
		}
		return nil, err
	}
	if head.ActiveRevisionID == nil {
		return nil, errAbort
	}
	profile, err := d.RuntimeProfiles.GetByTriple(ctx, *r.ImageRegistrationID, *head.ActiveRevisionID, r.NetworkRevisionID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, errAbort
		}
		return nil, err
	}
	if profile.Status != "ready" {
		return nil, errAbort
	}
	return profile, nil
}

func (s *Service) HeartbeatStage(ctx context.Context, runID string, stageNo int, fence int64, owner string) (bool, error) {
	now := s.clock.Now()
	return s.store.DAOs().Stages.Heartbeat(ctx, runID, stageNo, fence, owner, now, now.Add(s.policy.QueueBudget))
}

func (s *Service) MarkLaunchStarted(ctx context.Context, runID string, stageNo int, fence int64, owner string) (bool, error) {
	now := s.clock.Now()
	var ok bool
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		r, err := d.Runs.Get(ctx, runID)
		if err != nil {
			return err
		}
		st := contracts.RunStatus(r.Status)
		if st != contracts.RunStatusPreparing {
			return errAbort
		}
		if !r.TotalLifecycleDeadline.After(now) {
			return errAbort
		}
		ok, err = d.Stages.MarkLaunch(ctx, runID, stageNo, fence, owner, now)
		if err == nil && ok {
			ok, err = d.Runs.UpdateIf(ctx, runID,
				model.RunCondition{Status: strPtr(string(contracts.RunStatusPreparing)), CurrentStage: &stageNo},
				model.RunPatch{Status: model.Optional[string]{Set: true, Value: string(contracts.RunStatusRunning)}})
			if err == nil && !ok {
				return errAbort
			}
		}
		return err
	})
	if errors.Is(err, errAbort) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ok, nil
}

type PauseInput struct {
	RunID         string
	StageNo       int
	Fence         int64
	Owner         string
	InputID       string
	Kind          string
	Prompt        []byte
	Options       []byte
	Checkpoint    Checkpoint
	InputDeadline time.Time
}

type Checkpoint struct {
	RunID       string
	StageNo     int
	Fence       int64
	ObjectRef   string
	SHA256      string
	SizeBytes   int64
	StateFormat string
}

func (s *Service) PauseToAwaiting(ctx context.Context, p PauseInput) (bool, error) {
	now := s.clock.Now()
	var okResult bool
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		r, err := d.Runs.Get(ctx, p.RunID)
		if err != nil {
			return err
		}
		st := contracts.RunStatus(r.Status)
		if st != contracts.RunStatusRunning {
			return errAbort
		}
		if !r.TotalLifecycleDeadline.After(now) {
			return errAbort
		}

		insertDeadline := p.InputDeadline
		if insertDeadline.IsZero() {
			insertDeadline = s.policy.InputDeadline(now, r.TotalLifecycleDeadline)
		}

		leaseOK, err := d.Stages.Heartbeat(ctx, p.RunID, p.StageNo, p.Fence, p.Owner, now, now.Add(s.policy.QueueBudget))
		if err != nil {
			return err
		}
		if !leaseOK {
			return errAbort
		}

		cp := &model.Checkpoint{RunID: p.RunID, StageNo: p.StageNo, Fence: p.Fence,
			ObjectRef: p.Checkpoint.ObjectRef, SHA256: p.Checkpoint.SHA256, SizeBytes: p.Checkpoint.SizeBytes,
			StateFormat: p.Checkpoint.StateFormat, Verified: true}
		if err := d.Checkpoints.Create(ctx, cp); err != nil {
			return err
		}

		options := p.Options
		if len(options) == 0 {
			options = []byte("[]")
		}
		if err := s.chargeStageTime(ctx, d, p.RunID, p.StageNo, p.Fence, p.Owner, now); err != nil {
			return err
		}

		contentID, err := s.putContent(ctx, d, p.RunID, p.StageNo, ContentQuestion, p.Prompt, nil)
		if err != nil {
			return err
		}
		if err := d.Inputs.Create(ctx, &model.RunInput{
			RunID: p.RunID, StageNo: p.StageNo, InputID: p.InputID, Kind: p.Kind,
			State: "pending", CheckpointID: &cp.ID, AnswerContentID: &contentID,
			InputDeadline: insertDeadline, Options: string(options),
		}); err != nil {
			return err
		}
		if ok, err := d.Runs.UpdateIf(ctx, p.RunID,
			model.RunCondition{Status: strPtr(string(contracts.RunStatusRunning))},
			model.RunPatch{Status: model.Optional[string]{Set: true, Value: string(contracts.RunStatusAwaitingInput)}}); err != nil {
			return err
		} else if !ok {
			return errAbort
		}
		if err := s.enqueueOutbox(ctx, d, p.RunID, p.StageNo, contracts.EventInputRequested, now, contentID); err != nil {
			return err
		}
		okResult = true
		return nil
	})
	if errors.Is(err, errAbort) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return okResult, nil
}

type ResumeMaterial struct {
	Checkpoint Checkpoint
	InputID    string
	Answer     []byte
}

func (s *Service) LatestResumeMaterial(ctx context.Context, runID string) (*ResumeMaterial, error) {
	rr, err := s.store.DAOs().Checkpoints.LatestResume(ctx, runID, ContentAnswer)
	if err != nil {
		return nil, ErrNotFound
	}
	if rr.InputState != "answered" {
		return nil, fmt.Errorf("runs: checkpoint input is not answered")
	}
	answer, err := s.cipher.Decrypt(ContentAnswer, rr.Ciphertext, rr.KeyVersion)
	if err != nil {
		return nil, fmt.Errorf("runs: decrypt resume answer: %w", err)
	}
	return &ResumeMaterial{
		Checkpoint: Checkpoint{
			RunID: rr.RunID, StageNo: rr.StageNo, Fence: rr.Fence, ObjectRef: rr.ObjectRef,
			SHA256: rr.SHA256, SizeBytes: rr.SizeBytes, StateFormat: rr.StateFormat,
		},
		InputID: rr.InputID,
		Answer:  answer,
	}, nil
}

const ContentAccessRefresh = "run.access_refresh"

const ResourceRuntimeProfile = "runtime_profile"

func (s *Service) CancelImageRuns(ctx context.Context, clientID, imageRegID string) error {
	ids, err := s.store.DAOs().Runs.ListNonTerminalByImage(ctx, imageRegID, 100)
	if err != nil {
		return err
	}
	for _, runID := range ids {
		if _, err := s.CancelRun(ctx, clientID, runID); err != nil {

			if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrRunStateConflict) {
				return err
			}
		}
	}
	return nil
}

func (s *Service) ChargeModelRequest(ctx context.Context, runID string, maxRequests int64) (count int64, ok bool, err error) {
	count, ok, err = s.store.DAOs().Runs.ChargeModelRequest(ctx, runID, s.clock.Now())
	if err != nil {
		return 0, false, err
	}
	return count, ok, nil
}

func (s *Service) LoadAccessRefresh(ctx context.Context, runID string) (*contracts.AccessRefreshTargets, error) {
	rows, err := s.store.DAOs().Contents.ListByRunAndKind(ctx, runID, ContentAccessRefresh, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	plain, err := s.cipher.Decrypt(ContentAccessRefresh, rows[0].Ciphertext, rows[0].KeyVersion)
	if err != nil {
		return nil, fmt.Errorf("runs: decrypt access refresh: %w", err)
	}
	var ar contracts.AccessRefreshTargets
	if err := json.Unmarshal(plain, &ar); err != nil {
		return nil, fmt.Errorf("runs: decode access refresh: %w", err)
	}
	return &ar, nil
}

func (s *Service) EnqueueImageProfileCleanup(ctx context.Context, imageRegID string, dueAt time.Time) error {
	profiles, err := s.store.DAOs().RuntimeProfiles.ListByImage(ctx, imageRegID)
	if err != nil {
		return err
	}
	refs := make([]string, 0, len(profiles))
	for i := range profiles {
		refs = append(refs, profiles[i].ID)
	}
	_, err = s.enqueueCleanupJobs(ctx, ResourceRuntimeProfile, refs, dueAt)
	return err
}

func (s *Service) EnqueueOrphanObjects(ctx context.Context, objectKeys []string, dueAt time.Time) (int, error) {
	return s.enqueueCleanupJobs(ctx, "orphan_object", objectKeys, dueAt)
}

func (s *Service) enqueueCleanupJobs(ctx context.Context, kind string, refs []string, dueAt time.Time) (int, error) {
	if len(refs) == 0 {
		return 0, nil
	}
	created := 0
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		for _, ref := range refs {
			existing, err := d.CleanupJobs.ListByKindAndRef(ctx, kind, ref)
			if err != nil {
				return err
			}
			scheduled := false
			for i := range existing {
				if existing[i].Status != "completed" {
					scheduled = true
					break
				}
			}
			if scheduled {
				continue
			}
			if err := d.CleanupJobs.Create(ctx, &model.CleanupJob{
				ResourceKind: kind, InternalRef: ref, DueAt: dueAt, Status: "pending",
			}); err != nil {
				if model.IsDuplicate(err) {
					continue
				}
				return err
			}
			created++
		}
		return nil
	})
	return created, err
}

const WarmPoolMaxReplicas = model.WarmPoolMaxReplicas

var (
	ErrWarmPoolBudgetConflict      = errors.New("runs: warm pool budget would be exceeded")
	ErrWarmPoolMaxPerImageConflict = errors.New("runs: warm pool max_per_image would be exceeded")
)

type warmPoolDefaults struct {
	Budget      int
	MaxPerImage int
	DefaultPool int
}

var defaultWarmPoolFallback = warmPoolDefaults{Budget: 4, MaxPerImage: 2, DefaultPool: 0}

func (s *Service) WithWarmPoolDefaults(budget, maxPerImage, defaultPool int) *Service {
	s.warmPool = warmPoolDefaults{Budget: budget, MaxPerImage: maxPerImage, DefaultPool: defaultPool}
	return s
}

type QueueProgress struct {
	QueuePosition int
	QueueLength   int
	WaitReason    string
}

const (
	WaitPoolUnready   = "pool_unready"
	WaitInClientQueue = "in_client_queue"
	WaitQuotaExceeded = "quota_exceeded"
)

func (s *Service) QueueProgress(ctx context.Context, runID string) (*QueueProgress, error) {
	r, err := s.store.DAOs().Runs.Get(ctx, runID)
	if err != nil {
		return nil, ErrNotFound
	}
	if contracts.RunStatus(r.Status) != contracts.RunStatusQueued {
		return nil, nil
	}
	queued, err := s.store.DAOs().Runs.ListQueuedByClient(ctx, r.ClientID)
	if err != nil {
		return nil, err
	}
	p := &QueueProgress{QueueLength: len(queued)}
	for i := range queued {
		if queued[i].ID == runID {
			p.QueuePosition = i + 1
			break
		}
	}

	if r.ImageRegistrationID != nil {
		if unready, err := s.profileUnready(ctx, r); err != nil {
			return nil, err
		} else if unready {
			p.WaitReason = WaitPoolUnready
			return p, nil
		}
	}

	if p.QueuePosition > 1 {
		p.WaitReason = WaitInClientQueue
		return p, nil
	}

	if exceeded, err := s.quotaExceeded(ctx, r.ClientID); err != nil {
		return nil, err
	} else if exceeded {
		p.WaitReason = WaitQuotaExceeded
		return p, nil
	}
	return p, nil
}

func (s *Service) profileUnready(ctx context.Context, r *model.Run) (bool, error) {
	head, err := s.store.DAOs().PlatformNetwork.GetHead(ctx)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	if head.ActiveRevisionID == nil {
		return true, nil
	}
	profile, err := s.store.DAOs().RuntimeProfiles.GetByTriple(ctx, *r.ImageRegistrationID, *head.ActiveRevisionID, r.NetworkRevisionID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return true, nil
		}
		return false, err
	}
	return profile.Status != "ready", nil
}

func (s *Service) RuntimeProfileReady(ctx context.Context, imageRegistrationID string, networkRevisionID *string) (bool, error) {
	head, err := s.store.DAOs().PlatformNetwork.GetHead(ctx)
	if err != nil {
		return false, err
	}
	if head.ActiveRevisionID == nil {
		return false, nil
	}
	p, err := s.store.DAOs().RuntimeProfiles.GetByTriple(ctx, imageRegistrationID, *head.ActiveRevisionID, networkRevisionID)
	if errors.Is(err, model.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return p.Status == "ready", nil
}

func (s *Service) quotaExceeded(ctx context.Context, clientID string) (bool, error) {
	c, err := s.store.DAOs().Clients.Get(ctx, clientID)
	if err != nil {
		return false, err
	}
	if c.ConcurrencyQuota <= 0 {
		return false, nil
	}
	n, err := s.store.DAOs().Runs.CountActiveExecutions(ctx, clientID, s.clock.Now())
	if err != nil {
		return false, err
	}
	return n >= c.ConcurrencyQuota, nil
}

func (s *Service) SetClientQuota(ctx context.Context, clientID string, quota int) error {
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		if _, err := d.Clients.GetForUpdate(ctx, clientID); err != nil {
			return r07NotFound(err)
		}
		ok, err := d.Clients.SetConcurrencyQuota(ctx, clientID, quota)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		return nil
	})
	return err
}

func (s *Service) SetImageWarmPoolReplicas(ctx context.Context, clientID, imageID string, replicas int) error {
	if replicas < 0 || replicas > WarmPoolMaxReplicas {
		return fmt.Errorf("runs: warm_pool_replicas out of range")
	}
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		img, err := d.Images.GetByClientAndImageID(ctx, clientID, imageID)
		if err != nil {
			return r07NotFound(err)
		}
		settings, err := d.Platform.LockForUpdate(ctx)
		if err != nil {
			return r07NotFound(err)
		}
		eff := model.EffectivePlatformSettings(settings, model.WarmPoolBudget{
			Budget: s.warmPool.Budget, MaxPerImage: s.warmPool.MaxPerImage, DefaultPool: s.warmPool.DefaultPool,
		})
		if replicas > 0 && replicas > eff.MaxPerImage {
			return ErrWarmPoolMaxPerImageConflict
		}
		images, err := d.Images.ListEnabled(ctx)
		if err != nil {
			return err
		}

		total := model.WarmPoolWantedTotal(images, eff)
		own := replicas
		eligible := img.Status == "enabled" && img.Repository != nil && *img.Repository != ""
		if !eligible {
			own = 0
		}
		if own > eff.MaxPerImage {
			own = eff.MaxPerImage
		}
		if eligible {
			total = total - wantedOf(img.WarmPoolReplicas, eff) + own
		}
		if total > eff.Budget {
			return ErrWarmPoolBudgetConflict
		}
		v := replicas
		if ok, uerr := d.Images.UpdateIf(ctx, img.ID, model.ImageCondition{}, model.ImagePatch{WarmPoolReplicas: &v}); uerr != nil {
			return uerr
		} else if !ok {
			return ErrNotFound
		}
		return nil
	})
	return err
}

func r07NotFound(err error) error {
	if errors.Is(err, model.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func wantedOf(stored int, eff model.WarmPoolBudget) int {
	w := stored
	if w > eff.MaxPerImage {
		w = eff.MaxPerImage
	}
	return w
}

func (s *Service) SetPlatformWarmPool(ctx context.Context, budget, maxPerImage, defaultPool *int) error {
	err := s.store.Transaction(ctx, func(d model.DAOs) error {
		settings, err := d.Platform.LockForUpdate(ctx)
		if err != nil {
			return r07NotFound(err)
		}
		eff := model.EffectivePlatformSettings(settings, model.WarmPoolBudget{
			Budget: s.warmPool.Budget, MaxPerImage: s.warmPool.MaxPerImage, DefaultPool: s.warmPool.DefaultPool,
		})
		next := eff
		if budget != nil {
			next.Budget = *budget
		}
		if maxPerImage != nil {
			next.MaxPerImage = *maxPerImage
		}

		next.DefaultPool = 0
		images, err := d.Images.ListEnabled(ctx)
		if err != nil {
			return err
		}
		if model.WarmPoolExplicitExceedsMax(images, next.MaxPerImage) {
			return ErrWarmPoolMaxPerImageConflict
		}
		if model.WarmPoolWantedTotal(images, next) > next.Budget {
			return ErrWarmPoolBudgetConflict
		}
		zero := 0
		return d.Platform.Set(ctx, budget, maxPerImage, &zero)
	})
	return err
}
