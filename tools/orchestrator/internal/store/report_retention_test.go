package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type reportRetentionFixture struct {
	*diagnosticsReadFixture
	spec             ReportEventSpec
	ack              contract.DurableAck
	id, path, source string
}

var reportFixtureAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func downgradeReportRetentionSchema(t *testing.T, db *DB) {
	t.Helper()
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var rows int
	if err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM report_artifact_deletions)+(SELECT COUNT(*) FROM report_artifact_pins)+(SELECT COUNT(*) FROM report_artifact_resolutions)+(SELECT COUNT(*) FROM report_gc_policies)`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal(rows, err)
	}
	if _, err = tx.Exec(`DROP TRIGGER report_reservation_transition;DROP TRIGGER report_retention_attempt_fence;DROP TRIGGER report_retention_segment_fence;DROP TRIGGER report_retention_intake_fence;DROP TRIGGER report_resolution_immutable;DROP TRIGGER report_resolution_retained;DROP TRIGGER report_resolution_binding;DROP TRIGGER report_pin_initial;DROP TRIGGER report_pin_identity;DROP TRIGGER report_pin_transition;DROP TRIGGER report_pin_retained;DROP TRIGGER report_deletion_initial;DROP TRIGGER report_deletion_identity;DROP TRIGGER report_deletion_retained;DROP TRIGGER report_deletion_transition;DROP TRIGGER quota_report_deletion_INSERT;DROP TRIGGER quota_report_artifact_resolutions_INSERT;DROP TRIGGER quota_report_artifact_pins_INSERT;DROP TRIGGER quota_report_gc_policies_INSERT;DROP TRIGGER quota_report_gc_policies_UPDATE;DROP TABLE report_artifact_deletions;DROP TABLE report_artifact_pins;DROP TABLE report_artifact_resolutions;DROP TABLE report_gc_policies;UPDATE schema_meta SET version=38;`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func newReportRetentionFixture(t *testing.T, failure bool) *reportRetentionFixture {
	t.Helper()
	f := &reportRetentionFixture{diagnosticsReadFixture: newDiagnosticsReadFixture(t)}
	ctx := context.Background()
	root := t.TempDir()
	if e := os.Chmod(root, 0700); e != nil {
		t.Fatal(e)
	}
	f.source = filepath.Join(root, "report.bin")
	body := "retained report attachment"
	if e := os.WriteFile(f.source, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	g := f.grant
	_, e := f.db.RegisterReportCapability(ctx, contract.ReportCapabilityRegistration{CapabilityID: g.ReportCapabilityID, ProducerID: g.HostID, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, TokenHash: runtimeHash("report-token"), CapabilityDir: root})
	if e != nil {
		t.Fatal(e)
	}
	kind, status := "result", "completed"
	outcome := contract.EventResult
	code := 0
	if failure {
		kind, status = "failure", "failed"
		outcome = contract.EventFailed
		code = 1
	}
	raw, _ := json.Marshal(map[string]any{"status": status, "artifact": contract.ArtifactRef{ID: "report", Path: f.source, SHA256: runtimeHash(body), Size: int64(len(body))}})
	f.spec = ReportEventSpec{CapabilityID: g.ReportCapabilityID, Token: "report-token", EventID: "report-result", Sequence: 1, Kind: kind, Payload: raw}
	f.ack, e = f.db.CommitReportEvent(ctx, f.spec)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.CommitHostEvent(ctx, diagnosticOutcomeEvent(g, outcome, 1)); e != nil {
		t.Fatal(e)
	}
	exited := diagnosticOutcomeEvent(g, contract.EventExited, 2)
	exited.ExitCode = &code
	exited.ActiveMS = 1
	if _, e = f.db.CommitHostEvent(ctx, exited); e != nil {
		t.Fatal(e)
	}
	page, e := f.db.CollectPending(ctx, g.TaskID, "", 32, false)
	if e != nil {
		t.Fatal(e)
	}
	ds := []AckDecision{}
	for n, event := range page.Events {
		ds = append(ds, AckDecision{EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, Decision: "handled", CommandID: "report-ack-" + string(rune('a'+n))})
	}
	if len(ds) == 0 {
		t.Fatal("no original delivery")
	}
	if e = f.db.AckDecisions(ctx, g.TaskID, "report-delivery", runtimeHash("history"), ds); e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.sql.Exec(`UPDATE runtime_events SET created_at='2020-01-01T00:00:00Z';UPDATE runtime_decisions SET created_at='2020-01-01T00:00:00Z'`); e != nil {
		t.Fatal(e)
	}
	if e = f.db.sql.QueryRow(`SELECT i.id,json_extract(f.proof_json,'$.path') FROM managed_artifact_intakes i JOIN managed_artifact_refs f ON f.intake_id=i.id`).Scan(&f.id, &f.path); e != nil {
		t.Fatal(e)
	}
	return f
}
func (f *reportRetentionFixture) plan(t *testing.T, now time.Time) ReportArtifactRetentionPlan {
	t.Helper()
	p, e := f.db.planReportArtifactRetentionAt(context.Background(), "controller", f.token, ReportArtifactRetentionPlanRequest{Version: 1, RunID: f.grant.RunID, RequestID: "batch", Limit: 8}, now)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func (f *reportRetentionFixture) resolve(t *testing.T, now time.Time) {
	t.Helper()
	r := ReportArtifactResolutionRequest{Version: 1, RunID: f.grant.RunID, IntakeID: f.id, Resolution: "resolved", Reason: "owner confirmed failure addressed", RequestID: "resolve"}
	first, e := f.db.resolveReportArtifactAt(context.Background(), "controller", f.token, r, now)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.db.resolveReportArtifactAt(context.Background(), "controller", f.token, r, now.Add(time.Hour))
	if e != nil || again != first {
		t.Fatal("resolution replay drift", again, e)
	}
}
func TestReportRetentionEligibility(t *testing.T) {
	for _, name := range []string{"success_boundary", "expired", "recent_handled", "wrong_decision", "unknown", "pinned", "failure_unresolved", "failure_boundary", "failure_expired"} {
		t.Run(name, func(t *testing.T) {
			failure := strings.HasPrefix(name, "failure")
			f := newReportRetentionFixture(t, failure)
			now := reportFixtureAt.Add(31 * 24 * time.Hour)
			switch name {
			case "success_boundary":
				now = reportFixtureAt.Add(30 * 24 * time.Hour)
			case "recent_handled":
				if _, e := f.db.sql.Exec(`UPDATE runtime_decisions SET created_at='2020-01-20T00:00:00Z' WHERE event_id=?`, f.spec.EventID); e != nil {
					t.Fatal(e)
				}
			case "wrong_decision":
				if _, e := f.db.sql.Exec(`UPDATE runtime_decisions SET decision='waiting_user' WHERE event_id=?`, f.spec.EventID); e != nil {
					t.Fatal(e)
				}
			case "unknown":
				if _, e := f.db.sql.Exec(`UPDATE segment_runtime SET status='unknown'`); e != nil {
					t.Fatal(e)
				}
			case "pinned":
				if e := f.db.PinReportArtifact(context.Background(), "controller", f.token, f.grant.RunID, f.id, "consumer", "downstream input", "consumer finished", false); e != nil {
					t.Fatal(e)
				}
			case "failure_boundary":
				f.resolve(t, reportFixtureAt)
				now = reportFixtureAt.Add(7 * 24 * time.Hour)
			case "failure_expired":
				f.resolve(t, reportFixtureAt)
				now = reportFixtureAt.Add(7*24*time.Hour + time.Nanosecond)
			}
			p := f.plan(t, now)
			want := name == "expired" || name == "failure_expired"
			if (len(p.Items) == 1) != want {
				t.Fatalf("eligibility %s items=%d exclusions=%v", name, len(p.Items), p.Excluded)
			}
			if _, e := os.Stat(f.path); e != nil {
				t.Fatal("plan changed physical data", e)
			}
		})
	}
}
func TestReportRetentionDeletesAndReplaysMetadata(t *testing.T) {
	f := newReportRetentionFixture(t, true)
	f.resolve(t, reportFixtureAt)
	ctx := context.Background()
	now := reportFixtureAt.Add(8 * 24 * time.Hour)
	p := f.plan(t, now)
	first, e := f.db.applyReportArtifactRetentionAt(ctx, "controller", f.token, p, now, openManagedReportDeletion)
	if e != nil || first.Deleted != 1 {
		t.Fatal(first, e)
	}
	again, e := f.db.applyReportArtifactRetentionAt(ctx, "controller", f.token, p, now, openManagedReportDeletion)
	if e != nil || first != again {
		t.Fatal("delete replay", again, e)
	}
	if _, e = os.Stat(f.path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("raw body survived", e)
	}
	if _, e = os.Stat(f.source); e != nil {
		t.Fatal("provider source removed", e)
	}
	var state, phase string
	var retained, cost, computed int64
	var events, refs int
	e = f.db.sql.QueryRow(`SELECT r.state,r.retained_bytes,x.phase,x.control_bytes,`+reportDeletionCostSQL("x.")+`,(SELECT count(*) FROM report_events),(SELECT count(*) FROM managed_artifact_refs WHERE intake_id=x.intake_id) FROM report_artifact_deletions x JOIN managed_artifact_reservations r ON r.id=x.reservation_id`).Scan(&state, &retained, &phase, &cost, &computed, &events, &refs)
	if e != nil || state != "released" || retained != 0 || phase != "removed" || cost != computed || events != 1 || refs != 1 {
		t.Fatal(state, retained, phase, cost, computed, events, refs, e)
	}
	ack, e := f.db.CommitReportEvent(ctx, f.spec)
	if e != nil || ack != f.ack {
		t.Fatal("canonical ACK replay rebuilt body", ack, e)
	}
	f.spec.Payload = json.RawMessage(strings.Replace(string(f.spec.Payload), "failed", "blocked", 1))
	if _, e = f.db.CommitReportEvent(ctx, f.spec); e == nil {
		t.Fatal("changed payload accepted")
	}
	if e = f.db.PinReportArtifact(ctx, "controller", f.token, f.grant.RunID, f.id, "late-consumer", "input", "done", false); e == nil {
		t.Fatal("pin resurrected retired object")
	}
}

type reportRemoveFault struct {
	diagnosticDeletionLease
	failed *bool
}

func (l reportRemoveFault) VerifyRemoved(ctx context.Context) error {
	if !*l.failed {
		*l.failed = true
		return errors.New("injected after unlink")
	}
	return l.diagnosticDeletionLease.VerifyRemoved(ctx)
}
func TestReportRetentionPreparedRecoveryAndDispatchFence(t *testing.T) {
	f := newReportRetentionFixture(t, false)
	ctx := context.Background()
	now := reportFixtureAt.Add(31 * 24 * time.Hour)
	p := f.plan(t, now)
	failed := false
	open := func(ctx context.Context, root string, i artifacts.Intake, proof artifacts.Proof, id string, saved []byte) (diagnosticDeletionLease, error) {
		l, e := openManagedReportDeletion(ctx, root, i, proof, id, saved)
		return reportRemoveFault{l, &failed}, e
	}
	if _, e := f.db.applyReportArtifactRetentionAt(ctx, "controller", f.token, p, now, open); e == nil {
		t.Fatal("fault did not fire")
	}
	if _, e := os.Stat(f.path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	var phase, state string
	if e := f.db.sql.QueryRow(`SELECT x.phase,r.state FROM report_artifact_deletions x JOIN managed_artifact_reservations r ON r.id=x.reservation_id`).Scan(&phase, &state); e != nil || phase != "prepared" || state != "deleting" {
		t.Fatal(phase, state, e)
	}
	if _, e := f.db.sql.Exec(`INSERT INTO attempts(id,task_id,attempt_no,status,created_at) VALUES('late','task',2,'running','now')`); e == nil {
		t.Fatal("new attempt crossed prepared fence")
	}
	if _, e := f.db.sql.Exec(`INSERT INTO segments(id,attempt_id,segment_no,status,created_at) VALUES('late',?,2,'running','now')`, f.grant.AttemptID); e == nil {
		t.Fatal("new segment crossed prepared fence")
	}
	if e := f.db.PinReportArtifact(ctx, "controller", f.token, f.grant.RunID, f.id, "late", "input", "done", false); e == nil {
		t.Fatal("new pin crossed prepared fence")
	}
	path := f.db.path
	if e := f.db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	f.db = db
	result, e := db.applyReportArtifactRetentionAt(ctx, "controller", f.token, p, now, openManagedReportDeletion)
	if e != nil || result.Deleted != 1 {
		t.Fatal(result, e)
	}
}
func TestReportGCPolicyAndDurablePendingPlan(t *testing.T) {
	f := newReportRetentionFixture(t, false)
	ctx := context.Background()
	now := reportFixtureAt.Add(31 * 24 * time.Hour)
	control := func(action string, rev int64) ReportGCStatus {
		t.Helper()
		s, e := f.db.ControlReportGC(ctx, "controller", f.token, action, ReportGCRequest{Version: 1, RunID: f.grant.RunID, ExpectedRevision: rev})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	if s := control("status", 0); s.Enabled || s.Revision != 0 {
		t.Fatal(s)
	}
	if e := f.db.RunReportGC(ctx, now); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(f.path); e != nil {
		t.Fatal("default off deleted bytes", e)
	}
	control("enable", 0)
	if _, e := f.db.sql.Exec(`CREATE TRIGGER report_gc_fault BEFORE UPDATE OF phase ON report_artifact_deletions WHEN NEW.phase='removed' BEGIN SELECT RAISE(ABORT,'injected finalize failure');END;`); e != nil {
		t.Fatal(e)
	}
	if e := f.db.RunReportGC(ctx, now); e == nil {
		t.Fatal("finalize fault did not fire")
	}
	if s := control("status", 0); !s.PendingPlan {
		t.Fatal("lost exact GC plan", s)
	}
	control("disable", 1)
	control("enable", 2) // Resume the same durable plan with fresh explicit consent.
	if _, e := f.db.sql.Exec(`DROP TRIGGER report_gc_fault`); e != nil {
		t.Fatal(e)
	}
	if e := f.db.RunReportGC(ctx, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if s := control("status", 0); s.PendingPlan {
		t.Fatal(s)
	}
	control("disable", 3)
	if s := control("status", 0); s.Enabled || s.Revision != 4 {
		t.Fatal(s)
	}
	control("enable", 4)
	if _, e := f.db.sql.Exec(`UPDATE runs SET origin_birth='rebound'`); e != nil {
		t.Fatal(e)
	}
	if e := f.db.RunReportGC(ctx, now.Add(2*time.Minute)); !errors.Is(e, CodeError("report_gc_owner_changed")) {
		t.Fatal(e)
	}
	if s := control("status", 0); s.Enabled || s.Stopped != "report_gc_owner_changed" {
		t.Fatal(s)
	}
}

func TestReportRetentionMigrationPreservesExistingIntake(t *testing.T) {
	f := newReportRetentionFixture(t, false)
	downgradeHostRetentionSchema(t, f.db)
	var before string
	if e := f.db.sql.QueryRow(`SELECT json_array(i.descriptor_json,f.proof_json,r.state,r.retained_bytes) FROM managed_artifact_intakes i JOIN managed_artifact_refs f ON f.intake_id=i.id JOIN managed_artifact_reservations r ON r.id=i.reservation_id`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	// Recreate the immediately preceding schema only in this owned empty-retention
	// fixture. Original report intake, event and proof rows are left unchanged.
	downgradeReportRetentionSchema(t, f.db)
	path := f.db.path
	if e := f.db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	f.db = db
	var after string
	var version, intents, policies int
	if e = db.sql.QueryRow(`SELECT json_array(i.descriptor_json,f.proof_json,r.state,r.retained_bytes),(SELECT version FROM schema_meta),(SELECT count(*) FROM report_artifact_deletions),(SELECT count(*) FROM report_gc_policies) FROM managed_artifact_intakes i JOIN managed_artifact_refs f ON f.intake_id=i.id JOIN managed_artifact_reservations r ON r.id=i.reservation_id`).Scan(&after, &version, &intents, &policies); e != nil || after != before || version != currentSchemaVersion || intents != 0 || policies != 0 {
		t.Fatal(after, version, intents, policies, e)
	}
	if ack, e := db.CommitReportEvent(context.Background(), f.spec); e != nil || ack != f.ack {
		t.Fatal(ack, e)
	}
}
