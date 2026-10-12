package codexsession

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
)

// HostTools is a private, trusted Go binding, not a permission from the model.
// OnTool must be context-cooperative and authorize every resource independently.
// It runs on the reducer goroutine; Run never detaches a tool callback.
// For resume, Host must authenticate the persisted original tool contract before
// supplying ResumeContractSHA256. Native thread/resume cannot replace tools.
type HostTools struct {
	Writable             bool
	ResumeContractSHA256 string
	OnTool               func(context.Context, ToolCall) (ToolResult, error)
}
type ToolCall struct {
	ThreadID, TurnID, CallID, Tool string
	Arguments                      json.RawMessage
}
type ToolResult struct {
	Text    string
	Success bool
}
type hostToolCall struct {
	claim                [32]byte
	requested, responded bool
	result               [32]byte
}

const maxHostToolArguments = 64 << 10
const maxHostToolResult = 256 << 10

//go:embed host_tool_shapes.json
var hostToolShapes []byte

func (s *nativeShapes) addHostToolShapes() error {
	v, err := strictValue(hostToolShapes)
	if err != nil {
		return err
	}
	supplement := obj(v)
	for name, def := range obj(supplement["definitions"]) {
		if s.definitions[name] != nil {
			return ErrProtocol
		}
		s.definitions[name] = def
	}
	s.schemas["request:item/tool/call"] = supplement["request"]
	s.schemas["response:item/tool/call"] = supplement["response"]
	obj(obj(s.definitions["ClientRequest.json::ThreadStartParams"])["properties"])["dynamicTools"] = map[string]any{"type": "array", "items": supplement["spec"]}
	for _, prefix := range []string{"ServerNotification.json", "v2.ThreadResumeResponse.json", "v2.ThreadStartResponse.json", "v2.TurnStartResponse.json"} {
		def := obj(s.definitions[prefix+"::ThreadItem"])
		if def == nil {
			return ErrProtocol
		}
		def["oneOf"] = append(arr(def["oneOf"]), supplement["item"])
	}
	return nil
}

func hostToolSpecs(writable bool) []any {
	path := map[string]any{"type": "string"}
	makeTool := func(name, description string, properties map[string]any, required []string) any {
		fields := make([]any, len(required))
		for i, name := range required {
			fields[i] = name
		}
		return map[string]any{"type": "function", "name": name, "description": description, "deferLoading": false, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": fields, "additionalProperties": false}}
	}
	tools := []any{
		makeTool("read_file", "Read UTF-8 and full-file SHA-256. Prefer byte offset/limit_bytes (default 4096, max 16384); follow next_offset with expectedSHA256 to reject changed files. Without a range, max 32KiB.", map[string]any{"path": path, "offset": map[string]any{"type": "integer", "minimum": json.Number("0")}, "limit_bytes": map[string]any{"type": "integer", "minimum": json.Number("4")}, "expectedSHA256": map[string]any{"type": "string"}}, []string{"path"}),
		makeTool("list_files", "List one bounded directory in the authorized workspace.", map[string]any{"path": path}, []string{"path"}),
		makeTool("search_file", "Find a literal UTF-8 query (max 256 bytes) in one file. Return up to 20 non-overlapping byte offsets and 1-based lines plus full-file SHA-256; use read_file for context.", map[string]any{"path": path, "query": map[string]any{"type": "string"}}, []string{"path", "query"}),
	}
	if writable {
		tools = append(tools, makeTool("write_file", "Replace a bounded UTF-8 file only when expectedSHA256 matches; empty hash creates a new file.", map[string]any{"path": path, "expectedSHA256": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, []string{"path", "expectedSHA256", "content"}))
		tools = append(tools, makeTool("replace_text", "Replace exactly one occurrence of old_text with new_text in a declared UTF-8 file (max 32KiB). Requires current full-file expectedSHA256; rejects absent or ambiguous matches.", map[string]any{"path": path, "expectedSHA256": map[string]any{"type": "string"}, "old_text": map[string]any{"type": "string"}, "new_text": map[string]any{"type": "string"}}, []string{"path", "expectedSHA256", "old_text", "new_text"}))
	}
	return []any{map[string]any{"type": "namespace", "name": "agent_bird", "description": "Agent Bird constrained workspace tools v1", "tools": tools}}
}

// Hash covers exact canonical encoding/json tool inventory bytes (no newline).
// Domain separation prevents confusing a native tool schema with authorization.
func HostToolContractSHA256(writable bool) string {
	raw, _ := json.Marshal(hostToolSpecs(writable))
	h := sha256.New()
	_, _ = h.Write([]byte("agent-bird/codex-host-tools/v1\n"))
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

func (s *sessionState) hostToolClaim(value map[string]any, id string) (*hostToolCall, error) {
	if s.options.HostTools == nil || value["namespace"] != "agent_bird" || !validScalar(id) {
		return nil, ErrPolicy
	}
	tool := str(value["tool"])
	if tool != "read_file" && tool != "list_files" && tool != "search_file" && ((tool != "write_file" && tool != "replace_text") || !s.options.HostTools.Writable) {
		return nil, ErrPolicy
	}
	args, err := json.Marshal(value["arguments"])
	if err != nil || len(args) > maxHostToolArguments {
		return nil, ErrStateLimit
	}
	// Validate arguments against our fixed tool schema, independently of the
	// permissive native JSON value field. Unknown/duplicate keys fail closed.
	var schema any
	for _, entry := range arr(obj(hostToolSpecs(s.options.HostTools.Writable)[0])["tools"]) {
		t := obj(entry)
		if t["name"] == tool {
			schema = t["inputSchema"]
		}
	}
	if !s.shapes.match([]any{schema}, value["arguments"], 0, &shapeBudget{}) {
		return nil, ErrProtocol
	}
	claim := digest(map[string]any{"namespace": value["namespace"], "tool": tool, "arguments": value["arguments"]})
	if old := s.toolCalls[id]; old != nil {
		if old.claim != claim {
			return nil, ErrProtocol
		}
		return old, nil
	}
	if len(s.toolCalls) >= maxItems {
		return nil, ErrStateLimit
	}
	call := &hostToolCall{claim: claim}
	s.toolCalls[id] = call
	return call, nil
}

func (s *sessionState) hostToolRequest(request *codexrpc.Request, value map[string]any) error {
	call, err := s.hostToolClaim(value, str(value["callId"]))
	if err != nil {
		return err
	}
	if call.requested {
		return ErrProtocol
	}
	id, err := request.ID().MarshalJSON()
	if err != nil {
		return ErrProtocol
	}
	if s.requests[string(id)] {
		return ErrProtocol
	}
	if len(s.requests) >= codexrpc.MaxConsumedRequests {
		return ErrStateLimit
	}
	s.requests[string(id)] = true
	call.requested = true
	if s.out.TurnID == "" {
		if len(s.pendingTools) >= codexrpc.MaxRemoteRequests {
			return ErrStateLimit
		}
		s.pendingTools = append(s.pendingTools, request)
		return nil
	}
	return s.executeHostTool(request, value, call)
}

func (s *sessionState) flushHostTools() error {
	pending := s.pendingTools
	s.pendingTools = nil
	for _, request := range pending {
		value, err := s.shapes.object("request:item/tool/call", request.Params())
		if err != nil {
			return err
		}
		if err := s.executeHostTool(request, value, s.toolCalls[str(value["callId"])]); err != nil {
			return err
		}
	}
	return nil
}

func (s *sessionState) executeHostTool(request *codexrpc.Request, value map[string]any, call *hostToolCall) error {
	if s.terminal != nil || s.phase != "running" || s.out.TurnID == "" || value["turnId"] != s.out.TurnID || value["threadId"] != s.out.Binding.ThreadID || call == nil || call.responded {
		return ErrProtocol
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	args, _ := json.Marshal(value["arguments"])
	result, err := s.options.HostTools.OnTool(s.ctx, ToolCall{ThreadID: s.out.Binding.ThreadID, TurnID: s.out.TurnID, CallID: str(value["callId"]), Tool: str(value["tool"]), Arguments: args})
	if err != nil {
		return ErrCallback
	}
	if !utf8.ValidString(result.Text) || len(result.Text) > maxHostToolResult {
		return ErrStateLimit
	}
	reply := map[string]any{"contentItems": []any{map[string]any{"type": "inputText", "text": result.Text}}, "success": result.Success}
	raw, _ := json.Marshal(reply)
	if _, err := s.shapes.object("response:item/tool/call", raw); err != nil {
		return err
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if err := s.client.Respond(s.ctx, request, codexrpc.Reply{Result: raw}); err != nil {
		return ErrProtocol
	}
	call.responded = true
	call.result = digest(reply)
	return nil
}

func (s *sessionState) hostToolItem(value map[string]any) (bool, error) {
	call, err := s.hostToolClaim(value, str(value["id"]))
	if err != nil {
		return false, err
	}
	if value["status"] == "inProgress" {
		if value["contentItems"] != nil || value["success"] != nil || value["durationMs"] != nil {
			return false, ErrProtocol
		}
		return false, nil
	}
	if !call.responded || digest(map[string]any{"contentItems": value["contentItems"], "success": value["success"]}) != call.result {
		return false, ErrProtocol
	}
	if value["success"] == true && value["status"] != "completed" || value["success"] == false && value["status"] != "failed" {
		return false, ErrProtocol
	}
	return true, nil
}
