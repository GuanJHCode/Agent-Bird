package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/workspaceread"
)

func TestLightweightWorkerDisablesSynchronousUserInput(t *testing.T) {
	config := codexExpectedRuntimeConfig("/runtime")
	config["tools"] = map[string]any{"experimental_request_user_input": map[string]any{"enabled": true}}
	if err := verifyCodexReadEditConfig(config); err == nil {
		t.Fatal("worker can still request native user input")
	}
}

func TestUnconfiguredReviewerUsesPinnedNativeDefault(t *testing.T) {
	for _, config := range []map[string]any{{}, {"approvals_reviewer": nil}} {
		if got := codexThreadReviewer(config); got != "user" {
			t.Fatalf("pinned default: %v", got)
		}
		if config["approvals_reviewer"] != nil {
			t.Fatal("rewrote original config")
		}
	}
	if got := codexThreadReviewer(map[string]any{"approvals_reviewer": "guardian_subagent"}); got != "guardian_subagent" {
		t.Fatal("overrode explicit reviewer")
	}
}

func TestCodexDynamicReadToolsUseWorkspaceAndValidateArguments(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(root, "calc.py"), []byte("return a + b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := workspaceread.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	tools := codexReadTools(context.Background(), r)
	if len(tools) != 3 {
		t.Fatal("wrong read tool surface")
	}
	for _, tool := range tools {
		var args map[string]any
		switch tool.Name {
		case "bird_read_file":
			args = map[string]any{"path": "calc.py", "start_line": float64(1), "limit": float64(20)}
		case "bird_list_files":
			args = map[string]any{"directory": ".", "after": "", "limit": float64(20)}
		case "bird_search_text":
			args = map[string]any{"pattern": "return", "directory": ".", "after": "", "limit": float64(20)}
		default:
			t.Fatal("non-read tool registered")
		}
		text, err := tool.Call(args)
		if err != nil || !json.Valid([]byte(text)) || !strings.Contains(text, "calc.py") || strings.Contains(text, root) {
			t.Fatal("invalid workspace tool result", err)
		}
		args["command"] = "must not execute"
		if _, err := tool.Call(args); err == nil {
			t.Fatal("unknown argument accepted")
		}
		delete(args, "command")
		args["limit"] = true
		if _, err := tool.Call(args); err == nil {
			t.Fatal("bool accepted as numeric limit")
		}
	}
}

func TestCodexReadEditProjectionRejectsMissingOrEnabledProcessFeatures(t *testing.T) {
	features := map[string]any{}
	for _, name := range codexDisabledFeatures() {
		features[name] = false
	}
	config := map[string]any{"features": features, "agents": map[string]any{"enabled": false}, "web_search": "disabled", "tools": map[string]any{"experimental_request_user_input": map[string]any{"enabled": false}}}
	if err := verifyCodexReadEditConfig(config); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"shell_tool", "unified_exec", "unified_exec_tty", "code_mode"} {
		features[key] = true
		if err := verifyCodexReadEditConfig(config); err == nil {
			t.Fatal("process tool enabled", key)
		}
		delete(features, key)
		if err := verifyCodexReadEditConfig(config); err == nil {
			t.Fatal("unverified feature admitted", key)
		}
		features[key] = false
	}
}
