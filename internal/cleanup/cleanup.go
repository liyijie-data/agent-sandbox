package cleanup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"agent-platform/internal/lifecycle"
	"agent-platform/model"
)

const (
	ResourceRunContents     = "run_contents"
	ResourceSandboxPod      = "sandbox_pod"
	ResourceRecoveryPackage = "recovery_package"
	ResourceExpiredEvent    = "expired_event"
	ResourceRuntimeProfile  = "runtime_profile"
	ResourceSandboxTemplate = "sandbox_template"
	ResourceSandboxWarmPool = "sandbox_warm_pool"
	ResourceOrphanObject    = "orphan_object"
)

const (
	statusPending   = "pending"
	statusRunning   = "running"
	statusCompleted = "completed"
	statusFailed    = "failed"
)

const (
	codeUnsupportedKind = "unsupported_resource_kind"
	codeNoAdapter       = "no_adapter"
)

func safeCodeFor(kind string) string        { return kind + "_deleted" }
func alreadyGoneCodeFor(kind string) string { return kind + "_already_gone" }
func failCodeFor(kind string) string        { return kind + "_delete_failed" }
func referencedCodeFor(kind string) string  { return kind + "_referenced" }

var ErrReferenced = errors.New("cleanup: target is still referenced")

type DeletionOutcome int

const (
	OutcomeDeleted DeletionOutcome = iota

	OutcomeAlreadyGone
)

func strPtr(s string) *string { return &s }

type ContentDeleter interface {
	DeleteContent(ctx context.Context, runID string) (DeletionOutcome, error)
}

type ObjectDeleter interface {
	DeleteObject(ctx context.Context, objectRef string) (DeletionOutcome, error)
}

type EventCacheDeleter interface {
	DeleteEvent(ctx context.Context, eventID string) (DeletionOutcome, error)
}

type PodDeleter interface {
	DeletePod(ctx context.Context, podName string) (DeletionOutcome, error)
}

type ProfileDeleter interface {
	DeleteProfile(ctx context.Context, profileID string) (DeletionOutcome, error)
}

type K8sResourceDeleter interface {
	DeleteK8sObject(ctx context.Context, kind, name string) (DeletionOutcome, error)
}

type OrphanObjectDeleter interface {
	DeleteOrphanObject(ctx context.Context, objectKey string) (DeletionOutcome, error)
}

type Adapters struct {
	Content  ContentDeleter
	Objects  ObjectDeleter
	Events   EventCacheDeleter
	Pods     PodDeleter
	Profiles ProfileDeleter
	K8s      K8sResourceDeleter
	Orphans  OrphanObjectDeleter
}

type Options struct {
	ScanBatch int

	Lease time.Duration

	RetryBackoff time.Duration
}

type Runner struct {
	jobs  model.CleanupJobDAO
	adapt Adapters
	log   *slog.Logger
	clock lifecycle.Clock
	opts  Options
}

func New(jobs model.CleanupJobDAO, log *slog.Logger, clock lifecycle.Clock, adapt Adapters, opts Options) *Runner {
	if log == nil {
		log = slog.Default()
	}
	if clock == nil {
		clock = lifecycle.SystemClock
	}
	if opts.ScanBatch <= 0 || opts.ScanBatch > 100 {
		opts.ScanBatch = 100
	}
	if opts.Lease <= 0 {
		opts.Lease = time.Minute
	}
	if opts.RetryBackoff <= 0 {
		opts.RetryBackoff = 30 * time.Second
	}
	return &Runner{jobs: jobs, adapt: adapt, log: log, clock: clock, opts: opts}
}

func (r *Runner) Scan(ctx context.Context) error {
	now := r.clock.Now()
	due, err := r.jobs.ListDue(ctx, now, r.opts.ScanBatch)
	if err != nil {
		return fmt.Errorf("cleanup: list due jobs: %w", err)
	}
	for i := range due {
		job := due[i]
		claimed, err := r.claim(ctx, job, now)
		if err != nil {
			return fmt.Errorf("cleanup: claim job %s: %w", job.ID, err)
		}
		if !claimed {
			continue
		}
		safe, err := r.delete(ctx, job.ResourceKind, job.InternalRef)
		if err != nil {
			r.log.Error("cleanup job failed; retry recorded",
				"job", job.ID, "kind", job.ResourceKind, "ref", job.InternalRef, "safe_code", safe, "error", err)
			if rerr := r.retry(ctx, job.ID, job.Attempts, safe, now); rerr != nil {
				return fmt.Errorf("cleanup: record retry for job %s: %w", job.ID, rerr)
			}
			continue
		}
		if err := r.complete(ctx, job.ID, safe, now); err != nil {
			return fmt.Errorf("cleanup: complete job %s: %w", job.ID, err)
		}
		r.log.Info("cleanup job completed", "job", job.ID, "kind", job.ResourceKind, "ref", job.InternalRef, "safe_code", safe)
	}
	return nil
}

func (r *Runner) claim(ctx context.Context, job model.CleanupJob, now time.Time) (bool, error) {
	var cond model.CleanupJobCondition
	switch job.Status {
	case statusPending, statusFailed:
		cond.Status = &job.Status
	case statusRunning:
		if job.ClaimedUntil == nil || !job.ClaimedUntil.Before(now) {
			return false, nil
		}
		cond.Status = &job.Status
	default:
		return false, nil
	}
	until := now.Add(r.opts.Lease)
	return r.jobs.UpdateIf(ctx, job.ID, cond, model.CleanupJobPatch{
		Status:       strPtr(statusRunning),
		ClaimedUntil: &until,
	})
}

func (r *Runner) delete(ctx context.Context, kind, ref string) (string, error) {
	var outcome DeletionOutcome
	var err error
	switch kind {
	case ResourceRunContents:
		if r.adapt.Content == nil {
			return codeNoAdapter, fmt.Errorf("cleanup: no content deleter injected for kind %q", kind)
		}
		outcome, err = r.adapt.Content.DeleteContent(ctx, ref)
	case ResourceRecoveryPackage:
		if r.adapt.Objects == nil {
			return codeNoAdapter, fmt.Errorf("cleanup: no object deleter injected for kind %q", kind)
		}
		outcome, err = r.adapt.Objects.DeleteObject(ctx, ref)
	case ResourceExpiredEvent:
		if r.adapt.Events == nil {
			return codeNoAdapter, fmt.Errorf("cleanup: no event cache deleter injected for kind %q", kind)
		}
		outcome, err = r.adapt.Events.DeleteEvent(ctx, ref)
	case ResourceSandboxPod:
		if r.adapt.Pods == nil {
			return codeNoAdapter, fmt.Errorf("cleanup: no pod deleter injected for kind %q", kind)
		}
		outcome, err = r.adapt.Pods.DeletePod(ctx, ref)
	case ResourceRuntimeProfile:
		if r.adapt.Profiles == nil {
			return codeNoAdapter, fmt.Errorf("cleanup: no profile deleter injected for kind %q", kind)
		}
		outcome, err = r.adapt.Profiles.DeleteProfile(ctx, ref)
	case ResourceSandboxTemplate, ResourceSandboxWarmPool:
		if r.adapt.K8s == nil {
			return codeNoAdapter, fmt.Errorf("cleanup: no k8s deleter injected for kind %q", kind)
		}
		outcome, err = r.adapt.K8s.DeleteK8sObject(ctx, kind, ref)
	case ResourceOrphanObject:
		if r.adapt.Orphans == nil {
			return codeNoAdapter, fmt.Errorf("cleanup: no orphan object deleter injected for kind %q", kind)
		}
		outcome, err = r.adapt.Orphans.DeleteOrphanObject(ctx, ref)
	default:
		return codeUnsupportedKind, fmt.Errorf("cleanup: unsupported resource kind %q", kind)
	}
	if err != nil {
		if errors.Is(err, ErrReferenced) {
			return referencedCodeFor(kind), fmt.Errorf("cleanup: %s %q is still referenced; retry", kind, ref)
		}
		return failCodeFor(kind), fmt.Errorf("cleanup: delete %s %q: %w", kind, ref, err)
	}
	if outcome == OutcomeAlreadyGone {
		return alreadyGoneCodeFor(kind), nil
	}
	return safeCodeFor(kind), nil
}

func (r *Runner) complete(ctx context.Context, id, safe string, now time.Time) error {
	ok, err := r.jobs.UpdateIf(ctx, id,
		model.CleanupJobCondition{Status: strPtr(statusRunning)},
		model.CleanupJobPatch{Status: strPtr(statusCompleted), LastSafeCode: &safe, CompletedAt: &now})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cleanup: completion gate lost for job %s", id)
	}
	return nil
}

func (r *Runner) retry(ctx context.Context, id string, attempts int, safe string, now time.Time) error {
	nextAttempts := attempts + 1
	due := now.Add(r.opts.RetryBackoff)
	ok, err := r.jobs.UpdateIf(ctx, id,
		model.CleanupJobCondition{Status: strPtr(statusRunning)},
		model.CleanupJobPatch{
			Status:       strPtr(statusPending),
			Attempts:     &nextAttempts,
			LastSafeCode: &safe,
			DueAt:        &due,
		})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cleanup: retry gate lost for job %s", id)
	}
	return nil
}
