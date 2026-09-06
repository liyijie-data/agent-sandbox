package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"agent-platform/internal/config"
)

type Handle interface {
	Run(ctx context.Context, command string) (Result, error)
	Write(ctx context.Context, path string, data []byte) error
	Read(ctx context.Context, path string) ([]byte, error)
	Close(ctx context.Context) error

	SandboxName() string
}

type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type Client interface {
	Create(ctx context.Context, runID string) (Handle, error)
	CreateInPool(ctx context.Context, runID, warmPool string) (Handle, error)
}

type SDKClient struct {
	log     *slog.Logger
	cfg     config.K8sConfig
	factory Factory
}

type Factory func(ctx context.Context, cfg config.K8sConfig, runID string) (Handle, error)

func NewClient(log *slog.Logger, cfg config.K8sConfig, factory Factory) *SDKClient {
	if factory == nil {
		factory = defaultFactory
	}
	return &SDKClient{log: log, cfg: cfg, factory: factory}
}

func (c *SDKClient) Create(ctx context.Context, runID string) (Handle, error) {
	return c.CreateInPool(ctx, runID, c.cfg.WarmPoolName)
}

func (c *SDKClient) CreateInPool(ctx context.Context, runID, warmPool string) (Handle, error) {
	cfg := c.cfg
	if warmPool != "" {
		cfg.WarmPoolName = warmPool
	}
	c.log.Info("creating sandbox", "run_id", runID, "warm_pool", cfg.WarmPoolName)
	return c.factory(ctx, cfg, runID)
}

func WriteJSON(ctx context.Context, sb Handle, path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	return sb.Write(ctx, path, data)
}

func ReadJSON(ctx context.Context, sb Handle, path string, dest any) error {
	data, err := sb.Read(ctx, path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(dest)
}
