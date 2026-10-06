package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/bridgecodec"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// BridgeSenderProof is populated by B04 from the actual Unix peer and OS. It
// is never decoded from JSON and cannot be substituted by req.Peer.
type BridgeSenderProof struct {
	BindingID string
	PeerPID   int
	PeerBirth string
}
type bridgeSenderKey struct{}

func WithBridgeSenderProof(ctx context.Context, p BridgeSenderProof) context.Context {
	return context.WithValue(ctx, bridgeSenderKey{}, p)
}

// BridgePublicSecrets narrows output; it grants no permission. B04 attaches
// only raw values already held by the actual authenticated request.
type bridgePublicSecretsKey struct{}

func WithBridgePublicSecrets(ctx context.Context, secrets ...string) context.Context {
	return context.WithValue(ctx, bridgePublicSecretsKey{}, append([]string(nil), secrets...))
}
func bridgeKnownSecrets(ctx context.Context, extra ...string) []string {
	s, _ := ctx.Value(bridgePublicSecretsKey{}).([]string)
	out := append([]string(nil), s...)
	if p, ok := ctx.Value(bridgeHookKey{}).(BridgeHookProof); ok {
		out = append(out, p.HookToken)
	}
	if p, ok := ctx.Value(bridgeOwnerKey{}).(BridgeOwnerProof); ok {
		out = append(out, p.ControlToken, p.OwnerGrantToken)
	}
	return append(out, extra...)
}
func bridgePublicSafe(ctx context.Context, value any, extra ...string) error {
	secrets := bridgeKnownSecrets(ctx, extra...)
	var check func(reflect.Value) bool
	check = func(v reflect.Value) bool {
		if !v.IsValid() {
			return true
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if v.IsNil() {
				return true
			}
			return check(v.Elem())
		case reflect.String:
			for _, secret := range secrets {
				if secret != "" && strings.Contains(v.String(), secret) {
					return false
				}
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).IsExported() && !check(v.Field(i)) {
					return false
				}
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				if !check(v.Index(i)) {
					return false
				}
			}
		case reflect.Map:
			for _, key := range v.MapKeys() {
				if !check(key) || !check(v.MapIndex(key)) {
					return false
				}
			}
		}
		return true
	}
	if !check(reflect.ValueOf(value)) {
		return CodeError("bridge_public_secret")
	}
	return nil
}

// Current built-in capability generators use fixed ASCII prefixes and hex
// widths. Compare only those bounded candidate windows against stored hashes;
// never retrieve or persist raw owner/control/report capabilities.
func bridgeStoredSecretSafe(ctx context.Context, tx *sql.Tx, run, binding string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	hashes := map[string]bool{}
	rows, err := tx.QueryContext(ctx, "SELECT token_hash FROM run_control WHERE run_id=? UNION SELECT token_hash FROM owner_grants WHERE id=(SELECT owner_context_id FROM bridge_bindings WHERE binding_id=?) UNION SELECT token_hash FROM report_capabilities WHERE run_id=? UNION SELECT hook_token_hash FROM bridge_bindings WHERE binding_id=? UNION SELECT claim_token_hash FROM bridge_delivery_attempts WHERE binding_id=?", run, binding, run, binding, binding)
	if err != nil {
		return err
	}
	for rows.Next() {
		var h string
		if err = rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		if validDigest(h) {
			hashes[h] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, pattern := range []struct {
		prefix string
		hex    int
	}{{"owner-token-", 24}, {"control-token-", 24}, {"report-token-", 64}, {"hook-", 24}, {"bridge-claim-", 64}} {
		width := len(pattern.prefix) + pattern.hex
		for i := 0; i+width <= len(raw); i++ {
			if string(raw[i:i+len(pattern.prefix)]) != pattern.prefix {
				continue
			}
			candidate := raw[i : i+width]
			valid := true
			for _, c := range candidate[len(pattern.prefix):] {
				if c < '0' || c > '9' && c < 'a' || c > 'f' {
					valid = false
					break
				}
			}
			if valid && hashes[runtimeHash(string(candidate))] {
				return CodeError("bridge_public_secret")
			}
		}
	}
	return nil
}

// NewBridgeClaimToken is the single coordinator factory for a private reply.
// Its 256 random bits never enter a public Batch, status or wire envelope.
func NewBridgeClaimToken() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "bridge-claim-" + hex.EncodeToString(random[:]), nil
}
func bridgeClaimTokenValid(token string) bool {
	if len(token) != len("bridge-claim-")+64 || !strings.HasPrefix(token, "bridge-claim-") {
		return false
	}
	for _, c := range token[len("bridge-claim-"):] {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

const bridgeClaimLease = 10 * time.Second
const bridgeOfferedWait = 5 * time.Minute
const bridgeTimestamp = "2006-01-02T15:04:05.000000000Z"
const bridgeAttemptIDWidth = len("attempt-") + 24

func bridgeTime(t time.Time) string { return t.UTC().Format(bridgeTimestamp) }
func bridgeSHA(raw []byte) string   { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func bridgeBytes(parts ...string) int64 {
	var n int64
	for _, s := range parts {
		n += int64(len(s))
	}
	return n
}

type bridgeSource struct {
	ordinal int64
	event   contract.Event
}
type bridgeAttempt struct {
	id, batchID, bindingID, tokenHash, state, deadline, senderBirth, wireHash, receiptID, receivedAt, updatedAt string
	ownerEpoch                                                                                                  uint64
	attemptNo, senderPID                                                                                        int
	wire                                                                                                        []byte
}

func bridgeAttemptBytes(a bridgeAttempt, created, updated string) int64 {
	return 24 + int64(len(a.wire)) + bridgeBytes(a.id, a.batchID, a.bindingID, a.tokenHash, a.state, a.deadline, a.senderBirth, a.wireHash, a.receiptID, a.receivedAt, created, updated)
}
func bridgeBatchBytes(b contract.BridgeBatch, content, events, receipts []byte, created string) int64 {
	return 8 + int64(len(content)+len(events)+len(receipts)) + bridgeBytes(b.BatchID, b.BindingID, b.RenderSHA256, created)
}
func bridgeItemBytes(batchID string, e contract.BridgeBatchEvent) int64 {
	return 24 + bridgeBytes(batchID, e.EventID, e.EventHash, e.ActionSlot, e.TaskID)
}
func bridgeCurrentOwnerTx(ctx context.Context, tx *sql.Tx, id string, epoch uint64, mode string) (bridgeBinding, error) {
	var empty bridgeBinding
	b, err := scanBridgeBinding(tx.QueryRowContext(ctx, bridgeSelect+" WHERE binding_id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return empty, CodeError("bridge_no_active_binding")
	}
	if err != nil {
		return empty, err
	}
	p, err := bridgeOwnerTx(ctx, tx, b.view.RunID)
	if err != nil {
		return empty, err
	}
	if mode != p.OwnerMode || b.view.OwnerContextID != p.OwnerContextID || b.view.OwnerPID != p.OwnerPID || b.view.OwnerBirth != p.OwnerBirth {
		return empty, CodeError("owner_mismatch")
	}
	if epoch != b.view.OwnerEpoch {
		return empty, CodeError("invalid_epoch")
	}
	if b.view.Status != "ACTIVE" {
		return empty, CodeError("bridge_no_active_binding")
	}
	if err = bridgeRunCollectTx(ctx, tx, b.view.RunID); err != nil {
		return empty, err
	}
	return b, nil
}

func bridgeRunCollectTx(ctx context.Context, tx *sql.Tx, run string) error {
	var mode, created, cutover string
	if err := tx.QueryRowContext(ctx, "SELECT r.delivery_mode,r.created_at,m.bridge_cutover_at FROM runs r CROSS JOIN schema_meta m WHERE r.id=?", run).Scan(&mode, &created, &cutover); err != nil {
		return err
	}
	createdAt, first := time.Parse(time.RFC3339Nano, created)
	cutoverAt, second := time.Parse(time.RFC3339Nano, cutover)
	if mode != "collect" || first != nil || second != nil || !createdAt.After(cutoverAt) {
		return CodeError("bridge_mode_unsupported")
	}
	return nil
}
func bridgeEmitSenderTx(ctx context.Context, tx *sql.Tx, b bridgeBinding) (BridgeSenderProof, error) {
	p, ok := ctx.Value(bridgeSenderKey{}).(BridgeSenderProof)
	if !ok || p.BindingID != b.view.BindingID || p.PeerPID <= 0 || p.PeerBirth == "" {
		return p, CodeError("bridge_sender_unknown")
	}
	ancestry, err := bridgePeerAncestry(ctx, tx, p.PeerPID, p.PeerBirth)
	if err != nil {
		return p, err
	}
	if ancestry[b.view.NativePID] != b.view.NativeBirth {
		return p, CodeError("bridge_sender_unknown")
	}
	if err = bridgeNativeExecutable(ctx, b.view.NativePID, b.view.NativeExecutableSHA256); err != nil {
		return p, err
	}
	var origin, birth string
	var pid int
	err = tx.QueryRowContext(ctx, "SELECT origin_context_id,origin_pid,origin_birth FROM runs WHERE id=?", b.view.RunID).Scan(&origin, &pid, &birth)
	if err != nil {
		return p, err
	}
	if origin != b.view.OwnerContextID || pid != b.view.OwnerPID || birth != b.view.OwnerBirth {
		return p, CodeError("owner_mismatch")
	}
	return p, nil
}

const bridgeAttemptSelect = "SELECT attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,wire_bytes,wire_sha256,receipt_id,received_at,updated_at FROM bridge_delivery_attempts"

func bridgeScanAttempt(row *sql.Row) (bridgeAttempt, error) {
	var a bridgeAttempt
	err := row.Scan(&a.id, &a.batchID, &a.bindingID, &a.ownerEpoch, &a.attemptNo, &a.tokenHash, &a.state, &a.deadline, &a.senderPID, &a.senderBirth, &a.wire, &a.wireHash, &a.receiptID, &a.receivedAt, &a.updatedAt)
	return a, err
}
func bridgeAttemptTx(ctx context.Context, tx *sql.Tx, binding, batch, attempt string) (bridgeAttempt, error) {
	a, err := bridgeScanAttempt(tx.QueryRowContext(ctx, bridgeAttemptSelect+" WHERE binding_id=? AND batch_id=? AND attempt_id=?", binding, batch, attempt))
	if errors.Is(err, sql.ErrNoRows) {
		return a, CodeError("bridge_attempt_mismatch")
	}
	return a, err
}
func bridgeUnresolvedTx(ctx context.Context, tx *sql.Tx, binding string) (bridgeAttempt, error) {
	return bridgeScanAttempt(tx.QueryRowContext(ctx, bridgeAttemptSelect+" WHERE binding_id=? AND state IN ('CLAIMED','EMITTING','OFFERED','UNCERTAIN')", binding))
}

// The original delivery_order is the sole ordering authority. The source
// body is checked against the original event row and current work revision.
func bridgeSourceRows(ctx context.Context, tx *sql.Tx, run, binding string) ([]bridgeSource, error) {
	const q = "SELECT o.ordinal,e.event_id,e.task_id,e.payload_hash,e.body_json,e.event_revision,e.action_slot,r.work_revision FROM delivery_order o JOIN (SELECT event_id,task_id,payload_hash,body_json,event_revision,action_slot,delivery_status FROM runtime_events UNION ALL SELECT e.event_id,c.task_id,e.payload_hash,e.body_json,e.event_revision,e.action_slot,e.delivery_status FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id) e ON e.event_id=o.event_id JOIN tasks t ON t.id=e.task_id JOIN task_runtime r ON r.task_id=t.id WHERE t.run_id=? AND e.delivery_status='pending' AND NOT EXISTS(SELECT 1 FROM bridge_batch_items i JOIN bridge_delivery_attempts a ON a.batch_id=i.batch_id WHERE a.binding_id=? AND a.state='RECEIVED' AND i.event_id=e.event_id AND i.event_revision=e.event_revision AND i.event_hash=e.payload_hash) ORDER BY o.ordinal LIMIT 8"
	rows, err := tx.QueryContext(ctx, q, run, binding)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]bridgeSource, 0, 8)
	for rows.Next() {
		var s bridgeSource
		var id, task, hash, body, slot string
		var rev int64
		var work int
		if err = rows.Scan(&s.ordinal, &id, &task, &hash, &body, &rev, &slot, &work); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(body), &s.event) != nil {
			return nil, CodeError("bridge_source_changed")
		}
		e := s.event
		if e.EventID != id || e.TaskID != task || e.RunID != run || e.PayloadHash != hash || !validDigest(hash) || rev <= 0 || e.WorkRevision != work || work <= 0 {
			return nil, CodeError("bridge_source_changed")
		}
		s.event.EventRevision = rev
		s.event.ActionSlot = slot
		out = append(out, s)
	}
	return out, rows.Err()
}
func bridgeSummary(kind string) (string, error) {
	switch kind {
	case contract.EventPrepared:
		return "Agent Bird prepared a task.", nil
	case contract.EventSpawned:
		return "Agent Bird spawned a task.", nil
	case contract.EventRunning:
		return "Agent Bird reports a task is running.", nil
	case contract.EventQuestion:
		return "Agent Bird recorded a question.", nil
	case contract.EventResult:
		return "Agent Bird recorded a result.", nil
	case contract.EventExited:
		return "Agent Bird recorded an exit.", nil
	case contract.EventStopped:
		return "Agent Bird recorded a stop.", nil
	case contract.EventFailed:
		return "Agent Bird recorded a failure.", nil
	case contract.EventLaunchRejected:
		return "Agent Bird rejected a task before starting its worker.", nil
	case contract.EventUnknown:
		return "Agent Bird recorded an unknown state.", nil
	case contract.EventSession:
		return "Agent Bird recorded a session.", nil
	case contract.EventProgress:
		return "Agent Bird recorded progress.", nil
	default:
		return "", CodeError("bridge_source_changed")
	}
}
func bridgePublicEvent(e contract.Event) (contract.BridgeBatchEvent, error) {
	summary, err := bridgeSummary(e.Kind)
	if err != nil {
		return contract.BridgeBatchEvent{}, err
	}
	out := contract.BridgeBatchEvent{EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, TaskID: e.TaskID, WorkRevision: e.WorkRevision, Kind: e.Kind, Summary: summary}
	if e.Artifact != nil {
		if e.Artifact.ID == "" || !validDigest(e.Artifact.SHA256) {
			return out, CodeError("bridge_source_changed")
		}
		out.ArtifactRef = &contract.BridgeArtifactRef{ID: e.Artifact.ID, SHA256: e.Artifact.SHA256}
	}
	return out, nil
}
func bridgeCandidate(b contract.BridgeBatch, events []contract.Event) (contract.BridgeBatch, []byte, []byte, []byte, error) {
	b.Events = make([]contract.BridgeBatchEvent, 0, len(events))
	groups := map[string][]contract.Event{}
	order := []string{}
	for _, e := range events {
		if e.Kind == contract.EventProgress && e.ActionSlot != "" {
			return b, nil, nil, nil, CodeError("bridge_source_changed")
		}
		item, err := bridgePublicEvent(e)
		if err != nil {
			return b, nil, nil, nil, err
		}
		b.Events = append(b.Events, item)
		if e.ActionSlot != "" && e.Kind != contract.EventProgress {
			if _, ok := groups[e.TaskID]; !ok {
				order = append(order, e.TaskID)
			}
			groups[e.TaskID] = append(groups[e.TaskID], e)
		}
	}
	b.DeliveryReceipts = make([]contract.BridgeDeliveryReceipt, 0, len(order))
	for _, task := range order {
		id, proof, _, err := collectionReceiptParts(task, groups[task])
		if err != nil {
			return b, nil, nil, nil, err
		}
		ids := make([]string, 0, len(groups[task]))
		for _, e := range groups[task] {
			ids = append(ids, e.EventID)
		}
		b.DeliveryReceipts = append(b.DeliveryReceipts, contract.BridgeDeliveryReceipt{TaskID: task, DeliveryID: id, ProofMode: "collect", ProofSHA256: proof, EventIDs: ids})
	}
	content, err := bridgecodec.ContentBytes(b)
	if err != nil {
		return b, nil, nil, nil, err
	}
	hash, err := bridgecodec.RenderHash(b)
	if err != nil {
		return b, nil, nil, nil, err
	}
	b.RenderSHA256 = hash
	eventJSON, _ := json.Marshal(b.Events)
	receiptJSON, _ := json.Marshal(b.DeliveryReceipts)
	return b, content, eventJSON, receiptJSON, nil
}
func bridgeWire(b contract.BridgeBatch, provider, hook string) ([]byte, string, error) {
	raw, err := bridgecodec.WireBytes(b, b.AttemptID, provider, hook)
	if err != nil {
		return nil, "", err
	}
	return raw, bridgeSHA(raw), nil
}
func bridgeFitsProvider(b contract.BridgeBatch, provider string) error {
	for _, hook := range contract.BridgeEmitHooks(provider) {
		if _, _, err := bridgeWire(b, provider, hook); err != nil {
			return err
		}
	}
	return nil
}
func bridgeLoadBatchTx(ctx context.Context, tx *sql.Tx, batchID, attemptID string) (contract.BridgeBatch, []byte, error) {
	var b contract.BridgeBatch
	var content, events, receipts []byte
	err := tx.QueryRowContext(ctx, "SELECT binding_id,owner_epoch,content_bytes,render_sha256,events_json,receipts_json FROM bridge_batches WHERE batch_id=?", batchID).Scan(&b.BindingID, &b.OwnerEpoch, &content, &b.RenderSHA256, &events, &receipts)
	if err != nil {
		return b, nil, err
	}
	b.SchemaVersion = 1
	b.BatchID = batchID
	b.AttemptID = attemptID
	b.TransportMode = "hook_pull"
	if json.Unmarshal(events, &b.Events) != nil || json.Unmarshal(receipts, &b.DeliveryReceipts) != nil {
		return b, nil, CodeError("bridge_source_changed")
	}
	actual, err := bridgecodec.ContentBytes(b)
	if err != nil || subtle.ConstantTimeCompare(actual, content) != 1 {
		return b, nil, CodeError("bridge_hash_mismatch")
	}
	hash, err := bridgecodec.RenderHash(b)
	if err != nil || hash != b.RenderSHA256 {
		return b, nil, CodeError("bridge_hash_mismatch")
	}
	return b, content, nil
}
func bridgeValidateItemsTx(ctx context.Context, tx *sql.Tx, b contract.BridgeBatch, run string) error {
	rows, err := tx.QueryContext(ctx, "SELECT position,event_id,event_revision,event_hash,action_slot,task_id,work_revision FROM bridge_batch_items WHERE batch_id=? ORDER BY position", b.BatchID)
	if err != nil {
		return err
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var pos, work int
		var id, hash, slot, task string
		var rev int64
		if err = rows.Scan(&pos, &id, &rev, &hash, &slot, &task, &work); err != nil {
			return err
		}
		if pos != i || i >= len(b.Events) {
			return CodeError("bridge_source_changed")
		}
		e := b.Events[i]
		if e.EventID != id || e.EventRevision != rev || e.EventHash != hash || e.ActionSlot != slot || e.TaskID != task || e.WorkRevision != work {
			return CodeError("bridge_source_changed")
		}
		i++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if i != len(b.Events) {
		return CodeError("bridge_source_changed")
	}
	if err = rows.Close(); err != nil {
		return err
	}
	source, err := bridgeSourceRows(ctx, tx, run, b.BindingID)
	if err != nil {
		return err
	}
	if len(source) < len(b.Events) {
		return CodeError("bridge_source_changed")
	}
	for i, e := range b.Events {
		original := source[i].event
		if original.EventID != e.EventID || original.EventRevision != e.EventRevision || original.PayloadHash != e.EventHash || original.ActionSlot != e.ActionSlot || original.TaskID != e.TaskID || original.WorkRevision != e.WorkRevision {
			return CodeError("bridge_source_changed")
		}
	}
	return nil
}

// ClaimBridge is the only path that creates a usable sending claim. It either
// selects a new exact prefix or reclaims the same immutable pre-emit batch.
func (d *DB) ClaimBridge(ctx context.Context, req contract.BridgeClaimRequest) (contract.BridgeBatch, error) {
	var empty contract.BridgeBatch
	if req.SchemaVersion != 1 || req.BindingID == "" || req.OwnerEpoch == 0 || req.RequestID == "" || req.HookCapability == "" || !bridgeClaimTokenValid(req.ClaimToken) || (req.NativeHook != "SessionStart" && req.NativeHook != "UserPromptSubmit" && req.NativeHook != "PreInvocation") {
		return empty, CodeError("bridge_invalid_input")
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return empty, err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	b, err := bridgeHookTx(ctx, tx, req.BindingID, req.HookCapability, req.OwnerEpoch)
	if err != nil {
		return empty, err
	}
	if b.view.Status != "ACTIVE" || b.view.TransportMode != "hook_pull" || !contract.BridgeEmitSupported(b.view.ProviderID, req.NativeHook) {
		return empty, CodeError("bridge_no_active_binding")
	}
	if err = bridgeRunCollectTx(ctx, tx, b.view.RunID); err != nil {
		return empty, err
	}
	sender, ok := ctx.Value(bridgeHookKey{}).(BridgeHookProof)
	if !ok || sender.PeerPID <= 0 || sender.PeerBirth == "" {
		return empty, CodeError("bridge_sender_unknown")
	}
	now := time.Now().UTC()
	nowText := bridgeTime(now)
	previous, err := bridgeUnresolvedTx(ctx, tx, b.view.BindingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	unresolved := err == nil
	var batch contract.BridgeBatch
	var content, eventJSON, receiptJSON []byte
	var selected []contract.Event
	attemptNo := 1
	reuse := false
	if unresolved {
		if previous.ownerEpoch != b.view.OwnerEpoch {
			return empty, CodeError("invalid_epoch")
		}
		switch previous.state {
		case "EMITTING":
			if bridgeSenderExited(ctx, previous.senderPID, previous.senderBirth) {
				growth := int64(len("UNCERTAIN") + len(nowText) - len(previous.state) - len(previous.updatedAt))
				if growth > 0 {
					if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
						return empty, err
					}
				}
				result, e := tx.ExecContext(ctx, "UPDATE bridge_delivery_attempts SET state='UNCERTAIN',updated_at=? WHERE attempt_id=? AND state='EMITTING'", nowText, previous.id)
				if e != nil {
					return empty, e
				}
				n, _ := result.RowsAffected()
				if n != 1 {
					return empty, CodeError("bridge_claim_pending")
				}
				if err = tx.Commit(); err != nil {
					return empty, err
				}
			}
			return empty, CodeError("bridge_delivery_uncertain")
		case "OFFERED":
			deadline, e := time.Parse(time.RFC3339Nano, previous.deadline)
			if e != nil {
				return empty, CodeError("bridge_source_changed")
			}
			if !now.Before(deadline) {
				growth := int64(len("UNCERTAIN") + len(nowText) - len(previous.state) - len(previous.updatedAt))
				if growth > 0 {
					if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
						return empty, err
					}
				}
				result, e := tx.ExecContext(ctx, "UPDATE bridge_delivery_attempts SET state='UNCERTAIN',updated_at=? WHERE attempt_id=? AND state='OFFERED'", nowText, previous.id)
				if e != nil {
					return empty, e
				}
				n, _ := result.RowsAffected()
				if n != 1 {
					return empty, CodeError("bridge_claim_pending")
				}
				if err = tx.Commit(); err != nil {
					return empty, err
				}
			}
			return empty, CodeError("bridge_delivery_uncertain")
		case "UNCERTAIN":
			return empty, CodeError("bridge_delivery_uncertain")
		case "CLAIMED":
			batch, content, err = bridgeLoadBatchTx(ctx, tx, previous.batchID, previous.id)
			if err != nil {
				return empty, err
			}
			if err = bridgeValidateItemsTx(ctx, tx, batch, b.view.RunID); err != nil {
				return empty, err
			}
			deadline, e := time.Parse(time.RFC3339Nano, previous.deadline)
			if e != nil {
				return empty, CodeError("bridge_source_changed")
			}
			if now.Before(deadline) && previous.senderPID != 0 {
				return empty, CodeError("bridge_claim_pending")
			}
			if now.Before(deadline) && previous.senderPID == 0 {
				wire, wireHash, e := bridgeWire(batch, b.view.ProviderID, req.NativeHook)
				if e != nil {
					return empty, CodeError(e.Error())
				}
				if err = bridgePublicSafe(ctx, batch, req.ClaimToken); err != nil {
					return empty, err
				}
				if err = bridgeStoredSecretSafe(ctx, tx, b.view.RunID, b.view.BindingID, batch); err != nil {
					return empty, err
				}
				claimHash := runtimeHash(req.ClaimToken)
				growth := int64(len(sender.PeerBirth) + len(claimHash) + len(wire) + len(wireHash) + len(bridgeTime(now.Add(bridgeClaimLease))) + len(nowText) - len(previous.senderBirth) - len(previous.tokenHash) - len(previous.wire) - len(previous.wireHash) - len(previous.deadline) - len(previous.updatedAt))
				if growth > 0 {
					if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
						return empty, err
					}
				}
				adoptedAt := time.Now().UTC()
				if !adoptedAt.Before(deadline) {
					return empty, CodeError("bridge_claim_expired")
				}
				adoptedText := bridgeTime(adoptedAt)
				result, e := tx.ExecContext(ctx, "UPDATE bridge_delivery_attempts SET sender_pid=?,sender_birth=?,claim_token_hash=?,wire_bytes=?,wire_sha256=?,deadline_at=?,updated_at=? WHERE attempt_id=? AND state='CLAIMED' AND sender_pid=0 AND claim_token_hash='' AND wire_bytes IS NULL AND deadline_at>?", sender.PeerPID, sender.PeerBirth, claimHash, wire, wireHash, bridgeTime(adoptedAt.Add(bridgeClaimLease)), adoptedText, previous.id, adoptedText)
				if e != nil {
					return empty, e
				}
				n, _ := result.RowsAffected()
				if n != 1 {
					return empty, CodeError("bridge_claim_pending")
				}
				if err = tx.Commit(); err != nil {
					return empty, err
				}
				return batch, nil
			}
			// A Hook may safely reclaim only the lease that never reached EMITTING.
			growth := int64(len("EXPIRED") + len(nowText) - len(previous.state) - len(previous.tokenHash) - len(previous.updatedAt))
			if growth > 0 {
				if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
					return empty, err
				}
			}
			result, e := tx.ExecContext(ctx, "UPDATE bridge_delivery_attempts SET state='EXPIRED',claim_token_hash='',updated_at=? WHERE attempt_id=? AND state='CLAIMED'", nowText, previous.id)
			if e != nil {
				return empty, e
			}
			n, _ := result.RowsAffected()
			if n != 1 {
				return empty, CodeError("bridge_claim_pending")
			}
			attemptNo = previous.attemptNo + 1
			reuse = true
		default:
			return empty, CodeError("bridge_attempt_mismatch")
		}
	} else {
		// A previous expired pre-emit attempt retains priority over any new page.
		old, e := bridgeScanAttempt(tx.QueryRowContext(ctx, bridgeAttemptSelect+" a WHERE binding_id=? AND state='EXPIRED' AND NOT EXISTS(SELECT 1 FROM bridge_delivery_attempts z WHERE z.batch_id=a.batch_id AND z.attempt_no>a.attempt_no) ORDER BY created_at DESC,attempt_no DESC LIMIT 1", b.view.BindingID))
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return empty, e
		}
		if e == nil {
			batch, content, err = bridgeLoadBatchTx(ctx, tx, old.batchID, "")
			if err != nil {
				return empty, err
			}
			if err = bridgeValidateItemsTx(ctx, tx, batch, b.view.RunID); err != nil {
				return empty, err
			}
			attemptNo = old.attemptNo + 1
			reuse = true
		}
	}
	if !reuse {
		sources, e := bridgeSourceRows(ctx, tx, b.view.RunID, b.view.BindingID)
		if e != nil {
			return empty, e
		}
		if len(sources) == 0 {
			return empty, CodeError("bridge_empty_batch")
		}
		batchID, e := newID("batch")
		if e != nil {
			return empty, e
		}
		batch = contract.BridgeBatch{SchemaVersion: 1, BatchID: batchID, BindingID: b.view.BindingID, OwnerEpoch: b.view.OwnerEpoch, TransportMode: "hook_pull"}
		selected = make([]contract.Event, 0, 8)
		for _, src := range sources {
			if len(selected) == 8 {
				break
			}
			candidateEvents := append(append([]contract.Event(nil), selected...), src.event)
			candidate, _, _, _, e := bridgeCandidate(batch, candidateEvents)
			if e != nil {
				return empty, e
			}
			candidate.AttemptID = "attempt-" + strings.Repeat("f", 24)
			if e = bridgeFitsProvider(candidate, b.view.ProviderID); e != nil {
				if len(selected) == 0 {
					return empty, CodeError(e.Error())
				}
				break
			}
			selected = candidateEvents
		}
		batch, content, eventJSON, receiptJSON, err = bridgeCandidate(batch, selected)
		if err != nil {
			return empty, err
		}
	}
	attemptID, err := newID("attempt")
	if err != nil {
		return empty, err
	}
	if len(attemptID) != bridgeAttemptIDWidth {
		return empty, CodeError("bridge_invalid_input")
	}
	batch.AttemptID = attemptID
	wire, wireHash, err := bridgeWire(batch, b.view.ProviderID, req.NativeHook)
	if err != nil {
		return empty, CodeError(err.Error())
	}
	if err = bridgePublicSafe(ctx, batch, req.ClaimToken); err != nil {
		return empty, err
	}
	if err = bridgeStoredSecretSafe(ctx, tx, b.view.RunID, b.view.BindingID, batch); err != nil {
		return empty, err
	}
	a := bridgeAttempt{id: attemptID, batchID: batch.BatchID, bindingID: b.view.BindingID, ownerEpoch: b.view.OwnerEpoch, attemptNo: attemptNo, tokenHash: runtimeHash(req.ClaimToken), state: "CLAIMED", deadline: bridgeTime(now.Add(bridgeClaimLease)), senderPID: sender.PeerPID, senderBirth: sender.PeerBirth, wire: wire, wireHash: wireHash}
	growth := bridgeAttemptBytes(a, nowText, "")
	if !reuse {
		growth += bridgeBatchBytes(batch, content, eventJSON, receiptJSON, nowText)
		for _, item := range batch.Events {
			growth += bridgeItemBytes(batch.BatchID, item)
		}
		for _, r := range batch.DeliveryReceipts {
			group := bridgeReceiptGroup(selected, r.EventIDs)
			_, _, raw, e := collectionReceiptParts(r.TaskID, group)
			if e != nil {
				return empty, e
			}
			growth += bridgeBytes(r.DeliveryID, r.TaskID, r.ProofSHA256, raw)
		}
	}
	if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
		return empty, err
	}
	if !reuse {
		if _, err = tx.ExecContext(ctx, "INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,events_json,receipts_json,created_at) VALUES(?,?,?,?,?,?,?,?)", batch.BatchID, batch.BindingID, batch.OwnerEpoch, content, batch.RenderSHA256, eventJSON, receiptJSON, nowText); err != nil {
			return empty, mapConflict(err)
		}
		for i, item := range batch.Events {
			if _, err = tx.ExecContext(ctx, "INSERT INTO bridge_batch_items(batch_id,position,event_id,event_revision,event_hash,action_slot,task_id,work_revision) VALUES(?,?,?,?,?,?,?,?)", batch.BatchID, i, item.EventID, item.EventRevision, item.EventHash, item.ActionSlot, item.TaskID, item.WorkRevision); err != nil {
				return empty, mapConflict(err)
			}
		}
		for _, r := range batch.DeliveryReceipts {
			id, proof, e := collectionReceipt(ctx, tx, r.TaskID, bridgeReceiptGroup(selected, r.EventIDs))
			if e != nil {
				return empty, e
			}
			if id != r.DeliveryID || proof != r.ProofSHA256 {
				return empty, CodeError("bridge_source_changed")
			}
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,wire_bytes,wire_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", a.id, a.batchID, a.bindingID, a.ownerEpoch, a.attemptNo, a.tokenHash, a.state, a.deadline, a.senderPID, a.senderBirth, a.wire, a.wireHash, nowText)
	if err != nil {
		return empty, mapConflict(err)
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return batch, nil
}
func bridgeReceiptGroup(selected []contract.Event, ids []string) []contract.Event {
	out := make([]contract.Event, 0, len(ids))
	for _, id := range ids {
		for _, e := range selected {
			if e.EventID == id {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// RecoverBridgeDelivery is called once by B04 after reopening the coordinator
// DB and before serving bridge RPCs. An output permit from the previous
// coordinator lifetime is uncertain, even if that sender still appears alive.
// This never creates a new attempt, ACK or business transition.
func (d *DB) RecoverBridgeDelivery(ctx context.Context) error {
	if err := d.bridgeLockContext(ctx); err != nil {
		return err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT a.attempt_id,b.run_id,a.updated_at,a.claim_token_hash FROM bridge_delivery_attempts a JOIN bridge_bindings b ON b.binding_id=a.binding_id WHERE a.state='EMITTING'")
	if err != nil {
		return err
	}
	type old struct{ id, run, updated, hash string }
	var previous []old
	now := bridgeTime(time.Now())
	for rows.Next() {
		var item old
		if err = rows.Scan(&item.id, &item.run, &item.updated, &item.hash); err != nil {
			rows.Close()
			return err
		}
		previous = append(previous, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range previous {
		delta := int64(len("UNCERTAIN") + len(now) - len("EMITTING") - len(item.updated) - len(item.hash))
		if delta > 0 {
			if err = d.storageAdmission(ctx, tx, item.run, delta, false); err != nil {
				return err
			}
		}
		result, e := tx.ExecContext(ctx, "UPDATE bridge_delivery_attempts SET state='UNCERTAIN',claim_token_hash='',updated_at=? WHERE attempt_id=? AND state='EMITTING'", now, item.id)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return CodeError("bridge_emit_state")
		}
	}
	return tx.Commit()
}

func bridgeSendBindingTx(ctx context.Context, tx *sql.Tx, id string, epoch uint64) (bridgeBinding, BridgeSenderProof, error) {
	var empty bridgeBinding
	b, err := scanBridgeBinding(tx.QueryRowContext(ctx, bridgeSelect+" WHERE binding_id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return empty, BridgeSenderProof{}, CodeError("bridge_no_active_binding")
	}
	if err != nil {
		return empty, BridgeSenderProof{}, err
	}
	if b.view.OwnerEpoch != epoch {
		return empty, BridgeSenderProof{}, CodeError("invalid_epoch")
	}
	if b.view.Status != "ACTIVE" || b.view.TransportMode != "hook_pull" {
		return empty, BridgeSenderProof{}, CodeError("bridge_no_active_binding")
	}
	if err = bridgeRunCollectTx(ctx, tx, b.view.RunID); err != nil {
		return empty, BridgeSenderProof{}, err
	}
	sender, err := bridgeEmitSenderTx(ctx, tx, b)
	return b, sender, err
}
func bridgeExactSend(a bridgeAttempt, b contract.BridgeBatch, sender BridgeSenderProof, token, wireHash string) error {
	if a.ownerEpoch != b.OwnerEpoch || a.batchID != b.BatchID || a.senderPID <= 0 || a.senderPID != sender.PeerPID || a.senderBirth != sender.PeerBirth || a.tokenHash == "" || !bridgeEqualHash(a.tokenHash, token) || a.wireHash == "" || a.wireHash != wireHash || len(a.wire) == 0 || bridgeSHA(a.wire) != a.wireHash {
		return CodeError("bridge_claim_mismatch")
	}
	return nil
}
func (d *DB) BeginBridgeEmit(ctx context.Context, req contract.BridgeEmitRequest) error {
	if req.SchemaVersion != 1 || req.BindingID == "" || req.OwnerEpoch == 0 || req.BatchID == "" || req.AttemptID == "" || req.ClaimToken == "" || !validDigest(req.RenderSHA256) || !validDigest(req.WireSHA256) {
		return CodeError("bridge_invalid_input")
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, sender, err := bridgeSendBindingTx(ctx, tx, req.BindingID, req.OwnerEpoch)
	if err != nil {
		return err
	}
	a, err := bridgeAttemptTx(ctx, tx, b.view.BindingID, req.BatchID, req.AttemptID)
	if err != nil {
		return err
	}
	if a.state == "SUPERSEDED_UNCERTAIN" {
		return CodeError("bridge_attempt_superseded")
	}
	batch, _, err := bridgeLoadBatchTx(ctx, tx, req.BatchID, req.AttemptID)
	if err != nil {
		return err
	}
	if batch.BindingID != b.view.BindingID || batch.RenderSHA256 != req.RenderSHA256 {
		return CodeError("bridge_hash_mismatch")
	}
	if err = bridgeExactSend(a, batch, sender, req.ClaimToken, req.WireSHA256); err != nil {
		return err
	}
	// Stored hash agreement alone cannot authorize a different provider wrapper.
	exact := false
	for _, hook := range contract.BridgeEmitHooks(b.view.ProviderID) {
		wire, hash, renderErr := bridgeWire(batch, b.view.ProviderID, hook)
		if renderErr == nil && hash == a.wireHash && bytes.Equal(wire, a.wire) {
			exact = true
			break
		}
	}
	if !exact {
		return CodeError("bridge_hash_mismatch")
	}
	if a.state != "CLAIMED" {
		return CodeError("bridge_emit_state")
	}
	deadline, err := time.Parse(time.RFC3339Nano, a.deadline)
	if err != nil || !time.Now().UTC().Before(deadline) {
		return CodeError("bridge_claim_expired")
	}
	now := bridgeTime(time.Now())
	growth := int64(len("EMITTING") + len(now) - len(a.state) - len(a.updatedAt))
	if growth > 0 {
		if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
			return err
		}
	}
	if err = bridgeBeginEmitCAS(ctx, tx, now, a, sender); err != nil {
		return err
	}
	return tx.Commit()
}
func bridgeBeginEmitCAS(ctx context.Context, tx *sql.Tx, now string, a bridgeAttempt, sender BridgeSenderProof) error {
	result, err := tx.ExecContext(ctx, "UPDATE bridge_delivery_attempts SET state='EMITTING',updated_at=? WHERE attempt_id=? AND state='CLAIMED' AND deadline_at>? AND claim_token_hash=? AND sender_pid=? AND sender_birth=? AND wire_sha256=?", now, a.id, now, a.tokenHash, sender.PeerPID, sender.PeerBirth, a.wireHash)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return CodeError("bridge_claim_expired")
	}
	return nil
}
func (d *DB) MarkBridgeOffered(ctx context.Context, req contract.BridgeOfferedRequest) error {
	if req.SchemaVersion != 1 || req.BindingID == "" || req.OwnerEpoch == 0 || req.BatchID == "" || req.AttemptID == "" || req.ClaimToken == "" || !validDigest(req.WireSHA256) {
		return CodeError("bridge_invalid_input")
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	b, sender, err := bridgeSendBindingTx(ctx, tx, req.BindingID, req.OwnerEpoch)
	if err != nil {
		return err
	}
	a, err := bridgeAttemptTx(ctx, tx, b.view.BindingID, req.BatchID, req.AttemptID)
	if err != nil {
		return err
	}
	if a.state == "SUPERSEDED_UNCERTAIN" {
		return CodeError("bridge_attempt_superseded")
	}
	batch, _, err := bridgeLoadBatchTx(ctx, tx, req.BatchID, req.AttemptID)
	if err != nil {
		return err
	}
	if batch.BindingID != b.view.BindingID {
		return CodeError("bridge_attempt_mismatch")
	}
	if err = bridgeExactSend(a, batch, sender, req.ClaimToken, req.WireSHA256); err != nil {
		return err
	}
	// Stored hash agreement alone cannot authorize a different provider wrapper.
	exact := false
	for _, hook := range contract.BridgeEmitHooks(b.view.ProviderID) {
		wire, hash, renderErr := bridgeWire(batch, b.view.ProviderID, hook)
		if renderErr == nil && hash == a.wireHash && bytes.Equal(wire, a.wire) {
			exact = true
			break
		}
	}
	if !exact {
		return CodeError("bridge_hash_mismatch")
	}
	if a.state == "RECEIVED" || a.state == "OFFERED" {
		return nil
	}
	if a.state != "EMITTING" {
		return CodeError("bridge_emit_state")
	}
	now := time.Now().UTC()
	nowText := bridgeTime(now)
	offeredDeadline := bridgeTime(now.Add(bridgeOfferedWait))
	growth := int64(len("OFFERED") + len(offeredDeadline) + len(nowText) - len(a.state) - len(a.deadline) - len(a.updatedAt))
	if growth > 0 {
		if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, "UPDATE bridge_delivery_attempts SET state='OFFERED',deadline_at=?,updated_at=? WHERE attempt_id=? AND state='EMITTING' AND claim_token_hash=? AND sender_pid=? AND sender_birth=? AND wire_sha256=?", offeredDeadline, nowText, a.id, a.tokenHash, sender.PeerPID, sender.PeerBirth, a.wireHash)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return CodeError("bridge_emit_state")
	}
	return tx.Commit()
}
func bridgeHistoricalReceiveTx(ctx context.Context, tx *sql.Tx, req contract.BridgeReceiveRequest) (bridgeBinding, error) {
	return bridgeOwnerForHistoricalTx(ctx, tx, req.BindingID, req.OwnerEpoch, req.OwnerMode, bridgeHistoryReceive)
}
func bridgeExactReceiveTx(ctx context.Context, tx *sql.Tx, b bridgeBinding, req contract.BridgeReceiveRequest) (bridgeAttempt, contract.BridgeBatch, error) {
	var batch contract.BridgeBatch
	a, err := bridgeAttemptTx(ctx, tx, b.view.BindingID, req.BatchID, req.AttemptID)
	if err != nil {
		return a, batch, err
	}
	if a.state == "SUPERSEDED_UNCERTAIN" {
		return a, batch, CodeError("bridge_attempt_superseded")
	}
	batch, _, err = bridgeLoadBatchTx(ctx, tx, req.BatchID, req.AttemptID)
	if err != nil {
		return a, batch, err
	}
	if batch.BindingID != b.view.BindingID || batch.OwnerEpoch != b.view.OwnerEpoch || batch.RenderSHA256 != req.RenderSHA256 || a.ownerEpoch != b.view.OwnerEpoch {
		return a, batch, CodeError("bridge_hash_mismatch")
	}
	return a, batch, nil
}
func (d *DB) ReceiveBridge(ctx context.Context, req contract.BridgeReceiveRequest) (contract.BridgeReceiveReceipt, error) {
	var empty contract.BridgeReceiveReceipt
	if req.SchemaVersion != 1 || req.BindingID == "" || req.OwnerEpoch == 0 || req.BatchID == "" || req.AttemptID == "" || !validDigest(req.RenderSHA256) {
		return empty, CodeError("bridge_invalid_input")
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return empty, err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	b, err := bridgeHistoricalReceiveTx(ctx, tx, req)
	if err != nil {
		return empty, err
	}
	if err = bridgeRunCollectTx(ctx, tx, b.view.RunID); err != nil {
		return empty, err
	}
	a, batch, err := bridgeExactReceiveTx(ctx, tx, b, req)
	if err != nil {
		return empty, err
	}
	if b.view.Status == "STALE" && !bridgeSenderExited(ctx, a.senderPID, a.senderBirth) {
		return empty, CodeError("bridge_sender_unknown")
	}
	if a.state == "RECEIVED" {
		receipt := contract.BridgeReceiveReceipt{ReceiptID: a.receiptID, BatchID: a.batchID, AttemptID: a.id, RenderSHA256: batch.RenderSHA256, ReceivedAt: a.receivedAt}
		if receipt.ReceiptID == "" || receipt.ReceivedAt == "" {
			return empty, CodeError("bridge_attempt_mismatch")
		}
		if err = bridgePublicSafe(ctx, receipt); err != nil {
			return empty, err
		}
		return receipt, nil
	}
	if a.state != "EMITTING" && a.state != "OFFERED" && a.state != "UNCERTAIN" {
		return empty, CodeError("bridge_receive_state")
	}
	id, err := newID("bridge-received")
	if err != nil {
		return empty, err
	}
	now := bridgeTime(time.Now())
	receipt := contract.BridgeReceiveReceipt{ReceiptID: id, BatchID: a.batchID, AttemptID: a.id, RenderSHA256: batch.RenderSHA256, ReceivedAt: now}
	if err = bridgePublicSafe(ctx, receipt); err != nil {
		return empty, err
	}
	growth := int64(len("RECEIVED") + len(id) + len(now) + len(now) - len(a.state) - len(a.receiptID) - len(a.receivedAt) - len(a.updatedAt))
	if growth > 0 {
		if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
			return empty, err
		}
	}
	result, err := tx.ExecContext(ctx, "UPDATE bridge_delivery_attempts SET state='RECEIVED',receipt_id=?,received_at=?,updated_at=? WHERE attempt_id=? AND state=? AND owner_epoch=?", id, now, now, a.id, a.state, a.ownerEpoch)
	if err != nil {
		return empty, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return empty, CodeError("bridge_receive_state")
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return receipt, nil
}
func (d *DB) QueryBridgeBatch(ctx context.Context, req contract.BridgeReceiveRequest) (contract.BridgeBatch, error) {
	var empty contract.BridgeBatch
	if req.SchemaVersion != 1 || req.BindingID == "" || req.OwnerEpoch == 0 || req.BatchID == "" || req.AttemptID == "" || !validDigest(req.RenderSHA256) {
		return empty, CodeError("bridge_invalid_input")
	}
	tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	b, err := bridgeOwnerForHistoricalTx(ctx, tx, req.BindingID, req.OwnerEpoch, req.OwnerMode, bridgeHistoryQuery)
	if err != nil {
		return empty, err
	}
	if err = bridgeRunCollectTx(ctx, tx, b.view.RunID); err != nil {
		return empty, err
	}
	_, batch, err := bridgeExactReceiveTx(ctx, tx, b, req)
	if err != nil {
		return empty, err
	}
	if err = bridgePublicSafe(ctx, batch); err != nil {
		return empty, err
	}
	if err = bridgeStoredSecretSafe(ctx, tx, b.view.RunID, b.view.BindingID, batch); err != nil {
		return empty, err
	}
	return batch, nil
}
func (d *DB) RedeliverBridge(ctx context.Context, req contract.BridgeRedeliverRequest) (contract.BridgeAttemptView, error) {
	var empty contract.BridgeAttemptView
	if req.SchemaVersion != 1 || req.BindingID == "" || req.OwnerEpoch == 0 || req.BatchID == "" || req.AttemptID == "" || req.Reason == "" {
		return empty, CodeError("bridge_invalid_input")
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return empty, err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	b, err := bridgeCurrentOwnerTx(ctx, tx, req.BindingID, req.OwnerEpoch, req.OwnerMode)
	if err != nil {
		return empty, err
	}
	a, err := bridgeAttemptTx(ctx, tx, b.view.BindingID, req.BatchID, req.AttemptID)
	if err != nil {
		return empty, err
	}
	if a.state == "SUPERSEDED_UNCERTAIN" {
		return empty, CodeError("bridge_attempt_superseded")
	}
	if a.state != "UNCERTAIN" || a.ownerEpoch != b.view.OwnerEpoch {
		return empty, CodeError("bridge_redeliver_state")
	}
	if a.senderPID <= 0 || a.senderBirth == "" || !bridgeSenderExited(ctx, a.senderPID, a.senderBirth) {
		return empty, CodeError("bridge_sender_unknown")
	}
	batch, _, err := bridgeLoadBatchTx(ctx, tx, a.batchID, a.id)
	if err != nil {
		return empty, err
	}
	if err = bridgeValidateItemsTx(ctx, tx, batch, b.view.RunID); err != nil {
		return empty, err
	}
	id, err := newID("attempt")
	if err != nil {
		return empty, err
	}
	now := time.Now()
	nowText := bridgeTime(now)
	next := bridgeAttempt{id: id, batchID: a.batchID, bindingID: a.bindingID, ownerEpoch: a.ownerEpoch, attemptNo: a.attemptNo + 1, state: "CLAIMED", deadline: bridgeTime(now.Add(bridgeClaimLease))}
	view := contract.BridgeAttemptView{AttemptID: id, State: "CLAIMED", AllowedNextActions: []string{"wait_for_hook"}}
	if err = bridgePublicSafe(ctx, view); err != nil {
		return empty, err
	}
	if err = bridgeStoredSecretSafe(ctx, tx, b.view.RunID, b.view.BindingID, view); err != nil {
		return empty, err
	}
	growth := bridgeAttemptBytes(next, nowText, "") + int64(len("SUPERSEDED_UNCERTAIN")+len(nowText)-len(a.state)-len(a.tokenHash)-len(a.updatedAt))
	if growth > 0 {
		if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
			return empty, err
		}
	}
	result, err := tx.ExecContext(ctx, "UPDATE bridge_delivery_attempts SET state='SUPERSEDED_UNCERTAIN',claim_token_hash='',updated_at=? WHERE attempt_id=? AND state='UNCERTAIN'", nowText, a.id)
	if err != nil {
		return empty, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return empty, CodeError("bridge_redeliver_state")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,wire_bytes,wire_sha256,created_at) VALUES(?,?,?,?,?,'','CLAIMED',?,0,'',NULL,'',?)", next.id, next.batchID, next.bindingID, next.ownerEpoch, next.attemptNo, next.deadline, nowText)
	if err != nil {
		return empty, mapConflict(err)
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return view, nil
}

func bridgeCursor(ownerToken, binding string, epoch uint64, rowid int64) string {
	n := strconv.FormatInt(rowid, 10)
	mac := runtimeHash(ownerToken + "\x00" + binding + "\x00" + strconv.FormatUint(epoch, 10) + "\x00" + n)
	return "bridge-status-v1-" + n + "-" + mac[:32]
}
func bridgeParseCursor(ownerToken, binding string, epoch uint64, cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	const prefix = "bridge-status-v1-"
	if !strings.HasPrefix(cursor, prefix) {
		return 0, CodeError("invalid_cursor")
	}
	rest := strings.TrimPrefix(cursor, prefix)
	pos := strings.LastIndex(rest, "-")
	if pos < 1 {
		return 0, CodeError("invalid_cursor")
	}
	n, err := strconv.ParseInt(rest[:pos], 10, 64)
	if err != nil || n < 1 {
		return 0, CodeError("invalid_cursor")
	}
	expected := bridgeCursor(ownerToken, binding, epoch, n)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(cursor)) != 1 {
		return 0, CodeError("invalid_cursor")
	}
	return n, nil
}
func (d *DB) bridgeAgeOfferedTx(ctx context.Context, tx *sql.Tx, b bridgeBinding) error {
	now := bridgeTime(time.Now())
	rows, err := tx.QueryContext(ctx, "SELECT attempt_id,state,updated_at FROM bridge_delivery_attempts WHERE binding_id=? AND state='OFFERED' AND deadline_at<=?", b.view.BindingID, now)
	if err != nil {
		return err
	}
	ids := []string{}
	var growth int64
	for rows.Next() {
		var id, state, updated string
		if err = rows.Scan(&id, &state, &updated); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
		delta := int64(len("UNCERTAIN") + len(now) - len(state) - len(updated))
		if delta > 0 {
			growth += delta
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if growth > 0 {
		if err = d.storageAdmission(ctx, tx, b.view.RunID, growth, false); err != nil {
			return err
		}
	}
	for _, id := range ids {
		result, e := tx.ExecContext(ctx, "UPDATE bridge_delivery_attempts SET state='UNCERTAIN',updated_at=? WHERE attempt_id=? AND state='OFFERED' AND deadline_at<=?", now, id, now)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return CodeError("bridge_attempt_mismatch")
		}
	}
	return nil
}
func (d *DB) BridgeStatus(ctx context.Context, req contract.BridgeStatusRequest) (contract.BridgeStatusView, error) {
	var empty contract.BridgeStatusView
	if req.SchemaVersion != 1 || req.BindingID == "" || req.OwnerEpoch == 0 {
		return empty, CodeError("bridge_invalid_input")
	}
	if err := d.bridgeLockContext(ctx); err != nil {
		return empty, err
	}
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	b, err := bridgeOwnerForHistoricalTx(ctx, tx, req.BindingID, req.OwnerEpoch, req.OwnerMode, bridgeHistoryStatus)
	if err != nil {
		return empty, err
	}
	if err = bridgeRunCollectTx(ctx, tx, b.view.RunID); err != nil {
		return empty, err
	}
	p, ok := ctx.Value(bridgeOwnerKey{}).(BridgeOwnerProof)
	if !ok {
		return empty, CodeError("owner_mismatch")
	}
	after, err := bridgeParseCursor(p.ControlToken, b.view.BindingID, b.view.OwnerEpoch, req.Cursor)
	if err != nil {
		return empty, err
	}
	if err = d.bridgeAgeOfferedTx(ctx, tx, b); err != nil {
		return empty, err
	}
	view := contract.BridgeStatusView{SchemaVersion: 1, BindingID: b.view.BindingID, OwnerEpoch: b.view.OwnerEpoch, Cursor: req.Cursor, Batches: []contract.BridgeStatusBatch{}}
	const pending = "SELECT COUNT(*) FROM delivery_order o JOIN (SELECT event_id,task_id,payload_hash,event_revision,delivery_status FROM runtime_events UNION ALL SELECT e.event_id,c.task_id,e.payload_hash,e.event_revision,e.delivery_status FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id) e ON e.event_id=o.event_id JOIN tasks t ON t.id=e.task_id WHERE t.run_id=? AND e.delivery_status='pending' AND NOT EXISTS(SELECT 1 FROM bridge_batch_items i JOIN bridge_delivery_attempts a ON a.batch_id=i.batch_id WHERE a.binding_id=? AND a.state='RECEIVED' AND i.event_id=e.event_id AND i.event_revision=e.event_revision AND i.event_hash=e.payload_hash)"
	if err = tx.QueryRowContext(ctx, pending, b.view.RunID, b.view.BindingID).Scan(&view.Counts.Pending); err != nil {
		return empty, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_delivery_attempts WHERE binding_id=? AND state IN ('EMITTING','OFFERED')", b.view.BindingID).Scan(&view.Counts.WaitingReceipt); err != nil {
		return empty, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_delivery_attempts WHERE binding_id=? AND state='UNCERTAIN'", b.view.BindingID).Scan(&view.Counts.Uncertain); err != nil {
		return empty, err
	}
	const pendingBusiness = "SELECT COUNT(DISTINCT a.batch_id) FROM bridge_delivery_attempts a JOIN bridge_batch_items i ON i.batch_id=a.batch_id LEFT JOIN runtime_events r ON r.event_id=i.event_id LEFT JOIN report_events p ON p.event_id=i.event_id WHERE a.binding_id=? AND a.state='RECEIVED' AND i.action_slot!='' AND (r.delivery_status='pending' OR p.delivery_status='pending')"
	if err = tx.QueryRowContext(ctx, pendingBusiness, b.view.BindingID).Scan(&view.Counts.PendingBusinessDecision); err != nil {
		return empty, err
	}
	const page = "SELECT x.rowid,x.batch_id,x.render_sha256,x.receipts_json,a.attempt_id,a.wire_sha256,a.state FROM bridge_batches x JOIN bridge_delivery_attempts a ON a.batch_id=x.batch_id AND a.attempt_no=(SELECT MAX(z.attempt_no) FROM bridge_delivery_attempts z WHERE z.batch_id=x.batch_id) WHERE x.binding_id=? AND x.owner_epoch=? AND x.rowid>? ORDER BY x.rowid LIMIT 9"
	rows, err := tx.QueryContext(ctx, page, b.view.BindingID, b.view.OwnerEpoch, after)
	if err != nil {
		return empty, err
	}
	var last int64
	for rows.Next() {
		var rowid int64
		var item contract.BridgeStatusBatch
		var raw []byte
		if err = rows.Scan(&rowid, &item.BatchID, &item.RenderSHA256, &raw, &item.AttemptID, &item.WireSHA256, &item.State); err != nil {
			rows.Close()
			return empty, err
		}
		if len(view.Batches) == 8 {
			next := bridgeCursor(p.ControlToken, b.view.BindingID, b.view.OwnerEpoch, last)
			view.NextCursor = &next
			break
		}
		if json.Unmarshal(raw, &item.DeliveryReceipts) != nil {
			rows.Close()
			return empty, CodeError("bridge_source_changed")
		}
		view.Batches = append(view.Batches, item)
		last = rowid
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return empty, err
	}
	if err = bridgePublicSafe(ctx, view); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return view, nil
}
