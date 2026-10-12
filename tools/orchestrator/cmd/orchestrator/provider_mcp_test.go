package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestProviderMCPSerialCallCancellationAndDisconnect(t *testing.T) {
	input, write := io.Pipe()
	read, output := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer write.Close()
	defer read.Close()
	started := make(chan providerCallRequest, 1)
	stopped := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveProviderMCP(ctx, input, output, func(ctx context.Context, req providerCallRequest) (providerCallResult, error) {
			started <- req
			<-ctx.Done()
			stopped <- struct{}{}
			return providerCallResult{Version: 1, Provider: req.Provider, Status: "cancelled", FailureReason: "call_cancelled"}, nil
		})
		output.Close()
	}()
	encoder := json.NewEncoder(write)
	scanner := bufio.NewScanner(read)
	send := func(method string, id any, params any) {
		t.Helper()
		message := map[string]any{"jsonrpc": "2.0", "method": method}
		if id != nil {
			message["id"] = id
		}
		if params != nil {
			message["params"] = params
		}
		if err := encoder.Encode(message); err != nil {
			t.Fatal(err)
		}
	}
	receive := func() map[string]json.RawMessage {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("no reply: %v", scanner.Err())
		}
		var r map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	send("initialize", 1, map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if receive()["result"] == nil {
		t.Fatal("initialize")
	}
	send("notifications/initialized", nil, nil)
	send("tools/list", 2, nil)
	if receive()["result"] == nil {
		t.Fatal("tools/list")
	}
	request := map[string]any{"name": "agent_bird_call", "arguments": map[string]any{"provider": "claude", "task": "Review README", "directory": "/private/project"}}
	send("tools/call", "first", request)
	select {
	case got := <-started:
		if got.Provider != "claude" || got.Mode != "read-only" {
			t.Fatalf("route %+v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	send("tools/call", "second", request)
	if receive()["error"] == nil {
		t.Fatal("concurrent child admitted")
	}
	if _, err := io.WriteString(write, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"\u0066irst"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if receive()["result"] == nil {
		t.Fatal("cancelled result missing")
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	send("tools/call", "third", request)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	write.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("disconnect did not join active child")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("disconnect did not cancel")
	}
}
