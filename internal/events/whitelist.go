package events

import (
	"fmt"

	"agent-platform/internal/contracts"
	"agent-platform/internal/lifecycle"
)

type eventSpec struct {
	key      bool
	delta    bool
	input    bool
	result   bool
	progress bool
	tool     bool
}

var whitelist = map[contracts.EventType]eventSpec{
	contracts.EventAgentToken:        {delta: true},
	contracts.EventAgentReasoning:    {delta: true},
	contracts.EventRuntimeProgress:   {progress: true},
	contracts.EventAgentToolCall:     {tool: true},
	contracts.EventAgentToolResult:   {tool: true},
	contracts.EventTextDelta:         {delta: true},
	contracts.EventTextDone:          {key: true},
	contracts.EventRunQueued:         {key: true},
	contracts.EventRunStarted:        {key: true},
	contracts.EventInputRequested:    {key: true, input: true},
	contracts.EventInputAnswered:     {key: true},
	contracts.EventRunTerminal:       {key: true, result: true},
	contracts.EventSteerAccepted:     {key: true},
	contracts.EventSteerIncorporated: {key: true},
	contracts.EventSteerNotApplied:   {key: true},
	contracts.EventSteerUnknown:      {key: true},
}

func AllowedType(t contracts.EventType) bool {
	_, ok := whitelist[t]
	return ok
}

func (s *Service) ValidateEvent(ev contracts.Event, progress *Progress) error {
	spec, ok := whitelist[ev.Type]
	if !ok {
		return fmt.Errorf("events: %w: %q", ErrNotWhitelisted, ev.Type)
	}
	if !spec.progress && progress != nil {
		return fmt.Errorf("events: %w: %s does not carry a progress payload", ErrInvalidEvent, ev.Type)
	}
	switch {
	case spec.delta:
		if ev.Delta == "" {
			return fmt.Errorf("events: %w: %s requires a non-empty delta", ErrInvalidEvent, ev.Type)
		}
		if len(ev.Delta) > MaxDeltaBytes {
			return fmt.Errorf("events: %w: %s delta %d bytes exceeds %d", ErrTooLarge, ev.Type, len(ev.Delta), MaxDeltaBytes)
		}
	case spec.progress:
		if progress == nil {
			return fmt.Errorf("events: %w: %s requires a progress payload", ErrInvalidEvent, ev.Type)
		}
		if !lifecycle.ValidPhase(progress.Phase) {
			return fmt.Errorf("events: %w: %s phase %q is not a bounded phase", ErrInvalidEvent, ev.Type, progress.Phase)
		}
		if progress.Value < 0 || progress.Value > 100 {
			return fmt.Errorf("events: %w: %s value %d out of 0..100", ErrInvalidEvent, ev.Type, progress.Value)
		}
	case spec.input:
		if ev.Input == nil || ev.Input.ID == "" {
			return fmt.Errorf("events: %w: %s requires a real input_id", ErrInvalidEvent, ev.Type)
		}
	case spec.result:
		if ev.Status == "" || !ev.Status.IsTerminal() {
			return fmt.Errorf("events: %w: %s requires a terminal status", ErrInvalidEvent, ev.Type)
		}
	case spec.tool:
		if ev.Tool == nil || ev.Tool.ToolCallID == "" || ev.Tool.ToolName == "" {
			return fmt.Errorf("events: %w: %s requires a tool summary", ErrInvalidEvent, ev.Type)
		}
		if len(ev.Tool.ToolCallID) > contracts.RuntimeToolCallIDMaxLength || len(ev.Tool.ToolName) > contracts.RuntimeToolNameMaxLength || len(ev.Tool.Status) > contracts.RuntimeToolStatusMaxLength || len(ev.Tool.ErrorType) > contracts.RuntimeToolErrorTypeMaxLength {
			return fmt.Errorf("events: %w: %s tool summary field is too long", ErrTooLarge, ev.Type)
		}
	}
	return nil
}
