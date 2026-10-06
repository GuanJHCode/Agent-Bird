package codexsession

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
)

// Pure: actual M1 on net.Pipe, no TestMain, subprocess, Host or filesystem.
func TestRunBindsThreadBeforeManagedTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	local, peer := net.Pipe()
	client, err := codexrpc.New(ctx, local, codexrpc.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	methods := []string{}
	var bound atomic.Bool
	go func() {
		defer peer.Close()
		decoder := json.NewDecoder(peer)
		encoder := json.NewEncoder(peer)
		for _, method := range []string{"initialize", "initialized", "config/read", "thread/start", "turn/start"} {
			var frame map[string]json.RawMessage
			if err := decoder.Decode(&frame); err != nil {
				done <- err
				return
			}
			var actual string
			_ = json.Unmarshal(frame["method"], &actual)
			methods = append(methods, actual)
			if actual != method {
				done <- fmt.Errorf("unexpected method")
				return
			}
			if err := checkFixtureRequest(method, frame["params"], false); err != nil {
				done <- err
				return
			}
			if method == "initialized" {
				continue
			}
			if method == "turn/start" && !bound.Load() {
				done <- fmt.Errorf("turn before binding callback")
				return
			}
			result := map[string]any{}
			switch method {
			case "initialize":
				result = map[string]any{"codexHome": "/private/owned/codex", "platformFamily": "unix", "platformOs": "macos", "userAgent": "fixture"}
			case "config/read":
				result = map[string]any{"config": fixtureEffective(), "origins": map[string]any{}}
			case "thread/start":
				result = map[string]any{"approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": "/private/owned/project", "model": "gpt-5.4", "modelProvider": "openai", "reasoningEffort": "high", "serviceTier": "priority", "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}, "thread": fixtureThread()}
			case "turn/start":
				result = map[string]any{"turn": map[string]any{"id": "turn-1", "items": []any{}, "status": "inProgress", "startedAt": 2}}
			}
			if err := encoder.Encode(map[string]any{"id": frame["id"], "result": result}); err != nil {
				done <- err
				return
			}
		}
		item := map[string]any{"type": "agentMessage", "id": "final", "phase": "final_answer", "text": "{\"version\":1,\"kind\":\"technical_question\",\"text\":\"Which API?\"}"}
		turn := map[string]any{"id": "turn-1", "items": []any{item}, "status": "completed", "startedAt": 2, "completedAt": 3, "error": nil}
		if err := encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": turn}}); err != nil {
			done <- err
			return
		}
		// A native app-server stays connected until the owning Run closes it.
		// Waiting for EOF also joins this peer and catches an extra task call.
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			done <- fmt.Errorf("extra call or missing close")
			return
		}
		done <- nil
	}()
	projection, err := codexconfig.Project([]byte(`model = "gpt-5.4"
model_reasoning_effort = "high"
service_tier = "priority"
`), codexconfig.ReadOnly)
	if err != nil {
		_ = client.Close()
		<-done
		t.Fatal(err)
	}
	callbacks := 0
	options := Options{CWD: "/private/owned/project", OwnedHome: "/private/owned/codex", PlatformFamily: "unix", PlatformOS: "macos", Prompt: "Review API", OnThread: func(_ context.Context, b ThreadBinding) error {
		callbacks++
		bound.Store(true)
		if b != (ThreadBinding{ThreadID: "thread-1", Model: "gpt-5.4", ModelProvider: "openai"}) {
			return fmt.Errorf("unexpected binding")
		}
		return nil
	}}
	out, runErr := Run(ctx, client, projection, options)
	_ = client.Close()
	peerErr := <-done
	if runErr != nil {
		t.Fatalf("new managed session sequence missing: %v; peer=%v", runErr, peerErr)
	}
	if peerErr != nil || callbacks != 1 || out.Binding.ThreadID != "thread-1" || out.TurnID != "turn-1" || out.Status != "completed" || out.Completion.Kind != "technical_question" || out.Completion.Text != "Which API?" || !reflect.DeepEqual(methods, []string{"initialize", "initialized", "config/read", "thread/start", "turn/start"}) {
		t.Fatalf("invalid single managed turn: %#v, callback=%d, methods=%v peer=%v", out, callbacks, methods, peerErr)
	}
}

func checkFixtureRequest(method string, raw json.RawMessage, resume bool) error {
	var p map[string]any
	if len(raw) > 0 {
		if json.Unmarshal(raw, &p) != nil {
			return fmt.Errorf("request params")
		}
	}
	switch method {
	case "initialize":
		if !reflect.DeepEqual(p, map[string]any{"clientInfo": map[string]any{"name": "agent-bird-managed", "version": "1", "title": "Agent Bird"}, "capabilities": map[string]any{"experimentalApi": false, "requestAttestation": false}}) {
			return fmt.Errorf("initialize controls")
		}
	case "initialized":
		if len(raw) > 0 {
			return fmt.Errorf("initialized params")
		}
	case "config/read":
		if !reflect.DeepEqual(p, map[string]any{"cwd": "/private/owned/project", "includeLayers": false}) {
			return fmt.Errorf("config controls")
		}
	case "thread/start", "thread/resume":
		want := map[string]any{"cwd": "/private/owned/project", "model": "gpt-5.4", "modelProvider": "openai", "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": "read-only", "serviceTier": "priority"}
		if resume {
			want["threadId"] = "thread-1"
			want["excludeTurns"] = true
		} else {
			want["ephemeral"] = false
		}
		if !reflect.DeepEqual(p, want) {
			return fmt.Errorf("thread controls")
		}
	case "turn/start":
		var schema any
		_ = json.Unmarshal([]byte(adapter.ManagedTaskSchema()), &schema)
		want := map[string]any{"threadId": "thread-1", "cwd": "/private/owned/project", "model": "gpt-5.4", "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandboxPolicy": map[string]any{"type": "readOnly", "networkAccess": false}, "input": []any{map[string]any{"type": "text", "text": "Review API\n\n" + adapter.ManagedTaskInstruction(), "text_elements": []any{}}}, "outputSchema": schema, "effort": "high", "serviceTier": "priority"}
		if !reflect.DeepEqual(p, want) {
			return fmt.Errorf("turn controls or shared schema")
		}
	default:
		return fmt.Errorf("unexpected task call")
	}
	return nil
}

func fixtureThread() map[string]any {
	return map[string]any{"id": "thread-1", "sessionId": "session-1", "projectId": nil, "cliVersion": "0.154.0", "createdAt": 1, "updatedAt": 1, "cwd": "/private/owned/project", "ephemeral": false, "modelProvider": "openai", "source": "appServer", "preview": "", "status": map[string]any{"type": "idle"}, "turns": []any{}}
}
func fixtureEffective() map[string]any {
	features := map[string]any{}
	for _, key := range []string{"codex_hooks", "hooks", "plugin_hooks", "plugins", "apps", "enable_mcp_apps", "multi_agent", "multi_agent_v2", "collab", "browser_use", "computer_use", "memories", "memory_tool", "shell_snapshot", "shell_snapshot_v2", "remote_control", "background_paginated_rollout_migration"} {
		features[key] = false
	}
	config := map[string]any{"model": "gpt-5.4", "model_reasoning_effort": "high", "service_tier": "priority", "cli_auth_credentials_store": "file", "sandbox_mode": "read-only", "approval_policy": "on-request", "approvals_reviewer": "user", "check_for_update_on_startup": false, "features": features, "notify": []any{}, "history": map[string]any{"persistence": "none"}, "analytics": map[string]any{"enabled": false}, "web_search": "disabled"}
	fixtureCompatibilityDefaults(config)
	return config
}
