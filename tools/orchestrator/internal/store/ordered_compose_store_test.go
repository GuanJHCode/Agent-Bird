package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func newOrderedComposeStoreFixture(t *testing.T) (*DB, string) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	producer := func(id string) TaskSpec {
		return TaskSpec{ID: id, RunID: "run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"implementer","permission":"workspace-write"},"candidate_workspace":{"version":1}}`)}
	}
	compose := TaskSpec{ID: "compose", RunID: "run", Dependencies: []string{"a", "b"}, MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"kind":"candidate","directory":"/private/compose","candidate_action":{"version":2,"dependency_input_version":2,"operation":"compose","ordered_source_tasks":["b","a"]}}`)}
	plan, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 7, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{producer("a"), producer("b"), compose}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for ordinal, task := range []string{"a", "b"} {
		g, err := db.ClaimReady(ctx, host, 1, 2)
		if err != nil || g.TaskID != task {
			t.Fatalf("source=%+v err=%v", g, err)
		}
		body := []byte(task)
		e := contract.Event{Version: 1, ProducerID: host, EventID: task + "-result", RunID: "run", TaskID: task, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: int64(ordinal*2 + 1), Kind: contract.EventResult, PayloadHash: runtimeHash(string(body)), Artifact: managedHostArtifactForTest(t, db, g, "terminal", 0, body)}
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		zero := 0
		done := e
		done.EventID = task + "-exited"
		done.Sequence++
		done.Kind = contract.EventExited
		done.ExitCode = &zero
		if _, err = db.CommitHostEvent(ctx, done); err != nil {
			t.Fatal(err)
		}
		page, err := db.CollectPending(ctx, task, "", 0, false)
		if err != nil || len(page.Events) != 1 {
			t.Fatalf("events=%+v err=%v", page, err)
		}
		r := page.Events[0]
		if _, err = db.ReviewResult(ctx, ReviewSpec{TaskID: task, WorkRevision: 1, EventID: r.EventID, EventRevision: r.EventRevision, EventHash: r.PayloadHash, ActionSlot: r.ActionSlot, Decision: "accept", CommandID: "accept-" + task}); err != nil {
			t.Fatal(err)
		}
	}
	return db, host
}

func TestOrderedComposeStoreCaptureAndPendingReplay(t *testing.T) {
	ctx := context.Background()
	db, host := newOrderedComposeStoreFixture(t)
	g, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if g.TaskID != "compose" || g.Input != nil || g.DependencyInputs == nil || g.DependencyInputs.Version != 2 || len(g.DependencyInputs.Inputs) != 2 || g.DependencyInputs.Inputs[0].Event.TaskID != "b" || g.DependencyInputs.Inputs[1].Event.TaskID != "a" {
		t.Fatalf("compose=%+v", g)
	}
	pending, err := db.PendingLaunches(ctx, host, 1)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if pending[0].LaunchIntentHash != g.LaunchIntentHash || pending[0].DependencyInputs.Inputs[0].Event.TaskID != "b" {
		t.Fatal("replay changed ordered grant")
	}
	var attempts, quotaBefore int
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='compose'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`DROP TRIGGER dependency_inputs_pair_update; UPDATE segment_runtime SET dependency_inputs_json=NULL,dependency_inputs_sha256=NULL WHERE segment_id=?`, g.SegmentID); err != nil {
		t.Fatal(err)
	}
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&quotaBefore); err != nil {
		t.Fatal(err)
	}
	if _, err = db.PendingLaunches(ctx, host, 1); err == nil {
		t.Fatal("missing v2 capture replayed")
	}
	var attemptsAfter, quotaAfter int
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM attempts WHERE task_id='compose'`).Scan(&attemptsAfter); err != nil {
		t.Fatal(err)
	}
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&quotaAfter); err != nil {
		t.Fatal(err)
	}
	if attemptsAfter != attempts || quotaAfter != quotaBefore {
		t.Fatalf("replay drift attempts=%d/%d quota=%d/%d", attemptsAfter, attempts, quotaAfter, quotaBefore)
	}
}

// Synthetic old/corrupt decision rows exercise the same immutable authority
// checks as other candidate stages; they never grant compose a retry policy.
func TestOrderedComposePreservesDecisionAuthority(t *testing.T) {
	for _, phase := range []string{"pending", "consumed_without_proof"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			db, host := newOrderedComposeStoreFixture(t)
			var grant contract.LaunchCommand
			var err error
			if phase == "consumed_without_proof" {
				grant, err = db.ClaimReady(ctx, host, 1, 2)
				if err != nil {
					t.Fatal(err)
				}
			}
			var sourceSegment string
			if err = db.sql.QueryRow(`SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id='a'`).Scan(&sourceSegment); err != nil {
				t.Fatal(err)
			}
			if _, err = db.sql.Exec(`INSERT INTO retry_decisions(action_slot,task_id,work_revision,source_event_id,source_event_revision,source_event_hash,source_segment_id,next_attempt_no,use_next_fallback,command_id,created_at) VALUES('compose-decision','compose',1,'a-result',1,?,?,2,0,'compose-retry','2026-10-09T00:00:00Z')`, runtimeHash("a"), sourceSegment); err != nil {
				t.Fatal(err)
			}
			if phase == "pending" {
				if _, err = db.sql.Exec(`INSERT INTO decision_attempt_bindings(action_slot,task_id,target_work_revision,attempt_id,reserved_active_ms) VALUES('compose-decision','compose',1,NULL,1000)`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = db.sql.Exec(`INSERT INTO decision_attempt_bindings(action_slot,task_id,target_work_revision,attempt_id,reserved_active_ms) VALUES('compose-decision','compose',1,?,0); DELETE FROM decision_attempt_bindings WHERE action_slot='compose-decision'`, grant.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			before := continuationQuotaState(t, db)
			if phase == "pending" {
				if _, err = db.ClaimReady(ctx, host, 1, 2); err == nil {
					t.Fatal("compose bypassed pending decision authentication")
				}
			} else {
				if _, err = db.PendingLaunches(ctx, host, 1); !errors.Is(err, errLegacyDecisionAmbiguous) {
					t.Fatalf("lost consumed proof accepted: %v", err)
				}
			}
			if continuationQuotaState(t, db) != before {
				t.Fatal("refused decision changed attempt, budget, quota, vector or binding")
			}
		})
	}
}
