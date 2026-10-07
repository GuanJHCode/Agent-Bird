package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

// The first actual worker fails only after the test observes its live identity.
// The second holds until explicitly released. Exclusive markers reject a third
// Start rather than letting an overwritten marker hide duplicate execution.
func runBudgetRevisionWorkerFixture() {
	if len(os.Args) != 3 {
		os.Exit(31)
	}
	marker, release := os.Args[1], os.Args[2]
	f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	retry := false
	if errors.Is(err, os.ErrExist) {
		retry = true
		marker, release = marker+".retry", release+".retry"
		f, err = os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	if err != nil {
		os.Exit(32)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		_ = f.Close()
		os.Exit(33)
	}
	err = json.NewEncoder(f).Encode(originalOwnerWorkerMarker{os.Getpid(), os.Getppid(), birth})
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		os.Exit(34)
	}
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); {
		if _, err := os.Stat(release); err == nil {
			if !retry {
				fmt.Fprintln(os.Stderr, "controlled first attempt failure")
				os.Exit(7)
			}
			fmt.Println("amended budget worker result")
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(35)
}

func TestCLIBudgetRevisionRetriesFailedWorkerWithOriginalOwner(t *testing.T) {
	exerciseCLIOriginalOwner(t, 0, false, "retry")
}

func TestCLIBudgetRevisionResumesInterruptedWorkerWithinSameAttempt(t *testing.T) {
	exerciseCLIOriginalOwner(t, 0, false, "resume")
}

func exerciseNativeBudgetRevision(t *testing.T, ctx context.Context, bin, state, root, control string, bootstrap hostBootstrap, db *sql.DB, binding contract.OriginalOwnerBinding, bindingJSON string, host coreCLIHostIdentity, first originalOwnerWorkerMarker, markerPath, releasePath *string, actors *[]contract.OriginalOwnerTransport, resume bool) originalOwnerWorkerMarker {
	t.Helper()
	waitRunning := func() recoveryStatus {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); ; {
			status := recoveryTaskStatus(t, ctx, bin, state, "worker", control)
			if status.Status == "running" && status.AttemptID != "" && status.SegmentID != "" {
				return status
			}
			if time.Now().After(end) {
				t.Fatal("worker did not reach running", status)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	before := waitRunning()
	wantStatus, wantOutcome := "failed", contract.EventFailed
	if resume {
		wantStatus, wantOutcome = "interrupted", contract.EventStopped
		runBinary(t, ctx, bin, "stop", "--state-dir", state, "--task-id", "worker", "--control-file", control, "--work-revision", "1")
	} else if err := os.WriteFile(*releasePath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var failed recoveryStatus
	for end := time.Now().Add(10 * time.Second); ; {
		failed = recoveryTaskStatus(t, ctx, bin, state, "worker", control)
		if failed.Status == wantStatus && failed.SegmentStatus == "exited" {
			break
		}
		if time.Now().After(end) {
			t.Fatal("original worker did not fail and exit", failed)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if failed.HostStatus != "ready" || failed.HostPID != host.PID || failed.HostBirth != host.Birth || failed.AttemptID != before.AttemptID || failed.SegmentID != before.SegmentID {
		t.Fatal("pending failed task lost its original Host or attempt", failed)
	}
	spool, err := events.Open(filepath.Join(bootstrap.SpoolRoot, before.AttemptID, before.SegmentID))
	if err != nil {
		t.Fatal(err)
	}
	observed, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	exits, failures := 0, 0
	for _, event := range observed {
		if event.Kind == wantOutcome {
			failures++
			if resume && event.StopReason != "owner_stop_requested" {
				t.Fatal("interruption was not caused by the explicit owner stop", event.StopReason)
			}
		}
		if event.Kind == contract.EventExited {
			exits++
			if event.ExitCode == nil || *event.ExitCode == 0 || (!resume && *event.ExitCode != 7) || event.Process == nil || event.Process.PID != first.PID || event.Process.Birth != first.Birth {
				t.Fatal("first worker has no original nonzero exit receipt")
			}
		}
	}
	if exits != 1 || failures != 1 || !errors.Is(syscall.Kill(first.PID, 0), syscall.ESRCH) {
		t.Fatal("first worker failure/Wait/reap evidence incomplete", failures, exits)
	}
	var charge, oldGrant, oldDeadline int64
	if err := db.QueryRowContext(ctx, `SELECT b.used_ms,b.granted_ms,s.deadline_unix_ms FROM budget_runtime b JOIN segment_runtime s ON s.segment_id=b.segment_id WHERE b.segment_id=? AND b.status='settled'`, before.SegmentID).Scan(&charge, &oldGrant, &oldDeadline); err != nil || charge <= 0 || oldGrant != 30000 {
		t.Fatal("first attempt did not preserve real settled charge", charge, oldGrant, err)
	}
	var collected struct {
		Events []contract.Event `json:"events"`
	}
	if err := json.Unmarshal(runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", "worker", "--control-file", control), &collected); err != nil {
		t.Fatal(err)
	}
	var failure contract.Event
	for _, event := range collected.Events {
		if event.Kind == wantOutcome && event.SegmentID == before.SegmentID {
			failure = event
		}
	}
	if failure.ActionSlot == "" {
		t.Fatal("no exact pending failure action slot")
	}
	retryArgs := []string{"retry", "--state-dir", state, "--task-id", "worker", "--control-file", control, "--work-revision", "1", "--event-id", failure.EventID, "--event-revision", strconv.FormatInt(failure.EventRevision, 10), "--event-hash", failure.PayloadHash, "--action-slot", failure.ActionSlot, "--segment-id", failure.SegmentID, "--next-attempt", "2", "--command-id", "explicit-amended-retry"}
	if resume {
		retryArgs = []string{"resume", "--state-dir", state, "--task-id", "worker", "--control-file", control, "--work-revision", "1"}
	} else {
		denied := exec.CommandContext(ctx, bin, retryArgs...)
		denied.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
		output, err := denied.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "attempt_limit") {
			t.Fatalf("old attempt budget did not reject retry: %v %s", err, output)
		}
	}
	request := store.BudgetRevisionRequest{BudgetGroupID: "worker", ExpectedRevision: 0, CommandID: "native-increase", GroupMaxAttempts: 2, GroupMaxActiveMS: 120000, Tasks: []store.BudgetRevisionTask{{TaskID: "worker", WorkRevision: 1, MaxAttempts: 2, AttemptMaxActiveMS: 45000, TaskMaxActiveMS: 90000, SegmentMaxActiveMS: 45000}}}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "budget-revision.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var receipt store.BudgetRevisionReceipt
	if err := json.Unmarshal(runBinary(t, ctx, bin, "budget-revise", "--state-dir", state, "--task-id", "worker", "--control-file", control, "--request", path), &receipt); err != nil || receipt.Revision != 1 {
		t.Fatal("native amendment failed", err)
	}
	after := recoveryTaskStatus(t, ctx, bin, state, "worker", control)
	if after.Status != wantStatus || after.HostPID != failed.HostPID || after.HostBirth != failed.HostBirth || after.HostStatus != "ready" || after.AttemptID != before.AttemptID {
		t.Fatal("amendment revived task or replaced Host", after)
	}
	var attempts, decisions int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM attempts WHERE task_id='worker'),(SELECT COUNT(*) FROM runtime_decisions WHERE action_slot=?)`, failure.ActionSlot).Scan(&attempts, &decisions); err != nil || attempts != 1 || decisions != 0 {
		t.Fatal("amendment started worker or consumed failure slot", attempts, decisions, err)
	}
	// Set both cleanup paths before the retry can actually create a child.
	*markerPath, *releasePath = *markerPath+".retry", *releasePath+".retry"
	runBinary(t, ctx, bin, retryArgs...)
	var second originalOwnerWorkerMarker
	for end := time.Now().Add(10 * time.Second); ; {
		raw, err := os.ReadFile(*markerPath)
		if err == nil && json.Unmarshal(raw, &second) == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("explicit amended retry did not Start a new worker", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	*actors = append(*actors, contract.OriginalOwnerTransport{PID: second.PID, Birth: second.Birth})
	if second.PID == first.PID && second.Birth == first.Birth || second.ParentPID != binding.Owner.PID {
		t.Fatal("retry did not use a new child of the original owner", second)
	}
	if current, err := process.Birth(binding.Owner.PID); err != nil || current != binding.Owner.Birth {
		t.Fatal("retry replaced original owner", err)
	}
	next := waitRunning()
	if (next.AttemptID == before.AttemptID) != resume || next.SegmentID == before.SegmentID || next.WorkRevision != before.WorkRevision || next.HostPID != host.PID || next.HostBirth != host.Birth {
		t.Fatal("retry lost original scope or new attempt", next)
	}
	var gotCharge, gotOldGrant, gotDeadline, newGrant int64
	wantGrant, wantAttempts := int64(45000), 2
	if resume {
		wantGrant, wantAttempts = wantGrant-charge, 1
	}
	if err := db.QueryRowContext(ctx, `SELECT b.used_ms,b.granted_ms,s.deadline_unix_ms,(SELECT granted_ms FROM budget_runtime WHERE segment_id=?) FROM budget_runtime b JOIN segment_runtime s ON s.segment_id=b.segment_id WHERE b.segment_id=?`, next.SegmentID, before.SegmentID).Scan(&gotCharge, &gotOldGrant, &gotDeadline, &newGrant); err != nil || gotCharge != charge || gotOldGrant != oldGrant || gotDeadline != oldDeadline || newGrant != wantGrant {
		t.Fatal("amendment changed history or new grant", gotCharge, gotOldGrant, gotDeadline, newGrant, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE task_id='worker'`).Scan(&attempts); err != nil || attempts != wantAttempts {
		t.Fatal("retry duplicated or reset attempts", attempts, err)
	}
	var ownerJSON string
	if err := db.QueryRowContext(ctx, `SELECT original_binding_json FROM execution_owners WHERE run_id='owner-cli-run' AND is_current=1`).Scan(&ownerJSON); err != nil {
		t.Fatal(err)
	}
	if ownerJSON != bindingJSON {
		t.Fatal("budget revision changed original owner binding")
	}
	if resume {
		t.Log("actual CLI owner stop, revision, same-attempt/new-segment resume, preserved charge and reduced remaining grant verified")
	} else {
		t.Log("actual CLI failure/ExitCode=7, old-cap refusal, revision, same failure slot retry, new worker and preserved charge verified")
	}
	return second
}
