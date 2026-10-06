package store

import (
	"strings"
	"sync"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestBridgeRaceTwoRealHooksAdoptOneOwnerReservation(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	first, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec("UPDATE bridge_delivery_attempts SET state='UNCERTAIN',sender_pid=99999999,sender_birth='exited-birth' WHERE attempt_id=?", first.AttemptID); err != nil {
		t.Fatal(err)
	}
	redeliver := contract.BridgeRedeliverRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "redeliver"}, OwnerMode: "local"}, BatchID: first.BatchID, AttemptID: first.AttemptID, Reason: "owner explicit"}
	reserve, err := f.db.RedeliverBridge(f.owner, redeliver)
	if err != nil {
		t.Fatal(err)
	}
	requests := []contract.BridgeClaimRequest{f.claimReq(), f.claimReq()}
	requests[0].RequestID, requests[1].RequestID = "hook-one", "hook-two"
	type result struct {
		batch contract.BridgeBatch
		err   error
	}
	out := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, req := range requests {
		wg.Add(1)
		go func(req contract.BridgeClaimRequest) {
			defer wg.Done()
			<-start
			batch, e := f.db.ClaimBridge(f.hook, req)
			out <- result{batch, e}
		}(req)
	}
	close(start)
	wg.Wait()
	close(out)
	success, rejected := 0, 0
	for r := range out {
		if r.err == nil {
			success++
			if r.batch.AttemptID != reserve.AttemptID || r.batch.BatchID != first.BatchID || r.batch.RenderSHA256 != first.RenderSHA256 {
				t.Fatalf("adoption changed immutable batch %+v", r.batch)
			}
		} else if strings.Contains(r.err.Error(), "bridge_claim_pending") {
			rejected++
		} else {
			t.Fatalf("unexpected competing claim error: %v", r.err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("adoption winners=%d rejects=%d", success, rejected)
	}
	var sender int
	var hash, wire string
	if err = f.db.sql.QueryRow("SELECT sender_pid,claim_token_hash,wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", reserve.AttemptID).Scan(&sender, &hash, &wire); err != nil {
		t.Fatal(err)
	}
	if sender <= 0 || len(hash) != 64 || len(wire) != 64 {
		t.Fatalf("adoption incomplete sender=%d hash=%q wire=%q", sender, hash, wire)
	}
}
