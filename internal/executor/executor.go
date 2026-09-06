package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/lifecycle"
	"agent-platform/internal/security"
	"agent-platform/internal/service/runs"
	"agent-platform/internal/service/steers"
)

const (
	RunConfigPath  = "/app/run.json"
	ResultPath     = "/app/output/result.json"
	CheckpointPath = "/app/output/checkpoint.tar.gz"
	SteeringPath   = "/app/output/steering.json"

	InputCheckpoint = "/app/checkpoint.tar.gz"

	DefaultRuntimeEntryCommand = "python -m agent_runtime --config /app/run.json"
)

type ExecuteResult int

const (
	OutcomeRejected ExecuteResult = iota

	OutcomeStale

	OutcomeSucceeded

	OutcomeAwaitingInput

	OutcomeFailed

	OutcomeUnknown

	OutcomeCancelRequested
	OutcomeExpired
)

func (r ExecuteResult) String() string {
	switch r {
	case OutcomeRejected:
		return "rejected"
	case OutcomeStale:
		return "stale"
	case OutcomeSucceeded:
		return "succeeded"
	case OutcomeAwaitingInput:
		return "awaiting_input"
	case OutcomeFailed:
		return "failed"
	case OutcomeUnknown:
		return "unknown"
	case OutcomeCancelRequested:
		return "cancel_requested"
	case OutcomeExpired:
		return "expired"
	}
	return "unknown_outcome"
}

type Options struct {
	ExecuteTimeout time.Duration

	TokenTTL time.Duration

	RuntimeSteeringBaseURL string

	ModelGatewayBaseURL string

	RuntimeEntryCommand string

	DefaultWarmPoolName string
}

type Provisioner func(ctx context.Context, in ProvisionIn) (*contracts.RuntimeConfig, error)

type ProvisionIn struct {
	RunID       string
	ClientID    string
	StageNo     int
	Fence       int64
	ExecutionID string

	Resume *ResumeRef

	SteeringCursor int64
}

type ResumeRef struct {
	RunID       string
	StageNo     int
	Fence       int64
	ObjectRef   string
	SHA256      string
	StateFormat string
	InputID     string
	Answer      []byte
	checkpoint  []byte
}

type Executor struct {
	runs         *runs.Service
	steers       *steers.Service
	signer       *security.Signer
	provider     SandboxProvider
	recovery     RecoveryStore
	tokens       TokenMinter
	clock        lifecycle.Clock
	policy       lifecycle.Policy
	opts         Options
	log          *slog.Logger
	provision    Provisioner
	runtimeEntry string
}

func New(runsSvc *runs.Service, steersSvc *steers.Service, signer *security.Signer, provider SandboxProvider,
	recovery RecoveryStore, policy lifecycle.Policy, clock lifecycle.Clock, opts Options, log *slog.Logger) *Executor {
	if log == nil {
		log = slog.Default()
	}
	if opts.ExecuteTimeout <= 0 {
		opts.ExecuteTimeout = 30 * time.Minute
	}
	entry := opts.RuntimeEntryCommand
	if entry == "" {
		entry = DefaultRuntimeEntryCommand
	}
	tokens := &signerMinter{signer: signer, ttl: opts.TokenTTL, clock: clock}
	ex := &Executor{runs: runsSvc, steers: steersSvc, signer: signer, provider: provider, recovery: recovery,
		tokens: tokens, clock: clock, policy: policy, opts: opts, log: log, runtimeEntry: entry}
	ex.provision = ex.defaultProvision
	return ex
}

func (e *Executor) SetProvisioner(p Provisioner) { e.provision = p }

func (e *Executor) ExecuteStage(ctx context.Context, clientID, runID string, stageNo int, owner string) (ExecuteResult, error) {
	execID := newExecutionID(runID, stageNo, owner)
	st, err := e.runs.ClaimStage(ctx, clientID, runID, stageNo, owner, execID)
	if err != nil {
		e.log.Warn("claim stage rejected", "run", runID, "stage", stageNo, "error", err)
		return OutcomeRejected, err
	}
	e.log.Info("stage claimed", "run", runID, "stage", stageNo, "fence", st.Fence, "owner", owner, "pool", st.PoolName)

	pool := st.PoolName
	if pool == "" {
		pool = e.opts.DefaultWarmPoolName
	}
	pod, err := e.provider.Create(ctx, runID, stageNo, st.Fence, pool)
	if err != nil {
		e.log.Warn("pod create failed", "run", runID, "stage", stageNo, "error", err)
		return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, err)
	}
	defer func() {
		if err := pod.Delete(ctx); err != nil {

			e.log.Error("pod physical delete failed; enqueueing cleanup", "run", runID,
				"stage", stageNo, "fence", st.Fence, "pod", pod.Name(), "error", err)
			if cerr := e.runs.EnqueueCleanup(ctx, "sandbox_pod", pod.Name(), e.clock.Now()); cerr != nil {
				e.log.Error("enqueue sandbox pod cleanup failed", "pod", pod.Name(), "error", cerr)
			}
			return
		}
		if status, _ := e.runs.StatusOf(ctx, runID); status == contracts.RunStatusCancelRequested {
			ok, ferr := e.runs.FinishRun(ctx, runID, stageNo, st.Fence, owner,
				[]contracts.RunStatus{contracts.RunStatusCancelRequested},
				runs.FinishState{Status: contracts.RunStatusCancelled})
			if ferr != nil {
				e.log.Error("cancel confirmation failed", "run", runID, "stage", stageNo, "error", ferr)
			} else if ok {
				e.terminalClose(ctx, runID, contracts.SteerReasonRunCancelled)
			}
		}
	}()

	resume, cursor, err := e.resolveResume(ctx, runID, stageNo)
	if err != nil {
		return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, err)
	}
	if resume != nil {
		pkg, err := e.recovery.Get(ctx, resume.ObjectRef)
		if err != nil {
			return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, fmt.Errorf("executor: read recovery package: %w", err))
		}
		if int64(len(pkg)) <= 0 || int64(len(pkg)) > e.recovery.MaxBytes() {
			return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, fmt.Errorf("executor: recovery package size invalid"))
		}
		sum := sha256.Sum256(pkg)
		if hex.EncodeToString(sum[:]) != resume.SHA256 {
			return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, fmt.Errorf("executor: recovery package hash mismatch"))
		}
		if err := validateCheckpointPackage(pkg, checkpointIdentity{RunID: resume.RunID, Stage: resume.StageNo, Fence: resume.Fence, StateFormat: resume.StateFormat}, e.recovery.MaxBytes()); err != nil {
			return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, err)
		}
		resume.checkpoint = pkg
	}

	cfg, err := e.provision(ctx, ProvisionIn{
		RunID: runID, ClientID: clientID, StageNo: stageNo, Fence: st.Fence,
		ExecutionID: execID, Resume: resume, SteeringCursor: cursor,
	})
	if err != nil {
		return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, err)
	}
	cfg.ContractVersion = contracts.RuntimeContractVersion
	cfg.RunID = runID
	cfg.Stage = stageNo
	cfg.Fence = st.Fence
	cfg.ExecutionID = execID

	cfg.Runtime.BaseURL = e.opts.RuntimeSteeringBaseURL
	cfg.Runtime.Token = ""

	runtimeTok, err := e.tokens.Mint(security.Claims{
		RunID: runID, Stage: stageNo, Fence: st.Fence,
		Purpose: runtimeTokenPurpose,
	})
	if err != nil {
		return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, err)
	}
	cfg.Runtime.Token = runtimeTok

	if cfg.Model.Name == "" {
		cfg.Model.Name = "platform-gateway"
	}
	cfg.Model.BaseURL = e.opts.ModelGatewayBaseURL
	if cfg.Model.BaseURL == "" && e.opts.RuntimeSteeringBaseURL != "" {
		cfg.Model.BaseURL = e.opts.RuntimeSteeringBaseURL
	}
	if cfg.Model.Token == "" {
		modelTok, err := e.tokens.Mint(security.Claims{
			RunID: runID, Stage: stageNo, Fence: st.Fence,
			Purpose: security.PurposeModel,
		})
		if err != nil {
			return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, err)
		}
		cfg.Model.Token = modelTok
	}

	if cfg.Steering == nil {
		cfg.Steering = &contracts.RuntimeSteeringConfig{}
	}
	cfg.Steering.AfterSeq = cursor

	if cfg.Limits.RemainingExecutionSeconds <= 0 {
		rem, err := e.runs.StageBudgetRemaining(ctx, runID, stageNo)
		if err != nil {
			return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, err)
		}
		if rem <= 0 {
			return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner,
				fmt.Errorf("executor: execution budget exhausted"))
		}
		cfg.Limits.RemainingExecutionSeconds = rem
	}
	cfg.Limits.CheckpointMaxBytes = e.recovery.MaxBytes()

	runJSON, err := json.Marshal(cfg)
	if err != nil {
		return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, err)
	}

	status, err := e.runs.StatusOf(ctx, runID)
	if err != nil {
		return OutcomeStale, err
	}
	if !isPreLaunch(string(status)) {
		e.log.Info("run no longer executing before launch; refusing launch", "run", runID, "status", status)
		return outcomeByStatus(string(status)), nil
	}

	if resume != nil {
		if err := pod.Write(ctx, InputCheckpoint, resume.checkpoint); err != nil {
			return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, fmt.Errorf("executor: write recovery package: %w", err))
		}
	}
	if err := pod.Prepare(ctx, runJSON); err != nil {
		return e.preLaunchFailure(ctx, runID, stageNo, st.Fence, owner, err)
	}

	status, err = e.runs.StatusOf(ctx, runID)
	if err != nil {
		return OutcomeStale, err
	}
	if !isPreLaunch(string(status)) {
		e.log.Info("run cancelled during prepare; refusing launch", "run", runID, "status", status)
		return outcomeByStatus(string(status)), nil
	}

	started, err := e.runs.MarkLaunchStarted(ctx, runID, stageNo, st.Fence, owner)
	if err != nil {
		return OutcomeStale, err
	}
	if !started {

		return OutcomeStale, nil
	}

	execCtx, cancelExec, watchDone := leaseAndCancelWatch(ctx, e.runs, e.log,
		runID, stageNo, st.Fence, owner, e.policy.QueueBudget)
	defer func() {
		cancelExec()
		<-watchDone
	}()

	exitCode, execErr := pod.Execute(execCtx, e.resolveRuntimeEntry(ctx, runID), e.opts.ExecuteTimeout)
	if execErr != nil {

		if execCtx.Err() != nil {
			st2, _ := e.runs.StatusOf(ctx, runID)
			e.log.Info("execution aborted by lease/cancel watch", "run", runID, "status", st2, "err", execErr)
			return outcomeByStatus(string(st2)), nil
		}

		return e.executionOutcomeUnknown(ctx, runID, stageNo, st.Fence, owner, execErr)
	}

	res, verdict, err := e.readResult(ctx, pod, exitCode)
	if err != nil {

		return e.executionOutcomeUnknown(ctx, runID, stageNo, st.Fence, owner, err)
	}
	if verdict != "" {

		return e.commitFailed(ctx, runID, stageNo, st.Fence, owner, &contracts.RuntimeResult{
			Status: contracts.RuntimeError, ErrorCode: verdict})
	}
	return e.dispatchResult(ctx, runID, stageNo, st.Fence, owner, pod, res)
}
