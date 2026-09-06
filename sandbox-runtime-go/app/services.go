package app

import (
	"agent-platform/internal/contracts"
	"agent-platform/sandbox-runtime-go/artifacts"
	"agent-platform/sandbox-runtime-go/checkpoint"
	"agent-platform/sandbox-runtime-go/engine"
	"agent-platform/sandbox-runtime-go/trace"
	"context"
)

type CheckpointService interface {
	Save(context.Context, string, string, string, checkpoint.Options, []byte) error
	RestoreTo(context.Context, string, string, checkpoint.Options) (checkpoint.State, error)
}
type StandardCheckpointService struct{}

func (StandardCheckpointService) Save(c context.Context, ws, out, path string, o checkpoint.Options, d []byte) error {
	return checkpoint.Save(c, ws, out, path, o, d)
}
func (StandardCheckpointService) RestoreTo(c context.Context, path, root string, o checkpoint.Options) (checkpoint.State, error) {
	return checkpoint.RestoreTo(c, path, root, o)
}

type ArtifactService interface {
	Snapshot(string) (artifacts.Baseline, error)
	Collect(string, string, artifacts.Baseline) (artifacts.Bundle, error)
	DeliverContext(context.Context, artifacts.Bundle, *contracts.ResultBundle) (contracts.DeliveryOutcome, error)
}
type StandardArtifactService struct{ Bundler artifacts.Bundler }

func (s StandardArtifactService) Snapshot(root string) (artifacts.Baseline, error) {
	return artifacts.Snapshot(root)
}
func (s StandardArtifactService) Collect(ws, out string, b artifacts.Baseline) (artifacts.Bundle, error) {
	return s.Bundler.Collect(ws, out, b)
}
func (s StandardArtifactService) DeliverContext(c context.Context, b artifacts.Bundle, d *contracts.ResultBundle) (contracts.DeliveryOutcome, error) {
	return s.Bundler.DeliverContext(c, b, d)
}

type TraceService interface {
	Record(string, any) error
	Freeze()
}
type StandardTraceService struct{ Recorder *trace.Recorder }

func (s StandardTraceService) Record(n string, v any) error { return s.Recorder.Record(n, v) }
func (s StandardTraceService) Freeze()                      { s.Recorder.Freeze() }

type EventsService interface{ engine.EventSink }
