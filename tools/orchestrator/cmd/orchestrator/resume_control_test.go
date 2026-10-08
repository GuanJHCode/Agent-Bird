package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/coordinator"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type resumeOutputWriterFunc func([]byte) (int, error)

func (write resumeOutputWriterFunc) Write(body []byte) (int, error) { return write(body) }

func TestResumeCLIOutputFailureKeepsPending(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		write func([]byte) (int, error)
		want  error
	}{
		{"epipe", func([]byte) (int, error) { return 0, syscall.EPIPE }, syscall.EPIPE},
		{"enospc", func([]byte) (int, error) { return 0, syscall.ENOSPC }, syscall.ENOSPC},
		{"short_zero_nil", func([]byte) (int, error) { return 0, nil }, io.ErrShortWrite},
		{"short_partial_nil", func(body []byte) (int, error) { return len(body) - 1, nil }, io.ErrShortWrite},
		{"full_with_error", func(body []byte) (int, error) { return len(body), syscall.EPIPE }, syscall.EPIPE},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			state, path, cap := resumeCLIFixture(t)
			var first string
			var raw []byte
			calls := 0
			deps := resumeControlDependencies{requireCoordinator: func(context.Context, string) error { return nil }, call: func(_ context.Context, _ string, kind ipc.Kind, value any) (ipc.Envelope, error) {
				if kind != ipc.KindResume {
					t.Fatal("unexpected mutable gate")
				}
				req := value.(coordinator.ResumeRequest)
				calls++
				if first == "" {
					first = req.CommandID
				} else if req.CommandID != first {
					t.Fatalf("output retry changed command: %q -> %q", first, req.CommandID)
				}
				raw = resumeCLIReceipt(t, req, cap, "existing_owner")
				return ipc.Envelope{Payload: raw}, nil
			}}
			err := resumeControl(context.Background(), state, path, "task", 2, "", false, resumeOutputWriterFunc(scenario.write), deps)
			if !errors.Is(err, scenario.want) {
				t.Fatalf("err=%v want=%v", err, scenario.want)
			}
			body, err := os.ReadFile(filepath.Join(state, "resume-requests", resumeJournalName(first)))
			if err != nil {
				t.Fatal(err)
			}
			var pending resumeJournalRequest
			if json.Unmarshal(body, &pending) != nil || pending.State != "pending" {
				t.Fatalf("output failure completed journal: %s", body)
			}
			selected, err := selectResumeJournalRequest(state, path, "task", 2, "")
			if err != nil || selected.CommandID != first {
				t.Fatalf("implicit pending=%+v err=%v", selected, err)
			}
			var out bytes.Buffer
			if err = resumeControl(context.Background(), state, path, "task", 2, "", false, &out, deps); err != nil {
				t.Fatal(err)
			}
			if calls != 2 || !bytes.Equal(out.Bytes(), append(append([]byte(nil), raw...), '\n')) {
				t.Fatalf("calls=%d output=%s", calls, out.String())
			}
			completed, err := selectResumeJournalRequest(state, path, "task", 2, first)
			if err != nil || completed.State != "completed" {
				t.Fatalf("completed=%+v err=%v", completed, err)
			}
		})
	}
}

func TestResumeCLIOutputCompleteFailureReturnsError(t *testing.T) {
	state, path, cap := resumeCLIFixture(t)
	var first string
	var raw []byte
	deps := resumeControlDependencies{requireCoordinator: func(context.Context, string) error { return nil }, call: func(_ context.Context, _ string, kind ipc.Kind, value any) (ipc.Envelope, error) {
		if kind != ipc.KindResume {
			t.Fatal("unexpected mutable gate")
		}
		req := value.(coordinator.ResumeRequest)
		if first == "" {
			first = req.CommandID
		} else if req.CommandID != first {
			t.Fatalf("completion retry changed command: %q -> %q", first, req.CommandID)
		}
		raw = resumeCLIReceipt(t, req, cap, "existing_owner")
		return ipc.Envelope{Payload: raw}, nil
	}}
	var out bytes.Buffer
	unknown := filepath.Join(state, "resume-requests", "owned-output-fault")
	writer := resumeOutputWriterFunc(func(body []byte) (int, error) {
		n, err := out.Write(body)
		if err != nil {
			return n, err
		}
		// The real complete scan must fail closed after a successful output.
		if err = os.WriteFile(unknown, []byte("owned test fault"), 0600); err != nil {
			t.Fatal(err)
		}
		return n, nil
	})
	err := resumeControl(context.Background(), state, path, "task", 2, "", false, writer, deps)
	if err == nil || !bytes.Equal(out.Bytes(), append(append([]byte(nil), raw...), '\n')) {
		t.Fatalf("err=%v output=%s", err, out.String())
	}
	body, err := os.ReadFile(filepath.Join(state, "resume-requests", resumeJournalName(first)))
	if err != nil {
		t.Fatal(err)
	}
	var pending resumeJournalRequest
	if json.Unmarshal(body, &pending) != nil || pending.State != "pending" {
		t.Fatalf("failed complete changed journal: %s", body)
	}
	if err = os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = resumeControl(context.Background(), state, path, "task", 2, "", false, &out, deps); err != nil {
		t.Fatal(err)
	}
	completed, err := selectResumeJournalRequest(state, path, "task", 2, first)
	if err != nil || completed.State != "completed" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
}

func resumeCLIFixture(t *testing.T) (string, string, controlCapability) {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	cap := controlCapability{Version: 2, RunID: "run", ControllerThread: "controller", ControlToken: "private-token", BootstrapPath: filepath.Join(state, "missing-bootstrap.json"), HostLaunchID: "launch", HostLaunchToken: "launch-token"}
	path := filepath.Join(state, "control.json")
	body, _ := json.Marshal(cap)
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return state, path, cap
}

func resumeCLIReceipt(t *testing.T, req coordinator.ResumeRequest, cap controlCapability, mode string) []byte {
	t.Helper()
	hash, err := store.OriginalOwnerResumeRequestSHA256(store.OriginalOwnerResumeRequest{TaskID: req.TaskID, ControllerThread: req.ControllerThread, ControlToken: req.ControlToken, WorkRevision: req.WorkRevision, CommandID: req.CommandID})
	if err != nil {
		t.Fatal(err)
	}
	r := coordinator.ResumeResponse{Version: 1, Status: "resume_queued", OriginalOwnerResumeReceipt: store.OriginalOwnerResumeReceipt{Mode: mode, CommandID: req.CommandID, RequestSHA256: hash, RunID: cap.RunID, TaskID: req.TaskID, LaunchID: "launch", OwnerID: "owner", WorkRevision: req.WorkRevision, ControlRevision: 2, CoordinatorEpoch: 3, ReadySequence: 4, CreatedAt: "2026-10-08T03:04:05.123Z"}}
	if mode == "successor" || mode == "initial_ready" {
		r.AuthorizationID = "authorization"
	}
	if mode == "initial_ready" {
		r.Status = "initial_ready"
	}
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestResumeCLIReceiptStrict(t *testing.T) {
	_, _, cap := resumeCLIFixture(t)
	req := coordinator.ResumeRequest{TaskControlRequest: coordinator.TaskControlRequest{TaskID: "task", ControllerThread: cap.ControllerThread, ControlToken: cap.ControlToken, WorkRevision: 2}, CommandID: "command"}
	for _, mode := range []string{"existing_owner", "successor", "initial_ready"} {
		t.Run(mode, func(t *testing.T) {
			if got, err := validateResumeReceipt(resumeCLIReceipt(t, req, cap, mode), req, cap); err != nil || got.Mode != mode {
				t.Fatalf("receipt=%+v err=%v", got, err)
			}
		})
	}
	valid := resumeCLIReceipt(t, req, cap, "existing_owner")
	mutations := map[string]func(map[string]json.RawMessage){
		"version":             func(v map[string]json.RawMessage) { v["version"] = []byte(`2`) },
		"status":              func(v map[string]json.RawMessage) { v["status"] = []byte(`"queued"`) },
		"mode":                func(v map[string]json.RawMessage) { v["mode"] = []byte(`"legacy"`) },
		"command":             func(v map[string]json.RawMessage) { v["command_id"] = []byte(`"other"`) },
		"task":                func(v map[string]json.RawMessage) { v["task_id"] = []byte(`"other"`) },
		"run":                 func(v map[string]json.RawMessage) { v["run_id"] = []byte(`"other"`) },
		"launch":              func(v map[string]json.RawMessage) { v["launch_id"] = []byte(`"other"`) },
		"revision":            func(v map[string]json.RawMessage) { v["work_revision"] = []byte(`3`) },
		"hash":                func(v map[string]json.RawMessage) { v["request_sha256"] = []byte(`"other"`) },
		"owner_empty":         func(v map[string]json.RawMessage) { v["owner_id"] = []byte(`""`) },
		"control_zero":        func(v map[string]json.RawMessage) { v["control_revision"] = []byte(`0`) },
		"epoch_zero":          func(v map[string]json.RawMessage) { v["coordinator_epoch"] = []byte(`0`) },
		"sequence_zero":       func(v map[string]json.RawMessage) { v["ready_sequence"] = []byte(`0`) },
		"sequence_negative":   func(v map[string]json.RawMessage) { v["ready_sequence"] = []byte(`-1`) },
		"time_offset":         func(v map[string]json.RawMessage) { v["created_at"] = []byte(`"2026-10-08T03:04:05.123+00:00"`) },
		"time_noncanonical":   func(v map[string]json.RawMessage) { v["created_at"] = []byte(`"2026-10-08T03:04:05.1230Z"`) },
		"existing_auth":       func(v map[string]json.RawMessage) { v["authorization_id"] = []byte(`"authorization"`) },
		"existing_empty_auth": func(v map[string]json.RawMessage) { v["authorization_id"] = []byte(`""`) },
		"successor_no_auth":   func(v map[string]json.RawMessage) { v["mode"] = []byte(`"successor"`) },
		"successor_empty_auth": func(v map[string]json.RawMessage) {
			v["mode"] = []byte(`"successor"`)
			v["authorization_id"] = []byte(`""`)
		},
		"successor_null_auth": func(v map[string]json.RawMessage) {
			v["mode"] = []byte(`"successor"`)
			v["authorization_id"] = []byte(`null`)
		},
		"time_zero": func(v map[string]json.RawMessage) { v["created_at"] = []byte(`"0001-01-01T00:00:00Z"`) },
		"unknown":   func(v map[string]json.RawMessage) { v["unexpected"] = []byte(`1`) },
	}
	var original map[string]json.RawMessage
	_ = json.Unmarshal(valid, &original)
	for key := range original {
		key := key
		mutations["missing_"+key] = func(v map[string]json.RawMessage) { delete(v, key) }
		mutations["null_"+key] = func(v map[string]json.RawMessage) { v[key] = []byte(`null`) }
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(valid, &fields)
			mutate(fields)
			raw, _ := json.Marshal(fields)
			if _, err := validateResumeReceipt(raw, req, cap); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		})
	}
	for name, raw := range map[string][]byte{
		"duplicate":  append([]byte(`{"status":"resume_queued",`), valid[1:]...),
		"case_alias": bytes.Replace(valid, []byte(`"status"`), []byte(`"Status"`), 1),
		"trailing":   append(append([]byte(nil), valid...), []byte(` {}`)...),
		"oversize":   append(append([]byte(nil), valid...), bytes.Repeat([]byte(" "), 64<<10)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateResumeReceipt(raw, req, cap); err == nil {
				t.Fatal("accepted malformed response")
			}
		})
	}
	t.Run("cap_run_empty", func(t *testing.T) {
		empty := cap
		empty.RunID = ""
		if _, err := validateResumeReceipt(valid, req, empty); err == nil {
			t.Fatal("unscoped run")
		}
	})
	t.Run("v1_available_scope", func(t *testing.T) {
		v1 := cap
		v1.Version = 1
		v1.HostLaunchID = ""
		v1.HostLaunchToken = ""
		if _, err := validateResumeReceipt(valid, req, v1); err != nil {
			t.Fatal(err)
		}
	})
}

func TestResumeCLILocalAdmission(t *testing.T) {
	for _, scenario := range []string{"implicit_completed_new_id", "explicit_new_with_pending", "ambiguity_before_rpc", "conflict_before_rpc", "corrupt_before_rpc"} {
		t.Run(scenario, func(t *testing.T) {
			state, path, cap := resumeCLIFixture(t)
			first, err := selectResumeJournalRequest(state, path, "task", 2, "old")
			if err != nil {
				t.Fatal(err)
			}
			command, explicit := "", false
			switch scenario {
			case "implicit_completed_new_id":
				if err = completeResumeJournalRequest(state, first); err != nil {
					t.Fatal(err)
				}
			case "explicit_new_with_pending":
				command, explicit = "new / command", true
			case "ambiguity_before_rpc":
				if _, err = selectResumeJournalRequest(state, path, "task", 2, "second"); err != nil {
					t.Fatal(err)
				}
			case "conflict_before_rpc":
				command, explicit = first.CommandID, true
				path = filepath.Join(state, "other-cap.json")
				body, _ := json.Marshal(cap)
				if err = os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			case "corrupt_before_rpc":
				if err = os.WriteFile(filepath.Join(state, "resume-requests", resumeJournalName(first.CommandID)), []byte(`{`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			var selected string
			deps := resumeControlDependencies{requireCoordinator: func(context.Context, string) error { return nil }, call: func(_ context.Context, _ string, kind ipc.Kind, value any) (ipc.Envelope, error) {
				calls++
				if kind != ipc.KindResume {
					t.Fatal("mutable query")
				}
				req := value.(coordinator.ResumeRequest)
				selected = req.CommandID
				return ipc.Envelope{Payload: resumeCLIReceipt(t, req, cap, "existing_owner")}, nil
			}}
			err = resumeControl(context.Background(), state, path, "task", 2, command, explicit, io.Discard, deps)
			allowed := scenario == "implicit_completed_new_id" || scenario == "explicit_new_with_pending"
			if allowed {
				if err != nil || calls != 1 || selected == first.CommandID {
					t.Fatalf("err=%v calls=%d selected=%s", err, calls, selected)
				}
			} else if err == nil || calls != 0 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if scenario == "ambiguity_before_rpc" {
				var ambiguous *resumeJournalAmbiguityError
				if !errors.As(err, &ambiguous) || len(ambiguous.CommandIDs) != 2 {
					t.Fatalf("ambiguity lost: %v", err)
				}
			}
		})
	}
	for _, scenario := range []string{"missing", "symlink", "bad_version", "empty_controller", "empty_token", "relative_bootstrap", "missing_launch"} {
		t.Run("cap_"+scenario, func(t *testing.T) {
			state, path, cap := resumeCLIFixture(t)
			switch scenario {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				original := path
				path = filepath.Join(state, "cap-link.json")
				if err := os.Symlink(original, path); err != nil {
					t.Fatal(err)
				}
			default:
				switch scenario {
				case "bad_version":
					cap.Version = 9
				case "empty_controller":
					cap.ControllerThread = ""
				case "empty_token":
					cap.ControlToken = ""
				case "relative_bootstrap":
					cap.BootstrapPath = "relative"
				case "missing_launch":
					cap.HostLaunchID = ""
				}
				body, _ := json.Marshal(cap)
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			err := resumeControl(context.Background(), state, path, "task", 2, "", false, io.Discard, resumeControlDependencies{requireCoordinator: func(context.Context, string) error { called = true; return nil }})
			if err == nil || called {
				t.Fatalf("err=%v called=%t", err, called)
			}
			if _, err := os.Lstat(filepath.Join(state, "resume-requests")); !os.IsNotExist(err) {
				t.Fatalf("journal touched: %v", err)
			}
		})
	}
}

func TestResumeCLIJournalAndReceiptFlow(t *testing.T) {
	for _, failure := range []string{"ack_loss", "invalid_receipt", "host_error", "initial_host_error"} {
		t.Run(failure, func(t *testing.T) {
			state, path, cap := resumeCLIFixture(t)
			var first string
			calls := 0
			mode := "existing_owner"
			if failure == "host_error" {
				mode = "successor"
			}
			if failure == "initial_host_error" {
				mode = "initial_ready"
			}
			deps := resumeControlDependencies{
				requireCoordinator: func(context.Context, string) error { return nil },
				call: func(_ context.Context, _ string, kind ipc.Kind, value any) (ipc.Envelope, error) {
					if kind != ipc.KindResume {
						t.Fatalf("unexpected %s", kind)
					}
					req := value.(coordinator.ResumeRequest)
					calls++
					if first == "" {
						first = req.CommandID
					} else if req.CommandID != first {
						t.Fatalf("replay changed ID: %q -> %q", first, req.CommandID)
					}
					// Observe the real persisted record before the RPC boundary.
					body, err := os.ReadFile(filepath.Join(state, "resume-requests", resumeJournalName(req.CommandID)))
					if err != nil {
						t.Fatal(err)
					}
					var record resumeJournalRequest
					if json.Unmarshal(body, &record) != nil || record.State != "pending" || strings.Contains(string(body), cap.ControlToken) {
						t.Fatalf("not private pending: %s", body)
					}
					selected, err := selectResumeJournalRequest(state, path, "task", 2, req.CommandID)
					if err != nil || selected.CommandID != req.CommandID {
						t.Fatalf("journal lock held across RPC: selected=%+v err=%v", selected, err)
					}
					if calls == 1 && failure == "ack_loss" {
						return ipc.Envelope{}, io.ErrUnexpectedEOF
					}
					if calls == 1 && failure == "invalid_receipt" {
						return ipc.Envelope{Payload: []byte(`{"status":"resume_queued"}`)}, nil
					}
					return ipc.Envelope{Payload: resumeCLIReceipt(t, req, cap, mode)}, nil
				},
				ensureLegacyHost: func(context.Context, string, controlCapability) error { t.Fatal("legacy host used"); return nil },
				ensureSuccessorHost: func(context.Context, string, controlCapability, coordinator.ResumeResponse) error {
					if failure != "host_error" && failure != "initial_host_error" {
						t.Fatal("existing host inspected")
					}
					if calls == 1 {
						return errors.New("host failed")
					}
					return nil
				},
			}
			var out bytes.Buffer
			if err := resumeControl(context.Background(), state, path, "task", 2, "", false, &out, deps); err == nil || out.Len() != 0 {
				t.Fatalf("first err=%v out=%s", err, out.String())
			}
			record, err := selectResumeJournalRequest(state, path, "task", 2, "")
			if err != nil || record.CommandID != first || record.State != "pending" {
				t.Fatalf("pending=%+v err=%v", record, err)
			}
			if err = resumeControl(context.Background(), state, path, "task", 2, "", false, &out, deps); err != nil {
				t.Fatal(err)
			}
			record, err = selectResumeJournalRequest(state, path, "task", 2, first)
			if err != nil || record.State != "completed" {
				t.Fatalf("complete=%+v err=%v", record, err)
			}
			if calls != 2 || !bytes.Contains(out.Bytes(), []byte(first)) {
				t.Fatalf("calls=%d out=%s", calls, out.String())
			}
		})
	}
	t.Run("capability_before_journal", func(t *testing.T) {
		state, path, _ := resumeCLIFixture(t)
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		called := false
		err := resumeControl(context.Background(), state, path, "task", 2, "", false, io.Discard, resumeControlDependencies{requireCoordinator: func(context.Context, string) error { called = true; return nil }})
		if err == nil || called {
			t.Fatalf("err=%v called=%t", err, called)
		}
		if _, err = os.Lstat(filepath.Join(state, "resume-requests")); !os.IsNotExist(err) {
			t.Fatalf("journal touched: %v", err)
		}
	})
	t.Run("coordinator_before_journal", func(t *testing.T) {
		state, path, _ := resumeCLIFixture(t)
		err := resumeControl(context.Background(), state, path, "task", 2, "", false, io.Discard, resumeControlDependencies{requireCoordinator: func(context.Context, string) error { return errors.New("unsupported") }})
		if err == nil {
			t.Fatal("unsupported accepted")
		}
		if _, err = os.Lstat(filepath.Join(state, "resume-requests")); !os.IsNotExist(err) {
			t.Fatalf("journal touched: %v", err)
		}
	})
	t.Run("existing_raw_explicit_replay_without_bootstrap", func(t *testing.T) {
		state, path, cap := resumeCLIFixture(t)
		calls := 0
		var raw []byte
		deps := resumeControlDependencies{requireCoordinator: func(context.Context, string) error { return nil }, call: func(_ context.Context, _ string, kind ipc.Kind, value any) (ipc.Envelope, error) {
			if kind != ipc.KindResume {
				t.Fatal("mutable gate")
			}
			calls++
			req := value.(coordinator.ResumeRequest)
			raw = append([]byte(" \n"), resumeCLIReceipt(t, req, cap, "existing_owner")...)
			return ipc.Envelope{Payload: raw}, nil
		}}
		for i := 0; i < 2; i++ {
			var out bytes.Buffer
			if err := resumeControl(context.Background(), state, path, "task", 2, "explicit / id", true, &out, deps); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), append(append([]byte(nil), raw...), '\n')) {
				t.Fatal("receipt rewritten")
			}
		}
		if calls != 2 {
			t.Fatalf("calls=%d", calls)
		}
	})
}

func TestResumeCLILegacyFallback(t *testing.T) {
	for _, scenario := range []string{"implicit", "explicit", "other_error", "local_unsupported", "blocked", "invalid_legacy"} {
		t.Run(scenario, func(t *testing.T) {
			state, path, _ := resumeCLIFixture(t)
			var order []string
			var id string
			deps := resumeControlDependencies{requireCoordinator: func(context.Context, string) error { order = append(order, "require"); return nil }, ensureLegacyHost: func(context.Context, string, controlCapability) error { order = append(order, "host"); return nil }, call: func(_ context.Context, _ string, kind ipc.Kind, value any) (ipc.Envelope, error) {
				if kind == ipc.KindSummary {
					order = append(order, "summary")
					var summary store.RunSummary
					if scenario == "blocked" {
						summary.Tasks = append(summary.Tasks, store.SummaryTask{TaskID: "task", ResumeBlockedReason: "blocked"})
					}
					body, _ := json.Marshal(summary)
					return ipc.Envelope{Payload: body}, nil
				}
				req := value.(coordinator.ResumeRequest)
				if req.CommandID != "" {
					id = req.CommandID
					order = append(order, "identified")
					if scenario == "other_error" {
						return ipc.Envelope{}, remoteError("unauthorized")
					}
					if scenario == "local_unsupported" {
						return ipc.Envelope{}, errors.New("resume_command_not_supported")
					}
					return ipc.Envelope{}, remoteError("resume_command_not_supported")
				}
				order = append(order, "legacy")
				if scenario == "invalid_legacy" {
					return ipc.Envelope{Payload: []byte(`{"status":"resume_queued","extra":true}`)}, nil
				}
				return ipc.Envelope{Payload: []byte(`{"status":"resume_queued"}`)}, nil
			}}
			command := ""
			explicit := scenario == "explicit"
			if explicit {
				command = "explicit"
			}
			err := resumeControl(context.Background(), state, path, "task", 2, command, explicit, io.Discard, deps)
			want := "require,identified"
			if scenario == "implicit" || scenario == "invalid_legacy" {
				want += ",summary,host,legacy"
			}
			if scenario == "blocked" {
				want += ",summary"
			}
			if strings.Join(order, ",") != want {
				t.Fatalf("order=%v want=%s", order, want)
			}
			if (scenario == "implicit") != (err == nil) {
				t.Fatalf("err=%v", err)
			}
			r, readErr := selectResumeJournalRequest(state, path, "task", 2, id)
			if readErr != nil {
				t.Fatal(readErr)
			}
			expected := "pending"
			if scenario == "implicit" {
				expected = "completed"
			}
			if r.State != expected {
				t.Fatalf("state=%s", r.State)
			}
		})
	}
}

func TestResumeCLICommandIDFlags(t *testing.T) {
	for _, kind := range []ipc.Kind{ipc.KindResume, ipc.KindStopTask, ipc.KindSummary, ipc.KindCollect} {
		t.Run(string(kind), func(t *testing.T) {
			err := taskControl(context.Background(), kind, []string{"--task-id", "task", "--control-file", "/missing", "--work-revision", "1", "--command-id="}, io.Discard)
			if err != codeError("invalid_args") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestResumeCLIHighLevelNoBootstrap(t *testing.T) {
	state, path, cap := resumeCLIFixture(t)
	handle := filepath.Join(state, "handle.json")
	body, _ := json.Marshal(taskHandle{Version: 1, RunID: cap.RunID, StateDir: state, ControlFile: path, TaskIDs: []string{"task"}})
	if err := os.WriteFile(handle, body, 0600); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "implicit", true: "explicit"}[explicit], func(t *testing.T) {
			args := []string{"--handle", handle, "--task-id", "task", "--work-revision", "2"}
			if explicit {
				args = append(args, "--command-id", "high level / command")
			}
			var out bytes.Buffer
			invoked := false
			err := taskFollowupWithRunner(context.Background(), "resume", args, &out, func(ctx context.Context, forwarded []string, stdout, _ io.Writer) error {
				invoked = true
				if forwarded[0] != "resume" {
					t.Fatalf("args=%v", forwarded)
				}
				command := ""
				for i, arg := range forwarded {
					if arg == "--command-id" {
						command = forwarded[i+1]
					}
				}
				if explicit != (command != "") {
					t.Fatalf("explicit lost: %v", forwarded)
				}
				return taskControlWithResumeDependencies(ctx, ipc.KindResume, forwarded[1:], stdout, resumeControlDependencies{requireCoordinator: func(context.Context, string) error { return nil }, call: func(_ context.Context, rpcState string, kind ipc.Kind, value any) (ipc.Envelope, error) {
					if kind != ipc.KindResume {
						t.Fatal("mutable gate")
					}
					req := value.(coordinator.ResumeRequest)
					if rpcState != state || req.TaskID != "task" || req.WorkRevision != 2 || req.ControllerThread != cap.ControllerThread || req.ControlToken != cap.ControlToken {
						t.Fatalf("forwarded scope lost: state=%s req=%+v", rpcState, req)
					}
					return ipc.Envelope{Payload: resumeCLIReceipt(t, value.(coordinator.ResumeRequest), cap, "existing_owner")}, nil
				}})
			})
			if err != nil || !invoked || !bytes.Contains(out.Bytes(), []byte(`"mode":"existing_owner"`)) {
				t.Fatalf("err=%v invoked=%t out=%s", err, invoked, out.String())
			}
		})
	}
	for name, args := range map[string][]string{"empty": {"--handle", handle, "--work-revision", "2", "--command-id="}, "typed_extra": {"--handle", handle, "--request", "/missing", "--command-id", "extra"}} {
		t.Run(name, func(t *testing.T) {
			if err := taskFollowup(context.Background(), "resume", args, io.Discard); err != codeError("invalid_args") {
				t.Fatalf("err=%v", err)
			}
		})
	}
	decisionPath := filepath.Join(state, "decision.json")
	if err := os.WriteFile(decisionPath, []byte(`{"task_id":"task"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"typed_resume", "collect"} {
		t.Run(action+"_keeps_bootstrap_gate", func(t *testing.T) {
			args := []string{"--handle", handle}
			routed := action
			if action == "typed_resume" {
				routed = "resume"
				args = append(args, "--request", decisionPath)
			}
			err := taskFollowupWithRunner(context.Background(), routed, args, io.Discard, func(context.Context, []string, io.Writer, io.Writer) error {
				t.Fatal("legacy bootstrap gate bypassed")
				return nil
			})
			if err != codeError("untrusted_private_file") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
