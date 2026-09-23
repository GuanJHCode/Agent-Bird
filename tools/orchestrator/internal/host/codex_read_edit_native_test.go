package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Real pinned Codex, with a deterministic local Responses fixture. This tests
// protocol/tool execution, not model quality or production authentication.
func TestNativeCodexReadAndApplyPatchWithoutModel(t *testing.T) {
	runNativeCodexPatchFixture(t, "update")
}

func TestNativeCodexPatchVariantsWithoutModel(t *testing.T) {
	for _, operation := range []string{"add", "delete", "move"} {
		t.Run(operation, func(t *testing.T) { runNativeCodexPatchFixture(t, operation) })
	}
}

func runNativeCodexPatchFixture(t *testing.T, operation string) {
	if os.Getenv("AGENT_BIRD_NATIVE_CODEX_REMOTE") != "1" {
		t.Skip("opt-in native read/edit, synthetic Responses, no model")
	}
	binary := os.Getenv("AGENT_BIRD_NATIVE_CODEX_BINARY")
	if binary == "" {
		t.Fatal("fixed binary required")
	}
	patches := map[string]string{
		"update": "*** Begin Patch\n*** Update File: fixture.txt\n@@\n-before\n+after\n*** End Patch",
		"add":    "*** Begin Patch\n*** Add File: nested/new.txt\n+created\n*** End Patch",
		"delete": "*** Begin Patch\n*** Delete File: fixture.txt\n*** End Patch",
		"move":   "*** Begin Patch\n*** Update File: fixture.txt\n*** Move to: nested/moved.txt\n@@\n-before\n+after\n*** End Patch",
	}
	fixture := &readEditResponses{patch: patches[operation]}
	server := httptest.NewServer(fixture)
	defer server.Close()
	root, err := os.MkdirTemp("/private/tmp", "bird-codex-read-edit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("synthetic native evidence: %s", root)
	work, source, scratch := filepath.Join(root, "work"), filepath.Join(root, "source"), filepath.Join(root, "scratch")
	for _, dir := range []string{work, source, scratch} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(root, "repo")
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}, {"-C", repo, "worktree", "add", "--detach", work}} {
		git := exec.Command("/usr/bin/git", args...)
		git.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		if output, err := git.CombinedOutput(); err != nil {
			t.Fatalf("isolated fixture: %v %s", err, output)
		}
	}
	config := "model='gpt-5.5'\nmodel_provider='synthetic'\napproval_policy='never'\napprovals_reviewer='user'\nsandbox_mode='workspace-write'\nweb_search='disabled'\n[model_providers.synthetic]\nname='synthetic-offline'\nbase_url=" + strconv.Quote(server.URL) + "\nwire_api='responses'\nrequires_openai_auth=false\nsupports_websockets=false\nrequest_max_retries=0\nstream_max_retries=0\nstream_idle_timeout_ms=10000\n"
	// Test-only source: no real account, inherited auth environment or home.
	for path, value := range map[string]string{filepath.Join(source, "config.toml"): config, filepath.Join(source, "auth.json"): "{}", filepath.Join(work, "fixture.txt"): "before\n"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte("Preserve BIRD_NATIVE_INSTRUCTIONS and task isolation.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	home, err := createCodexHome(source, scratch, []byte(config))
	if err != nil {
		t.Fatal(err)
	}
	state := &codexRuntimeState{home: home}
	defer state.close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	original := process.Command{Path: binary, PinnedPath: binary, PinnedSHA256: adapter.CodexSHA256, Dir: work, ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + root, "CODEX_HOME=" + home.path, "TMPDIR=" + home.runtime}, Stdin: []byte("Read fixture.txt using bird_read_file, change before to after with apply_patch, then report completion.")}
	// Bootstrap only the fixture's builtin skills/config. The production path
	// performs its additional source-authentication checks before this phase.
	home.provisionSystem = true
	if err := os.Mkdir(filepath.Join(home.path, "skills"), 0700); err != nil {
		t.Fatal(err)
	}
	metadata := original
	metadata.Stdin = nil
	metadata.Args = append(codexRuntimeFlags(home), "app-server", "--listen", "stdio://")
	readonly := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 45000}
	metadata, err = codexSandboxCommand(ctx, metadata, readonly, scratch, home)
	if err != nil {
		t.Fatal(err)
	}
	rpc, stop, err := codexrpc.StartPinnedCommand(ctx, metadata)
	if err != nil {
		t.Fatal(err)
	}
	result, readErr := rpc.Call(ctx, "config/read", map[string]any{"includeLayers": true, "cwd": work})
	if readErr == nil {
		_, readErr = rpc.Call(ctx, "skills/list", map[string]any{"cwds": []string{work}, "forceReload": true})
	}
	stopErr := stop()
	if readErr != nil || stopErr != nil {
		t.Fatalf("fixture bootstrap: read=%v stop=%v", readErr, stopErr)
	}
	state.config, _ = result["config"].(map[string]any)
	home.provisionSystem = false
	if err := home.sealSystemSkills(); err != nil {
		t.Fatal(err)
	}
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 45000, Model: "gpt-5.5"}
	cmd, err := finishCodexWorker(ctx, original, original, profile, scratch, false, state)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	child, err := process.Start(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(ctx); err != nil {
		_ = child.Stop(context.Background())
		t.Fatal(err)
	}
	if err := child.ConfirmTreeExited(); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	requests, failure := fixture.requests, fixture.failure
	fixture.mu.Unlock()
	if err := state.verify(); err != nil {
		for _, line := range strings.Split(child.Output(), "\n") {
			var frame map[string]any
			if json.Unmarshal([]byte(line), &frame) == nil && frame["id"] == float64(3) {
				result, _ := frame["result"].(map[string]any)
				t.Logf("thread approval: expected=%v actual=%v; reviewer: expected=%v actual=%v", state.config["approval_policy"], result["approvalPolicy"], state.config["approvals_reviewer"], result["approvalsReviewer"])
			}
		}
		t.Fatalf("native read/edit: %v; turn=%v requests=%d fixture=%s bridge=%v output=%s", err, state.turn.Err(), requests, failure, state.bridge.Diagnostics(), safeCodexMetadataFailure(child.Output()))
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(work, "fixture.txt"))
	valid := err == nil && string(value) == "after\n"
	switch operation {
	case "add":
		added, addErr := os.ReadFile(filepath.Join(work, "nested", "new.txt"))
		valid = err == nil && string(value) == "before\n" && addErr == nil && string(added) == "created\n"
	case "delete":
		valid = os.IsNotExist(err)
	case "move":
		moved, moveErr := os.ReadFile(filepath.Join(work, "nested", "moved.txt"))
		valid = os.IsNotExist(err) && moveErr == nil && string(moved) == "after\n"
	}
	if !valid || requests != 3 || failure != "" || !strings.Contains(child.Output(), "SYNTHETIC_COMPLETE") {
		t.Fatalf("read/edit incomplete: value=%q requests=%d fixture=%s read=%v", value, requests, failure, err)
	}
	t.Logf("three local Responses requests; sealed AGENTS preserved; dynamic read returned before; native apply_patch %s verified; final/usage received; process groups exited; no model/account used", operation)
}

type readEditResponses struct {
	mu       sync.Mutex
	requests int
	failure  string
	patch    string
}

func (s *readEditResponses) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reject := func(reason string) { s.failure = reason; http.Error(w, reason, http.StatusConflict) }
	if r.Header.Get("Authorization") != "" || r.Header.Get("Api-Key") != "" || r.Header.Get("Cookie") != "" {
		reject("unexpected authentication")
		return
	}
	if r.Method == "GET" && r.URL.Path == "/models" {
		http.NotFound(w, r)
		return
	}
	if r.Method != "POST" || r.URL.Path != "/responses" {
		reject("unexpected request: " + r.Method + " " + r.URL.Path)
		return
	}
	var request map[string]any
	if json.NewDecoder(io.LimitReader(r.Body, 2*1024*1024)).Decode(&request) != nil || request["model"] != "gpt-5.5" {
		reject("invalid model request")
		return
	}
	s.requests++
	var item map[string]any
	switch s.requests {
	case 1:
		requestBytes, _ := json.Marshal(request)
		if !strings.Contains(string(requestBytes), "BIRD_NATIVE_INSTRUCTIONS") {
			reject("native project instructions missing")
			return
		}
		tools, _ := request["tools"].([]any)
		seen := map[string]bool{}
		var surface []string
		for _, value := range tools {
			tool, _ := value.(map[string]any)
			surface = append(surface, fmt.Sprint(tool["type"])+":"+fmt.Sprint(tool["name"]))
		}
		for _, value := range tools {
			tool, _ := value.(map[string]any)
			name, _ := tool["name"].(string)
			if name == "" || !(name == "bird_list_files" || name == "bird_read_file" || name == "bird_search_text" || name == "apply_patch" || name == "update_plan" || name == "skills") {
				reject("unexpected native tool surface: " + strings.Join(surface, ","))
				return
			}
			if (name == "apply_patch" && tool["type"] != "custom") || (name == "bird_read_file" && tool["type"] != "function") {
				reject("native tool type changed")
				return
			}
			seen[name] = true
		}
		if !seen["bird_read_file"] || !seen["apply_patch"] {
			reject("read/edit tools missing")
			return
		}
		item = map[string]any{"type": "function_call", "call_id": "call-read", "name": "bird_read_file", "arguments": `{"path":"fixture.txt","start_line":1,"limit":20}`}
	case 2:
		if !hasSyntheticOutput(request, "function_call_output", "call-read", "before") {
			reject("read result missing")
			return
		}
		item = map[string]any{"type": "custom_tool_call", "call_id": "call-patch", "name": "apply_patch", "input": s.patch}
	case 3:
		if !hasSyntheticOutput(request, "custom_tool_call_output", "call-patch", "Success") {
			reject("patch result missing")
			return
		}
		item = map[string]any{"type": "message", "role": "assistant", "id": "msg-final", "content": []any{map[string]any{"type": "output_text", "text": "SYNTHETIC_COMPLETE"}}}
	default:
		reject("unexpected retry")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	id := fmt.Sprintf("resp-%d", s.requests)
	for _, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id}},
		{"type": "response.output_item.done", "item": item},
		{"type": "response.completed", "response": map[string]any{"id": id, "end_turn": s.requests == 3, "usage": map[string]any{"input_tokens": 0, "input_tokens_details": nil, "output_tokens": 0, "output_tokens_details": nil, "total_tokens": 0}}},
	} {
		raw, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], raw)
	}
}

func hasSyntheticOutput(request map[string]any, kind, id, content string) bool {
	input, _ := request["input"].([]any)
	for _, value := range input {
		item, _ := value.(map[string]any)
		if item["type"] == kind && item["call_id"] == id {
			raw, _ := json.Marshal(item["output"])
			return strings.Contains(string(raw), content)
		}
	}
	return false
}
