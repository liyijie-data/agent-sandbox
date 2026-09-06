//go:build agentsandbox

package sandbox

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"agent-platform/internal/config"

	agentsandbox "sigs.k8s.io/agent-sandbox/clients/go/sandbox"
	"sigs.k8s.io/agent-sandbox/sandbox-router/authz"
)

func init() {
	defaultFactory = sdkFactory
}

func sdkFactory(ctx context.Context, cfg config.K8sConfig, runID string) (Handle, error) {
	opts := agentsandbox.Options{}
	switch {
	case cfg.APIURL != "":
		opts.APIURL = cfg.APIURL
	case cfg.GatewayName != "":
		opts.GatewayName = cfg.GatewayName
		opts.GatewayNamespace = cfg.GatewayNamespace
	}

	if cfg.RequestTimeout > 0 {
		opts.RequestTimeout = cfg.RequestTimeout
		opts.PerAttemptTimeout = cfg.RequestTimeout
	}
	if cfg.RouterScopedTokenSecretFile != "" {
		secret, err := os.ReadFile(cfg.RouterScopedTokenSecretFile)
		if err != nil {
			return nil, fmt.Errorf("read sandbox router scoped-token secret: %w", err)
		}
		secret = []byte(strings.TrimSpace(string(secret)))
		if len(secret) < 32 {
			return nil, fmt.Errorf("sandbox router scoped-token secret must be at least 32 bytes")
		}
		ttl := cfg.RouterScopedTokenTTL
		if ttl <= 0 {
			return nil, fmt.Errorf("sandbox router scoped-token TTL must be positive")
		}
		base := opts.HTTPTransport
		if base == nil {
			base = http.DefaultTransport
		}
		opts.HTTPTransport = scopedTokenTransport{base: base, secret: secret, ttl: ttl}
	}

	client, err := agentsandbox.NewClient(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("agent-sandbox client: %w", err)
	}

	ns := cfg.Namespace
	if ns == "" {
		ns = "default"
	}
	sb, err := client.CreateSandbox(ctx, cfg.WarmPoolName, ns)
	if err != nil {
		client.DeleteAll(ctx)
		return nil, fmt.Errorf("create sandbox for run %s: %w", runID, err)
	}

	return &sdkHandle{client: client, sb: sb}, nil
}

type scopedTokenTransport struct {
	base   http.RoundTripper
	secret []byte
	ttl    time.Duration
}

func (t scopedTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	namespace := clone.Header.Get("X-Sandbox-Namespace")
	name := clone.Header.Get("X-Sandbox-ID")
	if namespace == "" || name == "" {
		return nil, fmt.Errorf("sandbox router scoped-token requires X-Sandbox-Namespace and X-Sandbox-ID")
	}
	token, err := authz.MintScopedToken(t.secret, namespace, name, t.ttl)
	if err != nil {
		return nil, fmt.Errorf("mint sandbox router scoped token: %w", err)
	}
	clone.Header.Set("Authorization", "Bearer "+token)
	clone.Header.Del("X-Sandbox-Pod-IP")
	clone.Header.Del("X-Sandbox-UID")
	return t.base.RoundTrip(clone)
}

type sdkHandle struct {
	client *agentsandbox.Client
	sb     *agentsandbox.Sandbox
}

func (h *sdkHandle) Run(ctx context.Context, command string) (Result, error) {
	res, err := h.sb.Run(ctx, command)
	if err != nil {
		return Result{}, err
	}
	return Result{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
	}, nil
}

func (h *sdkHandle) Write(ctx context.Context, filePath string, data []byte) error {

	base := path.Base(strings.TrimPrefix(filePath, "/"))
	return h.sb.Write(ctx, base, data)
}

func (h *sdkHandle) Read(ctx context.Context, filePath string) ([]byte, error) {

	rel := strings.TrimPrefix(strings.TrimPrefix(filePath, "/"), "app/")
	return h.sb.Read(ctx, rel)
}

func (h *sdkHandle) Close(ctx context.Context) error {
	return h.sb.Close(ctx)
}

func (h *sdkHandle) SandboxName() string { return h.sb.SandboxName() }
