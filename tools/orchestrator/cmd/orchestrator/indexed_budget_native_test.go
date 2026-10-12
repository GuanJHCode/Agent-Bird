package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type indexedNativeLedger struct {
	Owner, Policy, Adapter, Inputs, InputHash string
	Used, Granted, Deadline                   int64
}

type indexedNativeStatus struct {
	recoveryStatus
	Process *contract.ProcessIdentity `json:"process,omitempty"`
}

func exerciseIndexedBudgetRetry(t *testing.T, ctx context.Context, bin, state, root, control, launch string, bootstrap hostBootstrap) {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.Join(state, "state.db"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var bindingJSON, mode string
	if err := db.QueryRowContext(ctx, `SELECT l.execution_owner_mode,o.original_binding_json FROM host_launches l JOIN execution_owners o ON o.launch_id=l.id WHERE l.id=? AND o.phase='bound' AND o.is_current=1`, launch).Scan(&mode, &bindingJSON); err != nil || mode != "supervised-v1" {
		t.Fatal("indexed fixture did not bind supervised original owner", mode, err)
	}
	var binding contract.OriginalOwnerBinding
	if err := json.Unmarshal([]byte(bindingJSON), &binding); err != nil {
		t.Fatal(err)
	}
	waitStatus := func(want string) indexedNativeStatus {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); ; {
			var s indexedNativeStatus
			if err := json.Unmarshal(runBinary(t, ctx, bin, "status", "--state-dir", state, "--task-id", "review-1", "--control-file", control), &s); err != nil {
				t.Fatal(err)
			}
			if s.Status == want && (want != "failed" && want != "result_ready" || s.SegmentStatus == "exited") {
				return s
			}
			if time.Now().After(end) {
				t.Fatal("indexed review did not reach expected state", want, s)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	running := waitStatus("running")
	if running.Process == nil || running.HostPID != binding.FirstHost.PID || running.HostBirth != binding.FirstHost.Birth {
		t.Fatal("original indexed running identity missing", running)
	}
	chain, err := process.AncestryContext(ctx, running.Process.PID)
	if err != nil || chain[binding.Owner.PID] != binding.Owner.Birth || chain[running.Process.PID] != running.Process.Birth {
		t.Fatal("indexed worker not owned by original owner", err)
	}
	if err := os.WriteFile(filepath.Join(root, "review-1", "release-first"), []byte("fail after identity observation"), 0600); err != nil {
		t.Fatal(err)
	}
	failed := waitStatus("failed")
	if failed.AttemptID != running.AttemptID || failed.SegmentID != running.SegmentID || failed.HostPID != running.HostPID || failed.HostBirth != running.HostBirth || failed.HostStatus != "ready" {
		t.Fatal("failure lost original indexed Host or attempt", failed)
	}
	readExit := func(s indexedNativeStatus, want int) contract.Event {
		t.Helper()
		spool, err := events.Open(filepath.Join(bootstrap.SpoolRoot, s.AttemptID, s.SegmentID))
		if err != nil {
			t.Fatal(err)
		}
		all, err := spool.Read()
		if err != nil {
			t.Fatal(err)
		}
		var exit contract.Event
		count := 0
		for _, e := range all {
			if e.Kind == contract.EventExited {
				count++
				exit = e
			}
		}
		if count != 1 || exit.ExitCode == nil || *exit.ExitCode != want || exit.Process == nil || !errors.Is(syscall.Kill(exit.Process.PID, 0), syscall.ESRCH) {
			t.Fatal("indexed worker lacks unique original Exited/reap", count, exit)
		}
		return exit
	}
	oldExit := readExit(failed, 7)
	if oldExit.Process.PID != running.Process.PID || oldExit.Process.Birth != running.Process.Birth {
		t.Fatal("original worker exit identity changed")
	}
	readLedger := func(segment string) indexedNativeLedger {
		t.Helper()
		var l indexedNativeLedger
		err := db.QueryRowContext(ctx, `SELECT o.original_binding_json,p.policy_sha256,r.adapter_payload,s.dependency_inputs_json,s.dependency_inputs_sha256,b.used_ms,b.granted_ms,s.deadline_unix_ms FROM execution_owners o JOIN task_runtime r ON r.host_launch_id=o.launch_id JOIN task_execution_policies p ON p.task_id=r.task_id JOIN segment_runtime s ON s.segment_id=? JOIN budget_runtime b ON b.segment_id=s.segment_id WHERE r.task_id='review-1' AND o.is_current=1`, segment).Scan(&l.Owner, &l.Policy, &l.Adapter, &l.Inputs, &l.InputHash, &l.Used, &l.Granted, &l.Deadline)
		if err != nil {
			t.Fatal("indexed ledger", err)
		}
		return l
	}
	before := readLedger(failed.SegmentID)
	if before.Owner != bindingJSON || before.Policy == "" || before.InputHash == "" || before.Used <= 0 || before.Granted != 15000 {
		t.Fatal("missing original budget/vector/policy evidence", before)
	}
	var collected struct {
		DeliveryID string           `json:"delivery_id"`
		Proof      string           `json:"collection_proof_sha256"`
		Events     []contract.Event `json:"events"`
	}
	if err := json.Unmarshal(runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", "review-1", "--control-file", control), &collected); err != nil {
		t.Fatal(err)
	}
	if len(collected.Events) != 1 || collected.Events[0].Kind != contract.EventFailed || collected.Events[0].ActionSlot == "" || collected.Events[0].SegmentID != failed.SegmentID {
		t.Fatal("missing original pending indexed failure", collected)
	}
	failure := collected.Events[0]
	args := []string{"retry", "--state-dir", state, "--task-id", "review-1", "--control-file", control, "--work-revision", "1", "--event-id", failure.EventID, "--event-revision", strconv.FormatInt(failure.EventRevision, 10), "--event-hash", failure.PayloadHash, "--action-slot", failure.ActionSlot, "--segment-id", failure.SegmentID, "--next-attempt", "2", "--command-id", "indexed-budget-retry"}
	deny := exec.CommandContext(ctx, bin, args...)
	deny.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	if out, err := deny.CombinedOutput(); err == nil || !strings.Contains(string(out), "attempt_limit") {
		t.Fatal("old indexed cap did not reject explicit retry", err, string(out))
	}
	request := store.BudgetRevisionRequest{BudgetGroupID: "review-1", CommandID: "indexed-native-increase", GroupMaxAttempts: 2, GroupMaxActiveMS: 60000, Tasks: []store.BudgetRevisionTask{{TaskID: "review-1", WorkRevision: 1, MaxAttempts: 2, AttemptMaxActiveMS: 20000, TaskMaxActiveMS: 45000, SegmentMaxActiveMS: 20000}}}
	path := filepath.Join(root, "indexed-budget.json")
	raw, _ := json.Marshal(request)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var revised store.BudgetRevisionReceipt
	if err := json.Unmarshal(runBinary(t, ctx, bin, "budget-revise", "--state-dir", state, "--task-id", "review-1", "--control-file", control, "--request", path), &revised); err != nil || revised.Revision != 1 {
		t.Fatal("indexed budget revision", revised, err)
	}
	if got := readLedger(failed.SegmentID); got != before {
		t.Fatal("revision rewrote existing indexed claim or binding")
	}
	var attempts, decisions int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM attempts WHERE task_id='review-1'),(SELECT COUNT(*) FROM runtime_decisions WHERE action_slot=?)`, failure.ActionSlot).Scan(&attempts, &decisions); err != nil || attempts != 1 || decisions != 0 {
		t.Fatal("revision started a retry or consumed failure slot", attempts, decisions, err)
	}
	receipt := runBinary(t, ctx, bin, args...)
	ack := map[string]any{"version": 1, "task_id": "review-1", "control_file": control, "delivery_id": collected.DeliveryID, "collection_proof_sha256": collected.Proof, "decisions": []map[string]any{{"event_id": failure.EventID, "event_revision": failure.EventRevision, "event_hash": failure.PayloadHash, "action_slot": failure.ActionSlot, "decision": "handled", "command_id": "ack-indexed-failure"}}}
	raw, _ = json.Marshal(ack)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	runBinary(t, ctx, bin, "ack", "--state-dir", state, "--request", path)
	args[len(args)-1] = "same-indexed-business-decision"
	if replay := runBinary(t, ctx, bin, args...); !bytes.Equal(receipt, replay) {
		t.Fatal("ACK changed exact indexed retry replay")
	}
	changed := append([]string(nil), args...)
	for i, arg := range changed {
		if arg == "--next-attempt" {
			changed[i+1] = "3"
		}
	}
	bad := exec.CommandContext(ctx, bin, changed...)
	bad.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	if out, err := bad.CombinedOutput(); err == nil || !strings.Contains(string(out), "conflict") {
		t.Fatal("changed indexed retry business tuple was accepted", err, string(out))
	}
	next := waitStatus("result_ready")
	newExit := readExit(next, 0)
	if next.AttemptID == failed.AttemptID || next.SegmentID == failed.SegmentID || next.HostPID != failed.HostPID || next.HostBirth != failed.HostBirth || newExit.Process.PID == oldExit.Process.PID && newExit.Process.Birth == oldExit.Process.Birth {
		t.Fatal("indexed retry lost owner or new worker/attempt", next)
	}
	if live, err := process.BirthContext(ctx, binding.Owner.PID); err != nil || live != binding.Owner.Birth {
		t.Fatal("budget retry replaced original owner", err)
	}
	after := readLedger(next.SegmentID)
	if readLedger(failed.SegmentID) != before || after.Owner != before.Owner || after.Policy != before.Policy || after.Adapter != before.Adapter || after.Inputs != before.Inputs || after.InputHash != before.InputHash || after.Granted != 20000 {
		t.Fatal("indexed retry changed immutable inputs or budget conservation", after)
	}
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM attempts WHERE task_id='review-1'),(SELECT COUNT(*) FROM retry_decisions WHERE action_slot=?)`, failure.ActionSlot).Scan(&attempts, &decisions); err != nil || attempts != 2 || decisions != 1 {
		t.Fatal("indexed retry duplicated decision or attempt", attempts, decisions, err)
	}
	t.Log("supervised indexed review: original worker Exit7, old-cap refusal, public revision, exact failure-slot retry/ACK/replay, new worker Exit0 and frozen vector/policy/binding verified")
}
