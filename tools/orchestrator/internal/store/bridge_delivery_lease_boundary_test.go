package store

import (
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestBridgeClaimDoesNotAdoptExpiredReservationAfterAdmission(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	first, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec("UPDATE bridge_delivery_attempts SET state='UNCERTAIN',sender_pid=99999999,sender_birth='exited-birth' WHERE attempt_id=?", first.AttemptID); err != nil {
		t.Fatal(err)
	}
	redeliver := contract.BridgeRedeliverRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "redeliver"}, OwnerMode: "local"}, BatchID: first.BatchID, AttemptID: first.AttemptID, Reason: "explicit"}
	reserved, err := f.db.RedeliverBridge(f.owner, redeliver)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(1200 * time.Millisecond)
	deadlineText := deadline.Format("2006-01-02T15:04:05.000000000Z")
	if _, err := f.db.sql.Exec("UPDATE bridge_delivery_attempts SET deadline_at=? WHERE attempt_id=?", deadlineText, reserved.AttemptID); err != nil {
		t.Fatal(err)
	}
	available := f.db.storageAvailable
	checked := false
	f.db.storageAvailable = func(path string) (uint64, error) {
		checked = true
		if delay := time.Until(deadline.Add(50 * time.Millisecond)); delay > 0 {
			time.Sleep(delay)
		}
		return available(path)
	}
	batch, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if !checked {
		t.Fatal("adoption did not reach post-validation quota admission")
	}
	if err == nil || batch.AttemptID != "" {
		t.Fatalf("expired reservation returned private send permit: batch=%+v err=%v", batch, err)
	}
	var senderPID int
	var tokenHash, actualDeadline string
	var wire []byte
	if err := f.db.sql.QueryRow("SELECT sender_pid,claim_token_hash,wire_bytes,deadline_at FROM bridge_delivery_attempts WHERE attempt_id=?", reserved.AttemptID).Scan(&senderPID, &tokenHash, &wire, &actualDeadline); err != nil {
		t.Fatal(err)
	}
	if senderPID != 0 || tokenHash != "" || wire != nil || actualDeadline != deadlineText {
		t.Fatalf("expired reservation renewed: pid=%d hash=%q wire-len=%d deadline=%q", senderPID, tokenHash, len(wire), actualDeadline)
	}
	f.db.storageAvailable = available
	reclaimed, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatalf("later legal Hook could not reclaim expired pre-emit reservation: %v", err)
	}
	if reclaimed.AttemptID == reserved.AttemptID || reclaimed.BatchID != first.BatchID {
		t.Fatalf("reclaim must use the immutable batch with a fresh attempt: %+v", reclaimed)
	}
	var oldState string
	if err := f.db.sql.QueryRow("SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?", reserved.AttemptID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if oldState != "EXPIRED" {
		t.Fatalf("old pre-emit reservation state = %q, want EXPIRED", oldState)
	}
}
