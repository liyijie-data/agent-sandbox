package engine

import (
	"agent-platform/sandbox-runtime-go/api"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxIterations = 40
const MaxToolConcurrency = 8
const maxToolErrorOutputBytes = 32 << 10

type Message = api.Message
type ToolCall = api.ToolCall
type ModelRequest = api.ModelRequest
type ModelResponse = api.ModelResponse
type Model = api.Model
type Tool = api.Tool
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewToolRegistry(ts []Tool) (*ToolRegistry, error) {
	r := &ToolRegistry{tools: map[string]Tool{}}
	for _, t := range ts {
		if t == nil || t.Name() == "" {
			return nil, fmt.Errorf("invalid tool")
		}
		if _, ok := r.tools[t.Name()]; ok {
			return nil, fmt.Errorf("duplicate tool %q", t.Name())
		}
		r.tools[t.Name()] = t
	}
	return r, nil
}
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}
func (r *ToolRegistry) Add(t Tool) error {
	if t == nil || t.Name() == "" {
		return fmt.Errorf("invalid tool")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Name()]; ok {
		return fmt.Errorf("duplicate tool %q", t.Name())
	}
	r.tools[t.Name()] = t
	return nil
}
func (r *ToolRegistry) Definitions() []api.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]api.ToolDefinition, 0, len(names))
	for _, n := range names {
		if provider, ok := r.tools[n].(interface{ Definition() api.ToolDefinition }); ok {
			out = append(out, provider.Definition())
			continue
		}
		out = append(out, api.ToolDefinition{Name: n, Description: r.tools[n].Description(), Parameters: json.RawMessage(`{"type":"object"}`)})
	}
	return out
}

type Event = api.Event
type EventSink = api.EventSink
type NopSink struct{}

func (NopSink) Emit(context.Context, Event) error { return nil }

type Agent struct {
	Model         Model
	Tools         *ToolRegistry
	Events        EventSink
	MaxIterations int
	BeforeModel   func(context.Context, *State) error
	OnModelError  func(context.Context, *State, error) (bool, error)
	Observer      func(string, any) error
}
type RunStatus string

const (
	StatusOK            RunStatus = "ok"
	StatusAwaitingInput RunStatus = "awaiting_input"
	StatusError         RunStatus = "error"
)

type Choice = api.Choice
type InputRequest struct {
	Kind    string   `json:"kind"`
	Prompt  string   `json:"prompt"`
	Options []Choice `json:"options,omitempty"`
}
type State struct {
	Messages []Message
	Cursor   int64
	Status   RunStatus
	Summary  string
	Request  *InputRequest
}

func (a *Agent) RunState(ctx context.Context, req ModelRequest) (State, error) {
	if a.Model == nil {
		return State{Status: StatusError}, fmt.Errorf("model is not configured")
	}
	if a.Tools == nil {
		a.Tools, _ = NewToolRegistry(nil)
	}
	if a.Events == nil {
		a.Events = NopSink{}
	}
	limit := a.MaxIterations
	if limit <= 0 || limit > MaxIterations {
		limit = MaxIterations
	}
	msgs := append([]Message(nil), req.Messages...)
	state := State{Messages: msgs, Status: StatusOK}
	for i := 0; i < limit; i++ {
		if err := ctx.Err(); err != nil {
			state.Status = StatusError
			return state, fmt.Errorf("execution cancelled: %w", err)
		}
		if a.BeforeModel != nil {
			if e := a.BeforeModel(ctx, &state); e != nil {
				state.Status = StatusError
				return state, e
			}
		}
		resp, e := a.Model.Complete(ctx, ModelRequest{Messages: state.Messages, Model: req.Model, ReasoningEffort: req.ReasoningEffort})
		if e != nil && a.OnModelError != nil {
			retry, hookErr := a.OnModelError(ctx, &state, e)
			if hookErr != nil {
				state.Status = StatusError
				return state, hookErr
			}
			if retry {
				resp, e = a.Model.Complete(ctx, ModelRequest{Messages: state.Messages, Model: req.Model, ReasoningEffort: req.ReasoningEffort})
			}
		}
		if e != nil {
			state.Status = StatusError
			a.observe("agent.final", map[string]any{"status": "error", "error": e.Error()})
			return state, e
		}
		state.Messages = append(state.Messages, resp.Message)
		if len(resp.Message.ToolCalls) == 0 {
			state.Summary = resp.Message.Content
			a.observe("agent.final", map[string]any{"status": "ok", "content": resp.Message.Content})
			return state, nil
		}
		ordinary := make([]ToolCall, 0, len(resp.Message.ToolCalls))
		var input *InputRequest
		var inputID string
		var extraInputs []ToolCall
		invalidInputs := make([]ToolCall, 0)
		invalidInputResults := make(map[string]string)
		for _, call := range resp.Message.ToolCalls {
			if call.Name == "agent_request_input" {
				v := toolStarted(call, nil)
				a.toolEvent(ctx, "agent.tool_call", v)
				a.observe("tool.call", v)
				var q InputRequest
				if reason := validateInputRequest(call.Arguments, &q); reason != "" {
					result := toolResult{content: invalidInputResult(reason), errType: "invalid_input_request"}
					v := toolEventResult(call, nil, result)
					a.toolEvent(ctx, "agent.tool_result", v)
					a.observe("tool.result", v)
					invalidInputs = append(invalidInputs, call)
					invalidInputResults[call.ID] = result.content
					continue
				}
				if input == nil {
					input = &q
					inputID = call.ID
				} else {
					extraInputs = append(extraInputs, call)
				}
			} else {
				ordinary = append(ordinary, call)
			}
		}
		results := runBatch(ctx, a.Tools, ordinary, func(call ToolCall, t Tool, result toolResult, started bool) {
			if started {
				v := toolStarted(call, t)
				a.toolEvent(ctx, "agent.tool_call", v)
				a.observe("tool.call", v)
				return
			}
			v := toolEventResult(call, t, result)
			a.toolEvent(ctx, "agent.tool_result", v)
			a.observe("tool.result", v)
		})
		for j, call := range ordinary {
			state.Messages = append(state.Messages, Message{Role: "tool", ToolCallID: call.ID, Content: results[j].content})
		}
		for _, call := range invalidInputs {
			state.Messages = append(state.Messages, Message{Role: "tool", ToolCallID: call.ID, Content: invalidInputResults[call.ID]})
		}
		if input != nil {
			state.Messages = append(state.Messages, Message{Role: "tool", ToolCallID: inputID, Content: "input request accepted"})
			v := toolEventResult(ToolCall{ID: inputID, Name: "agent_request_input"}, nil, toolResult{content: "input request accepted"})
			a.toolEvent(ctx, "agent.tool_result", v)
			a.observe("tool.result", v)
			for _, call := range extraInputs {
				state.Messages = append(state.Messages, Message{Role: "tool", ToolCallID: call.ID, Content: "input request deferred"})
				v := toolEventResult(call, nil, toolResult{content: "input request deferred"})
				a.toolEvent(ctx, "agent.tool_result", v)
				a.observe("tool.result", v)
			}
			state.Status = StatusAwaitingInput
			state.Request = input
			return state, nil
		}
	}
	state.Status = StatusError
	a.observe("agent.final", map[string]any{"status": "error", "error": "max iterations exceeded"})
	return state, fmt.Errorf("max iterations exceeded")
}

func (a *Agent) observe(typ string, value any) {
	if a.Observer != nil {
		_ = a.Observer(typ, value)
	}
}

func (a *Agent) toolEvent(ctx context.Context, typ string, value any) {
	if a.Events != nil {
		_ = a.Events.Emit(ctx, Event{Type: typ, Data: value})
	}
}

func (a *Agent) Run(ctx context.Context, req ModelRequest) (Message, error) {
	s, e := a.RunState(ctx, req)
	if len(s.Messages) > 0 {
		return s.Messages[len(s.Messages)-1], e
	}
	return Message{}, e
}

func runBatch(ctx context.Context, r *ToolRegistry, calls []ToolCall, observe func(ToolCall, Tool, toolResult, bool)) []toolResult {
	out := make([]toolResult, len(calls))
	sem := make(chan struct{}, MaxToolConcurrency)
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func(i int, c ToolCall) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				out[i] = toolResult{content: `{"error":"cancelled"}`, errType: "cancelled"}
				observe(c, nil, out[i], true)
				observe(c, nil, out[i], false)
				return
			}
			defer func() { <-sem }()
			start := time.Now()
			t, ok := r.Get(c.Name)
			if !ok {
				out[i] = toolResult{content: `{"error":"tool_not_found"}`, errType: "tool_not_found", duration: toolDuration(start)}
				observe(c, nil, out[i], true)
				observe(c, nil, out[i], false)
				return
			}
			observe(c, t, toolResult{}, true)
			v, e := t.Execute(ctx, c.Arguments)
			if e != nil {
				out[i] = toolResult{content: toolFailureResult(e, v), errType: "tool_failed", duration: toolDuration(start)}
			} else {
				out[i] = toolResult{content: v, errType: classifyToolResult(v), duration: toolDuration(start)}
			}
			observe(c, t, out[i], false)
		}(i, c)
	}
	wg.Wait()
	return out
}

func validateInputRequest(raw json.RawMessage, q *InputRequest) string {
	if json.Unmarshal(raw, q) != nil {
		return "arguments must be valid JSON"
	}
	if q.Kind != "question" && q.Kind != "choice" && q.Kind != "approval" {
		return "kind must be question, choice, or approval"
	}
	if strings.TrimSpace(q.Prompt) == "" {
		return "prompt is required"
	}
	if q.Kind == "choice" && len(q.Options) == 0 {
		return "choice requests require options"
	}
	return ""
}

func invalidInputResult(reason string) string {
	b, _ := json.Marshal(map[string]any{"error": "invalid_input_request", "message": reason, "retryable": true})
	return string(b)
}

func toolFailureResult(err error, output string) string {
	message := err.Error()
	payload := map[string]any{"error": "tool_failed", "message": boundedToolOutput(message)}
	if len(message) > maxToolErrorOutputBytes {
		payload["message_truncated"] = true
	}
	if output != "" {
		payload["output"] = boundedToolOutput(output)
		if len(output) > maxToolErrorOutputBytes {
			payload["output_truncated"] = true
		}
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

func boundedToolOutput(output string) string {
	limit := maxToolErrorOutputBytes
	if max := int(api.MaxOutputBytes); limit > max {
		limit = max
	}
	if len(output) <= limit {
		return output
	}
	output = output[:limit]
	for len(output) > 0 && !utf8.ValidString(output) {
		output = output[:len(output)-1]
	}
	return output
}
