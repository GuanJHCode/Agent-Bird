package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestDecisionBindingsReworkSuccessorsConsumeTheirLocalOrdinals(t *testing.T) {
	t.Run("complete", func(t *testing.T) { reworkBindingChain(t, false) })
	t.Run("missing_integrate", func(t *testing.T) { reworkBindingChain(t, true) })
}

func reworkBindingChain(t *testing.T, missing bool) {
	t.Helper()
	f := newReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	var candidate any
	var prev contract.Event
	dir := t.TempDir()
	seq := int64(6) // The original three fixture segments each committed two events.
	for i, stage := range []string{"edit", "validate", "review", "integrate"} {
		if missing && stage == "integrate" {
			var raw string
			var saved reworkDecision
			if err := f.db.sql.QueryRow(`SELECT revision_payload FROM retry_decisions`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &saved); err != nil {
				t.Fatal(err)
			}
			delete(saved.Receipt.TaskRevisions, "integrate")
			body, _ := json.Marshal(saved)
			if _, err := f.db.sql.Exec(`DELETE FROM decision_attempt_bindings; UPDATE retry_decisions SET revision_payload=?`, string(body)); err != nil {
				t.Fatal(err)
			}
			before := continuationQuotaState(t, f.db)
			if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, errLegacyDecisionAmbiguous) {
				t.Errorf("missing rework target became initial admission: %v", err)
			}
			if continuationQuotaState(t, f.db) != before {
				t.Error("ambiguous target advanced ledger")
			}
			return
		}
		g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
		if err != nil || g.TaskID != stage || g.WorkRevision != 2 {
			t.Fatalf("successor %s grant=%+v err=%v", stage, g, err)
		}
		var ordinal int
		var bound string
		want := 2
		if stage == "integrate" {
			want = 1
		} // It never ran in the rejected original chain.
		if err = f.db.sql.QueryRow(`SELECT a.attempt_no,b.attempt_id FROM attempts a JOIN decision_attempt_bindings b ON b.attempt_id=a.id WHERE b.task_id=? AND b.action_slot=?`, stage, f.spec.ActionSlot).Scan(&ordinal, &bound); err != nil || ordinal != want || bound != g.AttemptID {
			t.Fatalf("local target %s ordinal=%d binding=%s err=%v", stage, ordinal, bound, err)
		}
		binding := map[string]any{"run_id": "run", "task_id": stage, "attempt_id": g.AttemptID, "segment_id": g.SegmentID, "work_revision": 2, "plan_revision": 7}
		if i == 0 {
			candidate = map[string]any{"binding": binding, "repo_root": "/private/repo", "base_oid": f.spec.CandidateOID, "candidate_oid": strings.Repeat("e", 40), "tree_oid": strings.Repeat("f", 40)}
		}
		artifact := map[string]any{"version": 1, "candidate": candidate}
		if i > 0 {
			artifact["stage"] = stage
			artifact["binding"] = binding
			artifact["input_event_id"] = prev.EventID
			artifact["input_sha256"] = prev.PayloadHash
			artifact["validation_digest"] = strings.Repeat("d", 64)
			artifact["validation"] = map[string]any{"passed": true}
		}
		if stage == "review" {
			artifact["review"] = map[string]any{"decision": "approve", "summary": "both operands validated"}
		}
		body, _ := json.Marshal(artifact)
		path := filepath.Join(dir, stage+".json")
		if err = os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		seq++
		e := contract.Event{Version: 1, ProducerID: f.host, EventID: "revision2-" + stage, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 2, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: seq, Kind: contract.EventResult, PayloadHash: runtimeHash(string(body)), Artifact: &contract.ArtifactRef{ID: stage, Path: path, Size: int64(len(body)), SHA256: runtimeHash(string(body))}}
		if _, err = f.db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		seq++
		done := e
		done.EventID = e.EventID + "-exited"
		done.Sequence = seq
		done.Kind = contract.EventExited
		zero := 0
		done.ExitCode = &zero
		done.ActiveMS = 1
		if _, err = f.db.CommitHostEvent(ctx, done); err != nil {
			t.Fatal(err)
		}
		page, err := f.db.CollectPending(ctx, stage, "", 0, false)
		if err != nil || len(page.Events) == 0 {
			t.Fatal("collect", err)
		}
		for _, event := range page.Events {
			if event.EventID == e.EventID {
				prev = event
			}
		}
		if prev.EventID != e.EventID {
			t.Fatal("current result not collected")
		}
		if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: stage, WorkRevision: 2, EventID: prev.EventID, EventRevision: prev.EventRevision, EventHash: prev.PayloadHash, ActionSlot: prev.ActionSlot, Decision: "accept", CommandID: "accept-revision2-" + stage}); err != nil {
			t.Fatal(err)
		}
	}
	var pending, consumed int
	if err := f.db.sql.QueryRow(`SELECT SUM(attempt_id IS NULL),COUNT(attempt_id) FROM decision_attempt_bindings`).Scan(&pending, &consumed); err != nil || pending != 0 || consumed != 4 {
		t.Fatalf("chain bindings %d/%d err=%v", pending, consumed, err)
	}
	before := continuationQuotaState(t, f.db)
	if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
		t.Fatal("historical receipt", err)
	}
	if continuationQuotaState(t, f.db) != before {
		t.Fatal("consumed rework replay changed ledger")
	}
}

func TestDecisionBindingsContinuationKeepsConsumedAttemptAndPromise(t *testing.T) {
	db, host, spec := newOrdinaryDecisionFixture(t)
	ctx := context.Background()
	g, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Existing legacy question contract, on the actual bound retry attempt.
	seq := int64(2)
	for i, kind := range []string{contract.EventQuestion, contract.EventExited} {
		seq++
		e := contract.Event{Version: 1, ProducerID: host, EventID: fmt.Sprintf("retry-question-%d", i), RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: seq, Kind: kind, PayloadHash: fmt.Sprintf("%064x", seq), QuestionID: "question", QuestionRevision: 1, SessionKind: "conversation-id", SessionID: "same-session"}
		if kind == contract.EventExited {
			zero := 0
			e.ExitCode = &zero
			e.ActiveMS = 1
		}
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.AnswerQuestion(ctx, AnswerSpec{TaskID: g.TaskID, WorkRevision: 1, QuestionID: "question", QuestionRevision: 1, Answer: "answer"}); err != nil {
		t.Fatal(err)
	}
	resumed, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil || resumed.AttemptID != g.AttemptID || resumed.SegmentID == g.SegmentID {
		t.Fatalf("continuation changed attempt: %+v %v", resumed, err)
	}
	var attempts, bindings, promise int
	var bound string
	if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM decision_attempt_bindings),d.next_attempt_no,b.attempt_id FROM retry_decisions d JOIN decision_attempt_bindings b USING(action_slot) WHERE d.action_slot=?`, spec.ActionSlot).Scan(&attempts, &bindings, &promise, &bound); err != nil || attempts != 2 || bindings != 1 || promise != 2 || bound != g.AttemptID {
		t.Fatalf("continuation consumed another authority %d/%d/%d/%s err=%v", attempts, bindings, promise, bound, err)
	}
	if grants, err := db.PendingLaunches(ctx, host, 1); err != nil || len(grants) != 1 || grants[0].SegmentID != resumed.SegmentID {
		t.Fatal("continuation exact replay", err)
	}
}
