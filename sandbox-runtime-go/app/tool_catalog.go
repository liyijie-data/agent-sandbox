package app

import (
	"agent-platform/sandbox-runtime-go/api"
	"agent-platform/sandbox-runtime-go/engine"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

const toolSearchName = "agent_search_tools"

func ConfigureToolCatalog(reg *engine.ToolRegistry, maxBytes int) (func() []api.ToolDefinition, error) {
	if reg == nil || maxBytes <= 0 {
		return nil, fmt.Errorf("tool catalog requires a registry and positive budget")
	}
	all := reg.Definitions()
	core := make([]api.ToolDefinition, 0, len(all))
	for _, d := range all {
		if strings.HasPrefix(d.Name, "agent_") {
			core = append(core, d)
		}
	}
	search := api.ToolDefinition{Name: toolSearchName, Description: "Find authorized tools by name, description, or operation", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`)}
	fits := func(ds []api.ToolDefinition) bool { b, _ := json.Marshal(ds); return len(b) <= maxBytes }
	if !fits(core) {
		return nil, fmt.Errorf("core tool definitions exceed configured budget")
	}
	if fits(all) {
		frozen := cloneDefinitions(all)
		return func() []api.ToolDefinition { return cloneDefinitions(frozen) }, nil
	}
	if !fits(append(append([]api.ToolDefinition(nil), core...), search)) {
		return nil, fmt.Errorf("core tool definitions plus tool search exceed configured budget")
	}

	active := make(map[string]bool, len(core))
	for _, d := range core {
		active[d.Name] = true
	}
	var mu sync.RWMutex
	provider := func() []api.ToolDefinition {
		mu.RLock()
		defer mu.RUnlock()
		out := make([]api.ToolDefinition, 0, len(active)+1)
		for _, d := range all {
			if active[d.Name] {
				out = append(out, cloneDefinition(d))
			}
		}
		out = append(out, cloneDefinition(search))
		return cloneDefinitions(out)
	}
	if err := reg.Add(catalogSearchTool{activate: func(query string, limit int) ([]map[string]any, error) {
		q := strings.ToLower(strings.TrimSpace(query))
		if len([]rune(q)) > 256 {
			return nil, fmt.Errorf("tool search query exceeds 256 characters")
		}
		if limit <= 0 || limit > 16 {
			limit = 8
		}
		mu.Lock()
		defer mu.Unlock()
		active = make(map[string]bool, len(core))
		for _, d := range core {
			active[d.Name] = true
		}
		matches := make([]map[string]any, 0, limit)
		for _, d := range all {
			if d.Name == toolSearchName || active[d.Name] {
				continue
			}
			text := strings.ToLower(d.Name + " " + d.Description + " " + string(d.Parameters))
			if q == "" || !strings.Contains(text, q) {
				continue
			}
			candidate := append([]api.ToolDefinition(nil), core...)
			for _, current := range all {
				if active[current.Name] && !strings.HasPrefix(current.Name, "agent_") {
					candidate = append(candidate, current)
				}
			}
			candidate = append(candidate, d, search)
			if !fits(candidate) {
				matches = append(matches, map[string]any{"name": d.Name, "description": "", "activated": false, "reason": "schema_exceeds_budget"})
				if len(matches) == limit {
					break
				}
				continue
			}
			active[d.Name] = true
			desc := d.Description
			if len([]rune(desc)) > 512 {
				desc = string([]rune(desc)[:512])
			}
			matches = append(matches, map[string]any{"name": d.Name, "description": desc, "activated": true})
			if len(matches) == limit {
				break
			}
		}
		return matches, nil
	}}); err != nil {
		return nil, err
	}
	return provider, nil
}

type catalogSearchTool struct {
	activate func(string, int) ([]map[string]any, error)
}

func (catalogSearchTool) Name() string { return toolSearchName }
func (catalogSearchTool) Description() string {
	return "Find authorized tools by name, description, or operation"
}
func (t catalogSearchTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid tool search arguments")
	}
	if len([]rune(in.Query)) > 256 {
		return "", fmt.Errorf("tool search query exceeds 256 characters")
	}
	out, err := t.activate(in.Query, in.Limit)
	if err != nil {
		return "", err
	}
	if len(out) > 16 {
		out = out[:16]
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func cloneDefinition(d api.ToolDefinition) api.ToolDefinition {
	d.Parameters = append(json.RawMessage(nil), d.Parameters...)
	return d
}
func cloneDefinitions(in []api.ToolDefinition) []api.ToolDefinition {
	out := make([]api.ToolDefinition, len(in))
	for i, d := range in {
		out[i] = cloneDefinition(d)
	}
	return out
}
