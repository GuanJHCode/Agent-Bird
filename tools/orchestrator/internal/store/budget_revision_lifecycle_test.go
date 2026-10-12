package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func budgetRevisionFailedFixture(t *testing.T, policy bool, report bool) (budgetBatchFixture, BudgetRevisionCommand, contract.Event, RetrySpec) {
	t.Helper()
	task := policyTask("task", 3)
	if !policy {
		task.ExecutionPolicy = nil
	}
	return budgetRevisionFailedTaskFixture(t, task, report)
}

func budgetRevisionFailedTaskFixture(t *testing.T, task TaskSpec, report bool) (budgetBatchFixture, BudgetRevisionCommand, contract.Event, RetrySpec) {
	t.Helper()
	f, command := budgetRevisionRuntimeFixture(t, task)
	ctx := context.Background()
	f.start(t)
	if report {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "failure.txt")
		if err := os.WriteFile(path, []byte("failure"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.RegisterReportCapability(ctx, contract.ReportCapabilityRegistration{CapabilityID: f.grant.ReportCapabilityID, ProducerID: f.host, RunID: "run", TaskID: "task", AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, TokenHash: runtimeHash("report-token"), CapabilityDir: dir}); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]any{"status": "failed", "artifact": contract.ArtifactRef{ID: "failure", Path: path, Size: 7, SHA256: runtimeHash("failure")}})
		if _, err := f.db.CommitReportEvent(ctx, ReportEventSpec{CapabilityID: f.grant.ReportCapabilityID, Token: "report-token", EventID: "reported-failure", Sequence: 1, Kind: "failure", Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	f.sequence++
	event := contract.Event{Version: 1, ProducerID: f.host, EventID: "host-failure", RunID: "run", TaskID: "task", AttemptID: f.grant.AttemptID, SegmentID: f.grant.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: f.grant.CommandID, Sequence: f.sequence, Kind: contract.EventFailed, PayloadHash: strings.Repeat("a", 64)}
	if _, err := f.db.CommitHostEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	f.outcome = contract.EventFailed
	f.send(t, contract.EventExited, 50)
	f.assertTask(t, "failed")
	page, err := f.db.CollectPending(ctx, "task", "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	var source contract.Event
	for _, collected := range page.Events {
		if report && collected.EventID == "host-failure" {
			ackEvent(t, f.db, "task", collected, "handled")
		}
		if (!report && collected.EventID == "host-failure") || (report && collected.EventID == "reported-failure") {
			source = collected
		}
	}
	if source.ActionSlot == "" || source.Kind != contract.EventFailed {
		t.Fatal("fixture has no collected failure slot")
	}
	spec := RetrySpec{TaskID: "task", WorkRevision: 1, EventID: source.EventID, EventRevision: source.EventRevision, EventHash: source.PayloadHash, ActionSlot: source.ActionSlot, SegmentID: f.grant.SegmentID, NextAttemptNo: 2, CommandID: "explicit-retry"}
	return f, command, source, spec
}

func TestBudgetRevisionFailureRetainsPolicyHostUntilOwnerDecision(t *testing.T) {
	for _, source := range []string{"runtime", "report"} {
		t.Run(source, func(t *testing.T) {
			f, command, event, spec := budgetRevisionFailedFixture(t, true, source == "report")
			ctx := context.Background()
			ids, err := f.db.RetirableHosts(ctx)
			if err != nil || slices.Contains(ids, f.host) {
				t.Fatal("pending policy failure retired its only Host", ids, err)
			}
			if err = f.db.RetireHost(ctx, f.host); !errors.Is(err, ErrConflict) {
				t.Fatal("transaction did not recheck pending decision", err)
			}
			if _, err = f.db.ReviseBudget(ctx, command); err != nil {
				t.Fatal(err)
			}
			var decisions int
			if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM runtime_decisions WHERE action_slot=?`, event.ActionSlot).Scan(&decisions); err != nil || decisions != 0 {
				t.Fatal("revision consumed failure slot", decisions, err)
			}
			receipt, err := f.db.QueueRetry(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			ackEvent(t, f.db, "task", event, "handled")
			if replay, err := f.db.QueueRetry(ctx, spec); err != nil || replay != receipt {
				t.Fatal("ACK invalidated prior exact retry receipt", err)
			}
			duplicate := spec
			duplicate.CommandID = "another-delivery-of-same-decision"
			if replay, err := f.db.QueueRetry(ctx, duplicate); err != nil || replay != receipt {
				t.Fatal("ACK changed existing action-slot replay semantics", err)
			}
			duplicate.NextAttemptNo++
			if _, err := f.db.QueueRetry(ctx, duplicate); !errors.Is(err, ErrConflict) {
				t.Fatal("ACK allowed changed business decision", err)
			}
			var retryDecisions int
			if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM retry_decisions WHERE action_slot=?`, spec.ActionSlot).Scan(&retryDecisions); err != nil || retryDecisions != 1 {
				t.Fatal("delivery replay duplicated retry decision", retryDecisions, err)
			}
			if err = f.db.RetireHost(ctx, f.host); !errors.Is(err, ErrConflict) {
				t.Fatal("queued retry lost Host after ACK", err)
			}
			next, err := f.db.ClaimReady(ctx, f.host, 1, 1)
			if err != nil || next.AttemptID == f.grant.AttemptID || next.GrantedActiveMS != 400 {
				t.Fatal("explicit amended retry could not claim its new grant", next, err)
			}
			var used int64
			if err = f.db.sql.QueryRow(`SELECT used_ms FROM budget_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&used); err != nil || used != 50 {
				t.Fatal("amendment reset historical charge", used, err)
			}
		})
	}
}

func TestBudgetRevisionFailureACKClosesOnlyNewPolicyRetry(t *testing.T) {
	for _, decision := range []string{"handled", "waiting_user", "stale", "rejected"} {
		t.Run(decision, func(t *testing.T) {
			f, command, event, spec := budgetRevisionFailedFixture(t, true, false)
			ctx := context.Background()
			ackEvent(t, f.db, "task", event, decision)
			if _, err := f.db.ReviseBudget(ctx, command); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.QueueRetry(ctx, spec); !errors.Is(err, ErrConflict) {
				t.Fatal("ACK-disposed slot acquired first retry", err)
			}
			summary, err := f.db.SummarizeRun(ctx, "task", "owner", command.ControlToken)
			if err != nil || len(summary.Tasks) != 1 || slices.Contains(summary.Tasks[0].AllowedActions, "retry") {
				t.Fatal("summary advertises closed policy retry", summary, err)
			}
			ids, err := f.db.RetirableHosts(ctx)
			if err != nil || !slices.Contains(ids, f.host) {
				t.Fatal("disposed failed task retained unused Host", ids, err)
			}
			if err = f.db.RetireHost(ctx, f.host); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBudgetRevisionFailureLeavesLegacyRetirementAndRetryUnchanged(t *testing.T) {
	f, _, event, spec := budgetRevisionFailedFixture(t, false, false)
	ctx := context.Background()
	ids, err := f.db.RetirableHosts(ctx)
	if err != nil || !slices.Contains(ids, f.host) {
		t.Error("policy amendment changed legacy retirement", ids, err)
	}
	ackEvent(t, f.db, "task", event, "handled")
	if _, err = f.db.QueueRetry(ctx, spec); err != nil {
		t.Fatal("policy ACK rule leaked into legacy retry", err)
	}
}

func TestBudgetRevisionStatusAndSummaryExposeEffectiveCaps(t *testing.T) {
	for _, policy := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "policy"}[policy], func(t *testing.T) {
			f, command, _, _ := budgetRevisionFailedFixture(t, policy, false)
			ctx := context.Background()
			check := func(revision int, taskAttempts, groupAttempts int, attemptMS, taskMS, groupMS, segmentMS int64) {
				t.Helper()
				status, err := f.db.TaskSnapshot(ctx, "task")
				if err != nil {
					t.Fatal(err)
				}
				summary, err := f.db.SummarizeRun(ctx, "task", "owner", command.ControlToken)
				if err != nil || len(summary.Tasks) != 1 {
					t.Fatal(summary, err)
				}
				for _, view := range []any{status, summary.Tasks[0]} {
					raw, err := json.Marshal(view)
					if err != nil {
						t.Fatal(err)
					}
					var fields map[string]json.RawMessage
					if err = json.Unmarshal(raw, &fields); err != nil {
						t.Fatal(err)
					}
					budget, exists := fields["effective_budget"]
					if !policy {
						if exists {
							t.Fatal("legacy acquired effective budget projection", string(raw))
						}
						continue
					}
					var got map[string]any
					if !exists || json.Unmarshal(budget, &got) != nil {
						t.Fatal("missing effective budget", string(raw))
					}
					want := map[string]any{"budget_group_id": "task", "revision": float64(revision), "max_attempts": float64(taskAttempts), "group_max_attempts": float64(groupAttempts), "attempt_max_active_ms": float64(attemptMS), "task_max_active_ms": float64(taskMS), "group_max_active_ms": float64(groupMS), "segment_max_active_ms": float64(segmentMS)}
					for key, value := range want {
						if got[key] != value {
							t.Fatalf("%T %s=%v want %v", view, key, got[key], value)
						}
					}
				}
				if policy && (summary.Tasks[0].ExecutionPolicy == nil || summary.Tasks[0].ExecutionPolicy.GroupMaxAttempts != 3 || summary.Tasks[0].ExecutionPolicy.SegmentMaxActiveMS != 200) {
					t.Fatal("projection replaced immutable baseline")
				}
			}
			check(0, 3, 3, 500, 600, 1000, 200)
			if policy {
				if _, err := f.db.ReviseBudget(ctx, command); err != nil {
					t.Fatal(err)
				}
				check(1, 4, 4, 700, 900, 1500, 400)
			}
		})
	}
}

func TestBudgetRevisionPinEligibilityUsesCurrentCaps(t *testing.T) {
	for _, limit := range []string{"task_attempts", "group_attempts", "task_time", "group_time"} {
		t.Run(limit, func(t *testing.T) {
			task := policyTask("task", 3)
			task.ExecutionPolicy.Retry = nil
			switch limit {
			case "task_attempts":
				task.MaxAttempts = 1
			case "group_attempts":
				task.MaxAttempts, task.ExecutionPolicy.GroupMaxAttempts = 1, 1
			case "task_time":
				task.MaxActiveMS, task.ExecutionPolicy.TaskMaxActiveMS, task.ExecutionPolicy.SegmentMaxActiveMS = 50, 50, 50
			case "group_time":
				task.MaxActiveMS, task.ExecutionPolicy.TaskMaxActiveMS, task.ExecutionPolicy.GroupMaxActiveMS, task.ExecutionPolicy.SegmentMaxActiveMS = 50, 50, 50, 50
			}
			f, command, _, _ := budgetRevisionFailedTaskFixture(t, task, false)
			ctx := context.Background()
			page, err := f.db.CollectPending(ctx, "task", "", 0, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range page.Events {
				ackEvent(t, f.db, "task", event, "handled")
			}
			check := func(want bool) {
				t.Helper()
				items, err := f.db.ReleasableTasks(ctx)
				if err != nil || (len(items) == 1) != want {
					t.Fatalf("pin enumeration=%v want eligible=%v: %v", items, want, err)
				}
				// ReleaseConfigTaskUse rechecks this same predicate in its TX;
				// a stale enumeration must not authorize removal after amendment.
				tx, err := f.db.sql.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				var eligible bool
				err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id='task' AND `+releasableTaskPredicate+`)`).Scan(&eligible)
				if err != nil || eligible != want {
					t.Fatalf("TX pin eligibility=%v want %v: %v", eligible, want, err)
				}
			}
			check(true)
			if _, err := f.db.ReviseBudget(ctx, command); err != nil {
				t.Fatal(err)
			}
			check(false)
		})
	}
}

func TestBudgetRevisionInterruptedResumeKeepsAttemptAndCharge(t *testing.T) {
	f, command := budgetRevisionRuntimeFixture(t, policyTask("task", 3))
	ctx := context.Background()
	f.start(t)
	f.send(t, contract.EventStopped, 0)
	f.send(t, contract.EventExited, 150)
	f.assertTask(t, "interrupted")
	if _, err := f.db.ReviseBudget(ctx, command); err != nil {
		t.Fatal(err)
	}
	f.assertTask(t, "interrupted")
	if err := f.db.QueueResume(ctx, "task", 1); err != nil {
		t.Fatal(err)
	}
	next, err := f.db.ClaimReady(ctx, f.host, 1, 1)
	if err != nil || next.AttemptID != f.grant.AttemptID || next.SegmentID == f.grant.SegmentID || next.GrantedActiveMS != 400 {
		t.Fatalf("resume lost amended remaining budget or original attempt: %+v %v", next, err)
	}
	var used, granted int64
	if err = f.db.sql.QueryRow(`SELECT used_ms,granted_ms FROM budget_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&used, &granted); err != nil || used != 150 || granted != 200 {
		t.Fatal("resume amended historical ledger", used, granted, err)
	}
}

func TestBudgetRevisionUnknownKeepsReservationAndCannotRedispatch(t *testing.T) {
	f, command := budgetRevisionRuntimeFixture(t, policyTask("task", 3))
	ctx := context.Background()
	f.start(t)
	f.send(t, contract.EventUnknown, 0)
	if _, err := f.db.ReviseBudget(ctx, command); err != nil {
		t.Fatal(err)
	}
	f.assertTask(t, "unknown")
	if err := f.db.QueueResume(ctx, "task", 1); !errors.Is(err, ErrConflict) {
		t.Fatal("revision reactivated UNKNOWN", err)
	}
	// Leave a second global slot free: this must be a task-state refusal,
	// not merely rejection because UNKNOWN still occupies the first slot.
	if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, ErrNoReady) {
		t.Fatal("revision redispatched UNKNOWN", err)
	}
	var status string
	var granted, used int64
	var attempts int
	if err := f.db.sql.QueryRow(`SELECT status,granted_ms,used_ms,(SELECT COUNT(*) FROM attempts) FROM budget_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&status, &granted, &used, &attempts); err != nil || status != "reserved" || granted != 200 || used != 0 || attempts != 1 {
		t.Fatal("revision refunded or duplicated UNKNOWN", status, granted, used, attempts, err)
	}
}

func TestBudgetRevisionDoesNotReviveBlockedDependency(t *testing.T) {
	parent, child := policyTask("task", 3), policyTask("child", 3)
	parent.ExecutionPolicy.Retry, child.ExecutionPolicy.Retry = nil, nil
	child.Dependencies, child.BudgetGroupID = []string{"task"}, "task"
	f, command := budgetRevisionRuntimeFixture(t, parent, child)
	ctx := context.Background()
	if _, err := f.db.CommitHostEvent(ctx, policyRejection(t, &f, "probe_start_eagain")); err != nil {
		t.Fatal(err)
	}
	command.Request.Tasks = append(command.Request.Tasks, BudgetRevisionTask{TaskID: "child", WorkRevision: 1, MaxAttempts: 4, AttemptMaxActiveMS: 700, TaskMaxActiveMS: 900, SegmentMaxActiveMS: 400})
	if _, err := f.db.ReviseBudget(ctx, command); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id='child'`).Scan(&status); err != nil || status != "blocked_dependency" {
		t.Fatal("revision revived dependency", status, err)
	}
	if err := f.db.QueueResume(ctx, "child", 1); !errors.Is(err, ErrConflict) {
		t.Fatal("blocked dependency resumed", err)
	}
	if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, ErrNoReady) {
		t.Fatal("blocked dependency dispatched", err)
	}
}
