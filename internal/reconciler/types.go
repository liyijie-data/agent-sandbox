package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/security"
	"agent-platform/model"

	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	extensionsclientset "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned"
	extensionsv1beta1 "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/typed/api/v1beta1"
)

const (
	jobPending   = "pending"
	jobRunning   = "running"
	jobFailed    = "failed"
	jobCompleted = "completed"
)

const (
	profilePending = "pending"
	profileReady   = "ready"
	profileFailed  = "failed"
)

type Options struct {
	Store    *model.Store
	Baseline *security.PlatformNetworkBaseline
	K8s      extensionsv1beta1.ExtensionsV1beta1Interface
	Cfg      config.Config
	Log      *slog.Logger

	EnableCleanupScan bool

	Pods corev1.CoreV1Interface

	poolStatus func(ctx context.Context, namespace, name string) (ready, replicas int32, err error)

	sleep func(ctx context.Context, d time.Duration) error
}

type Runner struct {
	store    *model.Store
	baseline *security.PlatformNetworkBaseline
	k8s      extensionsv1beta1.ExtensionsV1beta1Interface
	cfg      config.Config
	log      *slog.Logger

	pollInterval time.Duration
	timeout      time.Duration
	cleanupScan  bool
	pods         corev1.CoreV1Interface
	poolStatus   func(ctx context.Context, namespace, name string) (ready, replicas int32, err error)
	sleep        func(ctx context.Context, d time.Duration) error
}

func New(opts Options) (*Runner, error) {
	if opts.Store == nil {
		return nil, errMissing("store")
	}
	if opts.Baseline == nil {
		return nil, errMissing("platform baseline")
	}
	if opts.K8s == nil {
		return nil, errMissing("extensions clientset")
	}
	if opts.Cfg.K8s.Namespace == "" {
		return nil, errMissing("k8s namespace")
	}

	if opts.Cfg.K8s.RegistrationWarmPoolReplicas < 0 {
		return nil, errMissing("warm pool replicas")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	r := &Runner{
		store: opts.Store, baseline: opts.Baseline, k8s: opts.K8s, cfg: opts.Cfg, log: opts.Log,
		pollInterval: opts.Cfg.K8s.RolloutPollInterval,
		timeout:      opts.Cfg.K8s.RolloutTimeout,
		cleanupScan:  opts.EnableCleanupScan,
		pods:         opts.Pods,
		poolStatus:   opts.poolStatus,
		sleep:        opts.sleep,
	}
	if r.pollInterval <= 0 {
		r.pollInterval = 5 * time.Second
	}
	if r.timeout <= 0 {
		r.timeout = 10 * time.Minute
	}
	if r.sleep == nil {
		r.sleep = sleepCtx
	}
	if r.poolStatus == nil {
		r.poolStatus = r.readPoolStatus
	}
	return r, nil
}

func NewInClusterClientset() (extensionsv1beta1.ExtensionsV1beta1Interface, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("reconciler: in-cluster k8s config: %w", err)
	}
	cs, err := extensionsclientset.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("reconciler: extensions clientset: %w", err)
	}
	return cs.ExtensionsV1beta1(), nil
}

func NewInClusterCoreV1() (corev1.CoreV1Interface, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("reconciler: in-cluster k8s config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("reconciler: core clientset: %w", err)
	}
	return cs.CoreV1(), nil
}

func (r *Runner) Scan(ctx context.Context) error {
	if err := EnsureBaseline(ctx, r.store, r.cfg, r.log); err != nil {
		r.log.Error("reconciler: ensure platform baseline", "error", err)
		return err
	}
	jobs, err := r.store.DAOs().RolloutJobs.ListActive(ctx)
	if err != nil {
		return err
	}

	if err := r.DriftCheckWarmPools(ctx); err != nil {
		r.log.Error("reconciler: warm pool drift check", "error", err)
		return err
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := r.processJob(ctx, &job); err != nil {
			r.log.Error("reconciler: process rollout job", "job", job.ID, "scope", job.Scope, "error", err)
			return err
		}
	}
	if r.cleanupScan {
		if n, err := r.EnqueueSupersededProfileCleanup(ctx); err != nil {
			r.log.Error("reconciler: superseded profile cleanup scan", "error", err)
		} else if n > 0 {
			r.log.Info("reconciler: superseded profile cleanup scheduled", "count", n)
		}
		if n, err := r.CleanupOrphanedVersionedResources(ctx); err != nil {
			r.log.Error("reconciler: orphaned versioned resource cleanup scan", "error", err)
		} else if n > 0 {
			r.log.Info("reconciler: orphaned versioned resource cleanup scheduled", "count", n)
		}
	}
	return nil
}

func errMissing(name string) error {
	return &MissingDependencyError{Name: name}
}

type MissingDependencyError struct{ Name string }

func (e *MissingDependencyError) Error() string { return "reconciler: missing dependency: " + e.Name }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
