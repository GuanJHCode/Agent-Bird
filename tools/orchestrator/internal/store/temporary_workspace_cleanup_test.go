package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func temporaryCleanupFixture(t *testing.T) (*DB, contract.LaunchCommand, string, contract.Event) {
	t.Helper()
	db, g, token := diagnosticOutcomeFixture(t, false)
	// Exercise event admission without running a Provider. The native Host test
	// separately verifies the real accepted-input/materialization lifecycle.
	if _, err := db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id=?`, `{"kind":"candidate","directory":"/private/tmp/owned-validation","candidate_action":{"version":1,"operation":"validate","auto_directory":true,"source_task":"producer","command":["/usr/bin/true"]}}`, g.TaskID); err != nil {
		t.Fatal(err)
	}
	e := diagnosticOutcomeEvent(g, contract.EventTemporaryWorkspaceCleanup, 3)
	e.TemporaryWorkspaceCleanup = &contract.TemporaryWorkspaceCleanup{Version: 1, Operation: "validate", State: "pending", Worktree: "/private/tmp/owned-validation.attempt-" + runtimeHash(g.RunID + "\x00" + g.TaskID + "\x00" + g.AttemptID + "\x00" + g.SegmentID)[:24], ReceiptSHA256: strings.Repeat("a", 64)}
	e.PayloadHash, _ = contract.TemporaryWorkspaceCleanupDigest(*e.TemporaryWorkspaceCleanup)
	return db, g, token, e
}

func publishTemporaryCleanupBusiness(t *testing.T, db *DB, g contract.LaunchCommand) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.CommitHostEvent(ctx, diagnosticOutcomeEvent(g, contract.EventResult, 1)); err != nil {
		t.Fatal(err)
	}
	exited := diagnosticOutcomeEvent(g, contract.EventExited, 2)
	code := 0
	exited.ExitCode, exited.ActiveMS = &code, 1
	if _, err := db.CommitHostEvent(ctx, exited); err != nil {
		t.Fatal(err)
	}
}

func TestTemporaryWorkspaceCleanupAdmission(t *testing.T) {
	db, g, _, pending := temporaryCleanupFixture(t)
	ctx := context.Background()
	premature := pending
	premature.Sequence = 1
	if _, err := db.CommitHostEvent(ctx, premature); err == nil {
		t.Fatal("cleanup preceded durable business outcome")
	}
	publishTemporaryCleanupBusiness(t, db, g)
	for _, mode := range []string{"removed_without_pending", "wrong_path", "wrong_receipt_digest", "question_fields", "manual_directory", "unknown_status", "unsettled_budget", "failed_exit"} {
		t.Run(mode, func(t *testing.T) {
			e := pending
			c := *pending.TemporaryWorkspaceCleanup
			e.TemporaryWorkspaceCleanup = &c
			tx, err := db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			switch mode {
			case "removed_without_pending":
				c.State = "removed"
			case "wrong_path":
				c.Worktree = "/private/tmp/foreign"
			case "wrong_receipt_digest":
				c.ReceiptSHA256 = "bad"
			case "question_fields":
				e.QuestionID = "foreign"
			case "manual_directory":
				_, err = tx.Exec(`UPDATE task_runtime SET adapter_payload=json_set(adapter_payload,'$.candidate_action.auto_directory',json('false'))`)
			case "unknown_status":
				_, err = tx.Exec(`UPDATE segment_runtime SET status='unknown'`)
			case "unsettled_budget":
				_, err = tx.Exec(`UPDATE budget_runtime SET status='reserved'`)
			case "failed_exit":
				_, err = tx.Exec(`UPDATE runtime_events SET body_json=json_set(body_json,'$.exit_code',1) WHERE json_extract(body_json,'$.kind')='exited'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			e.PayloadHash, _ = contract.TemporaryWorkspaceCleanupDigest(c)
			if err = contract.ValidateTemporaryWorkspaceCleanupEvent(e); err == nil {
				err = validateTemporaryWorkspaceCleanup(ctx, tx, e, false)
			}
			if err == nil {
				t.Fatal("invalid cleanup accepted", mode)
			}
		})
	}
	if _, err := db.CommitHostEvent(ctx, pending); err != nil {
		t.Fatal("rejected events changed event sequence", err)
	}
	changed := pending
	c := *pending.TemporaryWorkspaceCleanup
	changed.TemporaryWorkspaceCleanup = &c
	changed.Sequence, changed.EventID = 4, "cleanup-4"
	c.State, c.ReceiptSHA256 = "removed", strings.Repeat("b", 64)
	changed.PayloadHash, _ = contract.TemporaryWorkspaceCleanupDigest(c)
	if _, err := db.CommitHostEvent(ctx, changed); err == nil {
		t.Fatal("outcome changed receipt")
	}
}

func TestTemporaryWorkspaceCleanupProjectionAndReplay(t *testing.T) {
	db, g, token, pending := temporaryCleanupFixture(t)
	ctx := context.Background()
	publishTemporaryCleanupBusiness(t, db, g)
	var original string
	if err := db.sql.QueryRow(`SELECT body_json FROM runtime_events WHERE sequence=1`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommitHostEvent(ctx, pending); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.TaskSnapshot(ctx, g.TaskID)
	if err != nil || snapshot.TemporaryWorkspaceCleanup == nil || snapshot.TemporaryWorkspaceCleanup.State != "pending" {
		t.Fatal(snapshot, err)
	}
	removed := pending
	c := *pending.TemporaryWorkspaceCleanup
	removed.TemporaryWorkspaceCleanup = &c
	c.State = "removed"
	removed.Sequence, removed.EventID = 4, "cleanup-removed"
	removed.PayloadHash, _ = contract.TemporaryWorkspaceCleanupDigest(c)
	ack, err := db.CommitHostEvent(ctx, removed)
	if err != nil {
		t.Fatal(err)
	}
	again, err := db.CommitHostEvent(ctx, removed)
	if err != nil || again != ack {
		t.Fatal("exact cleanup replay changed", again, err)
	}
	summary, err := db.SummarizeRun(ctx, g.TaskID, "controller", token)
	if err != nil || len(summary.Tasks) != 1 || summary.Tasks[0].Status != "result_ready" || summary.Tasks[0].ActiveSegments != 0 || summary.Tasks[0].TemporaryWorkspaceCleanup == nil || summary.Tasks[0].TemporaryWorkspaceCleanup.State != "removed" {
		t.Fatal(summary, err)
	}
	snapshot, err = db.TaskSnapshot(ctx, g.TaskID)
	if err != nil || snapshot.Status != "result_ready" || snapshot.TemporaryWorkspaceCleanup.State != "removed" {
		t.Fatal(snapshot, err)
	}
	page, err := db.CollectPending(ctx, g.TaskID, "", 32, false)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != contract.EventResult {
		t.Fatal("cleanup leaked into business ACK queue", page, err)
	}
	var after, budget string
	if err = db.sql.QueryRow(`SELECT body_json FROM runtime_events WHERE sequence=1`).Scan(&after); err != nil || after != original {
		t.Fatal("business event changed", err)
	}
	if err = db.sql.QueryRow(`SELECT status FROM budget_runtime WHERE segment_id=?`, g.SegmentID).Scan(&budget); err != nil || budget != "settled" {
		t.Fatal(budget, err)
	}
	third := removed
	third.Sequence, third.EventID = 5, "cleanup-extra"
	if _, err = db.CommitHostEvent(ctx, third); err == nil {
		t.Fatal("unbounded cleanup observations accepted")
	}
	if _, err = db.sql.Exec(`UPDATE task_runtime SET work_revision=work_revision+1`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CommitHostEvent(ctx, removed); err != nil {
		t.Fatal("exact replay invalidated by rework", err)
	}
	// Old JSON is still readable and carries no invented cleanup observation.
	raw, _ := json.Marshal(TaskSnapshot{TaskID: "old"})
	if strings.Contains(string(raw), "temporary_workspace_cleanup") {
		t.Fatal(string(raw))
	}
}

func TestTemporaryWorkspaceCleanupSurvivesOwnerRework(t *testing.T) {
	db, g, _, pending := temporaryCleanupFixture(t)
	ctx := context.Background()
	publishTemporaryCleanupBusiness(t, db, g)
	if _, err := db.sql.Exec(`UPDATE task_runtime SET work_revision=work_revision+1,adapter_payload='{"provider":"fake"}'; UPDATE tasks SET status='ready'; UPDATE attempts SET status='failed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommitHostEvent(ctx, pending); err != nil {
		t.Fatal("late observation blocked Host replay", err)
	}
	c := *pending.TemporaryWorkspaceCleanup
	c.Code = "cleanup_pending"
	pending.TemporaryWorkspaceCleanup = &c
	pending.Sequence, pending.EventID = 4, "cleanup-pending-final"
	pending.PayloadHash, _ = contract.TemporaryWorkspaceCleanupDigest(c)
	if _, err := db.CommitHostEvent(ctx, pending); err != nil {
		t.Fatal(err)
	}
	s, err := db.TaskSnapshot(ctx, g.TaskID)
	if err != nil || s.Status != "ready" || s.WorkRevision != 2 || s.TemporaryWorkspaceCleanup == nil || s.TemporaryWorkspaceCleanup.Code != "cleanup_pending" {
		t.Fatal("observation altered replacement business state", s, err)
	}
}
