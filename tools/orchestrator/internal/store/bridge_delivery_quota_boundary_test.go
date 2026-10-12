package store

import (
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func bridgeQuotaUsed(t *testing.T, f bridgeDeliveryFixture) (run, global int64) {
	t.Helper()
	if err := f.db.sql.QueryRow("SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'").Scan(&run); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow("SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''").Scan(&global); err != nil {
		t.Fatal(err)
	}
	return
}

func bridgeQuotaEmit(t *testing.T, f bridgeDeliveryFixture, batch contract.BridgeBatch, token string) contract.BridgeEmitRequest {
	t.Helper()
	var wire string
	if err := f.db.sql.QueryRow("SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	return contract.BridgeEmitRequest{
		BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "emit"},
		BatchID:           batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: token, RenderSHA256: batch.RenderSHA256, WireSHA256: wire,
	}
}

func TestBridgeQuotaOfferedUsesNetStoredByteDelta(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	claim := f.claimReq()
	batch, err := f.db.ClaimBridge(f.hook, claim)
	if err != nil {
		t.Fatal(err)
	}
	emit := bridgeQuotaEmit(t, f, batch, claim.ClaimToken)
	if err := f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatal(err)
	}
	var state, deadline, updated string
	if err := f.db.sql.QueryRow("SELECT state,deadline_at,updated_at FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&state, &deadline, &updated); err != nil {
		t.Fatal(err)
	}
	if state != "EMITTING" || len(deadline) != 30 || len(updated) != 30 {
		t.Fatalf("unexpected old row state=%q deadline=%d updated=%d", state, len(deadline), len(updated))
	}
	runBefore, globalBefore := bridgeQuotaUsed(t, f)
	f.db.runControlLimit, f.db.userControlLimit = runBefore, globalBefore
	offered := contract.BridgeOfferedRequest{BridgeRequestBase: emit.BridgeRequestBase, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claim.ClaimToken, WireSHA256: emit.WireSHA256}
	if err := f.db.MarkBridgeOffered(f.sender, offered); err != nil {
		t.Fatalf("net-negative complete-output declaration refused: %v", err)
	}
	runAfter, globalAfter := bridgeQuotaUsed(t, f)
	if runAfter != runBefore-1 || globalAfter != globalBefore-1 {
		t.Fatalf("real trigger delta run %d->%d global %d->%d, want -1", runBefore, runAfter, globalBefore, globalAfter)
	}
}

func TestBridgeQuotaReceivedUsesNetStoredByteDelta(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	claim := f.claimReq()
	batch, err := f.db.ClaimBridge(f.hook, claim)
	if err != nil {
		t.Fatal(err)
	}
	emit := bridgeQuotaEmit(t, f, batch, claim.ClaimToken)
	if err := f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatal(err)
	}
	var state, updated, receiptID, receivedAt string
	if err := f.db.sql.QueryRow("SELECT state,updated_at,receipt_id,received_at FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&state, &updated, &receiptID, &receivedAt); err != nil {
		t.Fatal(err)
	}
	if state != "EMITTING" || len(updated) != 30 || receiptID != "" || receivedAt != "" {
		t.Fatalf("unexpected old row state=%q updated=%d receipt=%q received=%q", state, len(updated), receiptID, receivedAt)
	}
	runBefore, globalBefore := bridgeQuotaUsed(t, f)
	f.db.runControlLimit, f.db.userControlLimit = runBefore+70, globalBefore+70
	receipt, err := f.db.ReceiveBridge(f.owner, f.receiveReq(batch))
	if err != nil {
		t.Fatalf("exact +70-byte owner attestation refused: %v", err)
	}
	if len(receipt.ReceiptID) != 40 || len(receipt.ReceivedAt) != 30 {
		t.Fatalf("unexpected new receipt bytes id=%d time=%d", len(receipt.ReceiptID), len(receipt.ReceivedAt))
	}
	runAfter, globalAfter := bridgeQuotaUsed(t, f)
	if runAfter != runBefore+70 || globalAfter != globalBefore+70 {
		t.Fatalf("real trigger delta run %d->%d global %d->%d, want +70", runBefore, runAfter, globalBefore, globalAfter)
	}
}

func TestBridgeQuotaAdoptedBeginUsesNetStoredByteDelta(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	first, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec("UPDATE bridge_delivery_attempts SET state='UNCERTAIN',sender_pid=99999999,sender_birth='exited-birth' WHERE attempt_id=?", first.AttemptID); err != nil {
		t.Fatal(err)
	}
	redeliver := contract.BridgeRedeliverRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "redeliver"}, OwnerMode: "local"}, BatchID: first.BatchID, AttemptID: first.AttemptID, Reason: "explicit"}
	if _, err := f.db.RedeliverBridge(f.owner, redeliver); err != nil {
		t.Fatal(err)
	}
	claim := f.claimReq()
	adopted, err := f.db.ClaimBridge(f.hook, claim)
	if err != nil {
		t.Fatal(err)
	}
	var state, updated string
	if err := f.db.sql.QueryRow("SELECT state,updated_at FROM bridge_delivery_attempts WHERE attempt_id=?", adopted.AttemptID).Scan(&state, &updated); err != nil {
		t.Fatal(err)
	}
	if state != "CLAIMED" || len(updated) != 30 {
		t.Fatalf("unexpected adopted row state=%q updated=%d", state, len(updated))
	}
	runBefore, globalBefore := bridgeQuotaUsed(t, f)
	f.db.runControlLimit, f.db.userControlLimit = runBefore+1, globalBefore+1
	emit := bridgeQuotaEmit(t, f, adopted, claim.ClaimToken)
	if err := f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatalf("exact +1-byte adopted output permit refused: %v", err)
	}
	runAfter, globalAfter := bridgeQuotaUsed(t, f)
	if runAfter != runBefore+1 || globalAfter != globalBefore+1 {
		t.Fatalf("real trigger delta run %d->%d global %d->%d, want +1", runBefore, runAfter, globalBefore, globalAfter)
	}
}

func TestBridgeQuotaRedeliverCountsClearedOldAttemptFields(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	first, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec("UPDATE bridge_delivery_attempts SET state='UNCERTAIN',sender_pid=99999999,sender_birth='exited-birth' WHERE attempt_id=?", first.AttemptID); err != nil {
		t.Fatal(err)
	}
	var oldState, oldHash, oldUpdated string
	if err := f.db.sql.QueryRow("SELECT state,claim_token_hash,updated_at FROM bridge_delivery_attempts WHERE attempt_id=?", first.AttemptID).Scan(&oldState, &oldHash, &oldUpdated); err != nil {
		t.Fatal(err)
	}
	if oldState != "UNCERTAIN" || len(oldHash) != 64 || oldUpdated != "" {
		t.Fatalf("unexpected old attempt fields state=%q hash=%d updated=%q", oldState, len(oldHash), oldUpdated)
	}
	// The new reservation stores three 8-byte integers, one 30-byte deadline,
	// one 30-byte creation time, and the listed text fields. The old row
	// clears its 64-byte claim hash while changing state and updated_at.
	expected := int64(24 + len(first.AttemptID) + len(first.BatchID) + len(f.binding) + len("CLAIMED") + 30 + 30 + len("SUPERSEDED_UNCERTAIN") + 30 - len("UNCERTAIN") - 64)
	runBefore, globalBefore := bridgeQuotaUsed(t, f)
	f.db.runControlLimit, f.db.userControlLimit = runBefore+expected, globalBefore+expected
	redeliver := contract.BridgeRedeliverRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "redeliver"}, OwnerMode: "local"}, BatchID: first.BatchID, AttemptID: first.AttemptID, Reason: "explicit"}
	if _, err := f.db.RedeliverBridge(f.owner, redeliver); err != nil {
		t.Fatalf("exact net-growth redelivery refused: %v", err)
	}
	runAfter, globalAfter := bridgeQuotaUsed(t, f)
	if runAfter-runBefore != expected || globalAfter-globalBefore != expected {
		t.Fatalf("real trigger delta run=%d global=%d, want %d", runAfter-runBefore, globalAfter-globalBefore, expected)
	}
}

func TestBridgeQuotaBoundaryNoSecretDiagnostic(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	f.db.runControlLimit = 0
	_, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err == nil || strings.Contains(err.Error(), f.hookToken) {
		t.Fatalf("quota diagnostic leaked or admitted: %v", err)
	}
}
