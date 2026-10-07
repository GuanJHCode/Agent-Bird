package store

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/bridgecodec"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestACPStoreConcurrentClaimIsOneDurableAttempt(t *testing.T) {
	f := newACPStoreFixture(t, true)
	req := contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("same-claim")}
	type result struct {
		reply contract.ACPClaimReply
		err   error
	}
	ch := make(chan result, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := f.db.ClaimACPBridge(f.owner, req); ch <- result{r, e} }()
	}
	wg.Wait()
	close(ch)
	var first string
	for got := range ch {
		if got.err != nil {
			t.Fatal(got.err)
		}
		raw, e := json.Marshal(got.reply)
		if e != nil {
			t.Fatal(e)
		}
		if first == "" {
			first = string(raw)
		} else if first != string(raw) {
			t.Fatal("same request produced different claim")
		}
	}
	var attempts, receipts int
	if err := f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM bridge_delivery_attempts),(SELECT COUNT(*) FROM collection_receipts)`).Scan(&attempts, &receipts); err != nil || attempts != 1 || receipts != 1 {
		t.Fatalf("attempts=%d receipts=%d err=%v", attempts, receipts, err)
	}
	before := schema23Usage(t, f.db.sql, "global", "")
	f.db.userControlLimit = before - 1
	if _, err := f.db.ClaimACPBridge(f.owner, req); err != nil {
		t.Fatalf("no-growth replay rejected: %v", err)
	}
	if got := schema23Usage(t, f.db.sql, "global", ""); got != before {
		t.Fatal("replay grew metadata")
	}
}

func TestACPStoreMultipleTaskReceiptsAndUniqueSecondTurn(t *testing.T) {
	f := newACPStoreFixture(t, true)
	testBridgeAddEvent(t, f.bridgeDeliveryFixture, "task-b", "event-2", 2, "action-2")
	testBridgeAddEvent(t, f.bridgeDeliveryFixture, "task-c", "event-3", 3, "action-3")
	claim, err := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("first")})
	if err != nil {
		t.Fatal(err)
	}
	if len(claim.Batch.DeliveryReceipts) != 3 || len(claim.Batch.Events) != 3 {
		t.Fatalf("multi-task batch=%+v", claim.Batch)
	}
	for _, r := range claim.Batch.DeliveryReceipts {
		var proof string
		if err = f.db.sql.QueryRow(`SELECT proof_hash FROM collection_receipts WHERE id=? AND task_id=?`, r.DeliveryID, r.TaskID).Scan(&proof); err != nil || proof != r.ProofSHA256 {
			t.Fatalf("missing bound proof: %v", err)
		}
	}
	begin := f.beginRequest(claim)
	if err = f.db.BeginACPBridgeEmit(f.owner, begin); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ReceiveACPBridge(f.owner, acpReceiveRequest(begin, 4)); err != nil {
		t.Fatal(err)
	}
	testBridgeAddEvent(t, f.bridgeDeliveryFixture, "task", "event-4", 4, "action-4")
	next, err := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("second")})
	if err != nil {
		t.Fatal(err)
	}
	if next.TurnOrdinal != 2 || next.NativeSessionID != claim.NativeSessionID || len(next.Batch.Events) != 1 || next.Batch.Events[0].EventID != "event-4" {
		t.Fatalf("next turn=%+v", next)
	}
	begin = f.beginRequest(next)
	if err = f.db.BeginACPBridgeEmit(f.owner, begin); err != nil {
		t.Fatal(err)
	}
	before := schema23Usage(t, f.db.sql, "global", "")
	if _, err = f.db.ReceiveACPBridge(f.owner, acpReceiveRequest(begin, 4)); err == nil {
		t.Fatal("RPC response reused across attempts")
	}
	var state string
	var rpc any
	if err = f.db.sql.QueryRow(`SELECT state,acp_rpc_request_id FROM bridge_delivery_attempts WHERE attempt_id=?`, begin.AttemptID).Scan(&state, &rpc); err != nil || state != "EMITTING" || rpc != nil {
		t.Fatalf("failed receive mutated state=%s rpc=%v err=%v", state, rpc, err)
	}
	if got := schema23Usage(t, f.db.sql, "global", ""); got != before {
		t.Fatal("failed RPC uniqueness check changed accounting")
	}
	if _, err = f.db.ReceiveACPBridge(f.owner, acpReceiveRequest(begin, 5)); err != nil {
		t.Fatal(err)
	}
}

func TestACPStoreClaimQuotaCountsOnlyMissingReceipt(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "missing"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			f := newACPStoreFixture(t, true)
			req := contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("quota-claim")}
			tx, err := f.db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			sources, err := bridgeSourceRows(context.Background(), tx, "run", f.binding)
			_ = tx.Rollback()
			if err != nil || len(sources) != 1 {
				t.Fatalf("sources=%v err=%v", sources, err)
			}
			events := []contract.Event{sources[0].event}
			if existing {
				if _, _, err = f.db.CollectionReceipt(context.Background(), "task", events); err != nil {
					t.Fatal(err)
				}
			}
			batch, content, eventJSON, receiptJSON, err := bridgeCandidate(contract.BridgeBatch{SchemaVersion: 1, BatchID: "batch-" + strings.Repeat("a", 24), BindingID: f.binding, OwnerEpoch: 1, TransportMode: contract.BridgeTransportGrokACP}, events)
			if err != nil {
				t.Fatal(err)
			}
			batch.AttemptID = contract.ACPAttemptID(f.binding, 1, req.RequestID)
			prompt, err := bridgecodec.ACPPromptBytes(batch)
			if err != nil {
				t.Fatal(err)
			}
			stamp := bridgeTime(time.Now())
			a := bridgeAttempt{id: batch.AttemptID, batchID: batch.BatchID, bindingID: f.binding, tokenHash: strings.Repeat("a", 64), state: "CLAIMED", deadline: stamp, senderBirth: f.peerBirth, wire: prompt, wireHash: bridgeSHA(prompt)}
			growth := bridgeAttemptBytes(a, stamp, "") + bridgeBatchBytes(batch, content, eventJSON, receiptJSON, stamp)
			for _, e := range batch.Events {
				growth += bridgeItemBytes(batch.BatchID, e)
			}
			before := schema23Usage(t, f.db.sql, "global", "")
			f.db.userControlLimit = before + growth
			f.db.runControlLimit = schema23Usage(t, f.db.sql, "run", "run") + growth
			_, err = f.db.ClaimACPBridge(f.owner, req)
			if existing {
				if err != nil {
					t.Fatalf("existing receipt charged twice: %v", err)
				}
				if got := schema23Usage(t, f.db.sql, "global", ""); got != before+growth {
					t.Fatalf("growth=%d want=%d", got-before, growth)
				}
			} else {
				if err == nil || err.Error() != "control_spool_full" {
					t.Fatalf("missing receipt bypassed quota: %v", err)
				}
				for _, table := range []string{"collection_receipts", "bridge_batches", "bridge_batch_items", "bridge_delivery_attempts"} {
					var n int
					if e := f.db.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); e != nil || n != 0 {
						t.Fatalf("%s count=%d err=%v", table, n, e)
					}
				}
				if got := schema23Usage(t, f.db.sql, "global", ""); got != before {
					t.Fatal("failed quota admission grew metadata")
				}
			}
		})
	}
}

func TestACPStoreBeginAndReceiveQuotaRollback(t *testing.T) {
	f := newACPStoreFixture(t, true)
	claim, err := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("claim")})
	if err != nil {
		t.Fatal(err)
	}
	begin := f.beginRequest(claim)
	for _, stage := range []string{"begin", "receive"} {
		before := schema23Usage(t, f.db.sql, "global", "")
		runBefore := schema23Usage(t, f.db.sql, "run", "run")
		growth := int64(len(begin.NativeSessionID) + 8 + len(bridgeTime(time.Now())) + len("EMITTING") - len("CLAIMED"))
		invoke := func() error { return f.db.BeginACPBridgeEmit(f.owner, begin) }
		if stage == "receive" {
			r := acpReceiveRequest(begin, 4)
			growth = int64(len("bridge-received-") + 24 + len(bridgeTime(time.Now())) + 8 + len(r.ResponseSHA256) + len(r.StopReason))
			invoke = func() error { _, e := f.db.ReceiveACPBridge(f.owner, r); return e }
		}
		f.db.userControlLimit = before + growth - 1
		f.db.runControlLimit = runBefore + growth - 1
		if err = invoke(); err == nil || err.Error() != "control_spool_full" {
			t.Fatalf("%s quota=%v", stage, err)
		}
		if got := schema23Usage(t, f.db.sql, "global", ""); got != before {
			t.Fatalf("%s failed transition changed usage", stage)
		}
		f.db.userControlLimit++
		f.db.runControlLimit++
		if err = invoke(); err != nil {
			t.Fatalf("%s exact quota=%v", stage, err)
		}
		if got := schema23Usage(t, f.db.sql, "global", ""); got != before+growth {
			t.Fatalf("%s growth=%d want=%d", stage, got-before, growth)
		}
	}
}

func TestACPStoreBeginRejectsChangedTupleAndForeignPeer(t *testing.T) {
	f := newACPStoreFixture(t, true)
	claim, err := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("claim")})
	if err != nil {
		t.Fatal(err)
	}
	begin := f.beginRequest(claim)
	before := schema23Usage(t, f.db.sql, "global", "")
	for name, change := range map[string]func(*contract.ACPBeginRequest){"session": func(r *contract.ACPBeginRequest) { r.NativeSessionID = "other" }, "epoch": func(r *contract.ACPBeginRequest) { r.OwnerEpoch++ }, "batch": func(r *contract.ACPBeginRequest) { r.BatchID = "other" }, "attempt": func(r *contract.ACPBeginRequest) { r.AttemptID = "other" }, "claim": func(r *contract.ACPBeginRequest) { r.ClaimToken = "acp-claim-" + strings.Repeat("0", 64) }, "render": func(r *contract.ACPBeginRequest) { r.RenderSHA256 = strings.Repeat("1", 64) }, "prompt": func(r *contract.ACPBeginRequest) { r.PromptSHA256 = strings.Repeat("2", 64) }, "ordinal": func(r *contract.ACPBeginRequest) { r.TurnOrdinal++ }} {
		t.Run(name, func(t *testing.T) {
			r := begin
			change(&r)
			r.RequestID = contract.ACPBeginRequestID(r)
			if err := f.db.BeginACPBridgeEmit(f.owner, r); err == nil {
				t.Fatal("changed tuple accepted")
			}
		})
	}
	p := f.owner.Value(bridgeOwnerKey{}).(BridgeOwnerProof)
	p.PeerPID = f.bind.NativePID
	p.PeerBirth = f.bind.NativeBirth
	if err = f.db.BeginACPBridgeEmit(WithBridgeOwnerProof(context.Background(), p), begin); err == nil {
		t.Fatal("descendant accepted as original ACP controller")
	}
	if err = f.db.BeginACPBridgeEmit(context.Background(), begin); err == nil {
		t.Fatal("unauthenticated Begin accepted")
	}
	if got := schema23Usage(t, f.db.sql, "global", ""); got != before {
		t.Fatal("rejected tuple changed accounting")
	}
	if err = f.db.BeginACPBridgeEmit(f.owner, begin); err != nil {
		t.Fatal(err)
	}
}

func TestACPStoreReopenRecoveryNeverResends(t *testing.T) {
	f := newACPStoreFixture(t, true)
	claim, err := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("claim")})
	if err != nil {
		t.Fatal(err)
	}
	begin := f.beginRequest(claim)
	if err = f.db.BeginACPBridgeEmit(f.owner, begin); err != nil {
		t.Fatal(err)
	}
	path := f.db.path
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f.db = reopened
	if err = f.db.RecoverBridgeDelivery(f.owner); err != nil {
		t.Fatal(err)
	}
	var state, token string
	if err = f.db.sql.QueryRow(`SELECT state,claim_token_hash FROM bridge_delivery_attempts WHERE attempt_id=?`, begin.AttemptID).Scan(&state, &token); err != nil || state != "UNCERTAIN" || token != "" {
		t.Fatalf("recovery state=%s token=%q err=%v", state, token, err)
	}
	if err = f.db.BeginACPBridgeEmit(f.owner, begin); err == nil {
		t.Fatal("reopen authorized send")
	}
	if _, err = f.db.ReceiveACPBridge(f.owner, acpReceiveRequest(begin, 4)); err == nil {
		t.Fatal("late completion settled UNCERTAIN")
	}
}

func TestACPStoreConcurrentBeginReceiveCommitOnce(t *testing.T) {
	f := newACPStoreFixture(t, true)
	claim, err := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("claim")})
	if err != nil {
		t.Fatal(err)
	}
	begin := f.beginRequest(claim)
	before := schema23Usage(t, f.db.sql, "global", "")
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.db.BeginACPBridgeEmit(f.owner, begin) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	beginGrowth := int64(len(begin.NativeSessionID) + 8 + 30 + len("EMITTING") - len("CLAIMED"))
	if got := schema23Usage(t, f.db.sql, "global", ""); got != before+beginGrowth {
		t.Fatalf("concurrent Begin growth=%d want=%d", got-before, beginGrowth)
	}
	receive := acpReceiveRequest(begin, 4)
	type result struct {
		receipt contract.BridgeReceiveReceipt
		err     error
	}
	ch := make(chan result, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := f.db.ReceiveACPBridge(f.owner, receive); ch <- result{r, e} }()
	}
	wg.Wait()
	close(ch)
	var first contract.BridgeReceiveReceipt
	for got := range ch {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if first.ReceiptID == "" {
			first = got.receipt
		} else if first != got.receipt {
			t.Fatal("concurrent Receive returned different receipt")
		}
	}
	receiveGrowth := int64(len(first.ReceiptID) + len(first.ReceivedAt) + 8 + len(receive.ResponseSHA256) + len(receive.StopReason))
	if got := schema23Usage(t, f.db.sql, "global", ""); got != before+beginGrowth+receiveGrowth {
		t.Fatal("concurrent Receive charged more than once")
	}
	var count int
	var ordinal, rpc int64
	if err = f.db.sql.QueryRow(`SELECT COUNT(*),acp_turn_ordinal,acp_rpc_request_id FROM bridge_delivery_attempts WHERE state='RECEIVED'`).Scan(&count, &ordinal, &rpc); err != nil || count != 1 || ordinal != 1 || rpc != 4 {
		t.Fatalf("count=%d ordinal=%d rpc=%d err=%v", count, ordinal, rpc, err)
	}
}

func acpMetadataSnapshot(t *testing.T, db *DB) string {
	t.Helper()
	var out strings.Builder
	for _, table := range []string{"bridge_bindings", "bridge_ingress", "bridge_batches", "bridge_batch_items", "bridge_delivery_attempts", "collection_receipts", "storage_usage"} {
		rows, err := db.sql.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			scan := make([]any, len(cols))
			for i := range values {
				scan[i] = &values[i]
			}
			if err = rows.Scan(scan...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out.WriteString(table)
			out.Write(raw)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}

func TestACPStoreHookBindingRejectsEveryACPTransition(t *testing.T) {
	hook := newBridgeDeliveryFixture(t)
	f := acpStoreFixture{bridgeDeliveryFixture: hook}
	owner := f.ownerRequest("claim")
	activate := contract.ACPActivateRequest{BridgeOwnerRequest: owner, NativeSessionID: "session"}
	activate.RequestID = contract.ACPActivateRequestID(activate)
	begin := contract.ACPBeginRequest{BridgeOwnerRequest: owner, NativeSessionID: "session", BatchID: "batch", AttemptID: "attempt", ClaimToken: "acp-claim-" + strings.Repeat("0", 64), RenderSHA256: strings.Repeat("1", 64), PromptSHA256: strings.Repeat("2", 64), TurnOrdinal: 1}
	begin.RequestID = contract.ACPBeginRequestID(begin)
	before := acpMetadataSnapshot(t, f.db)
	for name, call := range map[string]func() error{"activate": func() error { _, e := f.db.ActivateACPBridge(f.owner, activate); return e }, "claim": func() error {
		_, e := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: owner})
		return e
	}, "begin": func() error { return f.db.BeginACPBridgeEmit(f.owner, begin) }, "receive": func() error { _, e := f.db.ReceiveACPBridge(f.owner, acpReceiveRequest(begin, 4)); return e }} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil || err.Error() != "bridge_mode_unsupported" {
				t.Fatalf("cross-mode error=%v", err)
			}
			if acpMetadataSnapshot(t, f.db) != before {
				t.Fatal("cross-mode request mutated metadata")
			}
		})
	}
}

func TestACPStoreRejectsHookRPCsAndReplacement(t *testing.T) {
	f := newACPStoreFixture(t, true)
	claim, err := f.db.ClaimACPBridge(f.owner, contract.ACPClaimRequest{BridgeOwnerRequest: f.ownerRequest("claim")})
	if err != nil {
		t.Fatal(err)
	}
	owner := f.ownerRequest("hook-request")
	base := owner.BridgeRequestBase
	receive := contract.BridgeReceiveRequest{BridgeOwnerRequest: owner, BatchID: claim.Batch.BatchID, AttemptID: claim.Batch.AttemptID, RenderSHA256: claim.Batch.RenderSHA256}
	token, err := NewBridgeClaimToken()
	if err != nil {
		t.Fatal(err)
	}
	hook := WithBridgeHookProof(f.owner, BridgeHookProof{BindingID: f.binding, HookToken: f.hookToken, PeerPID: f.bind.NativePID, PeerBirth: f.bind.NativeBirth})
	sender := WithBridgeSenderProof(f.owner, BridgeSenderProof{BindingID: f.binding, PeerPID: f.bind.NativePID, PeerBirth: f.bind.NativeBirth})
	emit := contract.BridgeEmitRequest{BridgeRequestBase: base, BatchID: claim.Batch.BatchID, AttemptID: claim.Batch.AttemptID, ClaimToken: token, RenderSHA256: claim.Batch.RenderSHA256, WireSHA256: claim.PromptSHA256}
	ingest := contract.BridgeIngestRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: base, HookCapability: f.hookToken}, Event: contract.BridgeHookEventV1{SchemaVersion: 1, EventID: "hook-event", BindingID: f.binding, OwnerEpoch: 1, ProviderID: "claude", NativeSessionID: claim.NativeSessionID, Kind: "session_started", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Metadata: contract.BridgeHookMetadata{NativeHook: "SessionStart"}}}
	before := acpMetadataSnapshot(t, f.db)
	calls := map[string]struct {
		want string
		call func() error
	}{
		"ingest": {"bridge_mode_unsupported", func() error { _, e := f.db.IngestHook(hook, ingest); return e }},
		"activate": {"bridge_mode_unsupported", func() error {
			_, e := f.db.ActivateBridge(f.owner, contract.BridgeActivateRequest{BridgeOwnerRequest: owner, NativeSessionID: claim.NativeSessionID, ObservationEventID: "hook-event", ObservationHash: strings.Repeat("a", 64)})
			return e
		}},
		"claim": {"bridge_mode_unsupported", func() error {
			r := contract.BridgeClaimRequest{BridgeHookRequest: ingest.BridgeHookRequest, NativeHook: "SessionStart", ClaimToken: token}
			_, e := f.db.ClaimBridge(hook, r)
			return e
		}},
		"begin": {"bridge_no_active_binding", func() error { return f.db.BeginBridgeEmit(sender, emit) }},
		"offered": {"bridge_no_active_binding", func() error {
			return f.db.MarkBridgeOffered(sender, contract.BridgeOfferedRequest{BridgeRequestBase: base, BatchID: emit.BatchID, AttemptID: emit.AttemptID, ClaimToken: token, WireSHA256: emit.WireSHA256})
		}},
		"receive": {"bridge_mode_unsupported", func() error { _, e := f.db.ReceiveBridge(f.owner, receive); return e }},
		"query":   {"bridge_mode_unsupported", func() error { _, e := f.db.QueryBridgeBatch(f.owner, receive); return e }},
		"redeliver": {"bridge_mode_unsupported", func() error {
			_, e := f.db.RedeliverBridge(f.owner, contract.BridgeRedeliverRequest{BridgeOwnerRequest: owner, BatchID: emit.BatchID, AttemptID: emit.AttemptID, Reason: "explicit"})
			return e
		}},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if e := call.call(); e == nil || e.Error() != call.want {
				t.Fatalf("cross-mode error=%v want=%s", e, call.want)
			}
			if acpMetadataSnapshot(t, f.db) != before {
				t.Fatal("cross-mode request mutated metadata")
			}
		})
	}
	bind := contract.BridgeBindRequest(f.bind)
	bind.TransportMode = "hook_pull"
	bind.ProviderID = "claude"
	bind.ManifestSHA256 = strings.Repeat("a", 64)
	bind.RequestID = "hook-replacement"
	if _, err = f.db.BindBridge(f.owner, bind); err != ErrConflict {
		t.Fatalf("Hook replacement=%v", err)
	}
	changed := f.bind
	changed.RequestID = "replacement"
	if _, err = f.db.BindACPBridge(f.owner, changed); err != ErrConflict {
		t.Fatalf("new ACP bind replaced old=%v", err)
	}
	changed = f.bind
	changed.NativeExecutableSHA256 = strings.Repeat("0", 64)
	if _, err = f.db.BindACPBridge(f.owner, changed); err == nil {
		t.Fatal("same RID changed digest accepted")
	}
	if acpMetadataSnapshot(t, f.db) != before {
		t.Fatal("rejected binding changed metadata")
	}
}
