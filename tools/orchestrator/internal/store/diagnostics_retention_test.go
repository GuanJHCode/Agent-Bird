package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"os"
	"path/filepath"
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

func TestDiagnosticsRetentionReconnectedReceiptEpoch(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	f.append(t, 0, []byte("original retained bytes"))
	ctx := context.Background()
	if e := f.writer.Close(ctx); e != nil {
		t.Fatal(e)
	}
	for i, kind := range []string{contract.EventRunning, contract.EventResult} {
		if _, e := f.db.CommitHostEvent(ctx, diagnosticOutcomeEvent(f.grant, kind, int64(i+1))); e != nil {
			t.Fatal(e)
		}
	}
	r := DiagnosticsRetentionPlanRequest{Version: 1, RunID: f.grant.RunID, RequestID: "batch", TerminalReceivedBefore: "2021-01-01T00:00:00Z", Limit: 8}

	if e := f.db.BeginCoordinatorEpoch(ctx, 2); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.RegisterHost(ctx, f.hello, 2); e != nil {
		t.Fatal(e)
	}
	if e := f.db.BeginHostReconcile(ctx, f.grant.HostID); e != nil {
		t.Fatal(e)
	}
	late := diagnosticOutcomeEvent(f.grant, contract.EventExited, 3)
	late.ExecutionEpoch = 2
	zero := 0
	late.ExitCode = &zero
	late.ActiveMS = 1
	if _, e := f.db.CommitHostEvent(ctx, late); e != nil {
		t.Fatal("real reconciled receive rejected", e)
	}
	if _, e := f.db.sql.Exec(`UPDATE runtime_events SET created_at=CASE WHEN sequence=3 THEN '2020-08-01T00:00:00Z' ELSE '2020-01-01T00:00:00Z' END`); e != nil {
		t.Fatal(e)
	}
	var epoch int
	if e := f.db.sql.QueryRow(`SELECT execution_epoch FROM segment_runtime WHERE segment_id=?`, f.grant.SegmentID).Scan(&epoch); e != nil || epoch != 1 {
		t.Fatal("original claim changed", epoch, e)
	}
	p := mustRetentionPlan(t, f, r)
	if len(p.Items) != 1 || p.Items[0].Retention.TerminalReceivedAt != "2020-08-01T00:00:00Z" {
		t.Fatal("authenticated receive epoch is not original launch epoch", p)
	}
	hash := p.Items[0].Retention.TerminalVectorSHA256
	if _, e := f.db.CommitHostEvent(ctx, late); e != nil {
		t.Fatal(e)
	}
	if got := mustRetentionPlan(t, f, r); got.Items[0].Retention.TerminalVectorSHA256 != hash {
		t.Fatal("reconnected exact ACK changed vector")
	}
}

// Multiple real Store admission/commit paths and private diagnostic namespaces;
// no actual worker or provider is started by this fixture.
func retentionRunFixture(t *testing.T, ids []string, completed int) (*diagnosticsReadFixture, DiagnosticsRetentionPlanRequest) {
	t.Helper()
	ctx := context.Background()
	state := t.TempDir()
	if e := os.Chmod(state, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := Open(filepath.Join(state, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	plan := diagnosticsPlan(&contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"})
	template := plan.Tasks[0]
	template.MaxAttempts = 1
	plan.Tasks = nil
	for _, id := range ids {
		task := template
		task.ID = id
		plan.Tasks = append(plan.Tasks, task)
	}
	receipt, e := db.SubmitPlan(ctx, plan)
	if e != nil {
		t.Fatal(e)
	}
	h := contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "generation", PID: 101, Birth: "host-birth", Executable: "/private/tmp/orchestrator"}
	if _, e = db.RegisterHost(ctx, h, 1); e != nil {
		t.Fatal(e)
	}
	f := &diagnosticsReadFixture{db: db, token: receipt.ControlToken, hello: h}
	for i := 0; i < completed; i++ {
		g, e := db.ClaimReady(ctx, h.LaunchID, 1, 1)
		if e != nil {
			t.Fatal(e)
		}
		root := filepath.Join(state, "host-spool", g.HostID, g.AttemptID, ".diagnostics")
		if e = os.MkdirAll(root, 0700); e != nil {
			t.Fatal(e)
		}
		w, e := diagnostics.OpenWriter(ctx, root, diagnostics.Scope{Version: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, ProducerID: g.HostID, PolicySHA256: g.Diagnostics.PolicySHA256})
		if e != nil {
			t.Fatal(e)
		}
		if e = w.Append(ctx, diagnostics.Record{SegmentID: g.SegmentID, Stream: diagnostics.Stdout, Data: []byte("retained source")}); e != nil {
			t.Fatal(e)
		}
		if e = w.Close(ctx); e != nil {
			t.Fatal(e)
		}
		if _, e = db.CommitHostEvent(ctx, diagnosticOutcomeEvent(g, contract.EventResult, int64(i*2+1))); e != nil {
			t.Fatal(e)
		}
		exit := diagnosticOutcomeEvent(g, contract.EventExited, int64(i*2+2))
		zero := 0
		exit.ExitCode = &zero
		exit.ActiveMS = 1
		if _, e = db.CommitHostEvent(ctx, exit); e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			f.grant = g
			f.root = root
		}
	}
	if _, e = db.sql.Exec(`UPDATE runtime_events SET created_at='2020-01-01T00:00:00Z'`); e != nil {
		t.Fatal(e)
	}
	return f, DiagnosticsRetentionPlanRequest{Version: 1, RunID: "run", RequestID: "batch", TerminalReceivedBefore: "2021-01-01T00:00:00Z", Limit: 8}
}
func TestDiagnosticsRetentionPaginationPublicationBytes(t *testing.T) {
	t.Run("excluded_long_cursor_not_consumed", func(t *testing.T) {
		ids := []string{}
		for i := 0; i < 7; i++ {
			ids = append(ids, fmt.Sprintf("a%d", i)+strings.Repeat(`"`, 230))
		}
		for i := 0; i < 57; i++ {
			ids = append(ids, fmt.Sprintf("z%02d", i)+strings.Repeat("\x01", 253))
		}
		f, r := retentionRunFixture(t, ids, 7)
		p := mustRetentionPlan(t, f, r)
		raw, _ := json.Marshal(p)
		if len(p.Items) != 7 || p.Scanned != 7 || len(p.Excluded) != 0 || p.NextAfterTaskID != ids[6] || len(raw)+1 > 8192 {
			t.Fatal("excluded candidate skipped or stdout too large", len(raw), p)
		}
		r.AfterTaskID = p.NextAfterTaskID
		next := mustRetentionPlan(t, f, r)
		if next.Scanned != 57 || next.Excluded["diagnostics_no_attempt"] != 57 || next.NextAfterTaskID != "" {
			t.Fatal("excluded continuation", next)
		}
		t.Logf("first stdout bytes=%d second scanned=%d", len(raw)+1, next.Scanned)
	})
	t.Run("exact_8192_requires_stdout_newline", func(t *testing.T) {
		f, r := retentionFixture(t)
		base := mustRetentionPlan(t, f, r)
		for n := 0; n < 254; n++ {
			for extra := 1; extra <= 128; extra++ {
				p := base
				p.RequestID = strings.Repeat("x", extra)
				p.Items = nil
				p.Scanned = 8
				for i := 0; i < 8; i++ {
					item := base.Items[0]
					guard := *item.Retention
					item.Retention = &guard
					item.TaskID = fmt.Sprintf("a%d", i) + strings.Repeat(`"`, n)
					item.AttemptID = fmt.Sprintf("attempt-%d", i)
					guard.ReservationID = fmt.Sprintf("reservation-%d", i)
					guard.NamespaceSHA256 = runtimeHash(item.AttemptID)
					item.RequestID = retentionItemRequestID(p, item)
					p.Items = append(p.Items, item)
				}
				p.NextAfterTaskID = p.Items[7].TaskID
				p.PlanSHA256, _ = p.digest()
				raw, _ := json.Marshal(p)
				if len(raw) == 8192 {
					if e := p.Validate(); e == nil {
						t.Fatal("accepted canonical8192 that publishes8193 stdout")
					}
					return
				}
			}
		}
		t.Fatal("did not construct exact boundary")
	})
}

func TestDiagnosticsRetentionBoundedAllEventsAndSegments(t *testing.T) {
	t.Run("512_authenticated_events_and_513_rejection", func(t *testing.T) {
		f, r := retentionFixture(t)
		ctx := context.Background()
		zero := 0
		for seq := int64(3); seq <= 513; seq++ {
			e := diagnosticOutcomeEvent(f.grant, contract.EventExited, seq)
			e.ExitCode = &zero
			if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal("authenticated late event", seq, err)
			}
			if _, err := f.db.sql.Exec(`UPDATE runtime_events SET created_at='2020-01-01T00:00:00Z' WHERE sequence=?`, seq); err != nil {
				t.Fatal(err)
			}
			if seq == 512 {
				p := mustRetentionPlan(t, f, r)
				if len(p.Items) != 1 {
					t.Fatal("exact512 rejected", p)
				}
			}
		}
		p := mustRetentionPlan(t, f, r)
		if len(p.Items) != 0 || p.Excluded["diagnostics_retention_evidence_too_large"] != 1 {
			t.Fatal("513 accepted", p)
		}
	})
	t.Run("body_65536_and_nonterminal_vector", func(t *testing.T) {
		f, r := retentionFixture(t)
		original := mustRetentionPlan(t, f, r).Items[0].Retention.TerminalVectorSHA256
		var body string
		if e := f.db.sql.QueryRow(`SELECT body_json FROM runtime_events WHERE sequence=1`).Scan(&body); e != nil {
			t.Fatal(e)
		}
		body += strings.Repeat(" ", 65536-len(body))
		if _, e := f.db.sql.Exec(`UPDATE runtime_events SET body_json=? WHERE sequence=1`, body); e != nil {
			t.Fatal(e)
		}
		p := mustRetentionPlan(t, f, r)
		if len(p.Items) != 1 || p.Items[0].Retention.TerminalVectorSHA256 == original {
			t.Fatal("exact body/non-Exited row absent from vector", p)
		}
		if _, e := f.db.sql.Exec(`UPDATE runtime_events SET body_json=? WHERE sequence=1`, body+" "); e != nil {
			t.Fatal(e)
		}
		p = mustRetentionPlan(t, f, r)
		if len(p.Items) != 0 || p.Excluded["diagnostics_retention_evidence_too_large"] != 1 {
			t.Fatal("65537 decoded/selected", p)
		}
	})
	t.Run("all_required_segments_max", func(t *testing.T) {
		f, r := retentionFixture(t)
		ctx := context.Background()
		// Synthetic durable second-segment fixture, not a claim of a new process.
		// The real first segment and its authenticated events remain unchanged.
		for _, q := range []string{
			`INSERT INTO segments SELECT 'second',attempt_id,2,'exited',created_at FROM segments WHERE segment_no=1`,
			`INSERT INTO segment_runtime(segment_id,attempt_id,host_id,command_id,execution_epoch,slot_token,launch_intent_hash,reservation_id,status,outcome,deadline_unix_ms,created_at) SELECT 'second',attempt_id,host_id,'second-command',execution_epoch,'second-slot',launch_intent_hash,'second-budget','exited',outcome,deadline_unix_ms,created_at FROM segment_runtime WHERE segment_id<>'second'`,
			`INSERT INTO budget_runtime SELECT 'second-budget',budget_group_id,'second',granted_ms,used_ms,'settled',created_at FROM budget_runtime WHERE segment_id<>'second'`,
			`INSERT INTO report_capabilities SELECT 'second-report','second',producer_id,token_hash,capability_dir,run_id,task_id,attempt_id,work_revision,execution_epoch,status,used_bytes,created_at FROM report_capabilities WHERE segment_id<>'second'`,
		} {
			if _, e := f.db.sql.Exec(q); e != nil {
				t.Fatal(e)
			}
		}
		p := mustRetentionPlan(t, f, r)
		if len(p.Items) != 0 || p.Excluded["diagnostics_retention_evidence_invalid"] != 1 {
			t.Fatal("missing required Exited accepted", p)
		}
		e := diagnosticOutcomeEvent(f.grant, contract.EventExited, 3)
		e.SegmentID = "second"
		e.CommandID = "second-command"
		zero := 0
		e.ExitCode = &zero
		if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.sql.Exec(`UPDATE runtime_events SET created_at='2020-09-01T00:00:00Z' WHERE sequence=3`); err != nil {
			t.Fatal(err)
		}
		p = mustRetentionPlan(t, f, r)
		if len(p.Items) != 1 || p.Items[0].Retention.TerminalReceivedAt != "2020-09-01T00:00:00Z" {
			t.Fatal("required segment max", p)
		}
		r.TerminalReceivedBefore = "2020-09-01T00:00:00Z"
		if got := mustRetentionPlan(t, f, r); len(got.Items) != 0 || got.Excluded["diagnostics_not_expired"] != 1 {
			t.Fatal("all segments strict cutoff", got)
		}
	})
}
func TestDiagnosticsRetentionWholePlanAndPartialReplay(t *testing.T) {
	t.Run("whole_plan_before_any_delete_and_pagination", func(t *testing.T) {
		f, r := retentionRunFixture(t, []string{"a", "b", "c"}, 3)
		r.Limit = 2
		p := mustRetentionPlan(t, f, r)
		if len(p.Items) != 2 || p.NextAfterTaskID != "b" || p.Items[0].RequestID == p.Items[1].RequestID {
			t.Fatal("limit/canonical run-unique tuple", p)
		}
		nextRequest := r
		nextRequest.AfterTaskID = p.NextAfterTaskID
		next := mustRetentionPlan(t, f, nextRequest)
		if len(next.Items) != 1 || next.Items[0].TaskID != "c" || next.NextAfterTaskID != "" {
			t.Fatal("limit continuation", next)
		}
		for _, kind := range []string{"hash", "order", "duplicate", "foreign_item", "foreign_run", "foreign_owner", "foreign_token", "future", "oversize"} {
			t.Run(kind, func(t *testing.T) {
				raw, _ := json.Marshal(p)
				var bad DiagnosticsRetentionPlan
				if e := json.Unmarshal(raw, &bad); e != nil {
					t.Fatal(e)
				}
				owner, token := "controller", f.token
				switch kind {
				case "hash":
					bad.PlanSHA256 = strings.Repeat("f", 64)
				case "order":
					bad.Items[0], bad.Items[1] = bad.Items[1], bad.Items[0]
				case "duplicate":
					bad.Items[1] = bad.Items[0]
				case "foreign_item":
					bad.Items[1].TaskID = "foreign"
					bad.Items[1].RequestID = retentionItemRequestID(bad, bad.Items[1])
				case "foreign_run":
					bad.RunID = "foreign"
					for i := range bad.Items {
						bad.Items[i].RequestID = retentionItemRequestID(bad, bad.Items[i])
					}
				case "foreign_owner":
					owner = "other"
				case "foreign_token":
					token += "other"
				case "future":
					bad.TerminalReceivedBefore = "2999-01-01T00:00:00Z"
				case "oversize":
					bad.NextAfterTaskID = strings.Repeat("x", 8192)
				}
				if kind != "hash" {
					bad.PlanSHA256, _ = bad.digest()
				}
				called := false
				_, e := f.db.applyDiagnosticsRetention(context.Background(), owner, token, bad, func(context.Context, string, string, DiagnosticsDeleteRequest) ([]byte, error) {
					called = true
					return nil, errors.New("must not reach delete")
				})
				if e == nil || called {
					t.Fatal("whole plan validation crossed first item", kind, e, called)
				}
			})
		}
	})
	for _, kind := range []string{"partial_failure", "cancel_after_first", "deep_freeze"} {
		t.Run(kind, func(t *testing.T) {
			f, r := retentionRunFixture(t, []string{"a", "b"}, 2)
			p := mustRetentionPlan(t, f, r)
			raw, _ := json.Marshal(p)
			var original DiagnosticsRetentionPlan
			if e := json.Unmarshal(raw, &original); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			out, e := f.db.applyDiagnosticsRetention(ctx, "controller", f.token, p, func(ctx context.Context, owner, token string, item DiagnosticsDeleteRequest) ([]byte, error) {
				calls++
				if calls == 2 && kind == "partial_failure" {
					return nil, errors.New("controlled interruption")
				}
				body, e := f.db.DeleteDiagnostics(ctx, owner, token, item)
				if e != nil {
					return nil, e
				}
				if calls == 1 {
					if kind == "cancel_after_first" {
						cancel()
					}
					if kind == "deep_freeze" {
						p.Items[1].TaskID = "foreign"
						p.Items[1].Retention.TerminalVectorSHA256 = strings.Repeat("f", 64)
					}
				}
				return body, nil
			})
			if e != nil || len(out.Items) < 1 || out.Items[0].Status != "deleted" {
				t.Fatal("first item failed", out, e)
			}
			if kind == "partial_failure" && (len(out.Items) != 2 || out.Items[1].Status != "failed") {
				t.Fatal("partial outcome", out)
			}
			if kind == "cancel_after_first" && (len(out.Items) != 1 || out.RemainingItems != 1 || out.Stopped != "diagnostics_cancelled") {
				t.Fatal("cancel boundary", out)
			}
			if kind == "deep_freeze" && (len(out.Items) != 2 || out.Items[1].Status != "deleted") {
				t.Fatal("caller mutation affected frozen second item", out)
			}
			usage := deletionUsage(t, f.db)
			first, _ := json.Marshal(out.Items[0].Result)
			replay, e := f.db.DeleteDiagnostics(context.Background(), "controller", f.token, original.Items[0])
			if e != nil || !bytes.Equal(first, replay) || usage != deletionUsage(t, f.db) {
				t.Fatal("partial first exact receipt/refund", e, string(first), string(replay))
			}
			done, e := f.db.ApplyDiagnosticsRetention(context.Background(), "controller", f.token, original)
			if e != nil || len(done.Items) != 2 || done.Items[0].Status != "deleted" || done.Items[1].Status != "deleted" {
				t.Fatal("original plan retry", done, e)
			}
			usage = deletionUsage(t, f.db)
			again, e := f.db.ApplyDiagnosticsRetention(context.Background(), "controller", f.token, original)
			left, _ := json.Marshal(done)
			right, _ := json.Marshal(again)
			if e != nil || !bytes.Equal(left, right) || usage != deletionUsage(t, f.db) {
				t.Fatal("full replay changed receipts/quota", e)
			}
		})
	}
}

func TestDiagnosticsRetentionWirePresence(t *testing.T) {
	f, r := retentionFixture(t)
	p := mustRetentionPlan(t, f, r)
	raw, _ := json.Marshal(p)
	for _, kind := range []string{"empty_object", "missing", "null_object", "null_count", "zero_count", "unknown", "case_alias", "duplicate", "positive_count"} {
		t.Run(kind, func(t *testing.T) {
			body := string(raw)
			valid := false
			switch kind {
			case "empty_object":
				valid = true
			case "missing":
				body = strings.Replace(body, `"excluded":{},`, "", 1)
			case "null_object":
				body = strings.Replace(body, `"excluded":{}`, `"excluded":null`, 1)
			case "null_count":
				body = strings.Replace(body, `"excluded":{}`, `"excluded":{"diagnostics_no_attempt":null}`, 1)
			case "zero_count":
				copy := p
				copy.Excluded = map[string]int{"diagnostics_no_attempt": 0}
				copy.PlanSHA256, _ = copy.digest()
				b, _ := json.Marshal(copy)
				body = string(b)
			case "unknown":
				body = strings.Replace(body, `"excluded":{}`, `"excluded":{"unknown":null}`, 1)
			case "case_alias":
				body = strings.Replace(body, `"excluded":{}`, `"excluded":{"Diagnostics_no_attempt":null}`, 1)
			case "duplicate":
				body = strings.Replace(body, `"excluded":{}`, `"excluded":{"diagnostics_no_attempt":null,"diagnostics_no_attempt":null}`, 1)
			case "positive_count":
				copy := p
				copy.Excluded = map[string]int{"diagnostics_no_attempt": 1}
				copy.Scanned = 2
				copy.PlanSHA256, _ = copy.digest()
				b, _ := json.Marshal(copy)
				body = string(b)
				valid = true
			}
			var decoded DiagnosticsRetentionPlan
			e := json.Unmarshal([]byte(body), &decoded)
			if valid {
				again, _ := json.Marshal(decoded)
				if e != nil || string(again) != body {
					t.Fatal("wire/count canonical changed", e, string(again))
				}
			} else if e == nil {
				t.Fatal("bad presence/count accepted", kind)
			}
		})
	}
}
