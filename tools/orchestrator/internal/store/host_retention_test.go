package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type hostRetentionFixture struct {
	*diagnosticsReadFixture
	event    contract.Event
	ack      contract.DurableAck
	proof    artifacts.Proof
	at       time.Time
	progress []artifacts.Proof
}

func newHostRetentionFixture(t *testing.T, failure, progress bool) *hostRetentionFixture {
	t.Helper()
	f := &hostRetentionFixture{diagnosticsReadFixture: newDiagnosticsReadFixture(t), at: time.Now().UTC().Add(time.Second)}
	ctx := context.Background()
	g := f.grant
	root := filepath.Join(filepath.Dir(f.db.path), "host-spool", g.HostID)
	materialize := func(class string, index int) (*contract.ArtifactRef, artifacts.Proof) {
		t.Helper()
		reservation, kind, id := g.HostArtifacts.TerminalReservationID, "terminal", "result-output.bin"
		if class == "progress" {
			reservation, kind, id = g.HostArtifacts.ProgressReservationID, "progress", "provider-progress"
		}
		body := []byte(class + " retained body")
		desc := artifacts.HostSlotDescriptor{HostSlotBinding: artifacts.HostSlotBinding{Version: 1, ReservationID: reservation, ScopeSHA256: g.HostArtifacts.ScopeSHA256, Class: class, Index: index}, Kind: kind, Size: int64(len(body)), SHA256: runtimeHash(string(body))}
		proof, err := artifacts.MaterializeHostSlot(ctx, root, desc, body)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := desc.Canonical()
		p, _ := json.Marshal(proof)
		return &contract.ArtifactRef{ID: id, Path: proof.Path, SHA256: proof.SHA256, Size: proof.Size, HostArtifact: &contract.HostArtifactReceipt{Version: 1, Descriptor: raw, Proof: p}}, proof
	}
	if _, err := f.db.CommitHostEvent(ctx, diagnosticOutcomeEvent(g, contract.EventPrepared, 1)); err != nil {
		t.Fatal(err)
	}
	if progress {
		for i := 0; i < 3; i++ {
			ref, proof := materialize("progress", i)
			f.progress = append(f.progress, proof)
			if i == 0 {
				ev := diagnosticOutcomeEvent(g, contract.EventProgress, 1)
				ev.EventID = "managed-progress"
				ev.Artifact = ref
				ev.PayloadHash = ref.SHA256
				if err := f.db.CommitHostProgress(ctx, ev, f.hello.LaunchID, f.hello.LaunchToken, f.hello.PID, f.hello.Birth); err != nil {
					t.Fatal(err)
				}
			}
			if i == 2 {
				if err := os.Rename(proof.Path, filepath.Join(filepath.Dir(proof.Path), "data.stage")); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	kind, code := contract.EventResult, 0
	if failure {
		kind, code = contract.EventFailed, 1
	}
	ref, proof := materialize("terminal", 0)
	f.proof = proof
	f.event = diagnosticOutcomeEvent(g, kind, 2)
	f.event.Artifact = ref
	f.event.PayloadHash = ref.SHA256
	var err error
	f.ack, err = f.db.CommitHostEvent(ctx, f.event)
	if err != nil {
		t.Fatal(err)
	}
	exited := diagnosticOutcomeEvent(g, contract.EventExited, 3)
	exited.ExitCode = &code
	exited.ActiveMS = 1
	exited.Artifact = ref
	if _, err = f.db.CommitHostEvent(ctx, exited); err != nil {
		t.Fatal(err)
	}
	page, err := f.db.CollectPending(ctx, g.TaskID, "", 32, false)
	if err != nil {
		t.Fatal(err)
	}
	ds := []AckDecision{}
	for _, ev := range page.Events {
		ds = append(ds, AckDecision{EventID: ev.EventID, EventRevision: ev.EventRevision, EventHash: ev.PayloadHash, ActionSlot: ev.ActionSlot, Decision: "handled", CommandID: "retention-ack"})
	}
	if len(ds) != 1 {
		t.Fatalf("terminal deliveries: %+v", page)
	}
	if err = f.db.AckDecisions(ctx, g.TaskID, "retention-history", runtimeHash("history"), ds); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE runtime_events SET created_at=?; UPDATE runtime_decisions SET created_at=?`, f.at.Format(time.RFC3339Nano), f.at.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *hostRetentionFixture) plan(t *testing.T, now time.Time) HostArtifactRetentionPlan {
	t.Helper()
	p, err := f.db.planHostArtifactRetentionAt(context.Background(), "controller", f.token, HostArtifactRetentionPlanRequest{Version: 1, RunID: f.grant.RunID, RequestID: "host-batch", Limit: 8}, now)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (f *hostRetentionFixture) resolve(t *testing.T, reservation string, now time.Time) {
	t.Helper()
	r := HostArtifactResolutionRequest{Version: 1, RunID: f.grant.RunID, ReservationID: reservation, RequestID: "resolve-" + reservation, Reason: "owner confirmed failure resolved", Resolution: "resolved"}
	first, err := f.db.resolveHostArtifactAt(context.Background(), "controller", f.token, r, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.db.resolveHostArtifactAt(context.Background(), "controller", f.token, r, now.Add(time.Hour))
	if err != nil || first != again {
		t.Fatal(first, again, err)
	}
}
func hostPlanOnly(p HostArtifactRetentionPlan, id string) HostArtifactRetentionPlan {
	for _, i := range p.Items {
		if i.ReservationID == id {
			p.Items = []HostArtifactRetentionItem{i}
			p.Scanned = 1
			p.Excluded = []HostRetentionExclusion{}
			p.PlanSHA256 = ""
			raw, _ := json.Marshal(p)
			p.PlanSHA256 = runtimeHash(string(raw))
			return p
		}
	}
	p.Items = nil
	return p
}
func TestHostRetentionEligibility(t *testing.T) {
	for _, name := range []string{"success_boundary", "success_expired", "failure_unresolved", "failure_boundary", "failure_expired", "unknown", "pin", "legacy"} {
		t.Run(name, func(t *testing.T) {
			failure := len(name) > 7 && name[:7] == "failure"
			f := newHostRetentionFixture(t, failure, false)
			now := f.at.Add(31 * 24 * time.Hour)
			id := f.grant.HostArtifacts.TerminalReservationID
			switch name {
			case "success_boundary":
				now = f.at.Add(30 * 24 * time.Hour)
			case "failure_boundary", "failure_expired":
				f.resolve(t, id, f.at)
				now = f.at.Add(7 * 24 * time.Hour)
				if name == "failure_expired" {
					now = now.Add(time.Nanosecond)
				}
			case "unknown":
				if _, err := f.db.sql.Exec(`UPDATE segment_runtime SET status='unknown'`); err != nil {
					t.Fatal(err)
				}
			case "pin":
				if err := f.db.PinHostArtifact(context.Background(), "controller", f.token, f.grant.RunID, id, "consumer", "downstream input", "completed", false); err != nil {
					t.Fatal(err)
				}
			case "legacy":
				if _, err := f.db.sql.Exec(`DROP TRIGGER retained_host_artifact_cleanup_authorities;DELETE FROM host_artifact_cleanup_authorities`); err != nil {
					t.Fatal(err)
				}
			}
			p := f.plan(t, now)
			found := false
			for _, i := range p.Items {
				if i.ReservationID == id {
					found = true
				}
			}
			want := name == "success_expired" || name == "failure_expired" || name == "legacy"
			if found != want {
				t.Fatal(name, p.Items, p.Excluded)
			}
			if name == "legacy" && len(p.Items) != 1 {
				t.Fatal("legacy unregistered progress acquired deletion authority", p)
			}
			if _, err := os.Stat(f.proof.Path); err != nil {
				t.Fatal("plan mutated data", err)
			}
		})
	}
}
func TestHostRetentionAllSlotsAndMetadataReplay(t *testing.T) {
	f := newHostRetentionFixture(t, false, true)
	ctx := context.Background()
	now := f.at.Add(31 * 24 * time.Hour)
	p := f.plan(t, now)
	if len(p.Items) != 2 {
		t.Fatal(p)
	}
	// Retire progress first: its registered, published orphan, partial orphan and
	// remaining empty slots must all be fenced without hiding the terminal body.
	progress := hostPlanOnly(p, f.grant.HostArtifacts.ProgressReservationID)
	out, err := f.db.applyHostArtifactRetentionAt(ctx, "controller", f.token, progress, now, openManagedHostDeletion)
	if err != nil || out.Deleted != 1 {
		t.Fatal(out, err)
	}
	tx, err := f.db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := readCommittedHostArtifact(ctx, tx, f.event, 1<<20)
	tx.Rollback()
	if err != nil || len(body) == 0 {
		t.Fatal("progress hid terminal reader", err)
	}
	summary, err := f.db.SummarizeRun(ctx, f.grant.TaskID, "controller", f.token)
	if err != nil || summary.Tasks[0].LatestProgress != nil {
		t.Fatal(summary, err)
	}
	page, err := f.db.CollectPending(ctx, f.grant.TaskID, "", 32, true)
	if err != nil || len(page.Events) != 0 {
		t.Fatal("retired progress still advertised", page, err)
	}
	out, err = f.db.applyHostArtifactRetentionAt(ctx, "controller", f.token, p, now, openManagedHostDeletion)
	if err != nil || out.Deleted != 2 {
		t.Fatal(out, err)
	}
	again, err := f.db.applyHostArtifactRetentionAt(ctx, "controller", f.token, p, now, openManagedHostDeletion)
	if err != nil || again != out {
		t.Fatal(again, err)
	}
	var released, slots, refs int
	var retained int64
	if err = f.db.sql.QueryRow(`SELECT (SELECT count(*) FROM managed_artifact_reservations WHERE class IN ('terminal','progress') AND state='released'),(SELECT count(*) FROM host_artifact_deletion_slots WHERE phase='removed'),(SELECT count(*) FROM managed_artifact_refs WHERE intake_id IS NULL),(SELECT sum(retained_bytes) FROM managed_artifact_reservations WHERE class IN ('terminal','progress'))`).Scan(&released, &slots, &refs, &retained); err != nil || released != 2 || slots != 22 || refs != 2 || retained != 0 {
		t.Fatal(released, slots, refs, retained, err)
	}
	for _, proof := range append(f.progress, f.proof) {
		if _, err = os.Stat(proof.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("body retained", err)
		}
		if _, err = os.Stat(filepath.Join(filepath.Dir(proof.Path), "deletion.marker")); err != nil {
			t.Fatal(err)
		}
	}
	ack, err := f.db.CommitHostEvent(ctx, f.event)
	if err != nil || ack != f.ack {
		t.Fatal("ACK replay read deleted bytes", ack, err)
	}
	tx, err = f.db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readCommittedHostArtifact(ctx, tx, f.event, 1<<20)
	tx.Rollback()
	if err == nil {
		t.Fatal("retired terminal acquired a new reader")
	}
}

type hostRetentionFault struct {
	hostSlotDeleteLease
	fail *bool
}

func (l hostRetentionFault) VerifyRemoved(ctx context.Context) error {
	if !*l.fail {
		*l.fail = true
		return errors.New("injected after unlink")
	}
	return l.hostSlotDeleteLease.VerifyRemoved(ctx)
}
func TestHostRetentionPreparedRecoveryAndFences(t *testing.T) {
	f := newHostRetentionFixture(t, false, true)
	ctx := context.Background()
	now := f.at.Add(31 * 24 * time.Hour)
	p := hostPlanOnly(f.plan(t, now), f.grant.HostArtifacts.ProgressReservationID)
	failed := false
	open := func(ctx context.Context, x hostDeletion, index int, saved []byte) (hostSlotDeleteLease, int64, error) {
		l, n, e := openManagedHostDeletion(ctx, x, index, saved)
		if e == nil && index == 1 {
			l = hostRetentionFault{l, &failed}
		}
		return l, n, e
	}
	if _, err := f.db.applyHostArtifactRetentionAt(ctx, "controller", f.token, p, now, open); err == nil {
		t.Fatal("fault was not reached")
	}
	var state string
	var count int
	if err := f.db.sql.QueryRow(`SELECT state,(SELECT count(*) FROM host_artifact_deletion_slots WHERE reservation_id=r.id AND phase='removed') FROM managed_artifact_reservations r WHERE id=?`, f.grant.HostArtifacts.ProgressReservationID).Scan(&state, &count); err != nil || state != "deleting" || count != 1 {
		t.Fatal(state, count, err)
	}
	if _, err := f.db.sql.Exec(`UPDATE tasks SET status='ready' WHERE id=?`, f.grant.TaskID); err == nil {
		t.Fatal("prepared deletion allowed new consumer")
	}
	if _, err := f.db.sql.Exec(`UPDATE task_runtime SET work_revision=work_revision+1 WHERE task_id=?`, f.grant.TaskID); err == nil {
		t.Fatal("prepared deletion allowed input revision drift")
	}
	if err := f.db.PinHostArtifact(ctx, "controller", f.token, f.grant.RunID, f.grant.HostArtifacts.ProgressReservationID, "late", "input", "done", false); err == nil {
		t.Fatal("late pin accepted")
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(f.db.path)
	if err != nil {
		t.Fatal(err)
	}
	f.db = db
	out, err := db.applyHostArtifactRetentionAt(ctx, "controller", f.token, p, now, openManagedHostDeletion)
	if err != nil || out.Deleted != 1 {
		t.Fatal(out, err)
	}
}

func TestHostGCPolicyAndDurablePendingPlan(t *testing.T) {
	f := newHostRetentionFixture(t, false, false)
	ctx := context.Background()
	now := f.at.Add(31 * 24 * time.Hour)
	control := func(action string, rev int64) HostGCStatus {
		t.Helper()
		s, e := f.db.ControlHostGC(ctx, "controller", f.token, action, HostGCRequest{Version: 1, RunID: f.grant.RunID, ExpectedRevision: rev})
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	if s := control("status", 0); s.Enabled || s.Revision != 0 {
		t.Fatal(s)
	}
	if e := f.db.RunHostGC(ctx, now); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(f.proof.Path); e != nil {
		t.Fatal("default off deleted bytes", e)
	}
	control("enable", 0)
	if _, e := f.db.sql.Exec(`CREATE TRIGGER host_gc_fault BEFORE UPDATE OF phase ON host_artifact_deletions WHEN NEW.phase='removed' BEGIN SELECT RAISE(ABORT,'injected finalize failure');END;`); e != nil {
		t.Fatal(e)
	}
	if e := f.db.RunHostGC(ctx, now); e == nil {
		t.Fatal("finalize fault did not fire")
	}
	if s := control("status", 0); !s.PendingPlan {
		t.Fatal("lost exact GC plan", s)
	}
	control("disable", 1)
	control("enable", 2) // Resume the same durable plan with fresh explicit consent.
	if _, e := f.db.sql.Exec(`DROP TRIGGER host_gc_fault`); e != nil {
		t.Fatal(e)
	}
	if e := f.db.RunHostGC(ctx, now.Add(time.Minute)); e != nil {
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
	if e := f.db.RunHostGC(ctx, now.Add(2*time.Minute)); !errors.Is(e, CodeError("host_gc_owner_changed")) {
		t.Fatal(e)
	}
	if s := control("status", 0); s.Enabled || s.Stopped != "host_gc_owner_changed" {
		t.Fatal(s)
	}
}

func TestHostRetentionZeroRefAuthority(t *testing.T) {
	for _, kind := range []string{"empty", "orphan", "namespace_replaced"} {
		t.Run(kind, func(t *testing.T) {
			f := &hostRetentionFixture{diagnosticsReadFixture: newDiagnosticsReadFixture(t), at: time.Now().UTC().Add(time.Second)}
			ctx := context.Background()
			g := f.grant
			root := filepath.Join(filepath.Dir(f.db.path), "host-spool", g.HostID)
			if kind == "orphan" {
				body := []byte("written before Store ref commit")
				d := artifacts.HostSlotDescriptor{HostSlotBinding: artifacts.HostSlotBinding{Version: 1, ReservationID: g.HostArtifacts.ProgressReservationID, ScopeSHA256: g.HostArtifacts.ScopeSHA256, Class: "progress", Index: 0}, Kind: "progress", Size: int64(len(body)), SHA256: runtimeHash(string(body))}
				if _, err := artifacts.MaterializeHostSlot(ctx, root, d, body); err != nil {
					t.Fatal(err)
				}
			}
			failed := diagnosticOutcomeEvent(g, contract.EventFailed, 1)
			if _, err := f.db.CommitHostEvent(ctx, failed); err != nil {
				t.Fatal(err)
			}
			exited := diagnosticOutcomeEvent(g, contract.EventExited, 2)
			code := 1
			exited.ExitCode = &code
			exited.ActiveMS = 1
			if _, err := f.db.CommitHostEvent(ctx, exited); err != nil {
				t.Fatal(err)
			}
			page, err := f.db.CollectPending(ctx, g.TaskID, "", 32, false)
			if err != nil || len(page.Events) != 1 {
				t.Fatal(page, err)
			}
			event := page.Events[0]
			if err = f.db.AckDecisions(ctx, g.TaskID, "zero-ref-history", runtimeHash("history"), []AckDecision{{EventID: event.EventID, EventRevision: event.EventRevision, EventHash: event.PayloadHash, ActionSlot: event.ActionSlot, Decision: "handled", CommandID: "zero-ref-ack"}}); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.sql.Exec(`UPDATE runtime_events SET created_at=?;UPDATE runtime_decisions SET created_at=?`, f.at.Format(time.RFC3339Nano), f.at.Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			var refs int
			if err = f.db.sql.QueryRow(`SELECT count(*) FROM managed_artifact_refs WHERE intake_id IS NULL`).Scan(&refs); err != nil || refs != 0 {
				t.Fatal("fixture gained a registered namespace anchor", refs, err)
			}
			f.resolve(t, g.HostArtifacts.TerminalReservationID, f.at)
			f.resolve(t, g.HostArtifacts.ProgressReservationID, f.at)
			now := f.at.Add(8 * 24 * time.Hour)
			p := f.plan(t, now)
			if len(p.Items) != 2 {
				t.Fatal(p)
			}
			if kind == "namespace_replaced" {
				if err = os.Rename(root, root+"-original"); err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(filepath.Join(root, "managed-artifacts"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			out, err := f.db.applyHostArtifactRetentionAt(ctx, "controller", f.token, p, now, openManagedHostDeletion)
			if kind == "namespace_replaced" {
				if err == nil || out.Deleted != 0 {
					t.Fatal("adopted replacement namespace", out, err)
				}
				return
			}
			if err != nil || out.Deleted != 2 {
				t.Fatal(out, err)
			}
			var count int
			if err = f.db.sql.QueryRow(`SELECT count(*) FROM host_artifact_deletion_slots WHERE phase='removed'`).Scan(&count); err != nil || count != 22 {
				t.Fatal(count, err)
			}
		})
	}
}
