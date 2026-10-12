package store

import (
	"context"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/bridgecodec"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"os"
	"strings"
	"testing"
)

func TestAGYClaimBeginAndReceivePreserveBusiness(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	if _, err := f.db.sql.Exec(`UPDATE bridge_bindings SET provider_id='agy' WHERE binding_id=?`, f.binding); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"SessionStart", "UserPromptSubmit", "Stop", "PostInvocation"} {
		req := f.claimReq()
		req.NativeHook = hook
		if _, err := f.db.ClaimBridge(f.hook, req); err == nil {
			t.Fatalf("cross-pair accepted %s", hook)
		}
	}
	before := bridgeBusinessSnapshot(t, f.db)
	req := f.claimReq()
	req.NativeHook = "PreInvocation"
	b, err := f.db.ClaimBridge(f.hook, req)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := bridgecodec.WireBytes(b, b.AttemptID, "agy", "PreInvocation")
	if err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: req.BridgeRequestBase, BatchID: b.BatchID, AttemptID: b.AttemptID, ClaimToken: req.ClaimToken, RenderSHA256: b.RenderSHA256, WireSHA256: bridgeSHA(wire)}
	if err := f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ReceiveBridge(f.owner, f.receiveReq(b)); err != nil {
		t.Fatal(err)
	}
	if after := bridgeBusinessSnapshot(t, f.db); after != before {
		t.Fatal("transport changed business")
	}
}

func TestAGYBeginRechecksExactProviderWrapper(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	if _, err := f.db.sql.Exec(`UPDATE bridge_bindings SET provider_id='agy' WHERE binding_id=?`, f.binding); err != nil {
		t.Fatal(err)
	}
	req := f.claimReq()
	req.NativeHook = "PreInvocation"
	b, err := f.db.ClaimBridge(f.hook, req)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := bridgecodec.WireBytes(b, b.AttemptID, "claude", "SessionStart")
	if err != nil {
		t.Fatal(err)
	}
	hash := bridgeSHA(wrong)
	if _, err := f.db.sql.Exec(`UPDATE bridge_delivery_attempts SET wire_bytes=?,wire_sha256=? WHERE attempt_id=?`, wrong, hash, b.AttemptID); err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: req.BridgeRequestBase, BatchID: b.BatchID, AttemptID: b.AttemptID, ClaimToken: req.ClaimToken, RenderSHA256: b.RenderSHA256, WireSHA256: hash}
	if err := f.db.BeginBridgeEmit(f.sender, emit); err == nil {
		t.Fatal("Begin accepted exact hash for wrong provider wrapper")
	}
}

func TestAGYClaimPreservesEpochCapabilityAndSenderGuards(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	if _, err := f.db.sql.Exec(`UPDATE bridge_bindings SET provider_id='agy' WHERE binding_id=?`, f.binding); err != nil {
		t.Fatal(err)
	}
	for _, which := range []string{"epoch", "capability", "sender", "no_proof"} {
		t.Run(which, func(t *testing.T) {
			req := f.claimReq()
			req.NativeHook = "PreInvocation"
			ctx := f.hook
			switch which {
			case "epoch":
				req.OwnerEpoch = 2
			case "capability":
				req.HookCapability = "UNTRUSTED"
			case "sender":
				ctx = WithBridgeHookProof(context.Background(), BridgeHookProof{BindingID: f.binding, HookToken: f.hookToken, PeerPID: os.Getpid(), PeerBirth: "WRONG-BIRTH"})
			case "no_proof":
				ctx = context.Background()
			}
			if _, err := f.db.ClaimBridge(ctx, req); err == nil {
				t.Fatalf("guard %s accepted", which)
			}
		})
	}
	var n int
	if err := f.db.sql.QueryRow("SELECT count(*) FROM bridge_delivery_attempts").Scan(&n); err != nil || n != 0 {
		t.Fatalf("guard created attempt=%d err=%v", n, err)
	}
}

func TestAGYOversizeHeadNeverSkipsAndEscapedWireIsBounded(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	if _, err := f.db.sql.Exec(`UPDATE bridge_bindings SET provider_id='agy' WHERE binding_id=?`, f.binding); err != nil {
		t.Fatal(err)
	}
	claim := func() contract.BridgeClaimRequest { r := f.claimReq(); r.NativeHook = "PreInvocation"; return r }
	var event contract.Event
	var body string
	if err := f.db.sql.QueryRow("SELECT body_json FROM runtime_events WHERE event_id='result-1'").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		t.Fatal(err)
	}
	event.EventID = strings.Repeat("界\n\"\\", 1600)
	if _, err := f.db.sql.Exec("UPDATE runtime_events SET event_id=?,body_json=? WHERE event_id='result-1'", event.EventID, string(mustJSON(t, event))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec("UPDATE delivery_order SET event_id=? WHERE event_id='result-1'", event.EventID); err != nil {
		t.Fatal(err)
	}
	testBridgeAddEventOversizeTail(t, f)
	if _, err := f.db.ClaimBridge(f.hook, claim()); err == nil || !strings.Contains(err.Error(), "bridge_wire_too_large") {
		t.Fatalf("oversized head skipped or admitted: %v", err)
	}
	var batches, receipts int
	f.db.sql.QueryRow("SELECT COUNT(*) FROM bridge_batches").Scan(&batches)
	f.db.sql.QueryRow("SELECT COUNT(*) FROM collection_receipts").Scan(&receipts)
	if batches != 0 || receipts != 0 {
		t.Fatalf("oversize half-write batches=%d receipts=%d", batches, receipts)
	}
	event.EventID = strings.Repeat("界\n\"\\", 40)
	if _, err := f.db.sql.Exec("UPDATE runtime_events SET event_id=?,body_json=? WHERE sequence=1", event.EventID, string(mustJSON(t, event))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec("UPDATE delivery_order SET event_id=? WHERE task_id='task' AND ordinal=1", event.EventID); err != nil {
		t.Fatal(err)
	}
	batch, err := f.db.ClaimBridge(f.hook, claim())
	if err != nil {
		t.Fatal(err)
	}
	if batch.Events[0].EventID != event.EventID {
		t.Fatal("escaped head lost order")
	}
	var wire []byte
	if err = f.db.sql.QueryRow("SELECT wire_bytes FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil || len(wire) > 8192 {
		t.Fatalf("wire len=%d err=%v", len(wire), err)
	}
	if len(batch.AttemptID) != bridgeAttemptIDWidth {
		t.Fatalf("attempt width=%d", len(batch.AttemptID))
	}
	maxBatch := batch
	maxBatch.AttemptID = "attempt-" + strings.Repeat("f", 24)
	for _, hook := range contract.BridgeEmitHooks("agy") {
		if _, err := bridgecodec.WireBytes(maxBatch, maxBatch.AttemptID, "agy", hook); err != nil {
			t.Fatalf("committed prefix does not fit maximum attempt wrapper: %v", err)
		}
	}
}
