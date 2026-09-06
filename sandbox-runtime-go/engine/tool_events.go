package engine

import (
	"encoding/json"
	"strings"
	"time"
)

type ToolMetadata interface {
	ToolID() string
	Operation() string
}

type toolResult struct {
	content  string
	errType  string
	duration int64
}

func toolDisplay(t Tool, modelName string) (string, string) {
	if m, ok := t.(ToolMetadata); ok {
		return m.ToolID(), m.Operation()
	}
	return "", modelName
}

func toolEventCall(call ToolCall, t Tool) map[string]any {
	id, operation := toolDisplay(t, call.Name)
	return map[string]any{"id": call.ID, "name": call.Name, "tool_call_id": call.ID, "tool_name": operation, "tool_id": id, "operation": operation, "model_tool_name": call.Name, "arguments": call.Arguments}
}

func toolEventResult(call ToolCall, t Tool, result toolResult) map[string]any {
	v := toolEventCall(call, t)
	v["status"] = map[bool]string{true: "succeeded", false: "failed"}[result.errType == ""]
	v["content"] = result.content
	v["raw_result"] = result.content
	v["result"] = decodedResult(result.content)
	v["duration_ms"] = result.duration
	if result.errType != "" {
		v["error_type"] = result.errType
	}
	return v
}

func decodedResult(raw string) any {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	nodes := 0
	var visit func(any, int) any
	visit = func(x any, depth int) any {
		nodes++
		if nodes > 256 || depth > 4 {
			return x
		}
		switch y := x.(type) {
		case string:
			if depth >= 4 {
				return y
			}
			trimmed := strings.TrimSpace(y)
			if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
				return y
			}
			var nested any
			if json.Unmarshal([]byte(trimmed), &nested) != nil {
				return y
			}
			if _, ok := nested.(map[string]any); !ok {
				if _, ok := nested.([]any); !ok {
					return y
				}
			}
			return visit(nested, depth+1)
		case []any:
			for i := range y {
				y[i] = visit(y[i], depth+1)
			}
		case map[string]any:
			for k := range y {
				y[k] = visit(y[k], depth+1)
			}
		}
		return x
	}
	return visit(v, 0)
}

func classifyToolResult(raw string) string {
	v, ok := decodedResult(raw).(map[string]any)
	if !ok {
		return ""
	}
	if isError, ok := v["isError"].(bool); ok && isError {
		return errorType(v)
	}
	if ok, exists := v["ok"].(bool); exists && !ok {
		return errorType(v)
	}
	if errorValue(v["error"]) {
		return errorType(v)
	}
	if nested, ok := v["result"].(map[string]any); ok {
		if isError, ok := nested["isError"].(bool); ok && isError {
			return errorType(nested)
		}
	}
	return ""
}

func errorType(v map[string]any) string {
	if s, ok := v["error_type"].(string); ok && s != "" {
		return s
	}
	return "tool_error"
}

func errorValue(v any) bool {
	if v == nil {
		return false
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	if m, ok := v.(map[string]any); ok {
		return len(m) > 0
	}
	if a, ok := v.([]any); ok {
		return len(a) > 0
	}
	return true
}

func toolStarted(call ToolCall, t Tool) map[string]any {
	v := toolEventCall(call, t)
	v["status"] = "started"
	return v
}
func toolDuration(start time.Time) int64 { return time.Since(start).Milliseconds() }
