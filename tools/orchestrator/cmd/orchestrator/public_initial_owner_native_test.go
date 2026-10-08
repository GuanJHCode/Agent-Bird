package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func exercisePublicInitialRecovery(t *testing.T, ctx context.Context, cli, state, control string, bootstrap hostBootstrap, db *sql.DB, initial contract.OriginalOwnerBinding, trigger, marker, release string, actors *[]contract.OriginalOwnerTransport) {
	t.Helper()
	for _, item := range []struct {
		kind  string
		actor contract.OriginalOwnerTransport
	}{
		{"owner", contract.OriginalOwnerTransport{PID: initial.Owner.PID, Birth: initial.Owner.Birth}}, {"first_host", initial.FirstHost},
	} {
		wait := publicSuccessionReadWait(t, state, item.kind, item.actor.PID)
		if wait.ExitCode != 0 || wait.WaitError != "" {
			t.Fatal("initial original Wait", wait)
		}
		if exited, err := process.IdentityExitedContext(ctx, item.actor.PID, item.actor.Birth); err != nil || !exited {
			t.Fatal("initial typed death", item.actor, err)
		}
		t.Logf("inc1 %s original Wait=%+v", item.kind, wait)
	}
	var revision, applied, attempts, segments, budgets int
	var status string
	var sequence, nextSequence, maxActive int64
	if err := db.QueryRow(`SELECT o.control_revision,o.applied_control_revision,t.status,tr.ready_sequence,tr.max_active_ms,
 (SELECT next_ready_sequence FROM runtime_meta),(SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM budget_runtime)
 FROM execution_owners o JOIN tasks t ON t.run_id=o.run_id JOIN task_runtime tr ON tr.task_id=t.id WHERE o.owner_id=? AND o.is_current=1 AND t.id='worker'`, initial.Scope.OwnerID).Scan(&revision, &applied, &status, &sequence, &maxActive, &nextSequence, &attempts, &segments, &budgets); err != nil || revision != 1 || applied != 0 || status != "ready" || sequence <= 0 || maxActive != 30000 || attempts != 0 || segments != 0 || budgets != 0 {
		t.Fatal("initial state is not pristine", revision, applied, status, sequence, maxActive, attempts, segments, budgets, err)
	}
	ledger := filepath.Join(bootstrap.SpoolRoot, ".coordinator-recovery.json")
	if _, err := os.Lstat(ledger); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("initial Bind-only fixture created recovery ledger", err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("worker ran before initial Applied", err)
	}
	if err := os.Remove(trigger); err != nil {
		t.Fatal(err)
	}
	args := []string{"resume", "--state-dir", state, "--task-id", "worker", "--control-file", control, "--work-revision", "1", "--command-id", "public-initial-recovery"}
	body := runBinary(t, ctx, cli, args...)
	var receipt struct {
		Mode, Status    string
		ReadySequence   int64  `json:"ready_sequence"`
		AuthorizationID string `json:"authorization_id"`
	}
	if err := json.Unmarshal(body, &receipt); err != nil || receipt.Mode != "initial_ready" || receipt.Status != "initial_ready" || receipt.ReadySequence != sequence || receipt.AuthorizationID == "" {
		t.Fatal("initial public resume receipt", string(body), err)
	}
	next := publicSuccessionBinding(t, db)
	if next.Scope.Incarnation != 2 || next.Scope.RunID != initial.Scope.RunID || next.Scope.LaunchID != initial.Scope.LaunchID || next.Owner.PID == initial.Owner.PID {
		t.Fatal("initial recovery did not enroll adjacent production owner", next)
	}
	*actors = append(*actors, next.FirstHost, contract.OriginalOwnerTransport{PID: next.Owner.PID, Birth: next.Owner.Birth})
	worker := publicSuccessionWorker(t, marker)
	*actors = append(*actors, contract.OriginalOwnerTransport{PID: worker.PID, Birth: worker.Birth})
	publicSuccessionAncestry(t, ctx, worker, next)
	running := publicSuccessionStatus(t, ctx, cli, state, control, "running", "running")
	var granted, readyAfter int64
	var phase, proofID string
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM attempts WHERE task_id='worker'),(SELECT granted_ms FROM budget_runtime WHERE segment_id=?),(SELECT next_ready_sequence FROM runtime_meta),p.phase,a.initial_proof_id FROM owner_initial_recoveries p JOIN owner_succession_authorizations a ON a.authorization_id=p.authorization_id WHERE p.owner_id=?`, running.SegmentID, initial.Scope.OwnerID).Scan(&attempts, &granted, &readyAfter, &phase, &proofID); err != nil || attempts != 1 || granted != 30000 || readyAfter != nextSequence || phase != "activated" || proofID != initial.Scope.OwnerID {
		t.Fatal("initial resume changed queue/budget or failed signed Applied", attempts, granted, readyAfter, phase, proofID, err)
	}
	if replay := runBinary(t, ctx, cli, args...); !bytes.Equal(replay, body) {
		t.Fatal("initial stable receipt replay drift", string(replay))
	}
	if current := publicSuccessionWorker(t, marker); current != worker {
		t.Fatal("replay replaced worker")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	terminal := publicSuccessionStatus(t, ctx, cli, state, control, "result_ready", "exited")
	if terminal.AttemptID != running.AttemptID || terminal.SegmentID != running.SegmentID {
		t.Fatal("completion replaced first execution")
	}
	publicSuccessionExit(t, bootstrap, terminal.AttemptID, terminal.SegmentID, worker, true)
	var collection struct {
		Events []contract.Event `json:"events"`
	}
	if err := json.Unmarshal(runBinary(t, ctx, cli, "collect", "--state-dir", state, "--task-id", "worker", "--control-file", control), &collection); err != nil {
		t.Fatal(err)
	}
	var result contract.Event
	for _, event := range collection.Events {
		if event.Kind == contract.EventResult && event.SegmentID == running.SegmentID {
			if result.EventID != "" {
				t.Fatal("duplicate result")
			}
			result = event
		}
	}
	if result.EventID == "" || result.ActionSlot == "" {
		t.Fatal("missing collected result")
	}
	runBinary(t, ctx, cli, "accept", "--state-dir", state, "--task-id", "worker", "--control-file", control, "--work-revision", "1", "--event-id", result.EventID, "--event-revision", strconv.FormatInt(result.EventRevision, 10), "--event-hash", result.PayloadHash, "--action-slot", result.ActionSlot, "--decision", "accept", "--command-id", "public-initial-accept")
	publicSuccessionStatus(t, ctx, cli, state, control, "completed", "exited")
	for _, item := range []struct {
		kind  string
		actor contract.OriginalOwnerTransport
	}{
		{"owner", contract.OriginalOwnerTransport{PID: next.Owner.PID, Birth: next.Owner.Birth}}, {"first_host", next.FirstHost},
	} {
		wait := publicSuccessionReadWait(t, state, item.kind, item.actor.PID)
		if wait.ExitCode != 0 || wait.WaitError != "" {
			t.Fatal("production successor original Wait", wait)
		}
		if exited, err := process.IdentityExitedContext(ctx, item.actor.PID, item.actor.Birth); err != nil || !exited {
			t.Fatal("successor not reaped", item.actor, err)
		}
		t.Logf("inc2 %s original Wait=%+v", item.kind, wait)
	}
	t.Logf("public initial Bind-before-ledger recovered: ready_sequence=%d grant=%d attempt=%s segment=%s", sequence, granted, running.AttemptID, running.SegmentID)
}
