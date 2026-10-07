//go:build darwin

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// These are explicitly source-injected boundary failures, not physical OS
// failures. All actors, IPC calls, original waits and database writes are real.
// No product environment switch or public fault-injection API is introduced.
func buildACPFaultBinary(t *testing.T, root, bin, fault string) {
	t.Helper()
	original, err := os.ReadFile("controller_acp.go")
	if err != nil {
		t.Fatal(err)
	}
	replace := func(needle, replacement string) {
		t.Helper()
		if bytes.Count(original, []byte(needle)) != 1 {
			t.Fatalf("owned fault boundary drifted: %s", fault)
		}
		original = bytes.Replace(original, []byte(needle), []byte(replacement), 1)
	}
	// Capture only the owned actor's non-secret offline trace before its private
	// directory is removed. Normal public acceptance uses an unmodified binary.
	boundary := "defer func() { retErr = s.finish(retErr, os.Stdout) }()"
	snapshot := fmt.Sprintf(`
	defer func() {
		entries, e := os.ReadDir(state + "/grok-origin"); if e != nil { return }
		for _, entry := range entries {
			raw, e := os.ReadFile(state + "/grok-origin/" + entry.Name() + "/home/acp-trace.jsonl")
			if e == nil { _ = os.WriteFile(%q, raw, 0600) }
		}
	}()`, filepath.Join(root, "owned-trace.jsonl"))
	replace(boundary, boundary+snapshot)
	fail := fmt.Sprintf("return codeError(%q)\n\t\t", "acp_fixture_"+fault)
	switch fault {
	case "before_claim", "cleanup_pending":
		replace("return s.deliver(client)", fmt.Sprintf("return codeError(%q)", "acp_fixture_"+fault))
		if fault == "cleanup_pending" {
			needle := "cleanupErr = s.prepared.Close(context.Background())"
			if bytes.Count(original, []byte(needle)) != 3 {
				t.Fatal("original cleanup stages changed")
			}
			original = bytes.Replace(original, []byte(needle), []byte("cleanupErr = process.ErrProcessTreeUnknown // owned source-injected proof unavailability"), 2)
		}
	case "after_claim":
		needle := "begin, err := controllerACPBeginForClaim"
		replace(needle, fail+needle)
	case "after_begin":
		needle := "op, done := s.operation()\n\t\tturn, err := client.Prompt"
		replace(needle, fail+needle)
	case "after_completion":
		needle := "var receipt contract.BridgeReceiveReceipt"
		replace(needle, fail+needle)
	case "receive_ack_loss":
		needle := "err = s.rpc(ipc.KindACPBridgeReceived, received, &receipt)"
		trace := fmt.Sprintf(`wire, _ := json.Marshal(received)
			f, e := os.OpenFile(%q, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); if e != nil { return e }
			_, e = f.Write(append(wire, '\n')); if e == nil { e = f.Sync() }; closeErr := f.Close(); if e != nil { return e }; if closeErr != nil { return closeErr }
			`, filepath.Join(root, "receive-calls.jsonl"))
		replace(needle, trace+needle+"\n\t\t\tif err == nil && attempt == 0 { err = io.ErrUnexpectedEOF } // real commit, discarded ACK")
		needle = "if ordinal >= math.MaxInt64-3 {"
		replace(needle, "return codeError(\"acp_fixture_receive_ack_loss\")\n\t\t"+needle)
	case "trace_only":
	case "sparse_activate":
		needle := "return decodeControllerACPResponse(reply.Payload, response)"
		replace(needle, "if kind == ipc.KindACPBridgeActivate { reply.Payload = []byte(`{\"status\":\"ACTIVE\",\"native_session_id\":\"owned-public-session\"}`) } // discard required identity fields from a real reply\n\t"+needle)
		replace("return s.deliver(client)", "return codeError(\"acp_fixture_unexpected_delivery\")")
	default:
		t.Fatal("unapproved owned fault variant")
	}
	replaced := filepath.Join(root, "controller-acp-overlay.go")
	if err = os.WriteFile(replaced, original, 0600); err != nil {
		t.Fatal(err)
	}
	originalPath, err := filepath.Abs("controller_acp.go")
	if err != nil {
		t.Fatal(err)
	}
	overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{originalPath: replaced}})
	overlayPath := filepath.Join(root, "overlay.json")
	if err = os.WriteFile(overlayPath, overlay, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-overlay", overlayPath, "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("owned overlay build: %s %v", out, err)
	}
}

func TestControllerACPPublicRejectsSparseActivateReceipt(t *testing.T) {
	f := startACPPublicFixture(t, "", "sparse_activate")
	f.await(t, "controller_acp_receipt_invalid", 8*time.Second)
	m := acpFaultMetadata(t, f)
	if m.Status != "failed" || m.NativeSessionID != "" {
		t.Fatal("sparse activation persisted an accepted session")
	}
	if ids := acpFaultTrace(t, f); len(ids) != 3 {
		t.Fatalf("delivery started from sparse activation: %v", ids)
	}
	for _, raw := range f.lines {
		var status map[string]json.RawMessage
		if json.Unmarshal(raw, &status) != nil || b05String(status["status"]) == "controller_acp_active" {
			t.Fatal("sparse activation announced active")
		}
	}
}

func TestControllerACPPublicReceiveACKLossOnlyReplaysOriginalReceipt(t *testing.T) {
	f := startACPPublicFixture(t, "", "receive_ack_loss")
	if status := f.read(t); b05String(status["status"]) != "controller_acp_active" {
		t.Fatal("missing active status")
	}
	f.await(t, "acp_fixture_receive_ack_loss", 8*time.Second)
	m := acpFaultMetadata(t, f)
	ids := acpFaultTrace(t, f)
	if m.Status != "failed" || len(ids) != 4 {
		t.Fatalf("ACK loss caused extra Prompt: status=%s ids=%v", m.Status, ids)
	}
	raw, err := os.ReadFile(filepath.Join(f.root, "receive-calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	calls := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	if len(calls) != 2 || !bytes.Equal(calls[0], calls[1]) {
		t.Fatal("Receive replay changed original request bytes")
	}
	req, err := contract.DecodeACPReceiveRequest(calls[0])
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(f.state, acpCompletionDirectory, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatal("one original completion required")
	}
	local, err := os.ReadFile(files[0])
	if err != nil || !bytes.Equal(bytes.TrimSpace(local), calls[0]) {
		t.Fatal("replay did not use durable original completion")
	}
	db, err := store.Open(filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attempts, received, decisions int
	if err = db.SQL().QueryRow(`SELECT COUNT(*),COALESCE(SUM(state='RECEIVED'),0) FROM bridge_delivery_attempts`).Scan(&attempts, &received); err != nil {
		t.Fatal(err)
	}
	if err = db.SQL().QueryRow(`SELECT COUNT(*) FROM runtime_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || received != 1 || decisions != 0 || req.RPCRequestID != 4 || req.TurnOrdinal != 1 {
		t.Fatal("ACK replay changed delivery/business lifecycle")
	}
	t.Logf("PUBLIC_RECEIVE_ACK_LOSS native_pid=%d original_Wait=%d rpcs=%v exact_receive_calls=2 attempts=%d received=%d decisions=%d", m.NativePID, m.NativeExitCode, ids, attempts, received, decisions)
}

func acpFaultTrace(t *testing.T, f *acpPublicFixture) []uint64 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, "owned-trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint64
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		var req struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(line, &req) != nil {
			t.Fatal("invalid owned trace")
		}
		if req.ID != uint64(len(ids)+1) {
			t.Fatalf("original RPC sequence changed: %s", raw)
		}
		if (req.ID == 1 && req.Method != "initialize") || (req.ID == 2 && req.Method != "session/new") || (req.ID >= 3 && req.Method != "session/prompt") {
			t.Fatal("session restart or unexpected RPC")
		}
		ids = append(ids, req.ID)
	}
	return ids
}

func acpFaultMetadata(t *testing.T, f *acpPublicFixture) controllerACPMetadata {
	t.Helper()
	raw, err := readBridgeRequestFile(f.handle + ".acp-origin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m controllerACPMetadata
	if decode(raw, &m) != nil || !m.NativeExitKnown || !m.NativeTreeExited || process.ConfirmGroupExited(m.NativePID) != nil {
		t.Fatalf("original native terminal proof absent: %s", raw)
	}
	return m
}

func TestControllerACPPublicUncertainWindowsNeverResend(t *testing.T) {
	for _, tc := range []struct {
		name, mode, fault, attempt, terminal string
		rpc, records                         int
	}{
		{"before_claim", "", "before_claim", "", "failed", 3, 0},
		{"after_claim", "", "after_claim", "CLAIMED", "failed", 3, 0},
		{"after_begin", "", "after_begin", "EMITTING", "uncertain", 3, 0},
		{"full_write_no_response", "lost_response", "trace_only", "EMITTING", "uncertain", 4, 0},
		{"partial_response", "partial_response", "trace_only", "EMITTING", "uncertain", 4, 0},
		{"after_completion", "", "after_completion", "EMITTING", "uncertain", 4, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startACPPublicFixture(t, tc.mode, tc.fault)
			if status := f.read(t); b05String(status["status"]) != "controller_acp_active" {
				t.Fatal("original session never activated")
			}
			want := "acp_fixture_" + tc.fault
			if tc.fault == "trace_only" {
				want = "controller_acp_"
			}
			f.await(t, want, 10*time.Second)
			m := acpFaultMetadata(t, f)
			if m.Status != tc.terminal {
				t.Fatalf("terminal=%s want=%s", m.Status, tc.terminal)
			}
			ids := acpFaultTrace(t, f)
			if len(ids) != tc.rpc {
				t.Fatalf("RPC sequence indicates resend: %v want_count=%d", ids, tc.rpc)
			}
			db, err := store.Open(filepath.Join(f.state, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var attempts, received, decisions int
			if err = db.SQL().QueryRow(`SELECT COUNT(*),COALESCE(SUM(state='RECEIVED'),0) FROM bridge_delivery_attempts`).Scan(&attempts, &received); err != nil {
				t.Fatal(err)
			}
			wantAttempts := 1
			if tc.attempt == "" {
				wantAttempts = 0
			}
			if attempts != wantAttempts || received != 0 {
				t.Fatalf("attempts=%d received=%d", attempts, received)
			}
			if attempts == 1 {
				var state string
				var ordinal *int64
				if err = db.SQL().QueryRow(`SELECT state,acp_turn_ordinal FROM bridge_delivery_attempts`).Scan(&state, &ordinal); err != nil {
					t.Fatal(err)
				}
				if state != tc.attempt && !(tc.attempt == "EMITTING" && state == "UNCERTAIN") {
					t.Fatalf("unexpected attempt state %s", state)
				}
				if tc.attempt == "CLAIMED" && ordinal != nil {
					t.Fatal("claim reserved a prompt ordinal before Begin")
				}
			}
			if err = db.SQL().QueryRow(`SELECT COUNT(*) FROM runtime_decisions`).Scan(&decisions); err != nil || decisions != 0 {
				t.Fatal("protocol failure created business decision", err)
			}
			files, err := filepath.Glob(filepath.Join(f.state, acpCompletionDirectory, "*.json"))
			if err != nil || len(files) != tc.records {
				t.Fatalf("local records=%d want=%d", len(files), tc.records)
			}
			for _, path := range files {
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				req, e := contract.DecodeACPReceiveRequest(raw)
				if e != nil || req.RPCRequestID != 4 || req.TurnOrdinal != 1 {
					t.Fatal("wrong original local completion", e)
				}
			}
			t.Logf("PUBLIC_FAULT boundary=%s native_pid=%d original_Wait=%d tree_exited=true rpcs=%v attempts=%d received=%d records=%d decisions=%d", tc.name, m.NativePID, m.NativeExitCode, ids, attempts, received, len(files), decisions)
		})
	}
}

func TestControllerACPPublicUnknownCleanupRetainsOriginalOwner(t *testing.T) {
	f := startACPPublicFixture(t, "timed_exit", "cleanup_pending")
	if status := f.read(t); b05String(status["status"]) != "controller_acp_active" {
		t.Fatal("missing active status")
	}
	pending := f.read(t)
	if b05String(pending["status"]) != "cleanup_pending" || string(pending["metadata_persisted"]) != "true" {
		t.Fatalf("missing honest pending status: %s", pending)
	}
	var native int
	_ = json.Unmarshal(pending["native_pid"], &native)
	if birth, err := process.Birth(f.cmd.Process.Pid); err != nil || birth != f.birth {
		t.Fatal("owner exited before original native proof")
	}
	if _, err := process.Birth(native); err != nil {
		t.Fatal("fault did not hold a live original native process")
	}
	f.await(t, "acp_fixture_cleanup_pending", 8*time.Second)
	m := acpFaultMetadata(t, f)
	if m.NativePID != native || m.NativeExitCode != 0 || m.Status != "failed" {
		t.Fatalf("wrong terminal original proof: %+v", m)
	}
	ids := acpFaultTrace(t, f)
	if len(ids) != 3 {
		t.Fatalf("cleanup pending performed extra RPC: %v", ids)
	}
	t.Logf("PUBLIC_CLEANUP_PENDING owner_pid=%d native_pid=%d retained_until_original_Wait=0 tree_exited=true rpcs=%v", f.cmd.Process.Pid, native, ids)
}
