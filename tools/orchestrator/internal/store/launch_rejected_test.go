package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func rejectionEvent(t *testing.T, f *typedContinuationFixture) contract.Event {
	e := f.event("launch_rejected")
	e.Artifact = managedHostArtifactForTest(t, f.db, f.grant, "prelaunch", 0, []byte(`{"status":"launch_rejected","reason":"fixture_rejection"}`))
	e.PayloadHash = e.Artifact.SHA256
	return e
}

func assertRejectedLedger(t *testing.T, f *typedContinuationFixture, status string, used int64) {
	t.Helper()
	var segment, attempt, task, budget string
	var got int64
	err := f.db.sql.QueryRow(`SELECT s.status,a.status,t.status,b.status,b.used_ms FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN budget_runtime b ON b.id=s.reservation_id WHERE s.segment_id=?`, f.grant.SegmentID).Scan(&segment, &attempt, &task, &budget, &got)
	if err != nil || budget != status || got != used {
		t.Fatalf("ledger=%s/%d segment=%s attempt=%s task=%s err=%v", budget, got, segment, attempt, task, err)
	}
	if status == "settled" && (segment != "not_started" || attempt != "failed" || task != "failed") {
		t.Fatalf("terminal projection=%s/%s/%s", segment, attempt, task)
	}
}

func TestLaunchRejectedSettlesOnlyNeverStartedGrantAtZeroAndReplays(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "invocation_rejected", true: "workspace_rejected"}[prepared], func(t *testing.T) {
			f := newTypedContinuationFixture(t, adapter.ProviderAGY)
			if prepared {
				f.send(t, f.event(contract.EventPrepared))
			}
			e := rejectionEvent(t, f)
			f.send(t, e)
			assertRejectedLedger(t, f, "settled", 0)
			if _, err := f.db.CommitHostEvent(context.Background(), e); err != nil {
				t.Fatal(err)
			}
			assertRejectedLedger(t, f, "settled", 0)
			var count int
			if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM runtime_events WHERE json_extract(body_json,'$.kind')='launch_rejected'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("duplicate count=%d %v", count, err)
			}
			page, err := f.db.CollectPending(context.Background(), "task", "", 0, false)
			if err != nil || len(page.Events) != 1 || page.Events[0].Kind != "launch_rejected" || page.Events[0].Artifact == nil {
				t.Fatalf("owner diagnostic=%+v %v", page, err)
			}
			if _, err = f.db.QueueRetry(context.Background(), RetrySpec{TaskID: "task", WorkRevision: 1, EventID: page.Events[0].EventID, EventRevision: page.Events[0].EventRevision, EventHash: page.Events[0].PayloadHash, ActionSlot: page.Events[0].ActionSlot, SegmentID: f.grant.SegmentID, NextAttemptNo: 2, CommandID: "retry"}); err == nil {
				t.Fatal("prelaunch rejection silently enabled retry before C05")
			}
			for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventSession, contract.EventResult, contract.EventUnknown, contract.EventExited} {
				late := f.event(kind)
				if kind == contract.EventSession {
					late.SessionKind, late.SessionID = f.sessionKind, f.sessionID
				}
				f.send(t, late)
				assertRejectedLedger(t, f, "settled", 0)
			}
			var bindings int
			if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempt_provider_bindings`).Scan(&bindings); err != nil || bindings != 0 {
				t.Fatalf("late init froze trusted binding: %d %v", bindings, err)
			}
		})
	}
}

func TestLaunchRejectedCannotEraseStartedUnknownOrConflictingEvidence(t *testing.T) {
	for _, kind := range []string{"spawned", "running", "session", "result", "question", "unknown", "process", "epoch", "command", "segment", "attempt", "revision", "run", "usage", "exit", "artifact"} {
		t.Run(kind, func(t *testing.T) {
			f := newTypedContinuationFixture(t, adapter.ProviderAGY)
			f.send(t, f.event(contract.EventPrepared))
			switch kind {
			case "spawned", "running":
				f.send(t, f.event(contract.EventSpawned))
				if kind == "running" {
					f.send(t, f.event(contract.EventRunning))
				}
			case "session":
				e := f.event(contract.EventSession)
				e.SessionKind, e.SessionID = f.sessionKind, f.sessionID
				f.send(t, e)
			case "result", "unknown":
				f.send(t, f.event(kind))
			case "question":
				f.start(t)
				q := f.event(contract.EventQuestion)
				q.QuestionID, q.QuestionRevision, q.QuestionKind = "q", 1, "technical"
				q.SessionKind, q.SessionID = f.sessionKind, f.sessionID
				f.send(t, q)
			}
			e := rejectionEvent(t, f)
			switch kind {
			case "process":
				e.Process = &contract.ProcessIdentity{PID: 10, Birth: "worker", PGID: 10}
			case "epoch":
				e.ExecutionEpoch = 2
			case "command":
				e.CommandID = "other"
			case "segment":
				e.SegmentID = "other"
			case "attempt":
				e.AttemptID = "other"
			case "revision":
				e.WorkRevision = 2
			case "run":
				e.RunID = "other"
			case "usage":
				e.ActiveMS = 1
			case "exit":
				x := -1
				e.ExitCode = &x
			case "artifact":
				e.Artifact = nil
			}
			if _, err := f.db.CommitHostEvent(context.Background(), e); err == nil {
				t.Fatalf("forged %s rejection settled reservation", kind)
			}
			var status string
			var used int64
			if err := f.db.sql.QueryRow(`SELECT status,used_ms FROM budget_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&status, &used); err != nil || status != "reserved" || used != 0 {
				t.Fatalf("reservation erased: %s/%d %v", status, used, err)
			}
		})
	}
}

func TestLaunchRejectedContinuationRetainsEarlierChargesAndResolvesConsumedAnswer(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderAGY)
	f.start(t)
	q := f.question(t, 1)
	if _, err := f.db.AnswerQuestion(context.Background(), AnswerSpec{TaskID: "task", WorkRevision: 1, QuestionID: q.QuestionID, QuestionRevision: 1, Answer: "continue"}); err != nil {
		t.Fatal(err)
	}
	g, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	f.grant = g
	f.send(t, f.event(contract.EventPrepared))
	f.send(t, rejectionEvent(t, f))
	assertRejectedLedger(t, f, "settled", 0)
	var total int64
	var status string
	if err = f.db.sql.QueryRow(`SELECT SUM(used_ms) FROM budget_runtime`).Scan(&total); err != nil || total != 200 {
		t.Fatalf("old attempt charges lost: %d %v", total, err)
	}
	if err = f.db.sql.QueryRow(`SELECT status FROM runtime_questions WHERE question_id=?`, q.QuestionID).Scan(&status); err != nil || status != "resolved" {
		t.Fatalf("consumed answer remained %s %v", status, err)
	}
}

func TestLaunchRejectedPreservesLegacyFailedExitedAccounting(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderAGY)
	f.send(t, f.event(contract.EventFailed))
	f.send(t, f.event(contract.EventExited))
	var used int64
	if err := f.db.sql.QueryRow(`SELECT used_ms FROM budget_runtime`).Scan(&used); err != nil || used != 1000 {
		t.Fatalf("legacy zero measurement changed: %d %v", used, err)
	}
}

func TestLaunchRejectedBridgeProjectionKeepsDiagnostic(t *testing.T) {
	e := contract.Event{Kind: "launch_rejected", EventID: "e", TaskID: "t", RunID: "r", PayloadHash: strings.Repeat("e", 64), Artifact: &contract.ArtifactRef{ID: "launch-failure", Path: filepath.Join("/private", "diagnostic"), Size: 100, SHA256: strings.Repeat("e", 64)}}
	// The same projection helper used by bridge ingest must admit the new terminal.
	summary, err := bridgeSummary(e.Kind)
	if err != nil || summary == "" {
		t.Fatalf("rejection dropped from bridge projection: %s %v", summary, err)
	}
	if _, err = json.Marshal(e); err != nil {
		t.Fatal(err)
	}
}
