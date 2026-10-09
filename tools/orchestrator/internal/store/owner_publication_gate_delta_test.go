package store

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// This synthetic latch exercises the gate predicate only. It is not a process
// proof or a valid publication journal and is never passed to Finalize. Actual
// rebind also revokes ACTIVE bridge authority; this tests defensive entry points.
func gateDeltaPending(t *testing.T, db *DB, task string) func() {
	t.Helper()
	var run, launch string
	if err := db.sql.QueryRow(`SELECT t.run_id,r.host_launch_id FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, task).Scan(&run, &launch); err != nil {
		t.Fatal(err)
	}
	const stamp = "2026-10-09T00:00:00Z"
	if _, err := db.sql.Exec(`INSERT INTO owner_rebinds(run_id,host_generation,old_host_generation,origin_pid,origin_birth,attachment_proof_sha256,created_at) VALUES(?,'gate-pending','synthetic-old',1,'synthetic',?,?)`, run, strings.Repeat("a", 64), stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO owner_rebind_publications(run_id,host_generation,launch_id,prior_sha256,expected_sha256,bootstrap_json,facts_json,state,created_at,scratch_bytes) VALUES(?,'gate-pending',?,?,?,'{}','{}','pending',?,2)`, run, launch, strings.Repeat("b", 64), strings.Repeat("c", 64), stamp); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if _, err := db.sql.Exec(`UPDATE owner_rebind_publications SET state='published',published_at=?,scratch_bytes=0 WHERE run_id=? AND host_generation='gate-pending'`, stamp, run); err != nil {
			t.Fatal(err)
		}
	}
}
func gateDeltaRejected(t *testing.T, err error) {
	t.Helper()
	if err == nil || err.Error() != "owner_rebind_publication_pending" {
		t.Errorf("wanted publication gate rejection, got %v", err)
	}
}
func TestPendingPublicationQueuedDecisionReplay(t *testing.T) {
	ctx := context.Background()
	t.Run("continuation", func(t *testing.T) {
		f := newTypedContinuationFixture(t, adapter.ProviderClaude)
		f.start(t)
		s := f.interrupted(t, 250)
		receipt, err := f.db.QueueContinuation(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		publish := gateDeltaPending(t, f.db, s.TaskID)
		before := bridgeBusinessSnapshot(t, f.db)
		_, err = f.db.QueueContinuation(ctx, s)
		gateDeltaRejected(t, err)
		if before != bridgeBusinessSnapshot(t, f.db) {
			t.Fatal("pending replay changed business")
		}
		publish()
		got, err := f.db.QueueContinuation(ctx, s)
		if err != nil || got != receipt {
			t.Fatalf("published replay changed: %+v %v", got, err)
		}
	})
	t.Run("rework_v0", func(t *testing.T) {
		f := newReworkFixture(t)
		receipt, err := f.db.QueueRework(ctx, f.spec)
		if err != nil {
			t.Fatal(err)
		}
		publish := gateDeltaPending(t, f.db, f.spec.TaskID)
		before := bridgeBusinessSnapshot(t, f.db)
		_, err = f.db.QueueRework(ctx, f.spec)
		gateDeltaRejected(t, err)
		if before != bridgeBusinessSnapshot(t, f.db) {
			t.Fatal("pending replay changed business")
		}
		publish()
		got, err := f.db.QueueRework(ctx, f.spec)
		if err != nil || !reflect.DeepEqual(got, receipt) {
			t.Fatalf("published replay changed: %+v %v", got, err)
		}
	})
	t.Run("rework_v1_fresh_and_replay", func(t *testing.T) {
		f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude)
		s := reworkVersionSpec(t, f.spec, 1)
		publish := gateDeltaPending(t, f.db, s.TaskID)
		before := bridgeBusinessSnapshot(t, f.db)
		_, err := f.db.QueueRework(ctx, s)
		gateDeltaRejected(t, err)
		if before != bridgeBusinessSnapshot(t, f.db) {
			t.Fatal("pending fresh request changed business")
		}
		publish()
		receipt, err := f.db.QueueRework(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.db.QueueRework(ctx, s)
		if err != nil || !reflect.DeepEqual(got, receipt) {
			t.Fatalf("published replay changed: %+v %v", got, err)
		}
	})
	t.Run("rework_v1_pending_replay", func(t *testing.T) {
		f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderClaude)
		s := reworkVersionSpec(t, f.spec, 1)
		receipt, err := f.db.QueueRework(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		publish := gateDeltaPending(t, f.db, s.TaskID)
		before := bridgeBusinessSnapshot(t, f.db)
		_, err = f.db.QueueRework(ctx, s)
		gateDeltaRejected(t, err)
		if before != bridgeBusinessSnapshot(t, f.db) {
			t.Fatal("pending replay changed business")
		}
		publish()
		got, err := f.db.QueueRework(ctx, s)
		if err != nil || !reflect.DeepEqual(got, receipt) {
			t.Fatalf("published replay changed: %+v %v", got, err)
		}
	})
}
func TestPendingPublicationBridgeObservationBoundary(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	batch, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatal(err)
	}
	publish := gateDeltaPending(t, f.db, "task")
	req := f.receiveReq(batch)
	before := bridgeBusinessSnapshot(t, f.db)
	t.Run("query", func(t *testing.T) {
		got, err := f.db.QueryBridgeBatch(f.owner, req)
		if err != nil || !reflect.DeepEqual(got, batch) {
			t.Fatalf("pending query lost exact historical batch: %+v %v", got, err)
		}
	})
	t.Run("status", func(t *testing.T) {
		if _, err := f.db.BridgeStatus(f.owner, contract.BridgeStatusRequest{BridgeOwnerRequest: req.BridgeOwnerRequest}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("receive", func(t *testing.T) { _, err := f.db.ReceiveBridge(f.owner, req); gateDeltaRejected(t, err) })
	t.Run("redeliver", func(t *testing.T) {
		_, err := f.db.RedeliverBridge(f.owner, contract.BridgeRedeliverRequest{BridgeOwnerRequest: req.BridgeOwnerRequest, BatchID: batch.BatchID, AttemptID: batch.AttemptID, Reason: "test"})
		gateDeltaRejected(t, err)
	})
	t.Run("activate", func(t *testing.T) {
		_, err := f.db.ActivateBridge(f.owner, contract.BridgeActivateRequest{BridgeOwnerRequest: req.BridgeOwnerRequest, NativeSessionID: "session", ObservationEventID: "observed", ObservationHash: strings.Repeat("a", 64)})
		gateDeltaRejected(t, err)
	})
	t.Run("bind", func(t *testing.T) {
		var digest string
		if err := f.db.sql.QueryRow(`SELECT native_executable_sha256 FROM bridge_bindings WHERE binding_id=?`, f.binding).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		_, err := f.db.BindBridge(f.owner, contract.BridgeBindRequest{SchemaVersion: 1, RequestID: "pending-bind", RunID: "run", ProviderID: "claude", NativePID: os.Getpid(), NativeBirth: f.peerBirth, NativeExecutableSHA256: digest, TransportMode: "hook_pull", OwnerMode: "local", ManifestSHA256: strings.Repeat("a", 64)})
		gateDeltaRejected(t, err)
	})

	t.Run("acp_bind", func(t *testing.T) {
		var digest string
		if err := f.db.sql.QueryRow(`SELECT native_executable_sha256 FROM bridge_bindings WHERE binding_id=?`, f.binding).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		_, err := f.db.BindACPBridge(f.owner, contract.ACPBindRequest{SchemaVersion: 1, RequestID: "pending-acp-bind", RunID: "run", ProviderID: "grok", NativePID: os.Getpid(), NativeBirth: f.peerBirth, NativeExecutableSHA256: digest, TransportMode: contract.BridgeTransportGrokACP, OwnerMode: "local", OwnerCapabilityFile: "/private/gate-fixture-owner", ControlFile: "/private/gate-fixture-control", ManifestSHA256: contract.GrokACPManifestSHA256()})
		gateDeltaRejected(t, err)
	})
	t.Run("observation_still_authenticates", func(t *testing.T) {
		if _, err := f.db.QueryBridgeBatch(context.Background(), req); err == nil || err.Error() != "owner_mismatch" {
			t.Fatalf("unauthenticated query: %v", err)
		}
		if _, err := f.db.BridgeStatus(context.Background(), contract.BridgeStatusRequest{BridgeOwnerRequest: req.BridgeOwnerRequest}); err == nil || err.Error() != "owner_mismatch" {
			t.Fatalf("unauthenticated status: %v", err)
		}
		if err := f.db.UnbindBridge(context.Background(), contract.BridgeUnbindRequest{BridgeOwnerRequest: req.BridgeOwnerRequest}); err == nil || err.Error() != "owner_mismatch" {
			t.Fatalf("unauthenticated unbind: %v", err)
		}
	})

	if before != bridgeBusinessSnapshot(t, f.db) {
		t.Fatal("observation/rejected mutations changed business")
	}
	var state string
	if err := f.db.sql.QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?`, batch.AttemptID).Scan(&state); err != nil || state != "CLAIMED" {
		t.Fatalf("mutation changed delivery authority: %s %v", state, err)
	}
	t.Run("unbind_reduces_authority", func(t *testing.T) {
		if err := f.db.UnbindBridge(f.owner, contract.BridgeUnbindRequest{BridgeOwnerRequest: req.BridgeOwnerRequest}); err != nil {
			t.Fatal(err)
		}
		var status, token string
		if err := f.db.sql.QueryRow(`SELECT status,hook_token_hash FROM bridge_bindings WHERE binding_id=?`, f.binding).Scan(&status, &token); err != nil || status != "DETACHED" || token != "" {
			t.Fatalf("unbind left authority: %s %v", status, err)
		}
	})
	publish()
}
