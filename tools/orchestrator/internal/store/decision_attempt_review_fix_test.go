package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Exercise the same two retry lifecycle as the existing third-attempt test,
// then reopen the actual schema12 DDL. Migration must not invent bindings.
func legacyReviewRetryThirdFixture(t *testing.T, consumed bool) (reviewRetryFixture, contract.LaunchCommand, RetrySpec, contract.LaunchCommand) {
	t.Helper()
	f := newReviewRetryFixtureArtifactMode(t, adapter.ProviderClaude, contract.EventStopped, nil, false, false, nil, nil, true)
	ctx := context.Background()
	if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
		t.Fatal(err)
	}
	second, err := f.db.ClaimReady(ctx, f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	e := contract.Event{Version: 1, ProducerID: f.host, RunID: second.RunID, TaskID: second.TaskID, AttemptID: second.AttemptID, SegmentID: second.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: second.CommandID, EventID: "second-stopped", Sequence: 7, Kind: contract.EventStopped, PayloadHash: runtimeHash("second stopped")}
	if _, err = f.db.CommitHostEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	done := e
	done.EventID, done.Sequence, done.Kind, done.ActiveMS = "second-exited", 8, contract.EventExited, 2345
	zero := 0
	done.ExitCode = &zero
	if _, err = f.db.CommitHostEvent(ctx, done); err != nil {
		t.Fatal(err)
	}
	page, err := f.db.CollectPending(ctx, "review", "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range page.Events {
		if candidate.EventID == e.EventID {
			e, found = candidate, true
		}
	}
	if !found {
		t.Fatal("original second failure not delivered")
	}
	spec := RetrySpec{TaskID: "review", WorkRevision: 1, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, SegmentID: e.SegmentID, NextAttemptNo: 3, CommandID: "retry-third"}
	if _, err = f.db.QueueRetry(ctx, spec); err != nil {
		t.Fatal(err)
	}
	var third contract.LaunchCommand
	if consumed {
		third, err = f.db.ClaimReady(ctx, f.host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
	}
	downgradePolicySchema(t, f.db)
	if _, err = f.db.sql.Exec(`DELETE FROM decision_attempt_bindings; DROP TABLE config_preparation_abandons; DROP TABLE decision_attempt_bindings; UPDATE schema_meta SET version=12`); err != nil {
		t.Fatal(err)
	}
	path := f.db.path
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Close() })
	if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM decision_attempt_bindings`) != 0 {
		t.Fatal("migration manufactured legacy target")
	}
	return f, second, spec, third
}

func TestLegacyReviewRetrySecondDecisionBindsNewAttemptAfterV12(t *testing.T) {
	f, second, spec, _ := legacyReviewRetryThirdFixture(t, false)
	third, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	var bound string
	if err = f.db.sql.QueryRow(`SELECT attempt_id FROM decision_attempt_bindings WHERE action_slot=? AND task_id='review'`, spec.ActionSlot).Scan(&bound); err != nil || bound != third.AttemptID || bound == second.AttemptID {
		t.Errorf("second retry consumed own source: target=%s source=%s third=%s err=%v", bound, second.AttemptID, third.AttemptID, err)
	}
	p2, _ := adapter.DecodeInvocationPayload(second.AdapterPayload)
	p3, _ := adapter.DecodeInvocationPayload(third.AdapterPayload)
	if p2.Directory == p3.Directory || filepath.Dir(p2.Directory) != filepath.Dir(p3.Directory) || !reflect.DeepEqual(third.Input, f.grant.Input) {
		t.Error("third retry lost saved input/profile or reused source directory")
	}
	var attempts, ordinal int
	var used int64
	if err = f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM attempts WHERE task_id='review'),(SELECT attempt_no FROM attempts WHERE id=?),(SELECT SUM(used_ms) FROM budget_runtime WHERE budget_group_id='review')`, third.AttemptID).Scan(&attempts, &ordinal, &used); err != nil || attempts != 3 || ordinal != 3 || used != 3579 {
		t.Fatalf("ordinal/budget=%d/%d/%d err=%v", attempts, ordinal, used, err)
	}
}

func mutateLegacyReviewSource(t *testing.T, f reviewRetryFixture, slot, kind string) {
	t.Helper()
	var err error
	switch kind {
	case "hash":
		_, err = f.db.sql.Exec(`UPDATE retry_decisions SET source_event_hash=? WHERE action_slot=?`, strings.Repeat("f", 64), slot)
	case "slot":
		_, err = f.db.sql.Exec(`UPDATE retry_decisions SET action_slot=action_slot||'-foreign' WHERE action_slot=?`, slot)
	case "segment":
		_, err = f.db.sql.Exec(`UPDATE retry_decisions SET source_segment_id=(SELECT segment_id FROM runtime_events WHERE task_id='validate' AND body_json LIKE '%"kind":"result"%') WHERE action_slot=?`, slot)
	case "producer", "command", "epoch":
		var event contract.Event
		var body string
		if err = f.db.sql.QueryRow(`SELECT e.body_json FROM runtime_events e JOIN retry_decisions d ON d.source_event_id=e.event_id WHERE d.action_slot=?`, slot).Scan(&body); err != nil || json.Unmarshal([]byte(body), &event) != nil {
			t.Fatal("original source", err)
		}
		if kind == "producer" {
			event.ProducerID = "foreign-host"
		} else if kind == "command" {
			event.CommandID = "foreign-command"
		} else {
			event.ExecutionEpoch = 999
		}
		raw, _ := json.Marshal(event)
		_, err = f.db.sql.Exec(`UPDATE runtime_events SET body_json=? WHERE event_id=?`, string(raw), event.EventID)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestLegacyReviewRetryConsumedTargetAuthenticatesSource(t *testing.T) {
	for _, kind := range []string{"hash", "slot", "segment", "producer", "command", "epoch"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviewRetryFixture(t, contract.EventStopped)
			ctx := context.Background()
			if _, err := f.db.QueueRetry(ctx, f.spec); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.sql.Exec(`DELETE FROM decision_attempt_bindings`); err != nil {
				t.Fatal(err)
			}
			mutateLegacyReviewSource(t, f, f.spec.ActionSlot, kind)
			before := continuationQuotaState(t, f.db)
			if _, err := f.db.PendingLaunches(ctx, f.host, 1); !errors.Is(err, errLegacyDecisionAmbiguous) {
				t.Errorf("changed consumed source %s accepted: %v", kind, err)
			}
			if continuationQuotaState(t, f.db) != before {
				t.Error("refused consumed source left a binding/epoch/ledger mutation")
			}
		})
	}
}

func TestLegacyReviewRetryPendingChecksHistoricalCommitments(t *testing.T) {
	f, _, _, _ := legacyReviewRetryThirdFixture(t, false)
	mutateLegacyReviewSource(t, f, f.spec.ActionSlot, "hash")
	before := continuationQuotaState(t, f.db)
	if _, err := f.db.ClaimReady(context.Background(), f.host, 1, 2); !errors.Is(err, errLegacyDecisionAmbiguous) {
		t.Errorf("malformed historical relevant commitment admitted: %v", err)
	}
	if continuationQuotaState(t, f.db) != before {
		t.Error("refused relevant commitment advanced ledger")
	}
}

func TestLegacyReviewRetrySameHashCompetingDecisionsRefuseGrant(t *testing.T) {
	f, _, _, third := legacyReviewRetryThirdFixture(t, true)
	before := continuationQuotaState(t, f.db)
	if _, err := f.db.PendingLaunches(context.Background(), f.host, 1); !errors.Is(err, contract.ErrHostArtifactGrant) {
		t.Errorf("same-hash competing old decisions guessed target: %v", err)
	}
	// Missing artifact authority refuses live replay first. The independent
	// historical proof must still reject competing decision ownership.
	third.HostArtifacts = nil
	if err := f.db.sql.QueryRow(`SELECT launch_intent_hash FROM segment_runtime WHERE segment_id=?`, third.SegmentID).Scan(&third.LaunchIntentHash); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = f.db.reconcileLegacyGrant(context.Background(), tx, &third)
	_ = tx.Rollback()
	if !errors.Is(err, errLegacyDecisionAmbiguous) {
		t.Fatal("competing historical commitments were not refused", err)
	}
	if continuationQuotaState(t, f.db) != before {
		t.Error("ambiguous consumed history advanced ledger")
	}
}

func reportFailureRetryFixture(t *testing.T, advance ...bool) (*DB, string, contract.LaunchCommand, RetrySpec, ReportEventSpec) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	plan, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	g, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	capDir := filepath.Join(t.TempDir(), "capability")
	if err = os.Mkdir(capDir, 0700); err != nil {
		t.Fatal(err)
	}
	token := "original-report-token"
	if _, err = db.RegisterReportCapability(ctx, contract.ReportCapabilityRegistration{CapabilityID: g.ReportCapabilityID, ProducerID: host, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, TokenHash: runtimeHash(token), CapabilityDir: capDir}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"failure":"measured"}`)
	artifact := filepath.Join(capDir, "failure.json")
	if err = os.WriteFile(artifact, body, 0600); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"status": "failed", "artifact": contract.ArtifactRef{Path: artifact, Size: int64(len(body)), SHA256: runtimeHash(string(body))}})
	report := ReportEventSpec{CapabilityID: g.ReportCapabilityID, Token: token, EventID: "worker-failure", Sequence: 1, Kind: "failure", Payload: payload}
	if _, err = db.CommitReportEvent(ctx, report); err != nil {
		t.Fatal(err)
	}
	epoch := uint64(1)
	if len(advance) != 0 && advance[0] {
		if err = db.BeginCoordinatorEpoch(ctx, 2); err != nil {
			t.Fatal(err)
		}
		if _, err = db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "gen", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 2); err != nil {
			t.Fatal(err)
		}
		if err = db.BeginHostReconcile(ctx, host); err != nil {
			t.Fatal(err)
		}
		if _, err = db.RegisterReportCapability(ctx, contract.ReportCapabilityRegistration{CapabilityID: g.ReportCapabilityID, ProducerID: host, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 2, TokenHash: runtimeHash(token), CapabilityDir: capDir}); err != nil {
			t.Fatal(err)
		}
		if err = db.FinishHostReconcile(ctx, host); err != nil {
			t.Fatal(err)
		}
		if grants, err := db.PendingLaunches(ctx, host, 2); err != nil || len(grants) != 1 || grants[0].SegmentID != g.SegmentID {
			t.Fatal("original launch_requested replay", err)
		}
		epoch = 2
		var historical string
		var event contract.Event
		var segmentEpoch uint64
		if err = db.sql.QueryRow(`SELECT e.body_json,s.execution_epoch FROM report_events e JOIN report_capabilities c USING(capability_id) JOIN segment_runtime s ON s.segment_id=c.segment_id WHERE e.event_id=?`, report.EventID).Scan(&historical, &segmentEpoch); err != nil || json.Unmarshal([]byte(historical), &event) != nil || event.ExecutionEpoch != 1 || segmentEpoch != 2 {
			t.Fatal("report body rewritten or segment replay did not advance", segmentEpoch, err)
		}
	}
	exit := 1
	for i, kind := range []string{contract.EventFailed, contract.EventExited} {
		e := contract.Event{Version: 1, ProducerID: host, EventID: "host-" + kind, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: epoch, CommandID: g.CommandID, Sequence: int64(i + 1), Kind: kind, ExitCode: &exit, PayloadHash: runtimeHash(kind)}
		if kind == contract.EventExited {
			e.ActiveMS = 1
		}
		if _, err = db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.CollectPending(ctx, g.TaskID, "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	var source contract.Event
	for _, e := range page.Events {
		if e.EventID == report.EventID {
			source = e
		}
	}
	if source.ProducerID != g.ReportCapabilityID || source.CommandID != "" {
		t.Fatal("real report namespace was rewritten", source.ProducerID, source.CommandID)
	}
	spec := RetrySpec{TaskID: g.TaskID, WorkRevision: g.WorkRevision, EventID: source.EventID, EventRevision: source.EventRevision, EventHash: source.PayloadHash, ActionSlot: source.ActionSlot, SegmentID: source.SegmentID, NextAttemptNo: 2, CommandID: "retry-report"}
	if _, err = db.QueueRetry(ctx, spec); err != nil {
		t.Fatal(err)
	}
	return db, host, g, spec, report
}

func TestReportFailureRetryBindsCapabilityNamespace(t *testing.T) {
	for _, status := range []string{"registered", "revoked"} {
		t.Run(status, func(t *testing.T) {
			db, host, original, spec, report := reportFailureRetryFixture(t)
			if status == "revoked" {
				if _, err := db.sql.Exec(`UPDATE report_capabilities SET status='revoked' WHERE capability_id=?`, original.ReportCapabilityID); err != nil {
					t.Fatal(err)
				}
				if ack, err := db.CommitReportEvent(context.Background(), report); err != nil || ack.Status != "durable" {
					t.Fatal("existing revoked exact report replay", err)
				}
			}
			next, err := db.ClaimReady(context.Background(), host, 1, 2)
			if err != nil {
				t.Fatal("legitimate report retry refused", err)
			}
			var bound, afterStatus string
			if err = db.sql.QueryRow(`SELECT b.attempt_id,c.status FROM decision_attempt_bindings b JOIN report_capabilities c ON c.capability_id=? WHERE b.action_slot=?`, original.ReportCapabilityID, spec.ActionSlot).Scan(&bound, &afterStatus); err != nil || bound != next.AttemptID || bound == original.AttemptID || afterStatus != status {
				t.Fatal("report provenance binding or revocation changed", bound, afterStatus, err)
			}
		})
	}
}

func TestReportFailureRetryKeepsHistoricalEpochAfterAuthenticatedReplay(t *testing.T) {
	db, host, original, spec, _ := reportFailureRetryFixture(t, true)
	next, err := db.ClaimReady(context.Background(), host, 2, 2)
	if err != nil {
		t.Fatal("authenticated epoch advance refused historical report", err)
	}
	var bound string
	if err = db.sql.QueryRow(`SELECT attempt_id FROM decision_attempt_bindings WHERE action_slot=?`, spec.ActionSlot).Scan(&bound); err != nil || bound != next.AttemptID || bound == original.AttemptID {
		t.Fatal("historical report lost exact target", bound, err)
	}
}

func TestReportFailureRetryRejectsCapabilityAndEventDriftAtomically(t *testing.T) {
	for _, kind := range []string{"cap_host", "cap_run", "cap_task", "cap_attempt", "cap_segment", "cap_revision", "cap_epoch", "cap_pending", "event_cap", "event_command", "event_run", "event_attempt", "event_segment", "event_revision", "event_epoch_zero", "event_epoch_future"} {
		t.Run(kind, func(t *testing.T) {
			db, host, original, _, report := reportFailureRetryFixture(t)
			var err error
			if strings.HasPrefix(kind, "cap_") {
				var query string
				switch kind {
				case "cap_host":
					query = `UPDATE report_capabilities SET producer_id='foreign' WHERE capability_id=?`
				case "cap_run":
					query = `UPDATE report_capabilities SET run_id='foreign' WHERE capability_id=?`
				case "cap_task":
					query = `UPDATE report_capabilities SET task_id='foreign' WHERE capability_id=?`
				case "cap_attempt":
					query = `UPDATE report_capabilities SET attempt_id='foreign' WHERE capability_id=?`
				case "cap_segment":
					query = `UPDATE report_capabilities SET segment_id=(SELECT segment_id FROM segment_runtime LIMIT 1)||'-foreign' WHERE capability_id=?`
				case "cap_revision":
					query = `UPDATE report_capabilities SET work_revision=2 WHERE capability_id=?`
				case "cap_epoch":
					query = `UPDATE report_capabilities SET execution_epoch=0 WHERE capability_id=?`
				case "cap_pending":
					query = `UPDATE report_capabilities SET status='pending' WHERE capability_id=?`
				}
				if kind == "cap_host" || kind == "cap_segment" {
					foreignHost, foreignSegment := foreignReportScope(t, db)
					if kind == "cap_host" {
						_, err = db.sql.Exec(`UPDATE report_capabilities SET producer_id=? WHERE capability_id=?`, foreignHost, original.ReportCapabilityID)
					} else {
						_, err = db.sql.Exec(`UPDATE report_capabilities SET segment_id=? WHERE capability_id=?`, foreignSegment, original.ReportCapabilityID)
					}
					query = ""
				}
				if query != "" {
					_, err = db.sql.Exec(query, original.ReportCapabilityID)
				}
			} else {
				var body string
				var event contract.Event
				if err = db.sql.QueryRow(`SELECT body_json FROM report_events WHERE event_id=?`, report.EventID).Scan(&body); err != nil || json.Unmarshal([]byte(body), &event) != nil {
					t.Fatal("original report", err)
				}
				switch kind {
				case "event_cap":
					event.ProducerID = "foreign-capability"
				case "event_command":
					event.CommandID = original.CommandID
				case "event_run":
					event.RunID = "foreign"
				case "event_attempt":
					event.AttemptID = "foreign"
				case "event_segment":
					event.SegmentID = "foreign"
				case "event_revision":
					event.WorkRevision++
				case "event_epoch_zero":
					event.ExecutionEpoch = 0
				case "event_epoch_future":
					event.ExecutionEpoch = 999
				}
				raw, _ := json.Marshal(event)
				_, err = db.sql.Exec(`UPDATE report_events SET body_json=? WHERE event_id=?`, string(raw), report.EventID)
			}
			if err != nil {
				t.Fatal("drift fixture", err)
			}
			before := continuationQuotaState(t, db)
			if _, err := db.ClaimReady(context.Background(), host, 1, 2); !errors.Is(err, ErrConflict) {
				t.Errorf("capability/event drift %s accepted: %v", kind, err)
			}
			if continuationQuotaState(t, db) != before {
				t.Error("report provenance refusal partially changed ledger")
			}
		})
	}
}

func foreignReportScope(t *testing.T, db *DB) (string, string) {
	t.Helper()
	ctx := context.Background()
	plan, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "foreign-run", ControllerThread: "foreign-owner", PlanRevision: 1, OriginContextID: "foreign-origin", OriginPID: 2, OriginBirth: "foreign-birth"}, Host: HostLaunchSpec{OriginContextID: "foreign-origin", HostGeneration: "foreign-gen", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{{ID: "foreign-task", RunID: "foreign-run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "foreign-origin", OriginPID: 2, OriginBirth: "foreign-birth", HostGeneration: "foreign-gen", PID: 11, Birth: "foreign-host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Create a real legacy segment without asking the managed global FIFO
	// to skip the earlier original ready retry. Its FK identity is genuine;
	// it supplies no matching Host/report runtime tuple.
	if err = db.CreateRun(ctx, RunSpec{ID: "legacy-foreign-run", ControllerThread: "legacy", PlanRevision: 1, OriginContextID: "legacy", OriginPID: 3, OriginBirth: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateTask(ctx, TaskSpec{ID: "legacy-foreign-task", RunID: "legacy-foreign-run", MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.StartAttempt(ctx, "legacy-foreign-task", "foreign-segment"); err != nil {
		t.Fatal(err)
	}
	return host, "foreign-segment"
}
