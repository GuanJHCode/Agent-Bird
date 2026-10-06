package store

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestBridgeHistoricalOwnerIsCurrentOwnerWithStableContextOnly(t *testing.T) {
	db, launch := quotaFixture(t)
	ctx := context.Background()
	if _, err := db.sql.Exec(`UPDATE segment_runtime SET status='exited' WHERE segment_id=?`, launch.SegmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_bindings(binding_id,run_id,provider_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES('old','run','claude','origin',7,'birth',8,'old-native',?,1,'owner','hook_pull',?,'ACTIVE',?,'/tmp/old','bind','now','now')`, strings.Repeat("a", 64), strings.Repeat("b", 64), runtimeHash("old-hook")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,created_at) VALUES('batch','old',1,x'01',?,'now')`, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,created_at) VALUES('attempt','batch','old',1,1,?,'EMITTING','later',8,'old-native','now')`, runtimeHash("claim")); err != nil {
		t.Fatal(err)
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RebindOwner(ctx, RebindOwnerSpec{RunID: "run", OriginContextID: "origin", OriginPID: os.Getpid(), OriginBirth: birth, HostGeneration: "next-gen", AttachmentProofSHA256: strings.Repeat("d", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO owner_grants(id,token_hash,controller_thread,origin_pid,origin_birth,created_at) VALUES('origin',?,'master',?,?,'now')`, runtimeHash("new-grant"), os.Getpid(), birth); err != nil {
		t.Fatal(err)
	}
	proof := BridgeOwnerProof{RunID: "run", OwnerMode: "local", ControllerThread: "master", ControlToken: "new-control", OwnerContextID: "origin", OwnerPID: os.Getpid(), OwnerBirth: birth, OwnerGrantID: "origin", OwnerGrantToken: "new-grant", PeerPID: os.Getpid(), PeerBirth: birth}
	// RebindOwner preserves the original run control. This fixture's SubmitPlan
	// generated it, so read only the fixture receipt token from its hash source.
	var currentHash string
	if err := db.sql.QueryRow(`SELECT token_hash FROM run_control WHERE run_id='run'`).Scan(&currentHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE run_control SET token_hash=? WHERE run_id='run'`, runtimeHash(proof.ControlToken)); err != nil {
		t.Fatal(err)
	}
	defer db.sql.Exec(`UPDATE run_control SET token_hash=? WHERE run_id='run'`, currentHash)
	authorized := WithBridgeOwnerProof(ctx, proof)
	for _, op := range []bridgeHistoricalOperation{bridgeHistoryStatus, bridgeHistoryQuery, bridgeHistoryReceive} {
		tx, err := db.sql.BeginTx(authorized, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, err := bridgeOwnerForHistoricalTx(authorized, tx, "old", 1, "local", op)
		_ = tx.Rollback()
		if err != nil || b.view.Status != "STALE" {
			t.Fatalf("current owner historical op %d: %+v %v", op, b.view, err)
		}
	}
	assertReject := func(label string, c context.Context, epoch uint64, mode string, op bridgeHistoricalOperation) {
		t.Helper()
		tx, err := db.sql.BeginTx(c, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = bridgeOwnerForHistoricalTx(c, tx, "old", epoch, mode, op)
		_ = tx.Rollback()
		if err == nil {
			t.Fatalf("%s admitted", label)
		}
	}
	assertReject("old epoch as send authority", authorized, 2, "local", bridgeHistoryStatus)
	assertReject("historical send operation", authorized, 1, "local", bridgeHistoricalOperation(99))
	assertReject("mode mixing", authorized, 1, "native", bridgeHistoryStatus)
	old := proof
	old.OwnerPID = 7
	old.OwnerBirth = "birth"
	old.OwnerGrantToken = "old-grant"
	assertReject("old owner proof", WithBridgeOwnerProof(ctx, old), 1, "local", bridgeHistoryStatus)
	wrong := proof
	wrong.ControlToken = "wrong"
	assertReject("wrong run control", WithBridgeOwnerProof(ctx, wrong), 1, "local", bridgeHistoryStatus)
	if _, err := db.sql.Exec(`UPDATE bridge_bindings SET owner_context_id='foreign' WHERE binding_id='old'`); err != nil {
		t.Fatal(err)
	}
	assertReject("cross-context historical binding", authorized, 1, "local", bridgeHistoryStatus)
	if _, err := db.sql.Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES('other','master',1,'origin',?,?, 'queued','now','collect')`, os.Getpid(), birth); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE bridge_bindings SET owner_context_id='origin',run_id='other' WHERE binding_id='old'`); err != nil {
		t.Fatal(err)
	}
	assertReject("cross-run historical binding", authorized, 1, "local", bridgeHistoryStatus)
	var state string
	if err := db.sql.QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id='attempt'`).Scan(&state); err != nil || state != "EMITTING" {
		t.Fatalf("historical authorization wrote attempt: %s %v", state, err)
	}
}
