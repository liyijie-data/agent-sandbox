package api

import (
	"context"
	"encoding/json"
	"time"
)

const (
	MaxIterations            = 40
	MaxToolConcurrency       = 8
	MaxBodyBytes       int64 = 4 * 1024 * 1024
	MaxOutputBytes     int64 = 2 * 1024 * 1024
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type ModelRequest struct {
	Messages        []Message
	Model           string
	ReasoningEffort string
}
type ModelResponse struct {
	Message      Message
	FinishReason string
	Raw          json.RawMessage
}
type Model interface {
	Complete(context.Context, ModelRequest) (ModelResponse, error)
}
type Tool interface {
	Name() string
	Description() string
	Execute(context.Context, json.RawMessage) (string, error)
}
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}
type ModelDelta struct {
	Kind string
	Text string
}
type Event struct {
	Type  string    `json:"type"`
	Stage int       `json:"stage,omitempty"`
	Data  any       `json:"data,omitempty"`
	At    time.Time `json:"at"`
}
type EventSink interface {
	Emit(context.Context, Event) error
}
