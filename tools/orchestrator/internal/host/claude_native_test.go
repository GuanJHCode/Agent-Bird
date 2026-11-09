package host

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestNativeClaudeGatewayReadOnlyTransport launches a fixed CLI against an owned
// loopback SSE fixture; it uses no real credential and spends no model credits.
// This is transport/tool evidence, not a real-model acceptance result.
func TestNativeClaudeGatewayReadOnlyTransport(t *testing.T) {
	nativeClaudeGatewayModelTransports(t, false)
}

func TestNativeClaudeGatewayManagedWriteTransport(t *testing.T) {
	nativeClaudeGatewayModelTransports(t, true)
}

func nativeClaudeGatewayModelTransports(t *testing.T, managed bool) {
	if os.Getenv("ORCHESTRATOR_CLAUDE_NATIVE_BINARY") == "" {
		t.Skip("requires explicit fixed native/Node CLI path; no model or real credential")
	}
	for _, test := range []struct {
		name       string
		cliDefault bool
	}{{"explicit-model", false}, {"source-cli-default", true}} {
		t.Run(test.name, func(t *testing.T) {
			nativeClaudeGatewayTransport(t, managed, test.cliDefault)
		})
	}
}

func nativeClaudeGatewayTransport(t *testing.T, managed, cliDefault bool) {
	binary := os.Getenv("ORCHESTRATOR_CLAUDE_NATIVE_BINARY")
	if binary == "" {
		t.Skip("requires explicit fixed native/Node CLI path; no model or real credential")
	}
	cmd, profile, scratch, source := claudeGatewayFixture(t)
	var workspace *adapter.CandidateWorkspace
	if managed {
		work, spec := managedPolicyFixture(t)
		workspace = spec
		workspace.Paths = []string{"NEW.txt"}
		cmd.Dir = work + "-native-candidate"
		profile.Role = adapter.Implementer
		profile.Permission = adapter.WorkspaceWrite
		source.workspace = workspace
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	source.homes = append(source.homes, account.HomeDir)
	cmd.Path = binary
	scratch = filepath.Join(scratch, "state", "host-spool", "launch", "attempt", "segment", "scratch")
	const sentinel = "SYNTHETIC_READ_SENTINEL"
	readPath := filepath.Join(cmd.Dir, "INPUT.txt")
	if managed {
		readPath = filepath.Join(cmd.Dir, "NEW.txt")
	}
	if !managed {
		if err := os.WriteFile(readPath, []byte(sentinel), 0600); err != nil {
			t.Fatal(err)
		}
	}
	discovery, err := adapter.Probe(context.Background(), adapter.ProviderClaude, binary)
	if err != nil {
		t.Fatal(err)
	}
	source.help, err = adapter.ProbePinnedOutput(context.Background(), adapter.ProviderClaude, discovery.Binary, "--help")
	if err != nil {
		t.Fatal(err)
	}
	apiKey := os.Getenv("ORCHESTRATOR_CLAUDE_NATIVE_AUTH") == "api-key"
	if apiKey {
		source.env["ANTHROPIC_API_KEY"] = source.env["ANTHROPIC_AUTH_TOKEN"]
		delete(source.env, "ANTHROPIC_AUTH_TOKEN")
	}
	credential := source.env["ANTHROPIC_AUTH_TOKEN"] + source.env["ANTHROPIC_API_KEY"]
	const wireModel = "deepseek-v4.1-flash-1017"
	var requests, reads atomic.Int32
	{
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/messages") {
				http.NotFound(w, r)
				return
			}
			wantAuth, wantKey := "Bearer "+credential, ""
			if apiKey {
				wantAuth, wantKey = "", credential
			}
			if r.Header.Get("Authorization") != wantAuth || r.Header.Get("X-Api-Key") != wantKey {
				t.Error("gateway credential header mismatch")
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
			var request struct {
				Model  string `json:"model"`
				Stream bool   `json:"stream"`
				Tools  []struct {
					Name string `json:"name"`
				} `json:"tools"`
				Messages []struct {
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err != nil || json.Unmarshal(body, &request) != nil || request.Model != wireModel {
				t.Error("invalid synthetic model request")
				http.Error(w, "invalid request", 400)
				return
			}
			hasRead := false
			for _, tool := range request.Tools {
				hasRead = hasRead || (!managed && tool.Name == "Read") || (managed && tool.Name == "Write")
				if strings.HasPrefix(tool.Name, "mcp__") || tool.Name == "Bash" || (!managed && (tool.Name == "Edit" || tool.Name == "MultiEdit" || tool.Name == "Write" || tool.Name == "NotebookEdit")) || tool.Name == "Agent" || tool.Name == "Task" || tool.Name == "WebFetch" || tool.Name == "WebSearch" {
					t.Errorf("forbidden tool advertised: %s", tool.Name)
				}
			}

			readSeen := false
			for _, message := range request.Messages {
				var blocks []struct {
					Type      string          `json:"type"`
					ToolUseID string          `json:"tool_use_id"`
					Content   json.RawMessage `json:"content"`
					IsError   bool            `json:"is_error"`
				}
				if json.Unmarshal(message.Content, &blocks) == nil {
					for _, block := range blocks {
						if block.Type == "tool_result" && block.ToolUseID == "read_fixture" && (managed || strings.Contains(string(block.Content), sentinel)) {
							if managed {
								t.Logf("synthetic Write result is_error=%t content=%s", block.IsError, block.Content)
							}
							readSeen = true
						}
					}
				}
			}
			requests.Add(1)
			if !request.Stream {
				content := []any{map[string]any{"type": "text", "text": "GATEWAY_FIXTURE"}}
				stopReason := "end_turn"
				if hasRead && !readSeen {
					stopReason = "tool_use"
					content = []any{map[string]any{"type": "tool_use", "id": "read_fixture", "name": func() string {
						if managed {
							return "Write"
						}
						return "Read"
					}(), "input": func() map[string]string {
						if managed {
							return map[string]string{"file_path": readPath, "content": sentinel}
						}
						return map[string]string{"file_path": readPath}
					}()}}
				}
				if readSeen {
					reads.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": request.Model, "content": content, "stop_reason": stopReason, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			write := func(kind string, data any) {
				b, _ := json.Marshal(data)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b)
			}
			write("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "model": "deepseek-v4.1-flash-1017", "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 0}}})
			stopReason := "tool_use"
			if readSeen || !hasRead {
				if readSeen {
					reads.Add(1)
				}
				stopReason = "end_turn"
				write("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
				write("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "GATEWAY_FIXTURE"}})
			} else {
				write("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "read_fixture", "name": func() string {
					if managed {
						return "Write"
					}
					return "Read"
				}(), "input": map[string]any{}}})
				input, _ := json.Marshal(func() map[string]string {
					if managed {
						return map[string]string{"file_path": readPath, "content": sentinel}
					}
					return map[string]string{"file_path": readPath}
				}())
				write("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": string(input)}})
			}
			write("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
			write("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}})
			write("message_stop", map[string]string{"type": "message_stop"})
		}))
		defer server.Close()
		source.env["ANTHROPIC_BASE_URL"] = server.URL
		if cliDefault {
			profile.CLIModelDefault = true
			for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL"} {
				source.env[name] = wireModel + "[1M]"
			}
		} else {
			profile.Model = wireModel
		}
		lock, err := adapter.Probe(context.Background(), adapter.ProviderClaude, binary)
		if err != nil {
			t.Fatal(err)
		}
		inv, err := adapter.BuildInvocation(adapter.Request{Provider: adapter.ProviderClaude, Binary: lock.Binary, Lock: &lock, Profile: profile, CWD: cmd.Dir, Workspace: workspace, Prompt: "Execute the provided file tool, then reply GATEWAY_FIXTURE"})
		if err != nil {
			t.Fatal(err)
		}
		cmd.Args = inv.Args()[1:]
		cmd.Stdin = inv.Stdin()
		if managed {
			t.Setenv("ANTHROPIC_BASE_URL", source.env["ANTHROPIC_BASE_URL"])
			for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"} {
				t.Setenv(name, source.env[name])
			}
			t.Setenv("ANTHROPIC_API_KEY", "")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
			if apiKey {
				t.Setenv("ANTHROPIC_API_KEY", credential)
			} else {
				t.Setenv("ANTHROPIC_AUTH_TOKEN", credential)
			}
			for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FD", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
				t.Setenv(name, "")
			}
			host, err := NewIPC(filepath.Dir(filepath.Dir(filepath.Dir(scratch))), "native-claude-write")
			if err != nil {
				t.Fatal(err)
			}
			result, err := host.ExecuteLaunch(context.Background(), contract.LaunchCommand{CommandID: "native-write", ReservationID: "native-write", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 20000}, inv)
			if err != nil || result.Status != "result_ready" {
				body, _ := os.ReadFile(result.ArtifactPath)
				t.Fatalf("native managed host status=%s err=%v artifact=%s requests=%d results=%d", result.Status, err, body, requests.Load(), reads.Load())
			}
			body, err := os.ReadFile(result.ArtifactPath)
			if err != nil || !strings.Contains(string(body), "GATEWAY_FIXTURE") || !strings.Contains(string(body), "candidate_oid") {
				t.Fatalf("candidate not frozen: %s %v", body, err)
			}
			written, err := os.ReadFile(readPath)
			if err != nil || string(written) != sentinel {
				t.Fatalf("native Write did not produce declared file: %q %v", written, err)
			}
			if requests.Load() < 2 || reads.Load() == 0 {
				t.Fatalf("tool transport incomplete requests=%d results=%d", requests.Load(), reads.Load())
			}
			t.Logf("managed native Write+freeze requests=%d results=%d", requests.Load(), reads.Load())
			return
		}
	}

	prepared, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source)
	if err != nil {
		t.Fatal(err)
	}
	h, err := process.Start(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = h.Wait(ctx)
	if err != nil {
		stop, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = h.Stop(stop)
	}
	if h.ExitCode() != 0 {
		t.Logf("exit=%d output=%q", h.ExitCode(), h.Output())
	} else {
		t.Logf("exit=0 captured_bytes=%d gateway_requests=%d read_results=%d", len(h.Output()), requests.Load(), reads.Load())
	}
	if requests.Load() < 2 || reads.Load() == 0 || !strings.Contains(h.Output(), "GATEWAY_FIXTURE") {
		t.Error("synthetic header/SSE/Read transport incomplete")
	}
	_ = filepath.WalkDir(scratch, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Size() < 8<<20 {
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if strings.Contains(string(body), credential) {
					t.Error("credential persisted in private CLI state")
				}
			}
		}
		return nil
	})
	if err != nil || h.ExitCode() != 0 {
		t.Fatalf("synthetic gateway command failed: %v", err)
	}
}
