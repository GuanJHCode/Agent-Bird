package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func bridgeBusinessSnapshot(t *testing.T, db *DB) string {
	t.Helper()
	h := sha256.New()
	for _, table := range []string{"tasks", "attempts", "segments", "task_runtime", "segment_runtime", "budget_runtime", "runtime_events", "report_events", "runtime_decisions", "delivery_receipts", "review_decisions", "retry_decisions", "runtime_questions"} {
		rows, err := db.sql.Query("SELECT * FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			scan := make([]any, len(columns))
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
			h.Write([]byte(table))
			h.Write(raw)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	return hex.EncodeToString(h.Sum(nil))
}

type bridgeDeliveryFixture struct {
	db        *DB
	owner     context.Context
	hook      context.Context
	sender    context.Context
	binding   string
	hookToken string
	ownerMode string
	peerBirth string
}

func newBridgeDeliveryFixture(t *testing.T) bridgeDeliveryFixture {
	t.Helper()
	db, launch := quotaFixture(t)
	ctx := context.Background()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	grant := OwnerGrant{ID: "owner-delivery-fixture", Token: "grant-test-secret-1287"}
	if _, err = db.sql.Exec(`INSERT INTO owner_grants(id,token_hash,controller_thread,origin_pid,origin_birth,created_at) VALUES(?,?,'master',?,?,'now')`, grant.ID, runtimeHash(grant.Token), os.Getpid(), birth); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`UPDATE runs SET origin_context_id=?,origin_pid=?,origin_birth=?,delivery_mode='collect' WHERE id='run'`, grant.ID, os.Getpid(), birth); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`UPDATE run_control SET token_hash=? WHERE run_id='run'`, runtimeHash("control")); err != nil {
		t.Fatal(err)
	}
	exe, err := process.ExecutablePath(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	digest, err := process.ExecutableDigest(ctx, exe)
	if err != nil {
		t.Fatal(err)
	}
	const binding = "binding-delivery-fixture"
	const hookToken = "hook-test-secret-7490"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.sql.Exec(`INSERT INTO bridge_bindings(binding_id,run_id,provider_id,native_session_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES(?, 'run','claude','session',?,?,?,?,?, ?,1,'owner','hook_pull',?,'ACTIVE',?,'/tmp/fixture','bind',?,?)`, binding, grant.ID, os.Getpid(), birth, os.Getpid(), birth, digest, strings.Repeat("a", 64), runtimeHash(hookToken), now, now)
	if err != nil {
		t.Fatal(err)
	}
	var revision int
	if err = db.sql.QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='task'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	event := contract.Event{Version: 1, EventID: "result-1", RunID: "run", TaskID: "task", AttemptID: launch.AttemptID, SegmentID: launch.SegmentID, WorkRevision: revision, Sequence: 1, Kind: contract.EventResult, PayloadHash: strings.Repeat("b", 64)}
	body, _ := json.Marshal(event)
	_, err = db.sql.Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,1,'action-1','pending',?)`, event.EventID, launch.HostID, 1, "task", launch.AttemptID, launch.SegmentID, event.PayloadHash, string(body), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`INSERT INTO delivery_order(event_id,task_id) VALUES('result-1','task')`); err != nil {
		t.Fatal(err)
	}
	owner := WithBridgeOwnerProof(ctx, BridgeOwnerProof{RunID: "run", OwnerMode: "local", ControllerThread: "master", ControlToken: "control", OwnerContextID: grant.ID, OwnerPID: os.Getpid(), OwnerBirth: birth, OwnerGrantID: grant.ID, OwnerGrantToken: grant.Token, PeerPID: os.Getpid(), PeerBirth: birth})
	hook := WithBridgeHookProof(ctx, BridgeHookProof{BindingID: binding, HookToken: hookToken, PeerPID: os.Getpid(), PeerBirth: birth})
	sender := WithBridgeSenderProof(ctx, BridgeSenderProof{BindingID: binding, PeerPID: os.Getpid(), PeerBirth: birth})
	return bridgeDeliveryFixture{db: db, owner: owner, hook: hook, sender: sender, binding: binding, hookToken: hookToken, ownerMode: "local", peerBirth: birth}
}

func (f bridgeDeliveryFixture) claimReq() contract.BridgeClaimRequest {
	token, err := NewBridgeClaimToken()
	if err != nil {
		panic(err)
	}
	return contract.BridgeClaimRequest{BridgeHookRequest: contract.BridgeHookRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "claim"}, HookCapability: f.hookToken}, NativeHook: "SessionStart", ClaimToken: token}
}
func (f bridgeDeliveryFixture) receiveReq(batch contract.BridgeBatch) contract.BridgeReceiveRequest {
	return contract.BridgeReceiveRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "received"}, OwnerMode: f.ownerMode}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, RenderSHA256: batch.RenderSHA256}
}

func TestBridgeClaimReceiptAndReceiveDoNotChangeBusiness(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	req := f.claimReq()
	if _, err := f.db.ClaimBridge(context.Background(), req); err == nil {
		t.Fatal("claim accepted without trusted Hook proof")
	}
	batch, err := f.db.ClaimBridge(f.hook, req)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(batch.Events) != 1 || batch.Events[0].EventID != "result-1" || len(batch.DeliveryReceipts) != 1 || batch.DeliveryReceipts[0].TaskID != "task" || len(batch.DeliveryReceipts[0].EventIDs) != 1 || batch.DeliveryReceipts[0].EventIDs[0] != "result-1" {
		t.Fatalf("wrong exact batch: %+v", batch)
	}
	if strings.Contains(string(mustJSON(t, batch)), req.ClaimToken) || strings.Contains(string(mustJSON(t, batch)), f.hookToken) {
		t.Fatal("secret in public batch")
	}
	var coreBefore string
	if err := f.db.sql.QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='result-1'`).Scan(&coreBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ReceiveBridge(f.owner, f.receiveReq(batch)); err == nil {
		t.Fatal("unemitted attempt received")
	}
	var wireSHA string
	if err := f.db.sql.QueryRow(`SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?`, batch.AttemptID).Scan(&wireSHA); err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "emit"}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: req.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wireSHA}
	if err := f.db.BeginBridgeEmit(context.Background(), emit); err == nil {
		t.Fatal("fake request Peer supplied output permit")
	}
	if err := f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatalf("begin emit: %v", err)
	}
	receipt, err := f.db.ReceiveBridge(f.owner, f.receiveReq(batch))
	if err != nil {
		t.Fatalf("received: %v", err)
	}
	again, err := f.db.ReceiveBridge(f.owner, f.receiveReq(batch))
	if err != nil || again != receipt {
		t.Fatalf("received duplicate: %+v %v", again, err)
	}
	offered := contract.BridgeOfferedRequest{BridgeRequestBase: emit.BridgeRequestBase, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: req.ClaimToken, WireSHA256: wireSHA}
	if err := f.db.MarkBridgeOffered(f.sender, offered); err != nil {
		t.Fatalf("late offered: %v", err)
	}
	var state, coreAfter string
	if err := f.db.sql.QueryRow(`SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?`, batch.AttemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT delivery_status FROM runtime_events WHERE event_id='result-1'`).Scan(&coreAfter); err != nil {
		t.Fatal(err)
	}
	if state != "RECEIVED" || coreBefore != "pending" || coreAfter != coreBefore {
		t.Fatalf("receipt changed business: %s %s -> %s", state, coreBefore, coreAfter)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestBridgeTwoClaimsAndPreEmitExpiryPreserveImmutableBatch(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	first, second := f.claimReq(), f.claimReq()
	second.RequestID = "second"
	type outcome struct {
		batch contract.BridgeBatch
		err   error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	for _, req := range []contract.BridgeClaimRequest{first, second} {
		wg.Add(1)
		go func(req contract.BridgeClaimRequest) {
			defer wg.Done()
			b, err := f.db.ClaimBridge(f.hook, req)
			results <- outcome{b, err}
		}(req)
	}
	wg.Wait()
	close(results)
	var winner contract.BridgeBatch
	success, failure := 0, 0
	for r := range results {
		if r.err == nil {
			success++
			winner = r.batch
		} else {
			failure++
		}
	}
	if success != 1 || failure != 1 {
		t.Fatalf("two claims success=%d failure=%d", success, failure)
	}
	var content []byte
	if err := f.db.sql.QueryRow("SELECT content_bytes FROM bridge_batches WHERE batch_id=?", winner.BatchID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec("UPDATE bridge_delivery_attempts SET deadline_at='2000-01-01T00:00:00Z' WHERE attempt_id=?", winner.AttemptID); err != nil {
		t.Fatal(err)
	}
	third := f.claimReq()
	third.RequestID = "third"
	third.NativeHook = "UserPromptSubmit"
	reclaimed, err := f.db.ClaimBridge(f.hook, third)
	if err != nil {
		t.Fatalf("safe reclaim: %v", err)
	}
	if reclaimed.BatchID != winner.BatchID || reclaimed.AttemptID == winner.AttemptID || reclaimed.RenderSHA256 != winner.RenderSHA256 {
		t.Fatalf("reclaim changed immutable batch: old=%+v new=%+v", winner, reclaimed)
	}
	var after []byte
	if err := f.db.sql.QueryRow("SELECT content_bytes FROM bridge_batches WHERE batch_id=?", winner.BatchID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(content) != string(after) {
		t.Fatal("immutable content changed on attempt")
	}
	var oldState string
	if err := f.db.sql.QueryRow("SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?", winner.AttemptID).Scan(&oldState); err != nil || oldState != "EXPIRED" {
		t.Fatalf("old state=%s %v", oldState, err)
	}
	if _, err := f.db.ClaimBridge(f.hook, third); err == nil {
		t.Fatal("second usable claim on same batch")
	}
}

func TestBridgeChangedSourceBlocksSafeReclaimAndKeepsOldClaim(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	batch, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec("UPDATE bridge_delivery_attempts SET deadline_at='2000-01-01T00:00:00Z' WHERE attempt_id=?", batch.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec("UPDATE runtime_events SET payload_hash=? WHERE event_id='result-1'", strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ClaimBridge(f.hook, f.claimReq()); err == nil || !strings.Contains(err.Error(), "bridge_source_changed") {
		t.Fatalf("changed source admitted: %v", err)
	}
	var state string
	if err = f.db.sql.QueryRow("SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&state); err != nil || state != "CLAIMED" {
		t.Fatalf("partial reclaim %q %v", state, err)
	}
}

func TestBridgeReportHashOnlyCanaryRejectsEmbeddedPublicSelector(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	reportToken := "report-token-" + hex.EncodeToString(random)
	if _, err := f.db.sql.Exec("UPDATE report_capabilities SET token_hash=? WHERE run_id='run'", runtimeHash(reportToken)); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := f.db.sql.QueryRow("SELECT body_json FROM runtime_events WHERE event_id='result-1'").Scan(&body); err != nil {
		t.Fatal(err)
	}
	var event contract.Event
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		t.Fatal(err)
	}
	event.EventID = "prefix-" + reportToken + "-suffix"
	if _, err := f.db.sql.Exec("UPDATE runtime_events SET event_id=?,body_json=? WHERE event_id='result-1'", event.EventID, string(mustJSON(t, event))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec("UPDATE delivery_order SET event_id=? WHERE event_id='result-1'", event.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimBridge(f.hook, f.claimReq()); err == nil || !strings.Contains(err.Error(), "bridge_public_secret") {
		t.Fatalf("hash-only report token leaked: %v", err)
	}
	var batches, receipts int
	if err := f.db.sql.QueryRow("SELECT COUNT(*) FROM bridge_batches").Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow("SELECT COUNT(*) FROM collection_receipts").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if batches != 0 || receipts != 0 {
		t.Fatalf("secret refusal half-wrote batches=%d receipts=%d", batches, receipts)
	}
	extra := WithBridgePublicSecrets(f.hook, event.EventID)
	if _, err := f.db.ClaimBridge(extra, f.claimReq()); err == nil {
		t.Fatal("known extra secret admitted")
	}
}

func TestBridgeRedeliverReservationCannotEmitBeforeRealHookAdopts(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	claim := f.claimReq()
	batch, err := f.db.ClaimBridge(f.hook, claim)
	if err != nil {
		t.Fatal(err)
	}
	var wire string
	if err = f.db.sql.QueryRow("SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "emit"}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claim.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wire}
	if err = f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ClaimBridge(f.hook, f.claimReq()); err == nil {
		t.Fatal("EMITTING auto-reclaimed")
	}
	if _, err = f.db.sql.Exec("UPDATE bridge_delivery_attempts SET state='UNCERTAIN' WHERE attempt_id=?", batch.AttemptID); err != nil {
		t.Fatal(err)
	}
	redeliver := contract.BridgeRedeliverRequest{BridgeOwnerRequest: contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "redeliver"}, OwnerMode: "local"}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, Reason: "owner explicitly requests recovery"}
	if _, err = f.db.RedeliverBridge(f.owner, redeliver); err == nil {
		t.Fatal("live sender redelivered")
	}
	if _, err = f.db.sql.Exec("UPDATE bridge_delivery_attempts SET sender_pid=99999999,sender_birth='exited-birth' WHERE attempt_id=?", batch.AttemptID); err != nil {
		t.Fatal(err)
	}
	reserve, err := f.db.RedeliverBridge(f.owner, redeliver)
	if err != nil {
		t.Fatalf("dead sender redeliver: %v", err)
	}
	if reserve.State != "CLAIMED" || reserve.AttemptID == batch.AttemptID {
		t.Fatalf("reserve=%+v", reserve)
	}
	var pid int
	var hash, storedWire string
	if err = f.db.sql.QueryRow("SELECT sender_pid,claim_token_hash,wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", reserve.AttemptID).Scan(&pid, &hash, &storedWire); err != nil {
		t.Fatal(err)
	}
	if pid != 0 || hash != "" || storedWire != "" {
		t.Fatalf("reserve has send authority pid=%d hash=%q wire=%q", pid, hash, storedWire)
	}
	emit.AttemptID = reserve.AttemptID
	if err = f.db.BeginBridgeEmit(f.sender, emit); err == nil {
		t.Fatal("unassigned reserve emitted")
	}
	unassigned := contract.BridgeBatch{BatchID: batch.BatchID, AttemptID: reserve.AttemptID, RenderSHA256: batch.RenderSHA256}
	if _, err = f.db.ReceiveBridge(f.owner, f.receiveReq(unassigned)); err == nil {
		t.Fatal("unassigned reserve received")
	}
	next := f.claimReq()
	next.RequestID = "adopt"
	next.NativeHook = "UserPromptSubmit"
	adopted, err := f.db.ClaimBridge(f.hook, next)
	if err != nil {
		t.Fatalf("real Hook adoption: %v", err)
	}
	if adopted.BatchID != batch.BatchID || adopted.AttemptID != reserve.AttemptID || adopted.RenderSHA256 != batch.RenderSHA256 {
		t.Fatalf("adoption mutated batch %+v", adopted)
	}
	if _, err = f.db.ReceiveBridge(f.owner, f.receiveReq(batch)); err == nil || !strings.Contains(err.Error(), "bridge_attempt_superseded") {
		t.Fatalf("old receive revived: %v", err)
	}
}

func TestBridgeQueryAndStatusUseAuthenticatedExactCursor(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	batch, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatal(err)
	}
	query := f.receiveReq(batch)
	if _, err := f.db.QueryBridgeBatch(context.Background(), query); err == nil {
		t.Fatal("unauthorized query")
	}
	got, err := f.db.QueryBridgeBatch(f.owner, query)
	if err != nil || got.RenderSHA256 != batch.RenderSHA256 {
		t.Fatalf("exact query %+v %v", got, err)
	}
	var before int
	if err = f.db.sql.QueryRow("SELECT COUNT(*) FROM bridge_delivery_attempts").Scan(&before); err != nil {
		t.Fatal(err)
	}
	req := contract.BridgeStatusRequest{BridgeOwnerRequest: query.BridgeOwnerRequest}
	status, err := f.db.BridgeStatus(f.owner, req)
	if err != nil || len(status.Batches) != 1 || status.Batches[0].BatchID != batch.BatchID {
		t.Fatalf("status %+v %v", status, err)
	}
	req.Cursor = "bridge-status-v1-1-forged"
	if _, err = f.db.BridgeStatus(f.owner, req); err == nil {
		t.Fatal("forged cursor")
	}
	var after int
	if err = f.db.sql.QueryRow("SELECT COUNT(*) FROM bridge_delivery_attempts").Scan(&after); err != nil || before != after {
		t.Fatalf("query/status wrote attempts %d -> %d %v", before, after, err)
	}
}

func testBridgeAddEvent(t *testing.T, f bridgeDeliveryFixture, task, id string, seq int, slot string) {
	t.Helper()
	var host, attempt, segment string
	var work int
	if err := f.db.sql.QueryRow("SELECT producer_id,attempt_id,segment_id FROM runtime_events WHERE event_id='result-1'").Scan(&host, &attempt, &segment); err != nil {
		t.Fatal(err)
	}
	if task != "task" {
		attempt, segment = "attempt-"+task, "segment-"+task
		if _, err := f.db.sql.Exec("INSERT OR IGNORE INTO tasks(id,run_id,status,dependencies_json,max_attempts,created_at) VALUES(?,'run','running','[]',1,'now')", task); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.sql.Exec("INSERT OR IGNORE INTO attempts(id,task_id,attempt_no,status,created_at) VALUES(?,?,1,'running','now')", attempt, task); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.sql.Exec("INSERT OR IGNORE INTO segments(id,attempt_id,segment_no,status,created_at) VALUES(?,?,1,'running','now')", segment, attempt); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.sql.Exec("INSERT OR IGNORE INTO task_runtime(task_id,host_launch_id,work_revision,budget_group_id,max_active_ms) SELECT ?,host_launch_id,work_revision,budget_group_id,max_active_ms FROM task_runtime WHERE task_id='task'", task); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.sql.QueryRow("SELECT work_revision FROM task_runtime WHERE task_id=?", task).Scan(&work); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%064x", seq)
	event := contract.Event{Version: 1, EventID: id, RunID: "run", TaskID: task, AttemptID: attempt, SegmentID: segment, WorkRevision: work, Sequence: int64(seq), Kind: contract.EventResult, PayloadHash: hash}
	if slot == "" {
		event.Kind = contract.EventSession
	}
	if _, err := f.db.sql.Exec("INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,'pending','now')", id, host, seq, task, attempt, segment, hash, string(mustJSON(t, event)), seq, slot); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec("INSERT INTO delivery_order(event_id,task_id) VALUES(?,?)", id, task); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeThreeTaskExactEightPrefixAndReceipts(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	entries := []struct{ task, id string }{{"task-b", "event-2"}, {"task-c", "event-3"}, {"task", "event-4"}, {"task-b", "event-5"}, {"task-c", "event-6"}, {"task", "event-7"}, {"task-b", "event-8"}, {"task-c", "event-9"}}
	for i, e := range entries {
		testBridgeAddEvent(t, f, e.task, e.id, i+2, fmt.Sprintf("action-%d", i+2))
	}
	batch, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 8 || batch.Events[7].EventID != "event-8" {
		t.Fatalf("wrong prefix %#v", batch.Events)
	}
	if len(batch.DeliveryReceipts) != 3 {
		t.Fatalf("receipt count=%d", len(batch.DeliveryReceipts))
	}
	want := [][]string{{"result-1", "event-4", "event-7"}, {"event-2", "event-5", "event-8"}, {"event-3", "event-6"}}
	wantProof := []string{"3eb37a057a065d9bd7e01383bdbc06f0c2cb68e7fe843b1ec3c41d5c4d59624c", "a3054c9937285ad4cf4ad7cda831f482dab5350baa57acc843c8aa1e8d8d1c96", "2beb030cc9d140b6de6abf905aa8f2b1f4e40578a272f647ea76352bde68ca3e"}
	for i, receipt := range batch.DeliveryReceipts {
		if strings.Join(receipt.EventIDs, ",") != strings.Join(want[i], ",") {
			t.Fatalf("task %d receipt %+v", i, receipt)
		}
		if receipt.ProofSHA256 != wantProof[i] || receipt.DeliveryID != "collection-"+wantProof[i] || receipt.ProofMode != "collect" {
			t.Fatalf("task %d proof %+v", i, receipt)
		}
	}
	var count int
	if err = f.db.sql.QueryRow("SELECT COUNT(*) FROM bridge_batch_items WHERE batch_id=?", batch.BatchID).Scan(&count); err != nil || count != 8 {
		t.Fatalf("items %d %v", count, err)
	}
	var pending string
	if err = f.db.sql.QueryRow("SELECT delivery_status FROM runtime_events WHERE event_id='event-9'").Scan(&pending); err != nil || pending != "pending" {
		t.Fatalf("ninth %s %v", pending, err)
	}
}

func TestBridgeObservationHasNoBusinessReceipt(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	var event contract.Event
	var body string
	if err := f.db.sql.QueryRow("SELECT body_json FROM runtime_events WHERE event_id='result-1'").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		t.Fatal(err)
	}
	event.Kind = contract.EventSession
	if _, err := f.db.sql.Exec("UPDATE runtime_events SET action_slot='',body_json=? WHERE event_id='result-1'", string(mustJSON(t, event))); err != nil {
		t.Fatal(err)
	}
	batch, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 1 || len(batch.DeliveryReceipts) != 0 {
		t.Fatalf("observation fabricated receipt: %+v", batch)
	}
	var n int
	if err = f.db.sql.QueryRow("SELECT COUNT(*) FROM collection_receipts").Scan(&n); err != nil || n != 0 {
		t.Fatalf("observation persisted proof %d %v", n, err)
	}
}

func TestBridgeOversizeHeadNeverSkipsAndEscapedWireIsBounded(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
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
	if _, err := f.db.ClaimBridge(f.hook, f.claimReq()); err == nil || !strings.Contains(err.Error(), "bridge_wire_too_large") {
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
	batch, err := f.db.ClaimBridge(f.hook, f.claimReq())
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
}

func testBridgeAddEventOversizeTail(t *testing.T, f bridgeDeliveryFixture) {
	t.Helper()
	var host, attempt, segment string
	var work int
	if err := f.db.sql.QueryRow("SELECT producer_id,attempt_id,segment_id FROM runtime_events WHERE sequence=1").Scan(&host, &attempt, &segment); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow("SELECT work_revision FROM task_runtime WHERE task_id='task'").Scan(&work); err != nil {
		t.Fatal(err)
	}
	e := contract.Event{Version: 1, EventID: "small-tail", RunID: "run", TaskID: "task", AttemptID: attempt, SegmentID: segment, WorkRevision: work, Sequence: 2, Kind: contract.EventResult, PayloadHash: strings.Repeat("d", 64)}
	if _, err := f.db.sql.Exec("INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,2,'action-tail','pending','now')", e.EventID, host, 2, "task", attempt, segment, e.PayloadHash, string(mustJSON(t, e))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec("INSERT INTO delivery_order(event_id,task_id) VALUES('small-tail','task')"); err != nil {
		t.Fatal(err)
	}
}

func TestBridgeReceivedLeavesEveryBusinessTableByteStable(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	req := f.claimReq()
	batch, err := f.db.ClaimBridge(f.hook, req)
	if err != nil {
		t.Fatal(err)
	}
	var wire string
	if err = f.db.sql.QueryRow("SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "emit"}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: req.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wire}
	if err = f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatal(err)
	}
	before := bridgeBusinessSnapshot(t, f.db)
	if _, err = f.db.ReceiveBridge(f.owner, f.receiveReq(batch)); err != nil {
		t.Fatal(err)
	}
	after := bridgeBusinessSnapshot(t, f.db)
	if before != after {
		t.Fatalf("Received changed core business SQL: %s -> %s", before, after)
	}
}

func TestBridgeHistoricalStaleQueryAndConfirmedDeadReceived(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	claim := f.claimReq()
	batch, err := f.db.ClaimBridge(f.hook, claim)
	if err != nil {
		t.Fatal(err)
	}
	var wire string
	if err = f.db.sql.QueryRow("SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "emit"}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claim.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wire}
	if err = f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec("UPDATE bridge_bindings SET status='STALE',hook_token_hash='' WHERE binding_id=?", f.binding); err != nil {
		t.Fatal(err)
	}
	original, err := f.db.QueryBridgeBatch(f.owner, f.receiveReq(batch))
	if err != nil || original.RenderSHA256 != batch.RenderSHA256 || original.Events[0].EventID != "result-1" {
		t.Fatalf("historical read %+v %v", original, err)
	}
	if _, err = f.db.ReceiveBridge(f.owner, f.receiveReq(batch)); err == nil {
		t.Fatal("old sender alive allowed STALE receipt")
	}
	if err = f.db.BeginBridgeEmit(f.sender, emit); err == nil {
		t.Fatal("STALE sender regained output permission")
	}
	if _, err = f.db.sql.Exec("UPDATE bridge_delivery_attempts SET sender_pid=99999999,sender_birth='exited-birth' WHERE attempt_id=?", batch.AttemptID); err != nil {
		t.Fatal(err)
	}
	owner, ok := BridgeOwnerProofFromContext(f.owner)
	if !ok {
		t.Fatal("missing owner proof")
	}
	wrong := owner
	wrong.ControlToken = "wrong"
	if _, err = f.db.QueryBridgeBatch(WithBridgeOwnerProof(context.Background(), wrong), f.receiveReq(batch)); err == nil {
		t.Fatal("old or wrong control read history")
	}
	before := bridgeBusinessSnapshot(t, f.db)
	receipt, err := f.db.ReceiveBridge(f.owner, f.receiveReq(batch))
	if err != nil || receipt.ReceiptID == "" {
		t.Fatalf("current owner historical attestation %+v %v", receipt, err)
	}
	if after := bridgeBusinessSnapshot(t, f.db); after != before {
		t.Fatalf("historical attestation changed business SQL %s -> %s", before, after)
	}
	statusReq := contract.BridgeStatusRequest{BridgeOwnerRequest: f.receiveReq(batch).BridgeOwnerRequest}
	status, err := f.db.BridgeStatus(f.owner, statusReq)
	if err != nil || len(status.Batches) != 1 || status.Batches[0].State != "RECEIVED" {
		t.Fatalf("historical status %+v %v", status, err)
	}
}

func TestBridgeBeginEmitAgainstUnbindHasSingleWinner(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	claim := f.claimReq()
	batch, err := f.db.ClaimBridge(f.hook, claim)
	if err != nil {
		t.Fatal(err)
	}
	var wire string
	if err = f.db.sql.QueryRow("SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "emit"}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claim.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wire}
	unbind := contract.BridgeUnbindRequest{BridgeOwnerRequest: f.receiveReq(batch).BridgeOwnerRequest}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var beginErr, unbindErr error
	go func() { defer wg.Done(); <-start; beginErr = f.db.BeginBridgeEmit(f.sender, emit) }()
	go func() { defer wg.Done(); <-start; unbindErr = f.db.UnbindBridge(f.owner, unbind) }()
	close(start)
	wg.Wait()
	if unbindErr != nil {
		t.Fatalf("unbind: %v", unbindErr)
	}
	var state, bindingState string
	if err = f.db.sql.QueryRow("SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err = f.db.sql.QueryRow("SELECT status FROM bridge_bindings WHERE binding_id=?", f.binding).Scan(&bindingState); err != nil {
		t.Fatal(err)
	}
	if bindingState != "DETACHED" || beginErr == nil && state != "EMITTING" || beginErr != nil && state != "STALE" {
		t.Fatalf("two winners/lost state begin=%v state=%s binding=%s", beginErr, state, bindingState)
	}
}

func TestBridgeQuotaRefusalRollsBackExactClaim(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	var used int64
	if err := f.db.sql.QueryRow("SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'").Scan(&used); err != nil {
		t.Fatal(err)
	}
	f.db.runControlLimit = used + 1
	if _, err := f.db.ClaimBridge(f.hook, f.claimReq()); err == nil || !strings.Contains(err.Error(), "control_spool_full") {
		t.Fatalf("quota admitted claim: %v", err)
	}
	for _, table := range []string{"bridge_batches", "bridge_batch_items", "bridge_delivery_attempts", "collection_receipts"} {
		var n int
		if err := f.db.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s partial rows=%d err=%v", table, n, err)
		}
	}
}

func TestBridgePriorFactoryClaimHashBlocksLaterEmbeddedSelector(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	first := f.claimReq()
	if len(first.ClaimToken) != 77 || !strings.HasPrefix(first.ClaimToken, "bridge-claim-") {
		t.Fatalf("factory token shape length=%d", len(first.ClaimToken))
	}
	batch, err := f.db.ClaimBridge(f.hook, first)
	if err != nil {
		t.Fatal(err)
	}
	var wire string
	if err = f.db.sql.QueryRow("SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "emit"}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: first.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wire}
	if err = f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ReceiveBridge(f.owner, f.receiveReq(batch)); err != nil {
		t.Fatal(err)
	}
	secretID := "prefix-" + first.ClaimToken + "-suffix"
	testBridgeAddEvent(t, f, "task", secretID, 2, "action-2")
	second := f.claimReq()
	if second.ClaimToken == first.ClaimToken {
		t.Fatal("factory reused claim token")
	}
	if _, err = f.db.ClaimBridge(f.hook, second); err == nil || !strings.Contains(err.Error(), "bridge_public_secret") {
		t.Fatalf("stored claim hash missed public canary: %v", err)
	}
	var count int
	if err = f.db.sql.QueryRow("SELECT COUNT(*) FROM bridge_batches").Scan(&count); err != nil || count != 1 {
		t.Fatalf("leak refusal wrote batch count=%d err=%v", count, err)
	}
}

func TestBridgeWrongSenderTokenEpochAndHashCannotEmit(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	claim := f.claimReq()
	batch, err := f.db.ClaimBridge(f.hook, claim)
	if err != nil {
		t.Fatal(err)
	}
	var wire string
	if err = f.db.sql.QueryRow("SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "emit"}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claim.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wire}
	evil := emit
	evil.ClaimToken = f.claimReq().ClaimToken
	if err := f.db.BeginBridgeEmit(f.sender, evil); err == nil {
		t.Fatal("wrong claim token")
	}
	evil = emit
	evil.OwnerEpoch = 2
	if err := f.db.BeginBridgeEmit(f.sender, evil); err == nil {
		t.Fatal("old epoch accepted")
	}
	evil = emit
	evil.RenderSHA256 = strings.Repeat("f", 64)
	if err := f.db.BeginBridgeEmit(f.sender, evil); err == nil {
		t.Fatal("wrong render hash")
	}
	evil = emit
	evil.WireSHA256 = strings.Repeat("f", 64)
	if err := f.db.BeginBridgeEmit(f.sender, evil); err == nil {
		t.Fatal("wrong wire hash")
	}
	evil = emit
	evil.Peer = contract.BridgeVerifiedPeer{PID: os.Getpid(), Birth: f.peerBirth}
	if err := f.db.BeginBridgeEmit(context.Background(), evil); err == nil {
		t.Fatal("informational fake Peer gained output permit")
	}
	wrongSender := WithBridgeSenderProof(context.Background(), BridgeSenderProof{BindingID: f.binding, PeerPID: os.Getpid(), PeerBirth: "wrong-birth"})
	if err := f.db.BeginBridgeEmit(wrongSender, emit); err == nil {
		t.Fatal("wrong OS birth gained output permit")
	}
	var state string
	if err = f.db.sql.QueryRow("SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&state); err != nil || state != "CLAIMED" {
		t.Fatalf("invalid sender changed state=%q err=%v", state, err)
	}
}

func TestBridgeReclaimRejectsChangedRevisionSlotOrWork(t *testing.T) {
	cases := []struct {
		name, sql string
	}{
		{"revision", "UPDATE runtime_events SET event_revision=2 WHERE event_id='result-1'"},
		{"slot", "UPDATE runtime_events SET action_slot='other-slot' WHERE event_id='result-1'"},
		{"work", "UPDATE task_runtime SET work_revision=2 WHERE task_id='task'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBridgeDeliveryFixture(t)
			batch, err := f.db.ClaimBridge(f.hook, f.claimReq())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.sql.Exec("UPDATE bridge_delivery_attempts SET deadline_at='2000-01-01T00:00:00Z' WHERE attempt_id=?", batch.AttemptID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.sql.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.ClaimBridge(f.hook, f.claimReq()); err == nil || !strings.Contains(err.Error(), "bridge_source_changed") {
				t.Fatalf("changed %s admitted: %v", tc.name, err)
			}
			var state string
			if err = f.db.sql.QueryRow("SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&state); err != nil || state != "CLAIMED" {
				t.Fatalf("changed source half-wrote %s %v", state, err)
			}
		})
	}
}

func TestBridgeReopenRecoveryMakesEmittingUncertainWithoutBusinessChange(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	claim := f.claimReq()
	batch, err := f.db.ClaimBridge(f.hook, claim)
	if err != nil {
		t.Fatal(err)
	}
	var wire string
	if err = f.db.sql.QueryRow("SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "emit"}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claim.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wire}
	if err = f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatal(err)
	}
	business := bridgeBusinessSnapshot(t, f.db)
	path := f.db.path
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.RecoverBridgeDelivery(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state, tokenHash string
	if err = reopened.sql.QueryRow("SELECT state,claim_token_hash FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&state, &tokenHash); err != nil || state != "UNCERTAIN" || tokenHash != "" {
		t.Fatalf("recovery state=%s hash=%q err=%v", state, tokenHash, err)
	}
	if _, err = reopened.ClaimBridge(f.hook, f.claimReq()); err == nil {
		t.Fatal("recovery automatically re-sent uncertain attempt")
	}
	if after := bridgeBusinessSnapshot(t, reopened); after != business {
		t.Fatalf("recovery changed business SQL %s -> %s", business, after)
	}
	if err = reopened.RecoverBridgeDelivery(context.Background()); err != nil {
		t.Fatalf("recovery idempotence: %v", err)
	}
}

func TestBridgeOfferedTimeoutOnlyBecomesUncertainThenOwnerCanReceive(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	claim := f.claimReq()
	batch, err := f.db.ClaimBridge(f.hook, claim)
	if err != nil {
		t.Fatal(err)
	}
	var wire string
	if err = f.db.sql.QueryRow("SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	emit := contract.BridgeEmitRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: "emit"}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claim.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wire}
	if err = f.db.BeginBridgeEmit(f.sender, emit); err != nil {
		t.Fatal(err)
	}
	offered := contract.BridgeOfferedRequest{BridgeRequestBase: emit.BridgeRequestBase, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claim.ClaimToken, WireSHA256: wire}
	if err = f.db.MarkBridgeOffered(f.sender, offered); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec("UPDATE bridge_delivery_attempts SET deadline_at='2000-01-01T00:00:00Z' WHERE attempt_id=?", batch.AttemptID); err != nil {
		t.Fatal(err)
	}
	statusReq := contract.BridgeStatusRequest{BridgeOwnerRequest: f.receiveReq(batch).BridgeOwnerRequest}
	status, err := f.db.BridgeStatus(f.owner, statusReq)
	if err != nil || len(status.Batches) != 1 || status.Batches[0].State != "UNCERTAIN" {
		t.Fatalf("offered timeout status %+v %v", status, err)
	}
	var count int
	if err = f.db.sql.QueryRow("SELECT COUNT(*) FROM bridge_delivery_attempts").Scan(&count); err != nil || count != 1 {
		t.Fatalf("timeout auto-created attempt %d %v", count, err)
	}
	if _, err = f.db.ClaimBridge(f.hook, f.claimReq()); err == nil {
		t.Fatal("timed-out OFFERED auto-reclaimed")
	}
	if _, err = f.db.ReceiveBridge(f.owner, f.receiveReq(batch)); err != nil {
		t.Fatalf("owner attestation after timeout: %v", err)
	}
}

func TestBridgeBeginEmitCASRejectsStateChangedAfterSnapshot(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	batch, err := f.db.ClaimBridge(f.hook, f.claimReq())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.sql.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	a, err := bridgeAttemptTx(context.Background(), tx, f.binding, batch.BatchID, batch.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE bridge_delivery_attempts SET state='STALE' WHERE attempt_id=?", batch.AttemptID); err != nil {
		t.Fatal(err)
	}
	sender := BridgeSenderProof{BindingID: f.binding, PeerPID: os.Getpid(), PeerBirth: f.peerBirth}
	if err = bridgeBeginEmitCAS(context.Background(), tx, bridgeTime(time.Now()), a, sender); err == nil {
		t.Fatal("stale claim acquired output permit after snapshot")
	}
	var state string
	if err = tx.QueryRow("SELECT state FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&state); err != nil || state != "STALE" {
		t.Fatalf("failed CAS changed state=%s %v", state, err)
	}
}

func TestBridgeStatusPaginatesEightAuthenticatedBatches(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	var first contract.BridgeBatch
	for i := 1; i <= 9; i++ {
		if i > 1 {
			testBridgeAddEvent(t, f, "task", fmt.Sprintf("page-event-%d", i), i, fmt.Sprintf("page-slot-%d", i))
		}
		claim := f.claimReq()
		claim.RequestID = fmt.Sprintf("claim-%d", i)
		batch, err := f.db.ClaimBridge(f.hook, claim)
		if err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		if i == 1 {
			first = batch
		}
		if len(batch.Events) != 1 {
			t.Fatalf("received revision filtered incorrectly at %d: %+v", i, batch.Events)
		}
		var wire string
		if err = f.db.sql.QueryRow("SELECT wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", batch.AttemptID).Scan(&wire); err != nil {
			t.Fatal(err)
		}
		emit := contract.BridgeEmitRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: f.binding, OwnerEpoch: 1, RequestID: fmt.Sprintf("emit-%d", i)}, BatchID: batch.BatchID, AttemptID: batch.AttemptID, ClaimToken: claim.ClaimToken, RenderSHA256: batch.RenderSHA256, WireSHA256: wire}
		if err = f.db.BeginBridgeEmit(f.sender, emit); err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.ReceiveBridge(f.owner, f.receiveReq(batch)); err != nil {
			t.Fatal(err)
		}
	}
	req := contract.BridgeStatusRequest{BridgeOwnerRequest: f.receiveReq(first).BridgeOwnerRequest}
	page, err := f.db.BridgeStatus(f.owner, req)
	if err != nil || len(page.Batches) != 8 || page.NextCursor == nil || page.Counts.PendingBusinessDecision != 9 {
		t.Fatalf("first page %+v %v", page, err)
	}
	req.Cursor = *page.NextCursor
	last, err := f.db.BridgeStatus(f.owner, req)
	if err != nil || len(last.Batches) != 1 || last.NextCursor != nil {
		t.Fatalf("second page %+v %v", last, err)
	}
	req.OwnerEpoch = 2
	if _, err := f.db.BridgeStatus(f.owner, req); err == nil {
		t.Fatal("old cursor crossed epoch")
	}
}

func TestBridgeBoundedSecretScan8192AndCanceledContext(t *testing.T) {
	f := newBridgeDeliveryFixture(t)
	tx, err := f.db.sql.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	public := strings.Repeat("界\n\"\\", 1024) + strings.Repeat("a", 2048)
	if len(public) != 8192 {
		t.Fatalf("public bytes=%d", len(public))
	}
	started := time.Now()
	for i := 0; i < 50; i++ {
		if err = bridgeStoredSecretSafe(context.Background(), tx, "run", f.binding, public); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("bounded 8192-byte scan took %s", elapsed)
	} else {
		t.Logf("50 bounded 8192-byte scans: %s", elapsed)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = bridgeStoredSecretSafe(canceled, tx, "run", f.binding, public); err == nil {
		t.Fatal("canceled scan used DB anyway")
	}
}

func TestBridgeOwnerAndControlHashOnlyCanaries(t *testing.T) {
	for _, kind := range []string{"owner", "control"} {
		t.Run(kind, func(t *testing.T) {
			f := newBridgeDeliveryFixture(t)
			prefix := kind + "-token"
			raw, err := newID(prefix)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "owner" {
				if _, err = f.db.sql.Exec("UPDATE owner_grants SET token_hash=? WHERE id=(SELECT owner_context_id FROM bridge_bindings WHERE binding_id=?)", runtimeHash(raw), f.binding); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = f.db.sql.Exec("UPDATE run_control SET token_hash=? WHERE run_id='run'", runtimeHash(raw)); err != nil {
					t.Fatal(err)
				}
			}
			var body string
			if err = f.db.sql.QueryRow("SELECT body_json FROM runtime_events WHERE event_id='result-1'").Scan(&body); err != nil {
				t.Fatal(err)
			}
			var event contract.Event
			if err = json.Unmarshal([]byte(body), &event); err != nil {
				t.Fatal(err)
			}
			event.EventID = "around-" + raw + "-around"
			if _, err = f.db.sql.Exec("UPDATE runtime_events SET event_id=?,body_json=? WHERE event_id='result-1'", event.EventID, string(mustJSON(t, event))); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.sql.Exec("UPDATE delivery_order SET event_id=? WHERE event_id='result-1'", event.EventID); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.ClaimBridge(f.hook, f.claimReq()); err == nil || !strings.Contains(err.Error(), "bridge_public_secret") {
				t.Fatalf("%s hash-only canary missed: %v", kind, err)
			}
		})
	}
}

func TestBridgeAdoptionQuotaFailureKeepsReservationUnassigned(t *testing.T) {
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
	var used int64
	if err = f.db.sql.QueryRow("SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'").Scan(&used); err != nil {
		t.Fatal(err)
	}
	f.db.runControlLimit = used + 1
	if _, err = f.db.ClaimBridge(f.hook, f.claimReq()); err == nil || !strings.Contains(err.Error(), "control_spool_full") {
		t.Fatalf("adoption escaped quota: %v", err)
	}
	var sender int
	var hash, wire string
	if err = f.db.sql.QueryRow("SELECT sender_pid,claim_token_hash,wire_sha256 FROM bridge_delivery_attempts WHERE attempt_id=?", reserve.AttemptID).Scan(&sender, &hash, &wire); err != nil || sender != 0 || hash != "" || wire != "" {
		t.Fatalf("quota failure half-adopted sender=%d hash=%q wire=%q err=%v", sender, hash, wire, err)
	}
}
