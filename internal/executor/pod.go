package executor

import (
	"context"
	"time"

	"agent-platform/internal/sandbox"
)

type Pod interface {
	Prepare(ctx context.Context, runJSON []byte) error

	Write(ctx context.Context, path string, data []byte) error

	Execute(ctx context.Context, command string, timeout time.Duration) (exitCode int, err error)

	Read(ctx context.Context, path string) ([]byte, error)

	Delete(ctx context.Context) error

	Name() string
}

type SandboxProvider interface {
	Create(ctx context.Context, runID string, stageNo int, fence int64, pool string) (Pod, error)
}

type ProductionSandboxProvider struct {
	client sandbox.Client
}

func NewProductionSandboxProvider(client sandbox.Client) *ProductionSandboxProvider {
	return &ProductionSandboxProvider{client: client}
}

func (p *ProductionSandboxProvider) Create(ctx context.Context, runID string, stageNo int, fence int64, pool string) (Pod, error) {
	var h sandbox.Handle
	var err error
	if pool != "" {

		h, err = p.client.CreateInPool(ctx, runID, pool)
	} else {
		h, err = p.client.Create(ctx, runID)
	}
	if err != nil {
		return nil, err
	}
	return &sandboxHandlePod{handle: h}, nil
}

type sandboxHandlePod struct {
	handle sandbox.Handle
	stdout string
	stderr string
}

func (p *sandboxHandlePod) Prepare(ctx context.Context, runJSON []byte) error {
	return p.handle.Write(ctx, RunConfigPath, runJSON)
}

func (p *sandboxHandlePod) Write(ctx context.Context, path string, data []byte) error {
	return p.handle.Write(ctx, path, data)
}

func (p *sandboxHandlePod) Execute(ctx context.Context, command string, timeout time.Duration) (int, error) {

	res, err := p.handle.Run(ctx, command)
	if err != nil {
		return 0, err
	}
	p.stdout, p.stderr = res.Stdout, res.Stderr
	return res.ExitCode, nil
}

func (p *sandboxHandlePod) RuntimeOutput() (string, string) { return p.stdout, p.stderr }

func (p *sandboxHandlePod) Read(ctx context.Context, path string) ([]byte, error) {
	return p.handle.Read(ctx, path)
}

func (p *sandboxHandlePod) Delete(ctx context.Context) error {
	return p.handle.Close(ctx)
}

func (p *sandboxHandlePod) Name() string { return p.handle.SandboxName() }
