package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpFrameMaxBytes = 2 * 1024 * 1024

type mcpWriteCloser struct{ io.Writer }

func (mcpWriteCloser) Close() error { return nil }

func mcpSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func mcpString(max int) map[string]any {
	return map[string]any{"type": "string", "minLength": 1, "maxLength": max}
}
func mcpInteger(min, max int64) map[string]any {
	return map[string]any{"type": "integer", "minimum": min, "maximum": max}
}
func mcpArray(item any, min, max int) map[string]any {
	return map[string]any{"type": "array", "items": item, "minItems": min, "maxItems": max}
}
func mcpToolSchema(name string) map[string]any {
	handle, task, cursor := mcpString(mcpTokenMaxBytes), mcpString(128), mcpString(4096)
	switch name {
	case "dispatch":
		budget := mcpSchema(map[string]any{"per_segment_ms": mcpInteger(1, 3600000), "total_ms": mcpInteger(1, 7200000), "max_attempts": mcpInteger(1, 3), "group_id": mcpString(96)}, "per_segment_ms", "total_ms", "max_attempts")
		return mcpSchema(map[string]any{
			"request_id": mcpString(96), "provider": map[string]any{"type": "string", "enum": []string{"codex", "claude", "agy", "grok"}}, "role": map[string]any{"type": "string", "enum": []string{"implementer", "reviewer"}},
			"directory": mcpString(4096), "prompt": mcpString(256 * 1024), "paths": mcpArray(mcpString(512), 0, 256), "acceptance": mcpArray(mcpString(16*1024), 1, 64), "model": mcpString(128), "reasoning": map[string]any{"type": "string", "enum": []string{"", "low", "medium", "high"}}, "grok_session_write": map[string]any{"type": "boolean"}, "budget": budget,
		}, "request_id", "provider", "role", "prompt", "acceptance", "model", "budget")
	case "inspect":
		return mcpSchema(map[string]any{"handle": handle, "task_id": task}, "handle")
	case "wait":
		return mcpSchema(map[string]any{"handle": handle, "task_id": task, "cursor": cursor, "timeout_ms": mcpInteger(1, 30000)}, "handle")
	case "collect":
		return mcpSchema(map[string]any{"handle": handle, "task_id": task, "cursor": cursor, "include_diagnostics": map[string]any{"type": "boolean"}}, "handle")
	case "accept":
		return mcpSchema(map[string]any{"handle": handle, "review_token": handle, "command_id": mcpString(128)}, "handle", "review_token", "command_id")
	case "request_changes":
		return mcpSchema(map[string]any{"handle": handle, "review_token": handle, "command_id": mcpString(128), "feedback": mcpString(256 * 1024), "acceptance": mcpString(256 * 1024), "version": map[string]any{"type": "integer", "enum": []int{2, 3}}}, "handle", "review_token", "command_id", "feedback", "acceptance")
	case "ack":
		decision := mcpSchema(map[string]any{"event_id": mcpString(256), "event_revision": mcpInteger(1, 9223372036854775807), "event_hash": mcpString(64), "action_slot": mcpString(256), "decision": map[string]any{"type": "string", "enum": []string{"handled", "waiting_user", "stale", "rejected"}}, "command_id": mcpString(128)}, "event_id", "event_revision", "event_hash", "action_slot", "decision", "command_id")
		return mcpSchema(map[string]any{"handle": handle, "task_id": task, "delivery_id": mcpString(256), "collection_proof_sha256": mcpString(64), "decisions": mcpArray(decision, 1, 64)}, "handle", "delivery_id", "collection_proof_sha256", "decisions")
	case "stop":
		return mcpSchema(map[string]any{"handle": handle, "task_id": task, "work_revision": mcpInteger(1, 2147483647)}, "handle", "work_revision")
	}
	panic("unknown MCP tool schema")
}
func mcpBusinessCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "request_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request_timed_out"
	}
	return errorCode(err)
}
func mcpToolResult(value map[string]any, err error) *mcp.CallToolResult {
	if value == nil {
		value = map[string]any{}
	}
	if _, ok := value["version"]; !ok {
		value["version"] = 1
	}
	if err != nil {
		value["error"] = mcpBusinessCode(err)
		if _, ok := value["status"]; !ok {
			value["status"] = "error"
		}
	}
	raw, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		err = codeError("mcp_result_invalid")
		raw = []byte(`{"version":1,"status":"error","error":"mcp_result_invalid"}`)
	}
	result := &mcp.CallToolResult{IsError: err != nil, StructuredContent: json.RawMessage(raw), Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
	encoded, e := json.Marshal(result)
	if e != nil || len(encoded) > mcpResultMaxBytes {
		bounded := map[string]any{"version": 1, "status": "error", "error": "mcp_result_too_large"}
		if handle, ok := value["handle"].(string); ok && len(handle) <= mcpTokenMaxBytes {
			bounded["handle"] = handle
		}
		raw, _ = json.Marshal(bounded)
		return &mcp.CallToolResult{IsError: true, StructuredContent: json.RawMessage(raw), Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
	}
	return result
}
func (f *mcpFacade) server() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "agent-bird", Version: "1"}, &mcp.ServerOptions{
		SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"},
		Instructions:              "Use durable handles and inspect verified candidates before explicit accept or request_changes. Reuse request_id and command_id after uncertain replies. Collection ACK is separate from acceptance. Cancel wait only cancels waiting; use stop for execution. These are ordinary tools with collect delivery, not Tasks or native chat callbacks. The configured workspace and existing owner/capability checks remain authoritative.",
	})
	handlers := map[string]func(context.Context, json.RawMessage) (map[string]any, error){"dispatch": f.dispatch, "inspect": f.inspect, "wait": f.wait, "collect": f.collect, "request_changes": f.requestChanges, "accept": f.accept, "ack": f.ack, "stop": f.stop}
	descriptions := map[string]string{
		"dispatch":        "Start one fresh worker with an explicit shared budget and stable request_id. Returns a durable handle promptly. Model must be explicit; cli-default preserves the configured CLI model.",
		"inspect":         "Observe task state, budget, evidence and allowed actions. Verified candidates receive an opaque review_token; observations do not grant mutation authority.",
		"wait":            "Wait at most 30 seconds for task events. Cancellation affects this wait only and never acknowledges or stops a worker.",
		"collect":         "Read one bounded event page and its collection proof. Pass next_cursor unchanged for the next page; acknowledge explicitly with ack.",
		"request_changes": "Request a fresh worker invocation from the signed candidate tuple using a stable command_id. Version defaults to immutable references v3; explicit v2 preserves legacy behavior.",
		"accept":          "Explicitly accept the signed candidate using a stable command_id. Acceptance does not acknowledge events or integrate source files.",
		"ack":             "Explicitly record per-event processing decisions for a complete collection page using its proof. This does not accept a candidate.",
		"stop":            "Request stopping at the observed revision. requested may be unknown after transport failure; confirmed requires an observation with no active or unknown execution.",
	}
	calls := make(chan struct{}, 4)
	for _, name := range []string{"dispatch", "inspect", "wait", "collect", "request_changes", "accept", "ack", "stop"} {
		handler := handlers[name]
		readOnly := name == "inspect" || name == "wait" || name == "collect"
		destructive, openWorld := name == "stop", name == "dispatch"
		// Low-level SDK handlers retain original integer bytes. Validation is
		// explicit in mcpArguments and the reused business admission functions.
		server.AddTool(&mcp.Tool{Name: name, Description: descriptions[name], InputSchema: mcpToolSchema(name), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, OpenWorldHint: &openWorld, IdempotentHint: name == "dispatch" || name == "accept" || name == "request_changes" || name == "ack"}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if err := ctx.Err(); err != nil {
				return mcpToolResult(nil, err), nil
			}
			select {
			case calls <- struct{}{}:
				defer func() { <-calls }()
			default:
				return mcpToolResult(nil, codeError("mcp_busy")), nil
			}
			if request == nil || request.Params == nil {
				return mcpToolResult(nil, codeError("invalid_mcp_arguments")), nil
			}
			ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			value, err := handler(ctx, request.Params.Arguments)
			return mcpToolResult(value, err), nil
		})
	}
	return server
}
func serveMCP(ctx context.Context, path string, in io.ReadCloser, out io.Writer) error {
	facade, err := newMCPFacade(path)
	if err != nil {
		return err
	}
	err = facade.server().Run(ctx, &mcp.IOTransport{Reader: in, Writer: mcpWriteCloser{out}, MaxLineLength: mcpFrameMaxBytes})
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
