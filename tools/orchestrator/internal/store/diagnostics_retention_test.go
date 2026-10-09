package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"strings"
	"testing"
)

func TestDiagnosticsRetentionRequestExtension(t *testing.T) {
	legacy := `{"version":1,"task_id":"task","attempt_id":"attempt","request_id":"delete","expected_revision":1}`
	t.Run("nil_legacy_bytes", func(t *testing.T) {
		var r DiagnosticsDeleteRequest
		if e := json.Unmarshal([]byte(legacy), &r); e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(r)
		if string(b) != legacy {
			t.Fatalf("nil guard rewrote original manual request: %s", b)
		}
	})
	t.Run("optional_guard", func(t *testing.T) {
		body := strings.TrimSuffix(legacy, "}") + `,"retention":{"version":1,"reservation_id":"reservation","namespace_sha256":"` + strings.Repeat("a", 64) + `","terminal_received_at":"2020-01-01T00:00:00Z","terminal_received_before":"2021-01-01T00:00:00Z","terminal_vector_sha256":"` + strings.Repeat("b", 64) + `"}}`
		var r DiagnosticsDeleteRequest
		if e := json.Unmarshal([]byte(body), &r); e != nil {
			t.Fatal("valid explicit guard rejected", e)
		}
		b, _ := json.Marshal(r)
		if string(b) != body {
			t.Fatal("full guard not preserved", string(b))
		}
	})
}

func retentionFixture(t *testing.T) (*diagnosticsReadFixture, DiagnosticsRetentionPlanRequest) {
	t.Helper()
	f, _ := completedDiagnosticsDeleteFixture(t)
	if _, e := f.db.sql.Exec(`UPDATE runtime_events SET created_at='2020-01-01T00:00:00Z' WHERE attempt_id=?`, f.grant.AttemptID); e != nil {
		t.Fatal(e)
	}
	return f, DiagnosticsRetentionPlanRequest{Version: 1, RunID: f.grant.RunID, RequestID: "batch", TerminalReceivedBefore: "2021-01-01T00:00:00Z", Limit: 8}
}
func mustRetentionPlan(t *testing.T, f *diagnosticsReadFixture, r DiagnosticsRetentionPlanRequest) DiagnosticsRetentionPlan {
	t.Helper()
	p, e := f.db.PlanDiagnosticsRetention(context.Background(), "controller", f.token, r)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestDiagnosticsRetentionTerminalVectorBoundaries(t *testing.T) {
	f, r := retentionFixture(t)
	ctx := context.Background()
	t.Run("real_producer_revision_zero", func(t *testing.T) {
		var rowRevision int
		var body string
		if e := f.db.sql.QueryRow(`SELECT event_revision,body_json FROM runtime_events WHERE sequence=2`).Scan(&rowRevision, &body); e != nil {
			t.Fatal(e)
		}
		var e contract.Event
		if json.Unmarshal([]byte(body), &e) != nil || e.EventRevision != 0 || rowRevision != 2 {
			t.Fatal("fixture not real producer revision shape")
		}
		before := diagnosticsBusinessSnapshot(t, f.db)
		p := mustRetentionPlan(t, f, r)
		if len(p.Items) != 1 || p.Items[0].Retention.TerminalReceivedAt != "2020-01-01T00:00:00Z" || p.Scanned != 1 || len(p.Excluded) != 0 || before != diagnosticsBusinessSnapshot(t, f.db) {
			t.Fatal("readonly terminal selection", p)
		}
	})
	t.Run("cutoff_is_strict", func(t *testing.T) {
		req := r
		req.TerminalReceivedBefore = "2020-01-01T00:00:00Z"
		p := mustRetentionPlan(t, f, req)
		if len(p.Items) != 0 || p.Excluded["diagnostics_not_expired"] != 1 {
			t.Fatal(p)
		}
	})
	for _, kind := range []string{"bad_json", "oversize", "bad_time", "foreign_scope", "row_revision", "body_revision", "missing_exit", "unknown_segment", "not_completed", "pending_delivery", "noncanonical_cutoff", "future_cutoff", "foreign_owner"} {
		t.Run(kind, func(t *testing.T) {
			f, r := f, r
			if kind == "pending_delivery" {
				f, r = retentionFixture(t)
			}
			var originalBody, originalTime string
			var originalRev int64
			if e := f.db.sql.QueryRow(`SELECT body_json,created_at,event_revision FROM runtime_events WHERE sequence=2`).Scan(&originalBody, &originalTime, &originalRev); e != nil {
				t.Fatal(e)
			}
			defer func() {
				if _, e := f.db.sql.Exec(`UPDATE runtime_events SET body_json=?,created_at=?,event_revision=? WHERE sequence=2`, originalBody, originalTime, originalRev); e != nil {
					t.Error(e)
				}
				f.db.sql.Exec(`UPDATE segment_runtime SET status='exited'`)
				f.db.sql.Exec(`UPDATE tasks SET status='completed'`)
				f.db.sql.Exec(`UPDATE diagnostic_delivery_state SET phase='idle'`)
			}()
			req := r
			controller := "controller"
			switch kind {
			case "bad_json":
				f.db.sql.Exec(`UPDATE runtime_events SET body_json='{' WHERE sequence=2`)
			case "oversize":
				f.db.sql.Exec(`UPDATE runtime_events SET body_json=? WHERE sequence=2`, strings.Repeat("x", 65537))
			case "bad_time":
				f.db.sql.Exec(`UPDATE runtime_events SET created_at='2020-01-01T00:00:00+00:00' WHERE sequence=2`)
			case "foreign_scope":
				f.db.sql.Exec(`UPDATE runtime_events SET body_json=json_set(body_json,'$.run_id','other') WHERE sequence=2`)
			case "row_revision":
				f.db.sql.Exec(`UPDATE runtime_events SET event_revision=1 WHERE sequence=2`)
			case "body_revision":
				f.db.sql.Exec(`UPDATE runtime_events SET body_json=json_set(body_json,'$.event_revision',1) WHERE sequence=2`)
			case "missing_exit":
				f.db.sql.Exec(`UPDATE runtime_events SET body_json=json_set(body_json,'$.kind','running') WHERE sequence=2`)
			case "unknown_segment":
				f.db.sql.Exec(`UPDATE segment_runtime SET status='unknown'`)
			case "not_completed":
				f.db.sql.Exec(`UPDATE tasks SET status='result_ready'`)
			case "pending_delivery":
				if _, e := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request); e != nil {
					t.Fatal(e)
				}
			case "noncanonical_cutoff":
				req.TerminalReceivedBefore = "2021-01-01T00:00:00+00:00"
			case "future_cutoff":
				req.TerminalReceivedBefore = "2999-01-01T00:00:00Z"
			case "foreign_owner":
				controller = "other"
			}
			p, e := f.db.PlanDiagnosticsRetention(ctx, controller, f.token, req)
			if kind == "noncanonical_cutoff" || kind == "future_cutoff" || kind == "foreign_owner" {
				if e == nil {
					t.Fatal("invalid request/authority planned")
				}
				return
			}
			if e != nil || len(p.Items) != 0 || p.Scanned != 1 {
				t.Fatal("bad evidence was eligible or silent failure", p, e)
			}
		})
	}
	t.Run("late_exit_max_and_replay", func(t *testing.T) {
		late := diagnosticOutcomeEvent(f.grant, contract.EventExited, 3)
		zero := 0
		late.ExitCode = &zero
		if _, e := f.db.CommitHostEvent(ctx, late); e != nil {
			t.Fatal(e)
		}
		if _, e := f.db.sql.Exec(`UPDATE runtime_events SET created_at='2020-06-01T00:00:00Z' WHERE sequence=3`); e != nil {
			t.Fatal(e)
		}
		p := mustRetentionPlan(t, f, r)
		if len(p.Items) != 1 || p.Items[0].Retention.TerminalReceivedAt != "2020-06-01T00:00:00Z" {
			t.Fatal(p)
		}
		hash := p.Items[0].Retention.TerminalVectorSHA256
		if _, e := f.db.CommitHostEvent(ctx, late); e != nil {
			t.Fatal(e)
		}
		again := mustRetentionPlan(t, f, r)
		if again.Items[0].Retention.TerminalVectorSHA256 != hash {
			t.Fatal("exact ACK replay reset receipt time")
		}
	})
}

type retentionFailRemoveLease struct{ diagnosticDeletionLease }

func (l retentionFailRemoveLease) Remove(context.Context) error {
	return errors.New("injected remove interruption")
}
func TestDiagnosticsRetentionGuardAdmissionAndDurableReplay(t *testing.T) {
	for _, kind := range []string{"plan_drift", "prepare_drift", "deep_freeze", "prepared_partial_replay", "quota"} {
		t.Run(kind, func(t *testing.T) {
			f, r := retentionFixture(t)
			p := mustRetentionPlan(t, f, r)
			if len(p.Items) != 1 {
				t.Fatal(p)
			}
			req := p.Items[0]
			originalGuard := *req.Retention
			ctx := context.Background()
			removed := false
			open := func(ctx context.Context, root string, scope diagnostics.Scope, command string, plan []byte) (diagnosticDeletionLease, error) {
				if kind == "prepare_drift" {
					if _, e := f.db.sql.Exec(`UPDATE runtime_events SET created_at='2020-03-01T00:00:00Z' WHERE sequence=2`); e != nil {
						t.Fatal(e)
					}
				}
				if kind == "deep_freeze" {
					req.Retention.TerminalVectorSHA256 = strings.Repeat("f", 64)
				}
				l, e := diagnostics.OpenDeletion(ctx, root, scope, command, plan)
				if e != nil {
					return nil, e
				}
				return retentionObserveLease{diagnosticDeletionLease: l, removed: &removed, fail: kind == "prepared_partial_replay"}, nil
			}
			if kind == "plan_drift" {
				if _, e := f.db.sql.Exec(`UPDATE runtime_events SET created_at='2020-03-01T00:00:00Z' WHERE sequence=2`); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "quota" {
				f.db.runControlLimit = 0
			}
			body, e := f.db.deleteDiagnostics(ctx, "controller", f.token, req, open)
			if kind == "plan_drift" || kind == "prepare_drift" || kind == "quota" {
				if e == nil || removed {
					t.Fatal("drift/quota crossed Remove", e, removed)
				}
				var n int
				f.db.sql.QueryRow(`SELECT count(*) FROM diagnostic_deletions`).Scan(&n)
				if n != 0 {
					t.Fatal("failed fresh admission persisted half intent")
				}
				return
			}
			if kind == "prepared_partial_replay" {
				if e == nil || !removed {
					t.Fatal("no injected prepared interruption")
				}
				var saved string
				if e = f.db.sql.QueryRow(`SELECT request_json FROM diagnostic_deletions`).Scan(&saved); e != nil {
					t.Fatal(e)
				}
				var full DiagnosticsDeleteRequest
				if json.Unmarshal([]byte(saved), &full) != nil || full.Retention == nil || *full.Retention != originalGuard {
					t.Fatal("guard missing from durable request", saved)
				}
				f.db.sql.Exec(`UPDATE runtime_events SET created_at='2020-04-01T00:00:00Z' WHERE sequence=2`)
				body, e = f.db.DeleteDiagnostics(ctx, "controller", f.token, req)
			}
			if e != nil {
				t.Fatal(e)
			}
			var result DiagnosticsDeleteResult
			if json.Unmarshal(body, &result) != nil || result.Status != "deleted" || result.ReleasedReservationBytes != detailedArtifactReservationBytes {
				t.Fatal(string(body))
			}
			if kind == "deep_freeze" {
				req.Retention = &originalGuard
			}
			usage := deletionUsage(t, f.db)
			again, e := f.db.DeleteDiagnostics(ctx, "controller", f.token, req)
			if e != nil || !bytes.Equal(body, again) || usage != deletionUsage(t, f.db) {
				t.Fatal("exact deletion replay changed receipt/quota", e)
			}
			var saved string
			if e = f.db.sql.QueryRow(`SELECT request_json FROM diagnostic_deletions`).Scan(&saved); e != nil {
				t.Fatal(e)
			}
			var full DiagnosticsDeleteRequest
			if json.Unmarshal([]byte(saved), &full) != nil || full.Retention == nil || *full.Retention != originalGuard {
				t.Fatal("full frozen guard not retained", saved)
			}
		})
	}
}

type retentionObserveLease struct {
	diagnosticDeletionLease
	removed *bool
	fail    bool
}

func (l retentionObserveLease) Remove(ctx context.Context) error {
	*l.removed = true
	if l.fail {
		return errors.New("injected remove interruption")
	}
	return l.diagnosticDeletionLease.Remove(ctx)
}
