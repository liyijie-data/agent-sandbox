package contracts

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	RoleSystem    = "system"
	RoleDeveloper = "developer"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

var validRoles = map[string]bool{
	RoleSystem: true, RoleDeveloper: true, RoleUser: true,
	RoleAssistant: true, RoleTool: true,
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Message struct {
	Role       string        `json:"role"`
	Content    *string       `json:"content,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID *string       `json:"tool_call_id,omitempty"`
	Files      []ResourceRef `json:"files,omitempty"`
	Skills     []ResourceRef `json:"skills,omitempty"`
}

func ValidateMessages(messages []Message) *validationError {
	if len(messages) == 0 {
		return validationErrorf(ErrInvalidMessages, "messages must be non-empty")
	}

	declared := map[string]bool{}
	answered := map[string]bool{}
	nonEmpty := false

	for i, m := range messages {
		if !validRoles[m.Role] {
			return validationErrorf(ErrInvalidMessages, "message[%d]: unknown role %q", i, m.Role)
		}

		switch m.Role {
		case RoleAssistant:
			if len(m.ToolCalls) > 0 && m.Content != nil && *m.Content == "" {

			}
			seen := map[string]bool{}
			for _, tc := range m.ToolCalls {
				if tc.ID == "" {
					return validationErrorf(ErrInvalidMessages, "message[%d]: assistant tool_call has empty id", i)
				}
				if tc.Type != "function" {
					return validationErrorf(ErrInvalidMessages, "message[%d]: tool_call type must be %q", i, "function")
				}
				if tc.Function.Name == "" {
					return validationErrorf(ErrInvalidMessages, "message[%d]: tool_call function name is empty", i)
				}
				if seen[tc.ID] || declared[tc.ID] {
					return validationErrorf(ErrInvalidMessages, "message[%d]: duplicate tool_call_id %q", i, tc.ID)
				}
				seen[tc.ID] = true
				declared[tc.ID] = true
			}
		case RoleTool:
			if m.ToolCallID == nil || *m.ToolCallID == "" {
				return validationErrorf(ErrInvalidMessages, "message[%d]: tool message must reference tool_call_id", i)
			}
			if !declared[*m.ToolCallID] {
				return validationErrorf(ErrInvalidMessages, "message[%d]: dangling tool_call_id %q has no declaration", i, *m.ToolCallID)
			}
			if answered[*m.ToolCallID] {
				return validationErrorf(ErrInvalidMessages, "message[%d]: tool_call_id %q answered more than once", i, *m.ToolCallID)
			}
			answered[*m.ToolCallID] = true
		}
		if m.Content != nil && *m.Content != "" {
			nonEmpty = true
		}
	}

	for id, d := range declared {
		if d && !answered[id] {
			return validationErrorf(ErrInvalidMessages, "unclosed tool_call_id %q has no tool response", id)
		}
	}

	if !nonEmpty {
		return validationErrorf(ErrInvalidMessages, "messages must contain at least one non-empty content")
	}
	return nil
}

func MessageFromJSON(data []byte) (*Message, *validationError) {
	var m Message
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, validationErrorf(ErrInvalidMessages, "message decode failed: %v", err)
	}
	return &m, nil
}

func (m *Message) String() string {
	if m == nil {
		return "<nil>"
	}
	if m.ToolCallID != nil {
		return fmt.Sprintf("role=%s tool_call_id=%s", m.Role, *m.ToolCallID)
	}
	if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
		return fmt.Sprintf("role=%s tool_calls=%d", m.Role, len(m.ToolCalls))
	}
	return "role=" + m.Role
}
