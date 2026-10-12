package store

import (
	"context"
	"strings"
	"testing"
)

func TestBridgeRebindRevokesOldCapabilityAndUnstartedClaimInOwnerTransaction(t *testing.T) {
	db, launch := quotaFixture(t)
	if _, err := db.sql.Exec(`UPDATE segment_runtime SET status='exited' WHERE segment_id=?`, launch.SegmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_bindings(binding_id,run_id,provider_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES('old','run','claude','origin',7,'birth',8,'native',?,1,'owner','hook_pull',?,'ACTIVE',?,'/tmp/old','bind','now','now')`, strings.Repeat("a", 64), strings.Repeat("b", 64), runtimeHash("old-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,created_at) VALUES('batch','old',1,x'01',?,'now')`, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,created_at) VALUES('claim','batch','old',1,1,?,'CLAIMED','later',8,'native','now')`, runtimeHash("claim")); err != nil {
		t.Fatal(err)
	}
	var coreBefore int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM runtime_events`).Scan(&coreBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.RebindOwner(context.Background(), RebindOwnerSpec{RunID: "run", OriginContextID: "origin", OriginPID: 10, OriginBirth: "new", HostGeneration: "new-gen", AttachmentProofSHA256: strings.Repeat("d", 64)}); err != nil {
		t.Fatal(err)
	}
	var state, hash, claimState string
	if err := db.sql.QueryRow(`SELECT status,hook_token_hash FROM bridge_bindings WHERE binding_id='old'`).Scan(&state, &hash); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id='claim'`).Scan(&claimState); err != nil {
		t.Fatal(err)
	}
	if state != "STALE" || hash != "" || claimState != "STALE" {
		t.Fatalf("old authority survived: %s %q %s", state, hash, claimState)
	}
	var coreAfter int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM runtime_events`).Scan(&coreAfter); err != nil {
		t.Fatal(err)
	}
	if coreAfter != coreBefore {
		t.Fatal("business history changed")
	}
	if _, err := db.sql.Exec(`UPDATE bridge_delivery_attempts SET state='EMITTING' WHERE attempt_id='claim'`); err != nil {
		t.Fatal(err)
	}
	if err := db.RebindOwner(context.Background(), RebindOwnerSpec{RunID: "run", OriginContextID: "origin", OriginPID: 11, OriginBirth: "newer", HostGeneration: "newer-gen", AttachmentProofSHA256: strings.Repeat("e", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id='claim'`).Scan(&claimState); err != nil || claimState != "EMITTING" {
		t.Fatalf("possibly emitted attempt was erased: %s %v", claimState, err)
	}
}
