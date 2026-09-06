package remote

import (
	"context"
	"fmt"

	"agent-platform/internal/contracts"
)

type Definition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}
type FunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
type Unavailable struct {
	ToolID string `json:"tool_id"`
	Error  string `json:"error"`
}

type adapter interface {
	definitions() ([]Definition, error)
	call(context.Context, string, string) (map[string]any, error)
	close() error
	id() string
}
type Registry struct {
	adapters []adapter
	defs     []Definition
	owners   map[string]adapter
}

func Discover(ctx context.Context, specs []contracts.ToolSpec) (*Registry, []Unavailable, error) {
	r := &Registry{owners: map[string]adapter{}}
	seenIDs := map[string]bool{}
	var unavailable []Unavailable
	for _, spec := range specs {
		if seenIDs[spec.ID] {
			return nil, nil, fmt.Errorf("duplicate remote tool id %q", spec.ID)
		}
		seenIDs[spec.ID] = true
		var a adapter
		var err error
		switch spec.Type {
		case contracts.ToolTypeOpenAPI:
			a, err = newOpenAPI(ctx, spec)
		case contracts.ToolTypeMCP:
			a, err = newMCP(ctx, spec)
		default:
			return nil, nil, fmt.Errorf("unsupported remote tool type %q", spec.Type)
		}
		if err != nil {
			if isUnavailable(err) {
				unavailable = append(unavailable, Unavailable{spec.ID, err.Error()})
				continue
			}
			_ = r.Close()
			return nil, nil, err
		}
		d, err := a.definitions()
		if err != nil {
			if isUnavailable(err) {
				unavailable = append(unavailable, Unavailable{spec.ID, err.Error()})
				_ = a.close()
				continue
			}
			_ = a.close()
			_ = r.Close()
			return nil, nil, err
		}
		r.adapters = append(r.adapters, a)
		r.defs = append(r.defs, d...)
		for _, definition := range d {
			r.owners[definition.Function.Name] = a
		}
	}
	return r, unavailable, nil
}
func (r *Registry) Definitions() []Definition { return append([]Definition(nil), r.defs...) }
func (r *Registry) Call(ctx context.Context, name, args string) (map[string]any, error) {
	if a := r.owners[name]; a != nil {
		return a.call(ctx, name, args)
	}
	return nil, fmt.Errorf("tool not found: %s", name)
}
func (r *Registry) Close() error {
	var first error
	for i := len(r.adapters) - 1; i >= 0; i-- {
		if err := r.adapters[i].close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
