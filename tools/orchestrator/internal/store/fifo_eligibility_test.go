package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// These durable-state fixtures exercise selection of a recovered backlog;
// actual claim still runs the public transaction and its admission guards.
func TestGlobalFIFOEligibilityDoesNotStarveOtherHost(t *testing.T) {
	for _, name := range []string{"old_epoch", "disabled_provider", "workspace_full", "attempt_limit", "hard_attempt_limit", "budget_exhausted", "zero_task_budget"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err = db.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":3,"workspaces":{"/private/older":1}}`)); err != nil {
				t.Fatal(err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.sql.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			if name == "workspace_full" {
				_, seed := fifoBatchPlan(t, db, "seed", 3, "other")
				exec(`UPDATE task_runtime SET adapter_payload='{"provider":"other","directory":"/private/older"}' WHERE task_id='seed-task'`)
				if _, err := db.ClaimReady(ctx, seed, 1, 0); err != nil {
					t.Fatal(err)
				}
			}
			provider := "fake"
			if name == "disabled_provider" {
				provider = "claude-code"
			}
			_, older := fifoBatchPlan(t, db, "older", 1, provider)
			var ownError error
			switch name {
			case "old_epoch":
				exec(`UPDATE runtime_hosts SET coordinator_epoch=2 WHERE id=?`, older)
				ownError = ErrHostRejected
			case "disabled_provider":
				exec(`INSERT INTO provider_settings(controller_thread,provider,enabled,model) VALUES('older','claude-code',0,'')`)
				ownError = ErrNoReady
			case "workspace_full":
				ownError = ErrNoReady
			case "attempt_limit", "hard_attempt_limit", "budget_exhausted":
				grant, err := db.ClaimReady(ctx, older, 1, 0)
				if err != nil {
					t.Fatal(err)
				}
				exec(`UPDATE segment_runtime SET status='exited' WHERE segment_id=?`, grant.SegmentID)
				exec(`UPDATE budget_runtime SET status='settled',used_ms=0 WHERE segment_id=?`, grant.SegmentID)
				exec(`UPDATE tasks SET status='ready' WHERE id='older-task'`)
				ownError = ErrAttemptLimit
				if name == "hard_attempt_limit" {
					exec(`UPDATE tasks SET max_attempts=4 WHERE id='older-task'`)
					for i, id := range []string{"older-attempt-2", "older-attempt-3"} {
						exec(`INSERT INTO attempts(id,task_id,attempt_no,status,created_at) VALUES(?,'older-task',?,'failed','fixture')`, id, i+2)
					}
				}
				if name == "budget_exhausted" {
					exec(`UPDATE tasks SET max_attempts=3 WHERE id='older-task'`)
					exec(`UPDATE budget_runtime SET used_ms=? WHERE segment_id=?`, defaultGroupActiveMS, grant.SegmentID)
					ownError = CodeError("budget_exhausted")
				}
			case "zero_task_budget":
				exec(`UPDATE task_runtime SET max_active_ms=0 WHERE task_id='older-task'`)
				ownError = CodeError("budget_exhausted")
			}
			_, newer := fifoBatchPlan(t, db, "newer", 2, "fake")
			if command, err := db.ClaimReady(ctx, newer, 1, 0); err != nil || command.TaskID != "newer-task" {
				t.Fatalf("ineligible older %s starved eligible newer: command=%+v err=%v", name, command, err)
			}
			if _, err := db.ClaimReady(ctx, older, 1, 0); !errors.Is(err, ownError) {
				t.Fatalf("own Host error changed: got %v want %v", err, ownError)
			}
		})
	}
}

func TestGlobalFIFOConcurrentClaimsRetainSingleSlot(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.ConfigureConcurrency(ctx, json.RawMessage(`{"version":1,"global":1}`)); err != nil {
		t.Fatal(err)
	}
	_, older := fifoBatchPlan(t, db, "older", 1, "fake")
	_, newer := fifoBatchPlan(t, db, "newer", 2, "fake")
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var claimed []string
	for _, host := range []string{newer, older, newer, older, newer, older, newer, older} {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			<-start
			command, err := db.ClaimReady(ctx, host, 1, 0)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				claimed = append(claimed, command.TaskID)
			} else if !errors.Is(err, ErrNoSlot) && !errors.Is(err, ErrNoReady) {
				t.Errorf("unexpected concurrent claim error: %v", err)
			}
		}(host)
	}
	close(start)
	wg.Wait()
	if len(claimed) != 1 || claimed[0] != "older-task" {
		t.Fatalf("FIFO or global cap violated: %v", claimed)
	}
	var segments, attempts, reserved int
	if err := db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM segment_runtime),(SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM budget_runtime WHERE status='reserved')`).Scan(&segments, &attempts, &reserved); err != nil {
		t.Fatal(err)
	}
	if segments != 1 || attempts != 1 || reserved != 1 {
		t.Fatalf("duplicate claim: segments=%d attempts=%d reservations=%d", segments, attempts, reserved)
	}
}

func TestGlobalFIFORetryResumeQueueAtTail(t *testing.T) {
	for _, mode := range []string{"retry", "resume"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_, firstHost := fifoBatchPlan(t, db, "first", 1, "fake", 3)
			first, err := db.ClaimReady(ctx, firstHost, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			_, otherHost := fifoBatchPlan(t, db, "other", 2, "fake")
			var sequence int64
			send := func(kind string) {
				t.Helper()
				sequence++
				exit := 0
				e := contract.Event{Version: 1, ProducerID: firstHost, EventID: mode + "-" + kind, RunID: first.RunID, TaskID: first.TaskID, AttemptID: first.AttemptID, SegmentID: first.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: first.CommandID, Sequence: sequence, Kind: kind, PayloadHash: runtimeHash(mode + kind), ActiveMS: 1}
				if kind == contract.EventExited {
					e.ExitCode = &exit
				}
				if _, err := db.CommitHostEvent(ctx, e); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "resume" {
				for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventStopped, contract.EventExited} {
					send(kind)
				}
				if err := db.QueueResume(ctx, first.TaskID, 1); err != nil {
					t.Fatal(err)
				}
			} else {
				send(contract.EventFailed)
				send(contract.EventExited)
				page, err := db.CollectPending(ctx, first.TaskID, "", 0, false)
				if err != nil || len(page.Events) != 1 {
					t.Fatalf("failure event not collectable: %+v %v", page, err)
				}
				e := page.Events[0]
				if _, err := db.QueueRetry(ctx, RetrySpec{TaskID: first.TaskID, WorkRevision: 1, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, SegmentID: first.SegmentID, NextAttemptNo: 2, CommandID: "fifo-retry"}); err != nil {
					t.Fatal(err)
				}
			}
			if command, err := db.ClaimReady(ctx, firstHost, 1, 2); !errors.Is(err, ErrNoReady) {
				t.Fatalf("%s bypassed earlier ready task after requeue: command=%+v err=%v", mode, command, err)
			}
			if command, err := db.ClaimReady(ctx, otherHost, 1, 2); err != nil || command.TaskID != "other-task" {
				t.Fatalf("earlier ready task lost its place: %+v %v", command, err)
			}
			queued, err := db.ClaimReady(ctx, firstHost, 1, 2)
			if err != nil || queued.TaskID != first.TaskID || queued.SegmentID == first.SegmentID {
				t.Fatalf("requeued task did not dispatch: %+v %v", queued, err)
			}
			if (mode == "resume") != (queued.AttemptID == first.AttemptID) {
				t.Fatalf("attempt continuity changed for %s: first=%s next=%s", mode, first.AttemptID, queued.AttemptID)
			}
		})
	}
}
