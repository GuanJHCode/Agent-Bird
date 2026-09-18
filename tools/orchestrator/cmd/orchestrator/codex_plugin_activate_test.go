package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type recordedRPC struct {
	calls           []rpcCall
	writeStatus     string
	finalOverridden bool
}

func TestAppServerCloseKillsChildThatInheritedStdout(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	old := appServerCommand
	defer func() { appServerCommand = old }()
	appServerCommand = func(context.Context) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "read x; echo '{\"id\":1,\"result\":{\"codexHome\":\"/private/test\"}}'; read x; (echo $$ > '"+child+"'; sleep 30) & exit 0")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, closeRPC, err := startCodexAppServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeRPC(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatal(err)
	}
}

func TestAppServerContextCancellationStopsProcessGroup(t *testing.T) {
	old := appServerCommand
	defer func() { appServerCommand = old }()
	appServerCommand = func(context.Context) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "read x; echo '{\"id\":1,\"result\":{\"codexHome\":\"/private/test\"}}'; read x; sleep 30")
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, closeRPC, err := startCodexAppServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := closeRPC(); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("app server did not stop")
		}
		return
	}
}

type rpcCall struct {
	method string
	params map[string]any
}

func (r *recordedRPC) Call(_ context.Context, method string, params map[string]any) (map[string]any, error) {
	r.calls = append(r.calls, rpcCall{method: method, params: params})
	if method == "config/read" {
		oldEnabled, newEnabled := true, false
		if len(r.calls) > 2 && !r.finalOverridden {
			oldEnabled, newEnabled = false, true
		}
		return map[string]any{
			"config":  map[string]any{"plugins": map[string]any{"codex-orchestrator@codex-bird": map[string]any{"enabled": oldEnabled}, "agent-bird@agent-bird": map[string]any{"enabled": newEnabled}}},
			"origins": map[string]any{},
			"layers":  []any{map[string]any{"name": map[string]any{"type": "user", "file": "/private/test/config.toml", "profile": nil}, "version": "sha256:before", "config": map[string]any{}}},
		}, nil
	}
	if method == "config/batchWrite" {
		status := r.writeStatus
		if status == "" {
			status = "ok"
		}
		return map[string]any{"status": status}, nil
	}
	return nil, codeError("unexpected_rpc")
}

func TestActivateConfigRejectsUnexpectedBatchWriteStatus(t *testing.T) {
	rpc := &recordedRPC{writeStatus: "conflict"}
	if err := activateConfig(context.Background(), rpc, "/private/test/workspace"); errorCode(err) != "config_write_rejected" {
		t.Fatalf("error = %v", err)
	}
	if len(rpc.calls) != 2 {
		t.Fatalf("calls = %#v", rpc.calls)
	}
}

func TestActivateConfigAcceptsOkOverriddenOnlyWhenEffectiveValuesMatch(t *testing.T) {
	rpc := &recordedRPC{writeStatus: "okOverridden"}
	if err := activateConfig(context.Background(), rpc, "/private/test/workspace"); err != nil {
		t.Fatal(err)
	}
	rpc = &recordedRPC{writeStatus: "okOverridden", finalOverridden: true}
	if err := activateConfig(context.Background(), rpc, "/private/test/workspace"); errorCode(err) != "plugin_activation_overridden" {
		t.Fatalf("error = %v", err)
	}
}

func TestActivateConfigSwitchesBothPluginIDsInOneCASBatch(t *testing.T) {
	rpc := &recordedRPC{}
	if err := activateConfig(context.Background(), rpc, "/private/test/workspace"); err != nil {
		t.Fatal(err)
	}
	if len(rpc.calls) != 3 {
		t.Fatalf("calls = %#v", rpc.calls)
	}
	read := rpc.calls[0]
	if read.method != "config/read" || read.params["includeLayers"] != true || read.params["cwd"] != "/private/test/workspace" {
		t.Fatalf("read = %#v", read)
	}
	write := rpc.calls[1]
	if write.method != "config/batchWrite" || write.params["expectedVersion"] != "sha256:before" || write.params["reloadUserConfig"] != false {
		t.Fatalf("write = %#v", write)
	}
	edits, ok := write.params["edits"].([]map[string]any)
	if !ok || len(edits) != 2 {
		encoded, _ := json.Marshal(write.params)
		t.Fatalf("edits = %s", encoded)
	}
	if edits[0]["keyPath"] != "plugins.\"codex-orchestrator@codex-bird\".enabled" || edits[0]["value"] != false || edits[0]["mergeStrategy"] != "upsert" {
		t.Fatalf("old edit = %#v", edits[0])
	}
	if edits[1]["keyPath"] != "plugins.\"agent-bird@agent-bird\".enabled" || edits[1]["value"] != true || edits[1]["mergeStrategy"] != "upsert" {
		t.Fatalf("new edit = %#v", edits[1])
	}
}

func TestActivateConfigRejectsNonUserLayer(t *testing.T) {
	rpc := &recordedRPC{}
	_, _ = rpc.Call(context.Background(), "config/read", nil)
	rpc.calls = nil
	bad := &layerRPC{layer: map[string]any{"type": "project", "dotCodexFolder": "/private/test/workspace/.codex"}}
	if err := activateConfig(context.Background(), bad, "/private/test/workspace"); errorCode(err) != "user_config_layer_invalid" {
		t.Fatalf("error = %v", err)
	}
	if len(bad.calls) != 1 {
		t.Fatalf("unsafe write = %#v", bad.calls)
	}
}

type layerRPC struct {
	layer map[string]any
	calls []rpcCall
}

func (r *layerRPC) Call(_ context.Context, method string, params map[string]any) (map[string]any, error) {
	r.calls = append(r.calls, rpcCall{method: method, params: params})
	return map[string]any{"config": map[string]any{"plugins": map[string]any{}}, "origins": map[string]any{}, "layers": []any{map[string]any{"name": r.layer, "version": "sha256:before", "config": map[string]any{}}}}, nil
}
