package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A real MCP transport with only the external Provider execution replaced.
func mcpRoundTrip(t *testing.T, execute func(context.Context, providerCallRequest) (providerCallResult, error)) func(string, any) map[string]any {
	t.Helper()
	input, writer := io.Pipe()
	reader, output := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- serveProviderMCP(ctx, input, output, execute); output.Close() }()
	t.Cleanup(func() { writer.Close(); reader.Close(); cancel(); <-done })
	encoder, scanner := json.NewEncoder(writer), bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	id := 0
	exchange := func(method string, params any) map[string]any {
		t.Helper()
		id++
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() {
			t.Fatalf("missing response: %v", scanner.Err())
		}
		var response map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	exchange("initialize", map[string]any{})
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	return exchange
}

func mcpToolBody(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("tool failed: %+v", response)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var body map[string]any
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestProviderMCPCompactResultAndScopedDetails(t *testing.T) {
	text := strings.Repeat("界", 6000)
	calls := 0
	rpc := mcpRoundTrip(t, func(_ context.Context, req providerCallRequest) (providerCallResult, error) {
		calls++
		return providerCallResult{Version: 1, CallID: "call-one", Provider: req.Provider, Status: "failed", FailureReason: "task_failed", Result: text, ChangesKnown: false, ProcessExited: true}, nil
	})
	invoke := func(name string, args any) map[string]any {
		return rpc("tools/call", map[string]any{"name": name, "arguments": args})
	}
	response := invoke("agent_bird_call", map[string]any{"provider": "grok", "task": "review", "directory": "/private/project"})
	body := mcpToolBody(t, response)
	excerpt := body["result"].(string)
	if len(excerpt) > 4096 || len(excerpt) == 0 || !utf8.ValidString(excerpt) || body["result_truncated"] != true || body["result_bytes"] != float64(len(text)) {
		t.Fatalf("unbounded or invalid excerpt: bytes=%d metadata=%v", len(excerpt), body["result_truncated"])
	}
	if body["failure_reason"] != "task_failed" || body["changes_known"] != false || body["process_exited"] != true || response["result"].(map[string]any)["isError"] != true {
		t.Fatal("lost failure or exit facts")
	}
	rebuilt := excerpt
	for offset := len(excerpt); offset < len(text); {
		page := mcpToolBody(t, invoke("agent_bird_result", map[string]any{"call_id": "call-one", "offset": offset, "limit_bytes": 4096}))
		part := page["result"].(string)
		if !utf8.ValidString(part) || len(part) > 4096 || len(part) == 0 || page["result_sha256"] != body["result_sha256"] {
			t.Fatal("invalid detail page")
		}
		rebuilt += part
		offset = int(page["next_offset"].(float64))
	}
	if rebuilt != text || calls != 1 {
		t.Fatal("details restarted Provider or lost bytes")
	}
	for _, args := range []map[string]any{{"call_id": "foreign", "offset": 0}, {"call_id": "call-one", "offset": 1}, {"call_id": "call-one", "offset": -1}, {"call_id": "call-one", "limit_bytes": 999999}} {
		if invoke("agent_bird_result", args)["error"] == nil {
			t.Fatalf("invalid detail request accepted: %v", args)
		}
	}
}

func TestProviderMCPDetailsNeverReadAnotherConnection(t *testing.T) {
	rpc := mcpRoundTrip(t, func(context.Context, providerCallRequest) (providerCallResult, error) {
		t.Fatal("details launched child")
		return providerCallResult{}, nil
	})
	got := rpc("tools/call", map[string]any{"name": "agent_bird_result", "arguments": map[string]any{"call_id": "call-one"}})
	if got["error"] == nil {
		t.Fatal("foreign connection result admitted")
	}
}

func TestProviderMCPNextCallInvalidatesPreviousDetails(t *testing.T) {
	calls := 0
	rpc := mcpRoundTrip(t, func(_ context.Context, req providerCallRequest) (providerCallResult, error) {
		calls++
		id := "first"
		if calls > 1 {
			id = "second"
		}
		return providerCallResult{Version: 1, CallID: id, Provider: req.Provider, Status: "result_ready", Result: "done", ProcessExited: true}, nil
	})
	call := map[string]any{"name": "agent_bird_call", "arguments": map[string]any{"provider": "claude", "task": "read", "directory": "/private/project"}}
	mcpToolBody(t, rpc("tools/call", call))
	mcpToolBody(t, rpc("tools/call", call))
	stale := rpc("tools/call", map[string]any{"name": "agent_bird_result", "arguments": map[string]any{"call_id": "first"}})
	if stale["error"] == nil {
		t.Fatal("previous task details survived next call")
	}
	page := mcpToolBody(t, rpc("tools/call", map[string]any{"name": "agent_bird_result", "arguments": map[string]any{"call_id": "second"}}))
	if page["result"] != "done" || calls != 2 {
		t.Fatal("wrong result or duplicate dispatch")
	}
}
