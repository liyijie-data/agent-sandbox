package contracts

import (
	"encoding/json"
	"strings"
)

type EventType string

const (
	EventRunQueued      EventType = "run.queued"
	EventRunStarted     EventType = "run.started"
	EventTextDelta      EventType = "text.delta"
	EventTextDone       EventType = "text.done"
	EventInputRequested EventType = "agent.request_input"
	EventInputAnswered  EventType = "agent.input_answered"
	EventRunTerminal    EventType = "run.terminal"

	EventRunQueueProgress EventType = "run.queue_progress"

	EventPlatformNetworkPolicyChanged EventType = "platform.network_policy_changed"

	EventSteerAccepted     EventType = "steer.accepted"
	EventSteerIncorporated EventType = "steer.incorporated"
	EventSteerNotApplied   EventType = "steer.not_applied"
	EventSteerUnknown      EventType = "steer.unknown"
)

type Event struct {
	ID        string    `json:"id"`
	Type      EventType `json:"type"`
	RunID     string    `json:"run_id"`
	Stage     int       `json:"stage"`
	Fence     int64     `json:"fence"`
	SourceSeq int64     `json:"source_seq,omitempty"`

	Status RunStatus         `json:"status,omitempty"`
	Delta  string            `json:"delta,omitempty"`
	Input  *PendingInput     `json:"input,omitempty"`
	Result *ResultSummary    `json:"result,omitempty"`
	Tool   *AgentToolPayload `json:"tool,omitempty"`

	Seq int64 `json:"seq,omitempty"`
}

func (e Event) KeyEvent() bool {
	switch e.Type {
	case EventRunQueued, EventRunStarted, EventInputRequested,
		EventInputAnswered, EventRunTerminal, EventTextDone,
		EventPlatformNetworkPolicyChanged,
		EventSteerAccepted, EventSteerIncorporated, EventSteerNotApplied, EventSteerUnknown:
		return true
	}
	return false
}

const (
	EventAgentToken      EventType = "agent.token"
	EventAgentReasoning  EventType = "agent.reasoning"
	EventAgentToolCall   EventType = "agent.tool_call"
	EventAgentToolResult EventType = "agent.tool_result"
	EventRuntimeProgress EventType = "runtime.progress"
)

const (
	RuntimeEventMaxBodyBytes      = 128 * 1024
	RuntimeEventDeltaMaxBytes     = 64 * 1024
	RuntimeProgressPhaseMaxLength = 64
	RuntimeToolCallIDMaxLength    = 128
	RuntimeToolNameMaxLength      = 128
	RuntimeModelToolNameMaxLength = 512
	RuntimeToolStatusMaxLength    = 32
	RuntimeToolErrorTypeMaxLength = 128
	RuntimeEventsMaxPerRun        = 20000
)

type AgentTokenPayload struct {
	Delta string `json:"delta"`
}

type AgentReasoningPayload struct {
	Delta string `json:"delta"`
}

type RuntimeProgressPayload struct {
	Phase string  `json:"phase"`
	Value float64 `json:"value"`
}

type AgentToolPayload struct {
	ToolCallID       string          `json:"tool_call_id"`
	ToolName         string          `json:"tool_name"`
	Status           string          `json:"status"`
	ErrorType        string          `json:"error_type,omitempty"`
	ToolID           string          `json:"tool_id,omitempty"`
	Operation        string          `json:"operation,omitempty"`
	ModelToolName    string          `json:"model_tool_name,omitempty"`
	Arguments        json.RawMessage `json:"arguments,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	DurationMs       *int64          `json:"duration_ms,omitempty"`
	DetailsTruncated bool            `json:"details_truncated,omitempty"`
}

type RuntimeEventEntry struct {
	ExecutionID string          `json:"execution_id"`
	SourceSeq   int64           `json:"source_seq"`
	Type        EventType       `json:"type"`
	Payload     json.RawMessage `json:"payload"`
}

type RuntimeEventEntryResponse struct {
	Accepted bool  `json:"accepted"`
	Seq      int64 `json:"seq,omitempty"`
	Deduped  bool  `json:"deduped,omitempty"`
}

func DecodeRuntimeEventEntry(data []byte) (*RuntimeEventEntry, *validationError) {
	if len(data) > RuntimeEventMaxBodyBytes {
		return nil, validationErrorf(ErrPayloadTooLarge, "runtime event entry exceeds %d bytes", RuntimeEventMaxBodyBytes)
	}
	var req RuntimeEventEntry
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return nil, validationErrorf(ErrInvalidRequest, "runtime event entry decode failed: %v", err)
	}
	if req.ExecutionID == "" {
		return nil, validationErrorf(ErrInvalidRequest, "runtime event entry requires execution_id")
	}
	if req.SourceSeq < 1 {
		return nil, validationErrorf(ErrInvalidRequest, "runtime event entry source_seq must be >= 1")
	}
	switch req.Type {
	case EventAgentToken:
		var p AgentTokenPayload
		if err := decodeStrict(req.Payload, &p); err != nil {
			return nil, validationErrorf(ErrInvalidRequest, "agent.token payload must be {\"delta\": string}")
		}
		if p.Delta == "" {
			return nil, validationErrorf(ErrInvalidRequest, "agent.token delta must be non-empty")
		}
		if len([]byte(p.Delta)) > RuntimeEventDeltaMaxBytes {
			return nil, validationErrorf(ErrPayloadTooLarge, "agent.token delta exceeds %d bytes", RuntimeEventDeltaMaxBytes)
		}
	case EventAgentReasoning:
		var p AgentReasoningPayload
		if err := decodeStrict(req.Payload, &p); err != nil {
			return nil, validationErrorf(ErrInvalidRequest, "agent.reasoning payload must be {\"delta\": string}")
		}
		if p.Delta == "" {
			return nil, validationErrorf(ErrInvalidRequest, "agent.reasoning delta must be non-empty")
		}
		if len([]byte(p.Delta)) > RuntimeEventDeltaMaxBytes {
			return nil, validationErrorf(ErrPayloadTooLarge, "agent.reasoning delta exceeds %d bytes", RuntimeEventDeltaMaxBytes)
		}
	case EventRuntimeProgress:
		var p RuntimeProgressPayload
		if err := decodeStrict(req.Payload, &p); err != nil {
			return nil, validationErrorf(ErrInvalidRequest, "runtime.progress payload must be {\"phase\": string, \"value\": number}")
		}
		if p.Phase == "" || len(p.Phase) > RuntimeProgressPhaseMaxLength {
			return nil, validationErrorf(ErrInvalidRequest, "runtime.progress phase must be 1-%d chars", RuntimeProgressPhaseMaxLength)
		}
		if p.Value < 0 || p.Value > 100 || p.Value != float64(int64(p.Value)) {
			return nil, validationErrorf(ErrInvalidRequest, "runtime.progress value must be an integer in 0..100")
		}
	case EventAgentToolCall, EventAgentToolResult:
		var p AgentToolPayload
		if err := decodeStrict(req.Payload, &p); err != nil || p.ToolCallID == "" || p.ToolName == "" ||
			len(p.ToolCallID) > RuntimeToolCallIDMaxLength || len(p.ToolName) > RuntimeToolNameMaxLength {
			return nil, validationErrorf(ErrInvalidRequest, "agent tool payload is invalid")
		}
		if req.Type == EventAgentToolCall && p.Status != "started" {
			return nil, validationErrorf(ErrInvalidRequest, "agent.tool_call status must be started")
		}
		if req.Type == EventAgentToolResult && p.Status != "succeeded" && p.Status != "failed" {
			return nil, validationErrorf(ErrInvalidRequest, "agent.tool_result status must be succeeded or failed")
		}
		if len(p.Status) > RuntimeToolStatusMaxLength || len(p.ErrorType) > RuntimeToolErrorTypeMaxLength || len(p.ToolID) > RuntimeToolNameMaxLength || len(p.Operation) > RuntimeToolNameMaxLength || len(p.ModelToolName) > RuntimeModelToolNameMaxLength {
			return nil, validationErrorf(ErrInvalidRequest, "agent tool payload field is too long")
		}
		if len(p.Arguments) > 16<<10 || len(p.Result) > 16<<10 {
			return nil, validationErrorf(ErrPayloadTooLarge, "agent tool details exceed 16KiB")
		}
		if p.DurationMs != nil && *p.DurationMs < 0 {
			return nil, validationErrorf(ErrInvalidRequest, "agent tool duration_ms must be non-negative")
		}
	default:
		return nil, validationErrorf(ErrInvalidRequest, "runtime event type %q is not whitelisted", req.Type)
	}
	return &req, nil
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
