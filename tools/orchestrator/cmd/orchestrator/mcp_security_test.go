package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

// All artifacts and signing keys in these tests are generated task-local fixtures.
func securityMCP(t *testing.T) *mcpFacade {
	t.Helper()
	base := privateTaskTemp(t)
	request := filepath.Join(base, "request.json")
	securityWriteJSON(t, request, mcpConfigureRequest{Version: 1, Root: filepath.Join(base, "mcp"), StateDir: filepath.Join(base, "state"), Workspace: base, Providers: map[string]string{}})
	if err := configureMCP(request, io.Discard); err != nil {
		t.Fatal(err)
	}
	f, err := newMCPFacade(filepath.Join(base, "mcp", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func securityWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func securityHandle(t *testing.T, f *mcpFacade) string {
	t.Helper()
	dir := filepath.Join(f.config.StateDir, "dispatch", "request")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "handle.json")
	securityWriteJSON(t, path, taskHandle{Version: 1, RunID: "run-one", StateDir: f.config.StateDir, TaskIDs: []string{"task-one"}, Status: "submitted"})
	securityWriteJSON(t, filepath.Join(dir, "reservation.json"), dispatchReservation{Version: 1, RunID: "run-one", ControllerThread: "owner-one"})
	return path
}
func TestMCPSecurityPrivateAuthorityFiles(t *testing.T) {
	for _, artifact := range []string{"config", "key"} {
		for _, attack := range []string{"public_mode", "symlink", "hardlink", "public_parent", "symlink_parent"} {
			t.Run(artifact+"/"+attack, func(t *testing.T) {
				f := securityMCP(t)
				path := f.path
				if artifact == "key" {
					path = filepath.Join(f.config.StateDir, "mcp-keys", f.config.Namespace, "token-key")
				}
				switch attack {
				case "public_mode":
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Rename(path, path+".real"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(path+".real", path); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					if err := os.Link(path, path+".link"); err != nil {
						t.Fatal(err)
					}
				case "public_parent":
					if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
				case "symlink_parent":
					parent := filepath.Dir(path)
					if err := os.Rename(parent, parent+".real"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(parent+".real", parent); err != nil {
						t.Fatal(err)
					}
				}
				if _, _, err := loadMCPConfiguration(f.path); err == nil {
					t.Fatal("untrusted authority file was accepted")
				}
			})
		}
	}
}
func TestMCPSecurityTokensPersistAndIsolateNamespaces(t *testing.T) {
	f := securityMCP(t)
	path := securityHandle(t, f)
	token, err := f.mintRoute(path, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	reload, err := newMCPFacade(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var route mcpRoute
	if err = reload.unseal(token, "route", &route); err != nil {
		t.Fatal(err)
	}
	again, err := reload.mintRoute(path, "owner-one")
	if err != nil || again != token {
		t.Fatalf("stable reload token: %v", err)
	}
	// A different namespace is rejected even if a caller mistakenly shares a key.
	other := securityMCP(t)
	other.key = append([]byte(nil), f.key...)
	if err = other.unseal(token, "route", &mcpRoute{}); err == nil {
		t.Fatal("cross-namespace token accepted")
	}
	if err = f.unseal(token, "review", &mcpReview{}); err == nil {
		t.Fatal("route used as review token")
	}
	parts := strings.Split(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("owner-one"), []byte("owner-two"), 1)
	if err = f.unseal(base64.RawURLEncoding.EncodeToString(raw)+"."+parts[1], "route", &mcpRoute{}); err == nil {
		t.Fatal("tampered owner accepted")
	}
	for _, mutation := range []string{"run", "tasks", "state"} {
		t.Run(mutation, func(t *testing.T) {
			h := taskHandle{Version: 1, RunID: route.RunID, StateDir: route.StateDir, TaskIDs: append([]string(nil), route.TaskIDs...)}
			switch mutation {
			case "run":
				h.RunID = "run-two"
			case "tasks":
				h.TaskIDs = []string{"task-two"}
			case "state":
				h.StateDir = filepath.Dir(route.StateDir)
			}
			securityWriteJSON(t, path, h)
			if _, _, _, err := f.resolveRoute(token); errorCode(err) != "mcp_route_scope_mismatch" {
				t.Fatalf("mutated handle: %v", err)
			}
		})
	}
}
func TestMCPSecurityReviewBindsRunOwnerTaskAndCandidate(t *testing.T) {
	f := securityMCP(t)
	route := mcpRoute{ID: "route-one", RunID: "run-one", Owner: "owner-one", TaskIDs: []string{"task-one"}}
	binding := store.ReviewableResultBinding{SegmentID: "segment", CandidateOID: strings.Repeat("a", 40), EventID: "event", EventRevision: 9007199254740993, EventHash: strings.Repeat("b", 64), ActionSlot: "review", WorkRevision: 2}
	token, err := f.reviewReference(route, "task-one", binding)
	if err != nil {
		t.Fatal(err)
	}
	review, err := f.resolveReview(token, route)
	if err != nil || review.Binding != binding {
		t.Fatalf("review tuple round trip: %+v %v", review, err)
	}
	for _, mutation := range []string{"route", "run", "owner", "task"} {
		t.Run(mutation, func(t *testing.T) {
			other := route
			switch mutation {
			case "route":
				other.ID = "route-two"
			case "run":
				other.RunID = "run-two"
			case "owner":
				other.Owner = "owner-two"
			case "task":
				other.TaskIDs = []string{"task-two"}
			}
			if _, err := f.resolveReview(token, other); err == nil {
				t.Fatal("review escaped its original scope")
			}
		})
	}
	if _, err = f.reviewReference(route, "task-two", binding); err == nil {
		t.Fatal("minted review for foreign task")
	}
	binding.CandidateOID = ""
	if _, err = f.reviewReference(route, "task-one", binding); err == nil {
		t.Fatal("minted unreviewable candidate")
	}
}
func TestMCPSecurityArgumentDecoding(t *testing.T) {
	for _, raw := range []string{`null`, `{"Handle":"x"}`, `{"handle":null}`, `{"handle":"x","owner":"forged"}`, `{"handle":"x","control_file":"/forged"}`, `{"handle":"x","state_dir":"/forged"}`, `{"handle":"x","capability":"forged"}`} {
		t.Run(raw, func(t *testing.T) {
			var args mcpReadArgs
			if err := mcpArguments(json.RawMessage(raw), &args); errorCode(err) != "invalid_mcp_arguments" {
				t.Fatalf("unsafe args accepted: %v", err)
			}
		})
	}
	for _, raw := range []string{`{"budget":{"Max_Attempts":1}}`, `{"budget":{"owner":"forged"}}`, `{"budget":null}`, `{"paths":[null]}`} {
		t.Run(raw, func(t *testing.T) {
			var args mcpDispatchArgs
			if err := mcpArguments(json.RawMessage(raw), &args); err == nil {
				t.Fatal("invalid nested arguments accepted")
			}
		})
	}
	for _, raw := range []string{`{"decisions":[{"Event_Revision":1}]}`, `{"decisions":[{"event_revision":1,"owner":"forged"}]}`, `{"decisions":[null]}`, `{"decisions":null}`, `{"decisions":[{"event_revision":9223372036854775808}]}`, `{"decisions":[{"event_revision":1.5}]}`} {
		t.Run(raw, func(t *testing.T) {
			var args mcpAckArgs
			if err := mcpArguments(json.RawMessage(raw), &args); err == nil {
				t.Fatal("invalid ACK tuple accepted")
			}
		})
	}
	var args mcpAckArgs
	if err := mcpArguments(json.RawMessage(`{"decisions":[{"event_revision":9223372036854775807}]}`), &args); err != nil {
		t.Fatal(err)
	}
	if args.Decisions[0].EventRevision != 9223372036854775807 {
		t.Fatal("ACK revision passed through float64")
	}
	raw, err := json.Marshal(args.Decisions)
	if err != nil || !bytes.Contains(raw, []byte(`9223372036854775807`)) {
		t.Fatalf("ACK revision changed during forwarding: %s %v", raw, err)
	}
}
func TestMCPSecurityConfigurationNeverUsesCachedAuthority(t *testing.T) {
	for _, mutation := range []string{"workspace", "provider", "key"} {
		t.Run(mutation, func(t *testing.T) {
			f := securityMCP(t)
			token, err := f.mintRoute(securityHandle(t, f), "owner-one")
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "workspace":
				c := f.config
				c.Workspace = filepath.Dir(c.Workspace)
				securityWriteJSON(t, f.path, c)
			case "provider":
				c := f.config
				c.Providers = map[string]string{"codex": "/changed-lock"}
				securityWriteJSON(t, f.path, c)
			case "key":
				if err := os.WriteFile(filepath.Join(f.config.StateDir, "mcp-keys", f.config.Namespace, "token-key"), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			f.invoke = func(context.Context, []string, io.Writer) error { calls++; return nil }
			if _, err = f.inspect(context.Background(), json.RawMessage(`{"handle":"`+token+`"}`)); errorCode(err) != "mcp_configuration_changed" {
				t.Fatalf("stale authority: %v", err)
			}
			if calls != 0 {
				t.Fatal("invoked CLI using stale configuration")
			}
		})
	}
}
func TestMCPSecurityCaptureAndResultBounds(t *testing.T) {
	t.Run("exact_limit", func(t *testing.T) {
		payload := `{"cursor":"` + strings.Repeat("c", mcpCLIMaxBytes-len(`{"cursor":""}`)) + `"}`
		value, err := mcpCapture(func(w io.Writer) error { _, err := io.WriteString(w, payload); return err })
		if err != nil || len(value["cursor"].(string)) != mcpCLIMaxBytes-len(`{"cursor":""}`) {
			t.Fatalf("exact limit lost cursor: %v", err)
		}
	})
	t.Run("overflow_after_valid_json", func(t *testing.T) {
		_, err := mcpCapture(func(w io.Writer) error {
			_, _ = io.WriteString(w, `{"next_cursor":"original","collection_proof_sha256":"proof"}`)
			_, _ = w.Write([]byte(strings.Repeat("x", mcpCLIMaxBytes)))
			return nil
		})
		if err == nil {
			t.Fatal("ignored writer overflow silently returned a partial successful collection")
		}
	})
	t.Run("string_writer_cannot_bypass_limit", func(t *testing.T) {
		_, err := mcpCapture(func(w io.Writer) error {
			_, err := io.WriteString(w, `{"next_cursor":"`+strings.Repeat("c", mcpCLIMaxBytes)+`"}`)
			return err
		})
		if err == nil {
			t.Fatal("StringWriter bypassed capture limit")
		}
	})
	t.Run("malformed_trailing_output", func(t *testing.T) {
		for _, raw := range []string{`null`, `[]`, `{} {}`, `{"next_cursor":"cut`} {
			if _, err := mcpCapture(func(w io.Writer) error { _, err := io.WriteString(w, raw); return err }); err == nil {
				t.Fatalf("invalid CLI result accepted: %q", raw)
			}
		}
	})
	t.Run("proof_cursor_must_not_be_silently_truncated", func(t *testing.T) {
		result := mcpToolResult(map[string]any{"handle": "opaque", "next_cursor": "cursor", "collection_proof_sha256": "proof", "events": strings.Repeat("e", mcpResultMaxBytes)}, nil)
		raw, err := json.Marshal(result)
		if err != nil || len(raw) > mcpResultMaxBytes || !result.IsError {
			t.Fatalf("unbounded or successful truncated result: %d %v", len(raw), err)
		}
		var bounded map[string]any
		raw, err = json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &bounded); err != nil {
			t.Fatal(err)
		}
		if bounded["error"] != "mcp_result_too_large" || bounded["handle"] != "opaque" {
			t.Fatalf("lost durable handle/error: %+v", bounded)
		}
		if _, ok := bounded["next_cursor"]; ok {
			t.Fatal("partial cursor advertised")
		}
		if _, ok := bounded["collection_proof_sha256"]; ok {
			t.Fatal("partial proof advertised")
		}
	})
}
func TestMCPSecurityDispatchPreservesExplicitProfileAndDurableFailure(t *testing.T) {
	for _, effort := range []string{"", "low"} {
		t.Run("reasoning_"+effort, func(t *testing.T) {
			f := securityMCP(t)
			c := f.config
			c.Providers = map[string]string{"codex": filepath.Join(c.Workspace, "fixture-lock.json")}
			securityWriteJSON(t, f.path, c)
			var err error
			f, err = newMCPFacade(f.path)
			if err != nil {
				t.Fatal(err)
			}
			path := securityHandle(t, f)
			calls := 0
			f.invoke = func(_ context.Context, args []string, out io.Writer) error {
				calls++
				if len(args) != 5 || args[0] != "dispatch" || args[1] != "--state-dir" || args[2] != f.config.StateDir || args[3] != "--request" {
					t.Fatalf("unexpected invocation: %v", args)
				}
				var req dispatchRequest
				if err := mcpReadPrivate(args[4], mcpConfigMaxBytes, &req); err != nil {
					t.Fatal(err)
				}
				if req.Reasoning != adapter.ReasoningEffort(effort) || req.Model != "cli-default" || req.ProviderLock != c.Providers["codex"] || req.Directory != c.Workspace || req.RequestID != "retry-one" {
					t.Fatalf("profile/request altered: %+v", req)
				}
				_ = json.NewEncoder(out).Encode(map[string]any{"version": 1, "status": "submitted", "handle": path})
				return codeError("host_unavailable")
			}
			raw, _ := json.Marshal(mcpDispatchArgs{RequestID: "retry-one", Provider: "codex", Role: adapter.Reviewer, Prompt: "review fixture", Acceptance: []string{"read only"}, Model: "cli-default", Reasoning: adapter.ReasoningEffort(effort), Budget: mcpBudget{PerSegmentMS: 60000, TotalMS: 90000, MaxAttempts: 1}})
			value, err := f.dispatchMapped(context.Background(), raw)
			if errorCode(err) != "host_unavailable" || calls != 1 {
				t.Fatalf("durable failure retried/lost: calls=%d err=%v", calls, err)
			}
			opaque, ok := value["handle"].(string)
			if !ok || opaque == path {
				t.Fatalf("durable route lost or exposed: %+v", value)
			}
			if value["cli_handle"] != path {
				t.Fatal("wrapper fallback lost original routing handle")
			}
			var route mcpRoute
			if err = f.unseal(opaque, "route", &route); err != nil || route.Handle != path || route.Owner != "owner-one" {
				t.Fatalf("wrong opaque route: %+v %v", route, err)
			}
			// cli_handle is routing metadata, never a control token or key.
			delete(value, "cli_handle")
			encoded, _ := json.Marshal(mcpToolResult(value, err))
			if bytes.Contains(encoded, []byte(path)) {
				t.Fatal("routing path exposed outside explicit fallback field")
			}
		})
	}
}

func TestMCPSecurityGrokRetainsExplicitSessionWriteGate(t *testing.T) {
	for _, tc := range []struct {
		name, provider, want string
		authorized           bool
	}{
		{"missing", "grok", "grok_session_write_required", false},
		{"authorized", "grok", "", true},
		{"wrong_provider", "codex", "profile_provider_state_mismatch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := securityMCP(t)
			c := f.config
			c.Providers = map[string]string{tc.provider: filepath.Join(c.Workspace, "lock.json")}
			securityWriteJSON(t, f.path, c)
			var err error
			f, err = newMCPFacade(f.path)
			if err != nil {
				t.Fatal(err)
			}
			handle := securityHandle(t, f)
			calls := 0
			f.invoke = func(ctx context.Context, args []string, out io.Writer) error {
				calls++
				var request dispatchRequest
				if err := mcpReadPrivate(args[4], mcpConfigMaxBytes, &request); err != nil {
					t.Fatal(err)
				}
				if !request.GrokSessionWrite {
					t.Fatal("explicit session-write grant was dropped")
				}
				plan, err := dispatchPlan(request, adapter.ProviderLock{}, "run-one", "")
				if err != nil {
					t.Fatal(err)
				}
				var p adapter.InvocationPayload
				if err = json.Unmarshal(plan.Tasks[0].AdapterPayload, &p); err != nil || !p.Profile.GrokSessionWrite || p.Profile.Permission != adapter.ReadOnly {
					t.Fatalf("session grant changed original profile: %+v %v", p, err)
				}
				return json.NewEncoder(out).Encode(map[string]any{"version": 1, "status": "submitted", "handle": handle})
			}
			raw := map[string]any{"request_id": "grok-review", "provider": tc.provider, "role": "reviewer", "prompt": "read only review", "acceptance": []string{"review"}, "model": "cli-default", "grok_session_write": tc.authorized, "budget": map[string]any{"per_segment_ms": 60000, "total_ms": 90000, "max_attempts": 1}}
			_, err = f.dispatchMapped(context.Background(), mcpTestJSON(t, raw))
			if (tc.want == "" && err != nil) || (tc.want != "" && errorCode(err) != tc.want) {
				t.Fatalf("want=%s actual=%v", tc.want, err)
			}
			if (tc.want == "" && calls != 1) || (tc.want != "" && calls != 0) {
				t.Fatalf("admission invoked provider incorrectly: calls=%d", calls)
			}
		})
	}
}
