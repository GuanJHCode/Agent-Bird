package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"testing"
	"time"
)

func gcControl(t *testing.T, f *diagnosticsReadFixture, action string, revision int64) DiagnosticsGCStatus {
	t.Helper()
	s, e := f.db.ControlDiagnosticsGC(context.Background(), "controller", f.token, action, DiagnosticsGCRequest{Version: 1, RunID: f.grant.RunID, ExpectedRevision: revision})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func gcConsumed(t *testing.T, f *diagnosticsReadFixture, at string) {
	t.Helper()
	var rev int64
	var slot, hash, id string
	if e := f.db.sql.QueryRow(`SELECT event_revision,action_slot,payload_hash,event_id FROM runtime_events WHERE attempt_id=? AND sequence=1`, f.grant.AttemptID).Scan(&rev, &slot, &hash, &id); e != nil {
		t.Fatal(e)
	}
	if e := f.db.AckDecisions(context.Background(), f.grant.TaskID, "gc-consumed", runtimeHash("proof"), []AckDecision{{EventID: id, EventRevision: rev, ActionSlot: slot, EventHash: hash, Decision: "handled", CommandID: "gc-ack"}}); e != nil {
		t.Fatal(e)
	}
	if at != "" {
		if _, e := f.db.sql.Exec(`UPDATE diagnostics_consumptions SET consumed_at=?`, at); e != nil {
			t.Fatal(e)
		}
	}
}
func TestDiagnosticsGCTTLAndConsumption(t *testing.T) {
	t.Run("completion_order", testDiagnosticsGCConsumptionCompletionOrder)
	for _, tc := range []struct {
		name, consumed string
		deleted        bool
	}{{"unconsumed", "", false}, {"unknown", "2020-01-01T00:00:00Z", false}, {"exact_boundary", "2020-01-05T00:00:00Z", false}, {"recent_consumption", "2020-01-10T00:00:00Z", false}, {"expired", "2020-01-01T00:00:00Z", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := retentionFixture(t)
			if tc.consumed != "" {
				gcConsumed(t, f, tc.consumed)
			}
			if tc.name == "unknown" {
				if _, e := f.db.sql.Exec(`UPDATE tasks SET status='unknown' WHERE id=?`, f.grant.TaskID); e != nil {
					t.Fatal(e)
				}
			}
			gcControl(t, f, "enable", 0)
			if e := f.db.RunDiagnosticsGC(context.Background(), time.Date(2020, 1, 12, 0, 0, 0, 0, time.UTC)); e != nil {
				t.Fatal(e)
			}
			var n int
			if e := f.db.sql.QueryRow(`SELECT count(*) FROM diagnostic_deletions`).Scan(&n); e != nil {
				t.Fatal(e)
			}
			if (n == 1) != tc.deleted {
				t.Fatalf("deleted=%d want %v", n, tc.deleted)
			}
		})
	}
}
func TestDiagnosticsGCDefaultDisableAndOwnerDrift(t *testing.T) {
	f, _ := retentionFixture(t)
	gcConsumed(t, f, "2020-01-01T00:00:00Z")
	if s := gcControl(t, f, "status", 0); s.Enabled || s.Revision != 0 {
		t.Fatal(s)
	}
	if _, e := f.db.ControlDiagnosticsGC(context.Background(), "foreign", f.token, "enable", DiagnosticsGCRequest{Version: 1, RunID: f.grant.RunID}); !errors.Is(e, CodeError("owner_mismatch")) {
		t.Fatal(e)
	}
	gcControl(t, f, "enable", 0)
	previousLimit := f.db.runControlLimit
	f.db.runControlLimit = 0
	gcControl(t, f, "disable", 1)
	f.db.runControlLimit = previousLimit
	if e := f.db.RunDiagnosticsGC(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	var n int
	f.db.sql.QueryRow(`SELECT count(*) FROM diagnostic_deletions`).Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
	gcControl(t, f, "enable", 2)
	if _, e := f.db.sql.Exec(`UPDATE runs SET origin_birth='rebound' WHERE id=?`, f.grant.RunID); e != nil {
		t.Fatal(e)
	}
	if e := f.db.RunDiagnosticsGC(context.Background(), time.Now()); !errors.Is(e, CodeError("diagnostics_gc_owner_changed")) {
		t.Fatal(e)
	}
	if s := gcControl(t, f, "status", 0); s.Enabled || s.Stopped != "diagnostics_gc_owner_changed" {
		t.Fatal(s)
	}
}
func TestDiagnosticsGCSavedExactPlanReplay(t *testing.T) {
	f, _ := retentionFixture(t)
	gcConsumed(t, f, "2020-01-01T00:00:00Z")
	gcControl(t, f, "enable", 0)
	ctx := context.Background()
	p, e := f.db.prepareDiagnosticsGC(ctx, time.Date(2020, 1, 12, 0, 0, 0, 0, time.UTC))
	if e != nil {
		t.Fatal(e)
	}
	if p == nil || len(p.plan.Items) != 1 {
		t.Fatal(p)
	}
	var before string
	f.db.sql.QueryRow(`SELECT plan_json FROM diagnostics_gc_policies`).Scan(&before)
	var exact DiagnosticsRetentionPlan
	if json.Unmarshal([]byte(before), &exact) != nil {
		t.Fatal(before)
	}
	path := f.db.path
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	f.db, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.db.RunDiagnosticsGC(ctx, time.Date(2030, 1, 12, 0, 0, 0, 0, time.UTC)); e != nil {
		t.Fatal(e)
	}
	var saved string
	f.db.sql.QueryRow(`SELECT request_json FROM diagnostic_deletions`).Scan(&saved)
	want, _ := json.Marshal(exact.Items[0])
	if saved != string(want) {
		t.Fatal("replanned saved request", saved, string(want))
	}
}

func TestDiagnosticsGCDisableAdmissionAndPreparedRecovery(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_intent", true: "prepared"}[prepared], func(t *testing.T) {
			f, _ := retentionFixture(t)
			gcConsumed(t, f, "2020-01-01T00:00:00Z")
			gcControl(t, f, "enable", 0)
			batch, e := f.db.prepareDiagnosticsGC(context.Background(), time.Now())
			if e != nil {
				t.Fatal(e)
			}
			ctx := context.WithValue(context.Background(), diagnosticsGCKey{}, batch.policy)
			removed := false
			_, e = f.db.deleteDiagnostics(ctx, "controller", "", batch.plan.Items[0], func(ctx context.Context, root string, scope diagnostics.Scope, command string, plan []byte) (diagnosticDeletionLease, error) {
				l, e := diagnostics.OpenDeletion(ctx, root, scope, command, plan)
				if e != nil {
					return nil, e
				}
				if !prepared {
					gcControl(t, f, "disable", 1)
				}
				return retentionObserveLease{diagnosticDeletionLease: l, removed: &removed, fail: true}, nil
			})
			if e == nil {
				t.Fatal("expected interrupted admission/removal")
			}
			if prepared {
				gcControl(t, f, "disable", 1)
			} else if removed {
				t.Fatal("disabled fresh admission crossed Remove")
			}
			if e = f.db.RunDiagnosticsGC(context.Background(), time.Now()); e != nil {
				t.Fatal(e)
			}
			var n int
			f.db.sql.QueryRow(`SELECT count(*) FROM diagnostic_deletions WHERE phase='removed'`).Scan(&n)
			if (n == 1) != prepared {
				t.Fatal(n, prepared)
			}
		})
	}
}
func testDiagnosticsGCConsumptionCompletionOrder(t *testing.T) {
	for _, ackFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete_then_ack", true: "ack_then_complete"}[ackFirst], func(t *testing.T) {
			f, _ := retentionFixture(t)

			if _, e := f.db.sql.Exec(`UPDATE tasks SET status='result_ready'; UPDATE task_runtime SET completion_policy='owner_review'`); e != nil {
				t.Fatal(e)
			}
			spec := ReviewSpec{TaskID: f.grant.TaskID, WorkRevision: f.grant.WorkRevision, Decision: "accept", CommandID: "gc-accept"}
			if e := f.db.sql.QueryRow(`SELECT event_id,event_revision,payload_hash,action_slot FROM runtime_events WHERE attempt_id=? AND sequence=1`, f.grant.AttemptID).Scan(&spec.EventID, &spec.EventRevision, &spec.EventHash, &spec.ActionSlot); e != nil {
				t.Fatal(e)
			}
			accept := func() {
				t.Helper()
				if _, e := f.db.ReviewResult(context.Background(), spec); e != nil {
					t.Fatal(e)
				}
			}
			if !ackFirst {
				accept()
			}
			gcConsumed(t, f, "")
			if ackFirst {
				accept()
			}

			var stamp string
			f.db.sql.QueryRow(`SELECT consumed_at FROM diagnostics_consumptions`).Scan(&stamp)
			at, e := time.Parse(time.RFC3339Nano, stamp)
			if e != nil || time.Since(at) > time.Minute {
				t.Fatal(stamp, e)
			}
			gcControl(t, f, "enable", 0)
			if e = f.db.RunDiagnosticsGC(context.Background(), time.Now()); e != nil {
				t.Fatal(e)
			}
			var n int
			f.db.sql.QueryRow(`SELECT count(*) FROM diagnostic_deletions`).Scan(&n)
			if n != 0 {
				t.Fatal("new completion was deleted", n)
			}
		})
	}
}
