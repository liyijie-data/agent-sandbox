package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agent-platform/internal/cleanup"
	"agent-platform/internal/config"
	"agent-platform/internal/controlplane"
	"agent-platform/internal/events"
	"agent-platform/internal/executor"
	"agent-platform/internal/images"
	"agent-platform/internal/lifecycle"
	"agent-platform/internal/modelgateway"
	"agent-platform/internal/queue"
	"agent-platform/internal/reconciler"
	"agent-platform/internal/recovery"
	"agent-platform/internal/registry"
	"agent-platform/internal/sandbox"
	"agent-platform/internal/security"
	"agent-platform/internal/service/networks"
	"agent-platform/internal/service/runs"
	"agent-platform/internal/service/steers"
	"agent-platform/internal/storage"
	"agent-platform/internal/worker"
	"agent-platform/model"
)

func RunServer(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	deps, err := BuildRebuildDeps(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("server: rebuild deps: %w", err)
	}
	defer deps.Store.Close()

	if err := reconciler.EnsureBaseline(ctx, deps.Store, cfg, log); err != nil {
		return fmt.Errorf("server: ensure platform baseline: %w", err)
	}

	cp := rebuiltServer(deps, cfg, log)
	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           healthzRoot(cp.Router()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("control plane listening (rebuilt)", "addr", cfg.HTTP.Addr)

	if cfg.Console.Addr != "" {
		consoleSrv := &http.Server{
			Addr:              cfg.Console.Addr,
			Handler:           RebuiltConsoleRouter(deps, cfg, log),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = consoleSrv.Shutdown(shutdownCtx)
		}()
		go func() {
			log.Info("admin console listening", "addr", cfg.Console.Addr)
			if cerr := consoleSrv.ListenAndServe(); cerr != nil && cerr != http.ErrServerClosed {
				log.Error("admin console listener failed", "error", cerr)
			}
		}()
	}

	err = srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func RunWorker(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	deps, err := BuildRebuildDeps(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("worker: rebuild deps: %w", err)
	}
	defer deps.Store.Close()

	if err := reconciler.EnsureBaseline(ctx, deps.Store, cfg, log); err != nil {
		return fmt.Errorf("worker: ensure platform baseline: %w", err)
	}

	w, err := RebuiltWorker(deps, cfg, log)
	if err != nil {
		return fmt.Errorf("worker: rebuild worker: %w", err)
	}
	log.Info("rebuilt worker starting", "consumer", cfg.Worker.WorkerID, "concurrency", cfg.Worker.Concurrency)
	return w.Run(ctx)
}

type RebuildDeps struct {
	Store    *model.Store
	Cipher   *storage.ContentCipher
	Signer   *security.Signer
	Recovery executor.RecoveryStore
	Objects  *storage.ObjectStore
	Baseline *security.PlatformNetworkBaseline
}

func BuildPlatformNetworkBaseline(cfg config.Config) (*security.PlatformNetworkBaseline, error) {
	if strings.TrimSpace(cfg.Security.PodCIDRs) == "" || strings.TrimSpace(cfg.Security.ServiceCIDRs) == "" || strings.TrimSpace(cfg.Security.NodeCIDRs) == "" {
		return nil, fmt.Errorf("platform network baseline requires pod, service and node cidrs")
	}
	protectedCIDRs := cfg.Security.ProtectedCIDRs + "," + cfg.Security.ReservedCIDRs
	objectStorageCIDR := strings.TrimSpace(cfg.K8s.ObjectStorageCIDR)
	if objectStorageCIDR != "" {

		protectedCIDRs += "," + objectStorageCIDR
	}
	base, err := security.NewPlatformNetworkBaseline(security.PlatformNetworkBaselineConfig{
		PodCIDRs: splitConfigList(cfg.Security.PodCIDRs), ServiceCIDRs: splitConfigList(cfg.Security.ServiceCIDRs), NodeCIDRs: splitConfigList(cfg.Security.NodeCIDRs),
		ProtectedCIDRs: splitConfigList(protectedCIDRs), BusinessPrivateCIDRs: splitConfigList(cfg.Security.BusinessPrivateCIDRs), ReservedHostnames: splitConfigList(cfg.Security.ReservedHostnames), IPv6Enabled: cfg.Security.IPv6Enabled,
	})
	if err != nil {
		return nil, err
	}
	endpoints := []string{dependencyHost(cfg.DB.DSN), dependencyHost(cfg.Redis.URL), dependencyHost(cfg.Security.RegistryEndpoint)}
	if objectStorageCIDR != "" {
		ip, _, err := net.ParseCIDR(objectStorageCIDR)
		if err != nil {
			return nil, fmt.Errorf("platform object storage cidr: %w", err)
		}
		endpoints = append(endpoints, ip.String())
	} else {
		endpoints = append(endpoints, dependencyHost(cfg.S3.Endpoint), dependencyHost(cfg.S3.PublicEndpoint), dependencyHost(cfg.S3.SandboxEndpoint))
	}
	var depErr error
	if cfg.Env == "development" && cfg.Security.AllowDNSDependencies {
		depErr = base.ValidateDependencyProtectionAllowDNS(endpoints)
	} else {
		depErr = base.ValidateDependencyProtection(endpoints)
	}
	if err := depErr; err != nil {
		return nil, err
	}
	return base, nil
}

func splitConfigList(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(s); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func dependencyHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

func ParseRebuildKeys(cfg config.Config) (encKey, hmacKey, signSecret []byte, err error) {
	enc, e := base64.StdEncoding.DecodeString(cfg.State.ContentEncryptionKey)
	if e != nil || len(enc) != 32 {
		return nil, nil, nil, fmt.Errorf("rebuild: CONTENT_ENCRYPTION_KEY must be base64-encoded 32 bytes (AES-256)")
	}
	hmacK, e := base64.StdEncoding.DecodeString(cfg.State.FingerprintHMACKey)
	if e != nil || len(hmacK) == 0 {
		return nil, nil, nil, fmt.Errorf("rebuild: FINGERPRINT_HMAC_KEY must be a base64-encoded non-empty value")
	}
	sec, e := base64.StdEncoding.DecodeString(cfg.Security.TokenSigningSecret)
	if e != nil || len(sec) < 16 {
		return nil, nil, nil, fmt.Errorf("rebuild: TOKEN_SIGNING_SECRET must be base64-encoded at least 16 bytes")
	}
	return enc, hmacK, sec, nil
}

func BuildRebuildDeps(ctx context.Context, cfg config.Config, log *slog.Logger) (*RebuildDeps, error) {
	if cfg.Env != "development" {
		if err := config.RequireRebuildKeys(cfg); err != nil {
			return nil, err
		}
	}
	encKey, hmacKey, signSecret, err := ParseRebuildKeys(cfg)
	if err != nil {
		return nil, err
	}

	cipher, err := storage.NewContentCipher(map[int][]byte{cfg.State.ContentKeyVersion: encKey}, hmacKey)
	if err != nil {
		return nil, fmt.Errorf("rebuild: content cipher: %w", err)
	}
	signer, err := security.NewSigner(signSecret)
	if err != nil {
		return nil, fmt.Errorf("rebuild: token signer: %w", err)
	}
	baseline, err := BuildPlatformNetworkBaseline(cfg)
	if err != nil {
		return nil, fmt.Errorf("rebuild: platform network baseline: %w", err)
	}

	store, err := model.Open(ctx, cfg.DB.DSN)
	if err != nil {
		return nil, fmt.Errorf("rebuild: model store: %w", err)
	}
	if err := store.Init(ctx); err != nil {
		store.Close()
		return nil, fmt.Errorf("rebuild: schema init: %w", err)
	}

	if err := store.DAOs().Platform.Seed(ctx, cfg.Platform.WarmPoolBudget, cfg.Platform.WarmPoolMaxPerImage, cfg.Platform.WarmPoolDefaultPool); err != nil {
		store.Close()
		return nil, fmt.Errorf("rebuild: platform settings seed: %w", err)
	}

	objects, err := storage.New(cfg.S3)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("rebuild: object store: %w", err)
	}
	storeRec, err := recovery.NewObjectStoreAdapter(objects, executor.MaxRecoveryBytes)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("rebuild: recovery store: %w", err)
	}

	if log != nil {
		log.Info("rebuilt dependencies assembled",
			"store", "postgres", "recovery", "object-store", "cipher_key_version", cfg.State.ContentKeyVersion)
	}
	return &RebuildDeps{Store: store, Cipher: cipher, Signer: signer, Recovery: storeRec, Objects: objects, Baseline: baseline}, nil
}

func RebuiltRouter(deps *RebuildDeps, cfg config.Config, log *slog.Logger) http.Handler {
	return healthzRoot(rebuiltServer(deps, cfg, log).Router())
}

func healthzRoot(handler http.Handler) http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	root.Handle("/", handler)
	return root
}

func RebuiltConsoleRouter(deps *RebuildDeps, cfg config.Config, log *slog.Logger) http.Handler {
	return rebuiltServer(deps, cfg, log).ConsoleRouter()
}

func rebuiltServer(deps *RebuildDeps, cfg config.Config, log *slog.Logger) *controlplane.Server {
	runsSvc := runs.New(deps.Store, deps.Cipher, lifecycle.DefaultPolicy(), lifecycle.SystemClock)
	runsSvc.WithWarmPoolDefaults(cfg.Platform.WarmPoolBudget, cfg.Platform.WarmPoolMaxPerImage, cfg.Platform.WarmPoolDefaultPool)
	steersSvc := steers.New(deps.Store, deps.Cipher, lifecycle.SystemClock)
	cp := controlplane.New(runsSvc, steersSvc, deps.Signer, lifecycle.SystemClock, log)

	if cache, cerr := storage.NewEventCache(cfg.Redis.URL); cerr == nil {
		eventsSvc := events.New(cache, deps.Cipher, deps.Store, lifecycle.DefaultPolicy().ContentRetention)
		cp.WithEvents(eventsSvc)
		if log != nil {
			log.Info("rebuild router: event stream wired")
		}
	} else if log != nil {
		log.Error("rebuild router: event cache unavailable; event endpoints 503", "error", cerr)
	}

	if gw := BuildModelGateway(runsSvc, cfg, log); gw != nil {
		cp.WithModelGateway(gw)
	}

	imgSvc := images.New(deps.Store, images.OperatorVerifier{}, log)
	imgSvc.WithRevokeCoordinator(imageRevokeCoordinator{runs: runsSvc, log: log})
	cp.WithImages(imgSvc)
	cp.WithImageResolver(registry.New(cfg.Security.RegistryResolverHost, cfg.Security.RegistryAuthFile))

	cp.WithAdminToken(cfg.ServiceToken)

	cp.WithNetworks(networks.New(deps.Store, deps.Baseline, log))
	return cp
}

func BuildModelGateway(runsSvc *runs.Service, cfg config.Config, log *slog.Logger) *controlplane.ModelGateway {
	base := cfg.Model.AllowedUpstream
	if base == "" {
		return nil
	}
	var permit modelgateway.ConcurrencyPermit = modelgateway.NewMemoryQuota()
	if cfg.Env != "development" {
		rq, err := modelgateway.NewRedisQuotaFromURL(cfg.Redis.URL)
		if err != nil {

			if log != nil {
				log.Error("rebuild router: redis permit url invalid; model gateway disabled", "error", err)
			}
			return nil
		}
		permit = rq
	}
	return controlplane.NewModelGateway(base, "", "",
		int64(cfg.Gateway.MaxRequestsPerRun), cfg.Gateway.MaxConcurrencyPerRun,
		runsSvc, permit, log)
}

type imageRevokeCoordinator struct {
	runs *runs.Service
	log  *slog.Logger
}

func (c imageRevokeCoordinator) CancelImageRuns(ctx context.Context, clientID, imageRegistrationID string) error {
	if err := c.runs.CancelImageRuns(ctx, clientID, imageRegistrationID); err != nil {
		return err
	}
	return c.runs.EnqueueImageProfileCleanup(ctx, imageRegistrationID, time.Now().UTC())
}

func SchemaInit(ctx context.Context, log *slog.Logger, cfg config.Config) error {
	store, err := model.Open(ctx, cfg.DB.DSN)
	if err != nil {
		return fmt.Errorf("schema init: model store: %w", err)
	}
	defer store.Close()
	if err := store.Init(ctx); err != nil {
		return fmt.Errorf("schema init: %w", err)
	}
	if log != nil {
		log.Info("rebuilt schema initialized", "schema_version", model.SchemaVersion)
	}
	return nil
}

func RebuiltWorker(deps *RebuildDeps, cfg config.Config, log *slog.Logger) (*worker.Worker, error) {
	q, err := queue.New(cfg.Redis.URL)
	if err != nil {
		return nil, fmt.Errorf("rebuild: queue: %w", err)
	}
	sb, err := RebuildSandboxProvider(cfg, log)
	if err != nil {
		q.Close()
		return nil, err
	}

	var k8sCleanup *reconciler.K8sCleanupDeleter
	var rolloutRunner worker.RolloutRunner
	if cfg.Env != "development" || cfg.K8s.RolloutEnabled {
		k8s, kerr := reconciler.NewInClusterClientset()
		if kerr != nil {
			q.Close()
			return nil, kerr
		}
		rr, rerr := reconciler.New(reconciler.Options{
			Store: deps.Store, Baseline: deps.Baseline, K8s: k8s, Cfg: cfg, Log: log,
			EnableCleanupScan: true,
		})
		if rerr != nil {
			q.Close()
			return nil, rerr
		}
		rolloutRunner = rr
		k8sCleanup = &reconciler.K8sCleanupDeleter{Namespace: cfg.K8s.Namespace, Ext: k8s}
		if coreCS, kerr := reconciler.NewInClusterCoreV1(); kerr == nil {
			k8sCleanup.Pods = coreCS
		}
	}
	runsSvc := runs.New(deps.Store, deps.Cipher, lifecycle.DefaultPolicy(), lifecycle.SystemClock)
	runsSvc.WithWarmPoolDefaults(cfg.Platform.WarmPoolBudget, cfg.Platform.WarmPoolMaxPerImage, cfg.Platform.WarmPoolDefaultPool)
	steersSvc := steers.New(deps.Store, deps.Cipher, lifecycle.SystemClock)
	daos := deps.Store.DAOs()
	exec := executor.New(runsSvc, steersSvc, deps.Signer, sb, deps.Recovery,
		lifecycle.DefaultPolicy(), lifecycle.SystemClock, executor.Options{
			ExecuteTimeout:         cfg.Worker.ExecutionTimeout,
			RuntimeSteeringBaseURL: cfg.Model.RuntimeGatewayBaseURL,
			ModelGatewayBaseURL:    cfg.Model.GatewayBaseURL,

			DefaultWarmPoolName: cfg.K8s.WarmPoolName,
		}, log)
	w := worker.New(q, runsSvc, exec, log, worker.Options{
		Consumer:    cfg.Worker.WorkerID,
		Concurrency: cfg.Worker.Concurrency,

		Cleanup: cleanup.New(deps.Store.DAOs().CleanupJobs, log, lifecycle.SystemClock, cleanup.Adapters{
			Content: cleanup.DBContentDeleter{Contents: deps.Store.DAOs().Contents},
			Objects: cleanup.RecoveryObjectDeleter{Store: deps.Recovery},
			Profiles: cleanup.DBProfileDeleter{
				Profiles: daos.RuntimeProfiles,
				Stages:   daos.Stages,
				Jobs:     daos.RolloutJobs,
				Images:   &daos.Images,
				Heads:    &daos.NetworkHeads,
				K8s:      k8sCleanup,
			},
			K8s:  k8sCleanup,
			Pods: k8sCleanup,
			Orphans: cleanup.DBOrphanObjectDeleter{
				Store: deps.Objects, Checkpoints: daos.Checkpoints,
			},
		}, cleanup.Options{}),

		Orphans: orphanScanner{objects: deps.Objects, runs: runsSvc, log: log},

		Rollouts:        rolloutRunner,
		RolloutInterval: cfg.K8s.RolloutPollInterval,
	})
	return w, nil
}

type orphanScanner struct {
	objects *storage.ObjectStore
	runs    *runs.Service
	log     *slog.Logger
}

func (o orphanScanner) ScanOrphans(ctx context.Context) error {
	keys, err := o.objects.ListKeys(ctx, "recovery/v1/", 1000)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	n, err := o.runs.EnqueueOrphanObjects(ctx, keys, time.Now().UTC())
	if err != nil {
		return err
	}
	if n > 0 && o.log != nil {
		o.log.Info("orphan object cleanup scheduled", "count", n)
	}
	return nil
}

func RebuildSandboxProvider(cfg config.Config, log *slog.Logger) (executor.SandboxProvider, error) {
	if sandbox.UseNoopFromEnv() {
		return nil, fmt.Errorf("rebuild: noop sandbox must not be selected by the rebuilt worker")
	}
	client := sandbox.NewClient(log, cfg.K8s, nil)
	return executor.NewProductionSandboxProvider(client), nil
}
