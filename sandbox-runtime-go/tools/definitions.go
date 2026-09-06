package tools

import (
	"agent-platform/sandbox-runtime-go/api"
	"encoding/json"
)

func (t builtinTool) Definition() api.ToolDefinition {
	var params json.RawMessage
	switch t.op {
	case "agent_list_files":
		params = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`)
	case "agent_read_file":
		params = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","minimum":0},"max_bytes":{"type":"integer","minimum":1,"maximum":16384}},"required":["path"],"additionalProperties":false}`)
	case "agent_search_file":
		params = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"query":{"type":"string","minLength":1,"maxLength":1024},"offset":{"type":"integer","minimum":0}},"required":["path","query"],"additionalProperties":false}`)
	case "agent_load_skill":
		params = json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","minLength":1}},"required":["name"],"additionalProperties":false}`)
	case "agent_write_file":
		params = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`)
	case "agent_run_script":
		params = json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","maxLength":16384},"timeout_seconds":{"type":"integer","minimum":1,"maximum":60}},"required":["command"],"additionalProperties":false}`)
	}
	return api.ToolDefinition{Name: t.name, Description: t.description, Parameters: params}
}
