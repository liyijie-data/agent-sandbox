package app

import (
	"agent-platform/internal/contracts"
	"agent-platform/sandbox-runtime-go/api"
	"agent-platform/sandbox-runtime-go/artifacts"
	"agent-platform/sandbox-runtime-go/engine"
	"agent-platform/sandbox-runtime-go/plugin"
	"agent-platform/sandbox-runtime-go/remote"
	"agent-platform/sandbox-runtime-go/resources"
	"agent-platform/sandbox-runtime-go/tools"
	"agent-platform/sandbox-runtime-go/trace"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

type ContextProcessor interface {
	Process(context.Context, *engine.State) error
}
type Env struct {
	Root      string
	Cfg       *contracts.RuntimeConfig
	Model     engine.Model
	Config    plugin.Config
	Specs     []contracts.ToolSpec
	Processor ContextProcessor
	Trace     TraceService
	Events    EventsService
}
type ResourceProvider struct{ Root string }
type CheckpointProvider struct{ Root string }
type ArtifactProvider struct{ Root string }
type EventProvider struct{ engine.EventSink }
type TraceProvider struct{ Recorder *trace.Recorder }
type nopTrace struct{}

func (nopTrace) Record(string, any) error { return nil }
func (nopTrace) Freeze()                  {}

type LoopProvider struct{ Agent *engine.Agent }
type Composition struct {
	Model      engine.Model
	Tools      *engine.ToolRegistry
	Resources  ResourcesService
	Checkpoint CheckpointService
	Artifacts  ArtifactService
	Events     EventsService
	Trace      TraceService
	Context    ContextProcessor
	Loop       LoopService
	Versions   map[string]string
	Close      func() error
}
type ResourcesService interface {
	Prepare(context.Context, *contracts.RuntimeConfig, bool) ([]contracts.Message, error)
}
type resourcesService struct{ root string }

func (s resourcesService) Prepare(ctx context.Context, cfg *contracts.RuntimeConfig, resume bool) ([]contracts.Message, error) {
	return resources.Prepare(ctx, s.root, cfg, resume)
}

type LoopService interface {
	RunState(context.Context, engine.ModelRequest) (engine.State, error)
	Configure(engine.EventSink, func(context.Context, *engine.State) error, func(context.Context, *engine.State, error) (bool, error))
	Observe(func(string, any) error)
}
type loopService struct{ a *engine.Agent }

func (s loopService) RunState(ctx context.Context, r engine.ModelRequest) (engine.State, error) {
	return s.a.RunState(ctx, r)
}
func (s loopService) Configure(events engine.EventSink, hook func(context.Context, *engine.State) error, recovery func(context.Context, *engine.State, error) (bool, error)) {
	s.a.Events = events
	s.a.BeforeModel = hook
	s.a.OnModelError = recovery
}
func (s loopService) Observe(fn func(string, any) error) { s.a.Observer = fn }
func BuildComposition(ctx context.Context, env Env) (*Composition, error) {
	root := env.Root
	if root == "" {
		root = "/app"
	}
	selected := env.Config
	trsvc := env.Trace
	if trsvc == nil {
		trsvc = nopTrace{}
	}
	evsvc := env.Events
	if evsvc == nil {
		evsvc = engine.NopSink{}
	}
	_, pr, closeFn, e := buildResolved(ctx, filepath.Join(root, "workspace"), env.Model, env.Specs, env.Processor, selected, func() *contracts.ResultBundle {
		if env.Cfg == nil {
			return nil
		}
		return env.Cfg.ResultBundle
	}(), trsvc, evsvc)
	if e != nil {
		return nil, e
	}
	resolve := func(name string, dst any) error {
		v, ok := pr.Resolve(name)
		if !ok {
			return fmt.Errorf("missing provider %q", name)
		}
		switch d := dst.(type) {
		case *engine.Model:
			x, ok := v.(engine.Model)
			if !ok {
				return fmt.Errorf("invalid provider %q", name)
			}
			*d = x
		case **engine.ToolRegistry:
			x, ok := v.(*engine.ToolRegistry)
			if !ok {
				return fmt.Errorf("invalid provider %q", name)
			}
			*d = x
		case *ResourcesService:
			x, ok := v.(ResourcesService)
			if !ok {
				return fmt.Errorf("invalid provider %q", name)
			}
			*d = x
		case *LoopService:
			x, ok := v.(LoopService)
			if !ok {
				return fmt.Errorf("invalid provider %q", name)
			}
			*d = x
		case *CheckpointService:
			x, ok := v.(CheckpointService)
			if !ok {
				return fmt.Errorf("invalid provider %q", name)
			}
			*d = x
		case *ArtifactService:
			x, ok := v.(ArtifactService)
			if !ok {
				return fmt.Errorf("invalid provider %q", name)
			}
			*d = x
		case *TraceService:
			x, ok := v.(TraceService)
			if !ok {
				return fmt.Errorf("invalid provider %q", name)
			}
			*d = x
		case *EventsService:
			x, ok := v.(EventsService)
			if !ok {
				return fmt.Errorf("invalid provider %q", name)
			}
			*d = x
		}
		return nil
	}
	var m engine.Model
	var ts *engine.ToolRegistry
	var rs ResourcesService
	var ls LoopService
	var cs CheckpointService
	var as ArtifactService
	var trs TraceService
	var evs EventsService
	var cps ContextProcessor
	for _, x := range []struct {
		n string
		d any
	}{{"model", &m}, {"tools", &ts}, {"resources", &rs}, {"loop", &ls}, {"checkpoint", &cs}, {"artifacts", &as}, {"trace", &trs}, {"events", &evs}} {
		if e := resolve(x.n, x.d); e != nil {
			_ = closeFn()
			return nil, e
		}
	}
	v, ok := pr.Resolve("context")
	if !ok {
		_ = closeFn()
		return nil, fmt.Errorf("missing provider %q", "context")
	}
	ca, ok := v.(contextAdapter)
	if !ok || ca.p == nil {
		_ = closeFn()
		return nil, fmt.Errorf("invalid provider %q", "context")
	}
	cps = ca.p
	versions := map[string]string{}
	for _, p := range selected.Plugins {
		versions[p.Name] = "go-runtime/1"
	}
	c := &Composition{Model: m, Tools: ts, Resources: rs, Checkpoint: cs, Artifacts: as, Events: evs, Trace: trs, Context: cps, Loop: ls, Versions: versions, Close: closeFn}
	return c, nil
}
func DefaultConfig() plugin.Config {
	names := []string{"model.gateway", "tools.standard", "resources.standard", "context.standard", "loop.standard", "checkpoint.standard", "artifacts.standard", "events.standard", "trace.standard"}
	c := plugin.Config{}
	for _, n := range names {
		c.Plugins = append(c.Plugins, plugin.PluginSelection{Name: n})
	}
	return c
}

type remoteTool struct {
	registry                      *remote.Registry
	name, remoteName, description string
	toolID, operation             string
	definition                    api.ToolDefinition
}

func (t remoteTool) Definition() api.ToolDefinition { return t.definition }
func (t remoteTool) ToolID() string {
	if t.toolID != "" {
		return t.toolID
	}
	id, _, ok := remote.DecodeToolName(t.name)
	if ok {
		return id
	}
	return ""
}
func (t remoteTool) Operation() string {
	if t.operation != "" {
		return t.operation
	}
	_, op, ok := remote.DecodeToolName(t.name)
	if ok {
		return op
	}
	return t.name
}

type standardPlugin struct {
	name, version, service string
	value                  any
}
type contextAdapter struct{ p ContextProcessor }
type noopProcessor struct{}

func (noopProcessor) Process(context.Context, *engine.State) error { return nil }

func (c contextAdapter) Process(ctx context.Context, s any) error {
	if c.p == nil {
		return nil
	}
	st, ok := s.(*engine.State)
	if !ok {
		return fmt.Errorf("invalid state")
	}
	return c.p.Process(ctx, st)
}
func (p *standardPlugin) Name() string                                { return p.name }
func (p *standardPlugin) Version() string                             { return p.version }
func (p *standardPlugin) Start(context.Context, *plugin.Config) error { return nil }
func (p *standardPlugin) Stop(context.Context) error                  { return nil }
func (p *standardPlugin) Components() plugin.Components {
	var c plugin.Components
	switch p.service {
	case "model":
		c.Model = p.value
	case "tools":
		c.Tools = p.value
	case "resources":
		c.Resources = p.value
	case "context":
		if p.value != nil {
			c.Context = contextAdapter{p.value.(ContextProcessor)}
		}
	case "loop":
		c.Loop = p.value
	case "checkpoint":
		c.Checkpoint = p.value
	case "artifacts":
		c.Artifacts = p.value
	case "events":
		c.Events = p.value
	case "trace":
		c.Trace = p.value
	}
	return c
}

func (t remoteTool) Name() string        { return t.name }
func (t remoteTool) Description() string { return t.description }
func (t remoteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	v, err := t.registry.Call(ctx, t.remoteName, string(args))
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func Build(ctx context.Context, root string, model engine.Model, specs []contracts.ToolSpec, processor ContextProcessor, selected plugin.Config) (*engine.Agent, func() error, error) {
	a, _, closeFn, e := buildResolved(ctx, root, model, specs, processor, selected, nil, nopTrace{}, engine.NopSink{})
	return a, closeFn, e
}

func buildResolved(ctx context.Context, root string, model engine.Model, specs []contracts.ToolSpec, processor ContextProcessor, selected plugin.Config, destination *contracts.ResultBundle, tr TraceService, ev EventsService) (*engine.Agent, *plugin.Registry, func() error, error) {
	_ = processor
	reg, err := engine.NewToolRegistry(tools.Builtin{Root: root, OutputRoot: filepath.Join(filepath.Dir(root), "output")}.Tools())
	if err != nil {
		return nil, nil, nil, err
	}
	remoteReg, unavailable, err := remote.DiscoverWithOptions(ctx, specs, remote.StdioOptions{SandboxRoot: root, WorkingDir: root})
	if err != nil {
		return nil, nil, nil, err
	}
	for _, d := range remoteReg.Definitions() {
		modelName := remote.ModelToolName(d.Function.Name)
		if _, ok := reg.Get(modelName); ok {
			return nil, nil, nil, fmt.Errorf("duplicate tool %q", modelName)
		}
		params, _ := json.Marshal(d.Function.Parameters)
		id, operation, _ := remote.DecodeToolName(d.Function.Name)
		if err := reg.Add(remoteTool{registry: remoteReg, name: modelName, remoteName: d.Function.Name, toolID: id, operation: operation, description: d.Function.Description, definition: api.ToolDefinition{Name: modelName, Description: d.Function.Description, Parameters: params}}); err != nil {
			return nil, nil, nil, err
		}
	}
	_ = unavailable
	a := &engine.Agent{Model: model, Tools: reg}
	if processor != nil {
		a.BeforeModel = processor.Process
	}

	if processor == nil {
		processor = noopProcessor{}
	}
	values := map[string]any{"model": model, "tools": reg, "resources": resourcesService{filepath.Dir(root)}, "context": processor, "loop": loopService{a}, "checkpoint": StandardCheckpointService{}, "artifacts": StandardArtifactService{Bundler: artifacts.Bundler{Destination: destination, TraceRoot: filepath.Join(filepath.Dir(root), ".runtime-trace")}}, "events": ev, "trace": tr}
	services := []struct{ name, service string }{{"model.gateway", "model"}, {"tools.standard", "tools"}, {"resources.standard", "resources"}, {"context.standard", "context"}, {"loop.standard", "loop"}, {"checkpoint.standard", "checkpoint"}, {"artifacts.standard", "artifacts"}, {"events.standard", "events"}, {"trace.standard", "trace"}}
	factories := make([]plugin.PluginFactory, 0, len(services))
	for _, s := range services {
		s := s
		factories = append(factories, plugin.PluginFactory{Name: s.name, Version: "go-runtime/1", Provides: []string{s.service}, New: func() plugin.Plugin {
			return &standardPlugin{name: s.name, version: "go-runtime/1", service: s.service, value: values[s.service]}
		}})
	}
	pr, e := plugin.NewRegistry(factories)
	if e != nil {
		return nil, nil, nil, e
	}
	if e = pr.Load(ctx, selected); e != nil {
		return nil, pr, func() error { _ = pr.Close(ctx); return remoteReg.Close() }, e
	}
	for _, s := range services {
		if _, ok := pr.Resolve(s.service); !ok {
			_ = pr.Close(ctx)
			return nil, pr, func() error { _ = pr.Close(ctx); return remoteReg.Close() }, fmt.Errorf("missing provider %q", s.service)
		}
	}
	if mv, ok := pr.Resolve("model"); ok {
		if mm, ok := mv.(engine.Model); ok {
			a.Model = mm
		}
	}
	return a, pr, func() error { _ = pr.Close(ctx); return remoteReg.Close() }, nil
}
