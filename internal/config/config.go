package config

import (
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	Env               string `envconfig:"APP_ENV" default:"development"`
	AllowInsecureAuth bool   `envconfig:"ALLOW_INSECURE_AUTH" default:"false"`
	ServiceToken      string `envconfig:"SERVICE_TOKEN"`
	HTTP              HTTPConfig
	Console           ConsoleConfig
	DB                DBConfig
	Redis             RedisConfig
	S3                S3Config
	K8s               K8sConfig
	Model             ModelConfig
	Worker            WorkerConfig
	State             StateConfig
	Security          SecurityConfig
	Gateway           GatewayConfig
	Platform          PlatformConfig
}

type HTTPConfig struct {
	Addr string `envconfig:"HTTP_ADDR" default:":8080"`
}

type ConsoleConfig struct {
	Addr string `envconfig:"CONSOLE_ADDR" default:""`
}

type StateConfig struct {
	ContentEncryptionKey string `envconfig:"CONTENT_ENCRYPTION_KEY"`
	FingerprintHMACKey   string `envconfig:"FINGERPRINT_HMAC_KEY"`
	ContentKeyVersion    int    `envconfig:"CONTENT_KEY_VERSION" default:"1"`
}

type SecurityConfig struct {
	TokenSigningSecret   string `envconfig:"TOKEN_SIGNING_SECRET"`
	ReservedCIDRs        string `envconfig:"PLATFORM_RESERVED_CIDRS"`
	PodCIDRs             string `envconfig:"PLATFORM_POD_CIDRS"`
	ServiceCIDRs         string `envconfig:"PLATFORM_SERVICE_CIDRS"`
	NodeCIDRs            string `envconfig:"PLATFORM_NODE_CIDRS"`
	ProtectedCIDRs       string `envconfig:"PLATFORM_PROTECTED_CIDRS"`
	BusinessPrivateCIDRs string `envconfig:"PLATFORM_BUSINESS_PRIVATE_CIDRS"`
	ReservedHostnames    string `envconfig:"PLATFORM_RESERVED_HOSTNAMES"`
	IPv6Enabled          bool   `envconfig:"PLATFORM_IPV6_ENABLED" default:"false"`
	AllowDNSDependencies bool   `envconfig:"PLATFORM_ALLOW_DNS_DEPENDENCIES" default:"false"`
	RegistryEndpoint     string `envconfig:"PLATFORM_REGISTRY_ENDPOINT"`

	RegistryResolverHost string `envconfig:"REGISTRY_RESOLVER_HOST"`

	RegistryAuthFile string `envconfig:"PLATFORM_REGISTRY_AUTH_FILE"`

	SandboxCPULimit    string `envconfig:"SANDBOX_CPU_LIMIT" default:"1"`
	SandboxCPURequest  string `envconfig:"SANDBOX_CPU_REQUEST" default:"250m"`
	SandboxMemoryLimit string `envconfig:"SANDBOX_MEMORY_LIMIT" default:"2Gi"`

	SandboxMemoryRequest string `envconfig:"SANDBOX_MEMORY_REQUEST" default:"512Mi"`
}

type GatewayConfig struct {
	MaxRequestsPerRun    int  `envconfig:"GATEWAY_MAX_REQUESTS_PER_RUN" default:"100"`
	MaxConcurrencyPerRun int  `envconfig:"GATEWAY_MAX_CONCURRENCY_PER_RUN" default:"1"`
	RequireHTTPS         bool `envconfig:"GATEWAY_REQUIRE_HTTPS" default:"false"`
	DisallowRedirects    bool `envconfig:"GATEWAY_DISALLOW_REDIRECTS" default:"true"`
}

type DBConfig struct {
	DSN string `envconfig:"DATABASE_DSN" required:"true"`
}

type RedisConfig struct {
	URL string `envconfig:"REDIS_URL" default:"redis://127.0.0.1:6379/0"`
}

type S3Config struct {
	Endpoint string `envconfig:"S3_ENDPOINT" required:"true"`

	PublicEndpoint string `envconfig:"S3_PUBLIC_ENDPOINT"`

	SandboxEndpoint string        `envconfig:"S3_SANDBOX_ENDPOINT"`
	Region          string        `envconfig:"S3_REGION" default:"us-east-1"`
	Bucket          string        `envconfig:"S3_BUCKET" required:"true"`
	AccessKeyID     string        `envconfig:"S3_ACCESS_KEY_ID" required:"true"`
	SecretAccessKey string        `envconfig:"S3_SECRET_ACCESS_KEY" required:"true"`
	UseSSL          bool          `envconfig:"S3_USE_SSL" default:"false"`
	PresignTTL      time.Duration `envconfig:"S3_PRESIGN_TTL" default:"15m"`
}

type K8sConfig struct {
	Namespace    string `envconfig:"K8S_NAMESPACE" default:"default"`
	WarmPoolName string `envconfig:"SANDBOX_WARM_POOL" default:"agent-sandbox-pool"`

	APIURL string `envconfig:"SANDBOX_API_URL"`

	GatewayName      string `envconfig:"SANDBOX_GATEWAY_NAME"`
	GatewayNamespace string `envconfig:"SANDBOX_GATEWAY_NAMESPACE" default:"default"`

	RequestTimeout              time.Duration `envconfig:"SANDBOX_REQUEST_TIMEOUT" default:"30m"`
	RouterScopedTokenSecretFile string        `envconfig:"SANDBOX_ROUTER_SCOPED_TOKEN_SECRET_FILE"`
	RouterScopedTokenTTL        time.Duration `envconfig:"SANDBOX_ROUTER_SCOPED_TOKEN_TTL" default:"5m"`

	RegistrationServerLabel      string `envconfig:"SANDBOX_REGISTRATION_SERVER_LABEL" default:"agent-platform-server"`
	RegistrationRouterLabel      string `envconfig:"SANDBOX_REGISTRATION_ROUTER_LABEL" default:"agent-platform-router"`
	RegistrationServerPort       int    `envconfig:"SANDBOX_REGISTRATION_SERVER_PORT" default:"8080"`
	RegistrationWarmPoolReplicas int    `envconfig:"SANDBOX_REGISTRATION_WARM_POOL_REPLICAS" default:"1"`
	RegistrationImagePullPolicy  string `envconfig:"SANDBOX_REGISTRATION_IMAGE_PULL_POLICY" default:"IfNotPresent"`

	ObjectStorageCIDR       string `envconfig:"PLATFORM_OBJECT_STORAGE_CIDR"`
	ObjectStoragePort       int    `envconfig:"PLATFORM_OBJECT_STORAGE_PORT" default:"9000"`
	KubeDNSServiceCIDR      string `envconfig:"PLATFORM_KUBE_DNS_SERVICE_CIDR"`
	ControlPlaneServiceCIDR string `envconfig:"PLATFORM_CONTROL_PLANE_SERVICE_CIDR"`
	TemplateNamePrefix      string `envconfig:"SANDBOX_TEMPLATE_PREFIX" default:"agent-platform"`

	RolloutEnabled      bool          `envconfig:"SANDBOX_ROLLOUT_ENABLED" default:"false"`
	RolloutPollInterval time.Duration `envconfig:"SANDBOX_ROLLOUT_POLL_INTERVAL" default:"5s"`
	RolloutTimeout      time.Duration `envconfig:"SANDBOX_ROLLOUT_TIMEOUT" default:"10m"`
}

type ModelConfig struct {
	GatewayBaseURL        string        `envconfig:"MODEL_GATEWAY_BASE_URL" default:"http://control-plane:8080/internal/model/v1"`
	TokenTTL              time.Duration `envconfig:"MODEL_TOKEN_TTL" default:"35m"`
	AllowedUpstream       string        `envconfig:"MODEL_ALLOWED_UPSTREAM" default:"https://opencode.ai/zen/go/v1"`
	MaxRequests           int           `envconfig:"MODEL_MAX_REQUESTS" default:"100"`
	RuntimeGatewayBaseURL string        `envconfig:"RUNTIME_GATEWAY_BASE_URL" default:"http://control-plane:8080/internal/v1/runtime"`
}

type PlatformConfig struct {
	WarmPoolBudget int `envconfig:"PLATFORM_WARM_POOL_BUDGET" default:"4"`

	WarmPoolMaxPerImage int `envconfig:"PLATFORM_WARM_POOL_MAX_PER_IMAGE" default:"2"`

	WarmPoolDefaultPool int `envconfig:"PLATFORM_WARM_POOL_DEFAULT_POOL" default:"0"`
}

type WorkerConfig struct {
	PollInterval      time.Duration `envconfig:"WORKER_POLL_INTERVAL" default:"1s"`
	Concurrency       int           `envconfig:"WORKER_CONCURRENCY" default:"4"`
	WorkerID          string        `envconfig:"WORKER_ID"`
	LeaseTTL          time.Duration `envconfig:"WORKER_LEASE_TTL" default:"60s"`
	HeartbeatInterval time.Duration `envconfig:"WORKER_HEARTBEAT_INTERVAL" default:"20s"`
	MaxAttempts       int           `envconfig:"WORKER_MAX_ATTEMPTS" default:"3"`
	ExecutionTimeout  time.Duration `envconfig:"SANDBOX_EXECUTION_TIMEOUT" default:"30m"`
}

func RequireRebuildKeys(cfg Config) error {
	if cfg.Env == "development" {
		return nil
	}
	if cfg.State.ContentEncryptionKey == "" {
		return fmt.Errorf("CONTENT_ENCRYPTION_KEY is required outside development")
	}
	if cfg.State.FingerprintHMACKey == "" {
		return fmt.Errorf("FINGERPRINT_HMAC_KEY is required outside development")
	}
	if cfg.Security.TokenSigningSecret == "" {
		return fmt.Errorf("TOKEN_SIGNING_SECRET is required outside development")
	}
	return nil
}

func Load() (Config, error) {
	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}
