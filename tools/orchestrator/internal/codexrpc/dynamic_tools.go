package codexrpc

import (
	"encoding/json"
	"strings"
	"unicode"
)

type DynamicTool struct {
	Name, Description string
	InputSchema       map[string]any
	Call              func(map[string]any) (string, error)
}

func (t *Turn) dynamicDefinitions() ([]any, error) {
	definitions := []any{}
	seen := map[string]bool{}
	for _, tool := range t.config.DynamicTools {
		if !readToolName(tool.Name) || seen[tool.Name] || tool.Call == nil || tool.InputSchema["type"] != "object" || tool.InputSchema["additionalProperties"] != false {
			return nil, CodeError("codex_dynamic_tool_definition_invalid")
		}
		seen[tool.Name] = true
		definitions = append(definitions, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema, "deferLoading": false})
	}
	return definitions, nil
}

func readToolName(name string) bool {
	return name == "bird_read_file" || name == "bird_list_files" || name == "bird_search_text"
}

func (t *Turn) dynamicCall(message map[string]any) error {
	invalid := CodeError("codex_dynamic_tool_request_rejected")
	if t.stage != 5 || t.turn == "" || t.thread == "" || t.complete {
		return invalid
	}
	params, ok := message["params"].(map[string]any)
	if !ok || len(params) != 6 || params["threadId"] != t.thread || params["turnId"] != t.turn {
		return invalid
	}
	if namespace, present := params["namespace"]; !present || namespace != nil {
		return invalid
	}
	call, ok := params["callId"].(string)
	if !ok || call == "" || len(call) > 256 || strings.IndexFunc(call, unicode.IsControl) >= 0 {
		return invalid
	}
	name, ok := params["tool"].(string)
	if !ok || !readToolName(name) {
		return invalid
	}
	args, ok := params["arguments"].(map[string]any)
	if !ok {
		return invalid
	}
	var key string
	switch id := message["id"].(type) {
	case string:
		if id == "" || len(id) > 128 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
			return invalid
		}
		raw, _ := json.Marshal(id)
		key = string(raw)
	case float64:
		if id < 0 || id > 9007199254740991 || id != float64(int64(id)) {
			return invalid
		}
		raw, _ := json.Marshal(id)
		key = string(raw)
	default:
		return invalid
	}
	if t.toolCalls[call] || t.toolRequests[key] || len(t.toolCalls) >= 128 {
		return invalid
	}
	var handler func(map[string]any) (string, error)
	for _, tool := range t.config.DynamicTools {
		if tool.Name == name {
			handler = tool.Call
			break
		}
	}
	if handler == nil {
		return invalid
	}
	if t.toolCalls == nil {
		t.toolCalls = map[string]bool{}
		t.toolRequests = map[string]bool{}
	}
	t.toolCalls[call], t.toolRequests[key] = true, true
	text, err := handler(args)
	if len(text) > 65536 {
		return CodeError("codex_dynamic_tool_output_limit")
	}
	success := err == nil
	if err != nil {
		text = err.Error()
		if text == "" || len(text) > 80 || strings.IndexFunc(text, func(r rune) bool { return r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) >= 0 {
			text = "codex_dynamic_tool_failed"
		}
	}
	return t.send(map[string]any{"id": message["id"], "result": map[string]any{"contentItems": []any{map[string]any{"type": "inputText", "text": text}}, "success": success}})
}
