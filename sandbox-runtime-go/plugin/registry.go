package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const (
	ContractVersion          = "agent-platform-runtime/v1"
	RuntimeVersion           = "agent-runtime-go/v1"
	MaxBodyBytes       int64 = 4 * 1024 * 1024
	MaxOutputBytes     int64 = 2 * 1024 * 1024
	CancelGraceSeconds       = 10
)

type Plugin interface {
	Name() string
	Version() string
	Start(context.Context, *Config) error
	Stop(context.Context) error
}
type ModelProvider interface{}
type ToolProvider interface{}
type ResourceProvider interface{}
type ContextProcessor interface {
	Process(context.Context, any) error
}
type AgentLoop interface{}
type CheckpointStore interface{}
type ArtifactStore interface{}
type EventSink interface{}
type TraceRecorder interface{}
type Components struct {
	Model      ModelProvider
	Tools      ToolProvider
	Resources  ResourceProvider
	Context    ContextProcessor
	Loop       AgentLoop
	Checkpoint CheckpointStore
	Artifacts  ArtifactStore
	Events     EventSink
	Trace      TraceRecorder
}
type ComponentProvider interface{ Components() Components }
type PluginFactory struct {
	Name, Version      string
	Requires, Provides []string
	New                func() Plugin
}

type Config struct {
	Plugins []PluginSelection `json:"plugins"`
}
type PluginSelection struct {
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config,omitempty"`
}

type Registry struct {
	factories map[string]PluginFactory
	plugins   []Plugin
	services  map[string]any
	frozen    bool
}

func NewRegistry(fs []PluginFactory) (*Registry, error) {
	r := &Registry{factories: make(map[string]PluginFactory, len(fs)), services: map[string]any{}}
	for _, f := range fs {
		if f.Name == "" || f.New == nil {
			return nil, fmt.Errorf("invalid plugin factory")
		}
		if _, ok := r.factories[f.Name]; ok {
			return nil, fmt.Errorf("duplicate plugin %q", f.Name)
		}
		r.factories[f.Name] = f
	}
	return r, nil
}
func (r *Registry) Load(ctx context.Context, cfg Config) error {
	if r.frozen {
		return fmt.Errorf("plugin registry is frozen")
	}
	selected := map[string]bool{}
	providers := map[string]string{}
	for _, s := range cfg.Plugins {
		if selected[s.Name] {
			return fmt.Errorf("duplicate plugin %q", s.Name)
		}
		selected[s.Name] = true
		if _, ok := r.factories[s.Name]; !ok {
			return fmt.Errorf("unknown plugin %q", s.Name)
		}
	}
	for _, s := range cfg.Plugins {
		for _, capability := range r.factories[s.Name].Provides {
			if previous, ok := providers[capability]; ok {
				return fmt.Errorf("duplicate provider %q: %s and %s", capability, previous, s.Name)
			}
			providers[capability] = s.Name
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var order []string
	var visit func(string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("plugin dependency cycle at %q", name)
		}
		visiting[name] = true
		f := r.factories[name]
		for _, dep := range f.Requires {
			if !selected[dep] {
				return fmt.Errorf("plugin %q requires missing plugin %q", name, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		order = append(order, name)
		return nil
	}
	for _, s := range cfg.Plugins {
		if err := visit(s.Name); err != nil {
			return err
		}
	}
	for _, name := range order {
		factory := r.factories[name]
		p := factory.New()
		if p == nil {
			_ = r.stopStarted(ctx)
			return fmt.Errorf("plugin %q returned nil", name)
		}
		if err := p.Start(ctx, &cfg); err != nil {
			_ = r.stopStarted(ctx)
			return fmt.Errorf("start plugin %q: %w", name, err)
		}
		r.plugins = append(r.plugins, p)
		if p.Name() != name || (factory.Version != "" && p.Version() != factory.Version) {
			_ = r.stopStarted(ctx)
			r.services = map[string]any{}
			return fmt.Errorf("plugin %q identity mismatch", name)
		}
		if cp, ok := p.(ComponentProvider); ok {
			c := cp.Components()
			actual := map[string]any{"model": c.Model, "tools": c.Tools, "resources": c.Resources, "context": c.Context, "loop": c.Loop, "checkpoint": c.Checkpoint, "artifacts": c.Artifacts, "events": c.Events, "trace": c.Trace}
			declared := map[string]bool{}
			for _, x := range factory.Provides {
				declared[x] = true
			}
			for k, v := range actual {
				if v != nil && !declared[k] {
					_ = r.stopStarted(ctx)
					r.services = map[string]any{}
					return fmt.Errorf("plugin %q provides undeclared service %q", name, k)
				}
				if v != nil {
					if _, exists := r.services[k]; exists {
						_ = r.stopStarted(ctx)
						r.services = map[string]any{}
						return fmt.Errorf("duplicate service %q", k)
					}
					r.services[k] = v
				}
			}
			for _, k := range factory.Provides {
				if actual[k] == nil {
					_ = r.stopStarted(ctx)
					r.services = map[string]any{}
					return fmt.Errorf("plugin %q missing declared service %q", name, k)
				}
			}
		} else if len(factory.Provides) != 0 {
			_ = r.stopStarted(ctx)
			return fmt.Errorf("plugin %q does not provide declared components", name)
		}
	}
	r.frozen = true
	return nil
}
func (r *Registry) Resolve(name string) (any, bool) { v, ok := r.services[name]; return v, ok }
func (r *Registry) stopStarted(ctx context.Context) error {
	var first error
	for i := len(r.plugins) - 1; i >= 0; i-- {
		if err := r.plugins[i].Stop(ctx); err != nil && first == nil {
			first = err
		}
	}
	r.plugins = nil
	r.services = map[string]any{}
	return first
}
func (r *Registry) Close(ctx context.Context) error { return r.stopStarted(ctx) }
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxBodyBytes+1))
	if err != nil {
		return Config{}, err
	}
	if int64(len(b)) > MaxBodyBytes {
		return Config{}, fmt.Errorf("runtime config too large")
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("runtime config: %w", err)
	}
	if c.Plugins == nil || len(c.Plugins) == 0 {
		return Config{}, fmt.Errorf("plugins must be non-empty")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Config{}, fmt.Errorf("runtime config trailing data")
	}
	return c, nil
}
func DecodeConfig(b []byte) (Config, error) {
	if int64(len(b)) > MaxBodyBytes {
		return Config{}, fmt.Errorf("runtime config too large")
	}
	var c Config
	if string(bytes.TrimSpace(b)) == "null" {
		return Config{}, fmt.Errorf("runtime config must be object")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(&c); e != nil {
		return Config{}, e
	}
	if c.Plugins == nil || len(c.Plugins) == 0 {
		return Config{}, fmt.Errorf("plugins must be non-empty")
	}
	var x any
	if e := d.Decode(&x); e != io.EOF {
		return Config{}, fmt.Errorf("runtime config trailing data")
	}
	return c, nil
}
