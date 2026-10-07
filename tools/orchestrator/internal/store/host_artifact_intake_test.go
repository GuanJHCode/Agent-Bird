package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func hostArtifactEventFixture(t *testing.T) (*DB, contract.LaunchCommand, contract.Event, artifacts.Proof) {
	t.Helper()
	db, g := quotaFixture(t)
	root := filepath.Join(filepath.Dir(db.path), "host-spool", g.HostID)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("managed Host terminal")
	d := artifacts.HostSlotDescriptor{HostSlotBinding: artifacts.HostSlotBinding{Version: 1, ReservationID: g.HostArtifacts.TerminalReservationID, ScopeSHA256: g.HostArtifacts.ScopeSHA256, Class: "terminal", Index: 0}, Kind: "terminal", Size: int64(len(body)), SHA256: runtimeHash(string(body))}
	p, err := artifacts.MaterializeHostSlot(context.Background(), root, d, body)
	if err != nil {
		t.Fatal(err)
	}
	desc, _ := d.Canonical()
	proof, _ := json.Marshal(p)
	ref := &contract.ArtifactRef{ID: "result-output.bin", Path: p.Path, Size: p.Size, SHA256: p.SHA256, HostArtifact: &contract.HostArtifactReceipt{Version: 1, Descriptor: desc, Proof: proof}}
	e := contract.Event{Version: 1, ProducerID: g.HostID, EventID: "managed-result", Sequence: 1, Kind: contract.EventResult, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, PayloadHash: p.SHA256, Artifact: ref}
	return db, g, e, p
}

func TestHostArtifactCommitRetainsOneObjectAndReplaysWithoutFile(t *testing.T) {
	db, g, e, p := hostArtifactEventFixture(t)
	ctx := context.Background()
	ack, err := db.CommitHostEvent(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	var refs, retained, metadata int64
	err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM managed_artifact_refs WHERE reservation_id=r.id),retained_bytes,(SELECT SUM(manifest_bytes) FROM managed_artifact_refs WHERE reservation_id=r.id) FROM managed_artifact_reservations r WHERE id=?`, g.HostArtifacts.TerminalReservationID).Scan(&refs, &retained, &metadata)
	if err != nil || refs != 1 || retained != p.Size || metadata != int64(len(e.Artifact.HostArtifact.Descriptor)) {
		t.Fatal("event was not committed with its verified physical ref", refs, retained, metadata, err)
	}
	exit := e
	exit.EventID = "managed-exited"
	exit.Sequence = 2
	exit.Kind = contract.EventExited
	code := 0
	exit.ExitCode = &code
	exit.PayloadHash = runtimeHash("exit")
	if _, err = db.CommitHostEvent(ctx, exit); err != nil {
		t.Fatal(err)
	}
	if err = db.sql.QueryRow(`SELECT retained_bytes FROM managed_artifact_reservations WHERE id=?`, g.HostArtifacts.TerminalReservationID).Scan(&retained); err != nil || retained != p.Size {
		t.Fatal("Exited double charged the same artifact", retained, err)
	}
	if err = os.Remove(p.Path); err != nil {
		t.Fatal(err)
	}
	again, err := db.CommitHostEvent(ctx, e)
	if err != nil || again != ack {
		t.Fatal("exact durable ACK reread missing physical source", again, ack, err)
	}
}

func TestHostArtifactCommitRejectsUnverifiedObject(t *testing.T) {
	for _, mode := range []string{"missing_receipt", "forged_inode", "changed_file", "foreign_path"} {
		t.Run(mode, func(t *testing.T) {
			db, _, e, p := hostArtifactEventFixture(t)
			switch mode {
			case "missing_receipt":
				e.Artifact.HostArtifact = nil
			case "forged_inode":
				p.Inode++
				e.Artifact.HostArtifact.Proof, _ = json.Marshal(p)
			case "changed_file":
				if err := os.WriteFile(p.Path, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign_path":
				e.Artifact.Path = filepath.Join(filepath.Dir(db.path), "unrelated")
			}
			before := continuationQuotaState(t, db)
			if _, err := db.CommitHostEvent(context.Background(), e); err == nil {
				t.Fatal("unverified Host object became durable", mode)
			}
			if after := continuationQuotaState(t, db); after != before {
				t.Fatal("rejected object changed ledger/ref/event state")
			}
		})
	}
}

func TestHostArtifactRawBytesStayOutsideControlQuota(t *testing.T) {
	db, g, e, p := hostArtifactEventFixture(t)
	before, globalBefore := continuationQuotaUsage(t, db)
	if _, err := db.CommitHostEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(e)
	receipt, _ := json.Marshal(e.Artifact.HostArtifact)
	// Independent row cost: runtime envelope, one immutable ref, and the
	// reservation's reserved→active string shrinks by two bytes. The physical
	// manifest was already charged at dispatch; raw data stays in its pool.
	expected := int64(len(body) + 64 + len(p.IntakeID) + len(g.HostArtifacts.TerminalReservationID) + len(g.RunID) + len("0") + len(receipt) - 2)
	after, globalAfter := continuationQuotaUsage(t, db)
	if after-before != expected || globalAfter-globalBefore != expected {
		t.Fatal("managed raw data entered control quota", after-before, globalAfter-globalBefore, expected)
	}
}

func TestHostArtifactRefAndRetentionRollBackWithEvent(t *testing.T) {
	db, g, e, _ := hostArtifactEventFixture(t)
	if _, err := db.sql.Exec(`CREATE TRIGGER reject_host_event BEFORE INSERT ON runtime_events BEGIN SELECT RAISE(ABORT,'fixture_event_failure'); END`); err != nil {
		t.Fatal(err)
	}
	before := continuationQuotaState(t, db)
	if _, err := db.CommitHostEvent(context.Background(), e); err == nil {
		t.Fatal("injected event failure succeeded")
	}
	if after := continuationQuotaState(t, db); after != before {
		t.Fatal("failed event retained partial ref/raw/control state")
	}
	if _, err := db.sql.Exec(`DROP TRIGGER reject_host_event`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommitHostEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	var retained int64
	if err := db.sql.QueryRow(`SELECT retained_bytes FROM managed_artifact_reservations WHERE id=?`, g.HostArtifacts.TerminalReservationID).Scan(&retained); err != nil || retained != e.Artifact.Size {
		t.Fatal(retained, err)
	}
}

func TestHostArtifactProgressRetainsSlotAndChargesOnlyControl(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderClaude)
	f.start(t)
	ctx := context.Background()
	body := []byte(`{"version":1,"kind":"provider_progress","status":"incomplete"}`)
	ref := managedHostArtifactForTest(t, f.db, f.grant, "progress", 0, body)
	e := f.event(contract.EventProgress)
	e.EventID = "managed-progress"
	e.Sequence = 1
	e.Artifact = ref
	e.PayloadHash = ref.SHA256
	before, globalBefore := continuationQuotaUsage(t, f.db)
	if err := f.db.CommitHostProgress(ctx, e, f.receipt.LaunchID, f.receipt.LaunchToken, 10, "host"); err != nil {
		t.Fatal(err)
	}
	var retained, used int64
	if err := f.db.sql.QueryRow(`SELECT (SELECT retained_bytes FROM managed_artifact_reservations WHERE id=?),(SELECT progress_bytes FROM storage_usage WHERE scope='run' AND scope_id=?)`, f.grant.HostArtifacts.ProgressReservationID, f.grant.RunID).Scan(&retained, &used); err != nil {
		t.Fatal(err)
	}
	after, globalAfter := continuationQuotaUsage(t, f.db)
	if retained != int64(len(body)) || used != after-before+2 || used != globalAfter-globalBefore+2 {
		t.Fatal("progress raw/ref/control accounting diverged", retained, used, after-before, globalAfter-globalBefore)
	}
	if err := os.Remove(ref.Path); err != nil {
		t.Fatal(err)
	}
	if err := f.db.CommitHostProgress(ctx, e, f.receipt.LaunchID, f.receipt.LaunchToken, 10, "host"); err != nil {
		t.Fatal("progress durable replay read its file", err)
	}
	snapshot := continuationQuotaState(t, f.db)
	bad := e
	bad.EventID = "wrong-progress-slot"
	bad.Sequence = 2
	if err := f.db.CommitHostProgress(ctx, bad, f.receipt.LaunchID, f.receipt.LaunchToken, 10, "host"); err == nil {
		t.Fatal("different progress index reused old slot")
	}
	if after := continuationQuotaState(t, f.db); after != snapshot {
		t.Fatal("invalid progress changed durable state")
	}
}

func TestHostArtifactLegacyRawRefundDoesNotChangeWhenRefArrives(t *testing.T) {
	db, g, e, _ := hostArtifactEventFixture(t)
	// A pre-v21 event may contain forward-compatible opaque fields. Its raw
	// claim was billed without verification, even if a later valid ref matches.
	downgradeHostMetadataSchema(t, db)
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,1,'legacy-slot','internal','now')`, e.EventID, e.ProducerID, e.Sequence, e.TaskID, e.AttemptID, e.SegmentID, e.PayloadHash, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err = db.migrateRuntime(); err != nil {
		t.Fatal(err)
	}
	next := e
	next.EventID = "later-verified"
	next.Sequence = 2
	if _, err = db.CommitHostEvent(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	var oldFlag, newFlag int
	if err = db.sql.QueryRow(`SELECT (SELECT host_artifact_verified FROM runtime_events WHERE event_id=?),(SELECT host_artifact_verified FROM runtime_events WHERE event_id=?)`, e.EventID, next.EventID).Scan(&oldFlag, &newFlag); err != nil || oldFlag != 0 || newFlag != 1 {
		t.Fatal("migration/ref arrival changed original charge classification", oldFlag, newFlag, err)
	}
	if _, err = db.sql.Exec(`UPDATE runtime_events SET host_artifact_verified=1 WHERE event_id=?`, e.EventID); err == nil {
		t.Fatal("legacy raw charge could be erased")
	}
	before, globalBefore := continuationQuotaUsage(t, db)
	if _, err = db.sql.Exec(`DELETE FROM runtime_events WHERE event_id=?`, e.EventID); err != nil {
		t.Fatal(err)
	}
	after, globalAfter := continuationQuotaUsage(t, db)
	want := int64(len(raw)) + e.Artifact.Size
	if before-after != want || globalBefore-globalAfter != want {
		t.Fatal("later ref changed legacy raw refund", before-after, globalBefore-globalAfter, want, g.SegmentID)
	}
}

func TestHostArtifactReauthorizesInsideVerifiedCommit(t *testing.T) {
	db, _, e, _ := hostArtifactEventFixture(t)
	ctx := context.Background()
	gate := &hostArtifactGate{}
	if _, err := db.commitHostEvent(ctx, e, gate); err != errHostArtifactVerification || gate.item == nil {
		t.Fatal("first pass did not release for verification", err)
	}
	err := artifacts.VerifyHostSlotAndCommit(ctx, gate.item.root, gate.item.descriptor, gate.item.proof, func() error {
		// The owner/coordinator epoch changed after the initial authorization.
		if _, err := db.sql.Exec(`UPDATE runtime_hosts SET coordinator_epoch=2 WHERE id=?`, e.ProducerID); err != nil {
			t.Fatal(err)
		}
		gate.verified = true
		_, err := db.commitHostEvent(ctx, e, gate)
		return err
	})
	if !errors.Is(err, ErrHostRejected) {
		t.Fatal("verified file bypassed current Host authorization", err)
	}
	var refs, retained, events int
	if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM managed_artifact_refs),(SELECT SUM(retained_bytes) FROM managed_artifact_reservations),(SELECT COUNT(*) FROM runtime_events)`).Scan(&refs, &retained, &events); err != nil || refs != 0 || retained != 0 || events != 0 {
		t.Fatal("rejected reauthorization left partial commit", refs, retained, events, err)
	}
}
