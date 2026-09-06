package sandbox

import (
	"context"
	"fmt"
	"os"

	"agent-platform/internal/config"
)

var defaultFactory Factory = stubFactory

func stubFactory(ctx context.Context, cfg config.K8sConfig, runID string) (Handle, error) {
	_ = ctx
	_ = cfg
	if UseNoopFromEnv() {
		return &noopHandle{runID: runID}, nil
	}
	return nil, fmt.Errorf(
		"agent-sandbox SDK not linked; rebuild with '-tags agentsandbox' (requires Go 1.26+) or set AGENT_PLATFORM_SANDBOX=noop",
	)
}

func NewNoopFactory() Factory {
	return func(ctx context.Context, cfg config.K8sConfig, runID string) (Handle, error) {
		_ = ctx
		_ = cfg
		return &noopHandle{runID: runID}, nil
	}
}

type noopHandle struct{ runID string }

func (h *noopHandle) Run(ctx context.Context, command string) (Result, error) {
	_ = ctx
	return Result{ExitCode: 0, Stdout: "noop: " + command}, nil
}

func (h *noopHandle) Write(ctx context.Context, path string, data []byte) error {
	_ = ctx
	_ = path
	_ = data
	return nil
}

func (h *noopHandle) Read(ctx context.Context, path string) ([]byte, error) {
	_ = ctx
	_ = path
	return []byte(`{"status":"ok","run_id":"` + h.runID + `"}`), nil
}

func (h *noopHandle) Close(ctx context.Context) error {
	_ = ctx
	return nil
}

func (h *noopHandle) SandboxName() string { return h.runID }

func UseNoopFromEnv() bool {
	return os.Getenv("AGENT_PLATFORM_SANDBOX") == "noop"
}
