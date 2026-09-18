package store

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type PendingPage struct {
	Events     []contract.Event
	NextCursor string
}

const (
	collectPageEvents = 32
	collectPageBytes  = 48 * 1024
)

func deliveryCursor(taskID string, ordinal int64) string {
	value := strconv.FormatInt(ordinal, 10)
	return "delivery-v1-" + value + "-" + runtimeHash(taskID + "\x00" + value)[:24]
}

func parseDeliveryCursor(taskID, cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	const prefix = "delivery-v1-"
	if !strings.HasPrefix(cursor, prefix) {
		return 0, CodeError("invalid_cursor")
	}
	remainder := strings.TrimPrefix(cursor, prefix)
	separator := strings.LastIndexByte(remainder, '-')
	if separator < 1 {
		return 0, CodeError("invalid_cursor")
	}
	value, proof := remainder[:separator], remainder[separator+1:]
	ordinal, err := strconv.ParseInt(value, 10, 64)
	want := runtimeHash(taskID + "\x00" + value)[:24]
	if err != nil || ordinal < 1 || subtle.ConstantTimeCompare([]byte(proof), []byte(want)) != 1 {
		return 0, CodeError("invalid_cursor")
	}
	return ordinal, nil
}

func (d *DB) CollectPending(ctx context.Context, taskID, cursor string, limit int, includeDiagnostics bool) (PendingPage, error) {
	if limit < 1 || limit > collectPageEvents {
		limit = collectPageEvents
	}
	ordinal, err := parseDeliveryCursor(taskID, cursor)
	if err != nil {
		return PendingPage{}, err
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT o.ordinal,e.body_json,e.event_revision,e.action_slot FROM delivery_order o JOIN (
	 SELECT event_id,body_json,event_revision,action_slot FROM runtime_events WHERE task_id=? AND delivery_status='pending'
 UNION ALL
 SELECT event_id,body_json,sequence AS event_revision,'' AS action_slot FROM host_progress WHERE task_id=? AND ?
	 UNION ALL
	 SELECT e.event_id,e.body_json,e.event_revision,e.action_slot FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=? AND (e.delivery_status='pending' OR (? AND e.kind='progress'))
	) e ON e.event_id=o.event_id WHERE o.task_id=? AND o.ordinal>? ORDER BY o.ordinal LIMIT ?`, taskID, taskID, includeDiagnostics, taskID, includeDiagnostics, taskID, ordinal, limit+1)
	if err != nil {
		return PendingPage{}, err
	}
	defer rows.Close()
	page := PendingPage{Events: make([]contract.Event, 0, collectPageEvents)}
	pageBytes := 0
	var lastOrdinal int64
	for rows.Next() {
		var body, actionSlot string
		var eventOrdinal, revision int64
		if err = rows.Scan(&eventOrdinal, &body, &revision, &actionSlot); err != nil {
			return PendingPage{}, err
		}
		var event contract.Event
		if err = json.Unmarshal([]byte(body), &event); err != nil {
			return PendingPage{}, err
		}
		event.EventRevision, event.ActionSlot = revision, actionSlot
		encoded, err := json.Marshal(event)
		if err != nil {
			return PendingPage{}, err
		}
		if len(page.Events) >= limit || (len(page.Events) > 0 && pageBytes+len(encoded)+1 > collectPageBytes) {
			page.NextCursor = deliveryCursor(taskID, lastOrdinal)
			break
		}
		page.Events = append(page.Events, event)
		pageBytes += len(encoded) + 1
		lastOrdinal = eventOrdinal
	}
	if err = rows.Err(); err != nil {
		return PendingPage{}, err
	}
	return page, nil
}

type AckDecision struct {
	EventID       string `json:"event_id"`
	EventRevision int64  `json:"event_revision"`
	EventHash     string `json:"event_hash"`
	ActionSlot    string `json:"action_slot"`
	Decision      string `json:"decision"`
	CommandID     string `json:"command_id"`
}

func validAckDecision(decision AckDecision) bool {
	return decision.EventID != "" && decision.EventRevision > 0 && decision.EventHash != "" && decision.ActionSlot != "" && decision.CommandID != "" && (decision.Decision == "handled" || decision.Decision == "waiting_user" || decision.Decision == "stale" || decision.Decision == "rejected")
}

func (d *DB) AckDecisions(ctx context.Context, taskID, deliveryID, historyProofSHA256 string, decisions []AckDecision) error {
	if taskID == "" || deliveryID == "" || !validDigest(historyProofSHA256) || len(decisions) == 0 || len(decisions) > 64 {
		return CodeError("invalid_ack")
	}
	seen := make(map[string]bool, len(decisions))
	for _, decision := range decisions {
		if !validAckDecision(decision) || seen[decision.ActionSlot] {
			return CodeError("invalid_ack")
		}
		seen[decision.ActionSlot] = true
	}
	encoded, _ := json.Marshal(decisions)
	decisionsHash := runtimeHash(string(encoded))
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	var storedTask, storedProof, storedDecisions string
	err = tx.QueryRowContext(ctx, `SELECT task_id,history_proof_sha256,decisions_hash FROM delivery_receipts WHERE delivery_id=?`, deliveryID).Scan(&storedTask, &storedProof, &storedDecisions)
	if err == nil {
		if storedTask != taskID || storedProof != strings.ToLower(historyProofSHA256) || storedDecisions != decisionsHash {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	for _, decision := range decisions {
		if err = applyAckDecision(ctx, tx, taskID, decision); err != nil {
			return rollback(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_receipts(delivery_id,task_id,history_proof_sha256,decisions_hash,created_at) VALUES(?,?,?,?,?)`, deliveryID, taskID, strings.ToLower(historyProofSHA256), decisionsHash, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return rollback(mapConflict(err))
	}
	return tx.Commit()
}

func applyAckDecision(ctx context.Context, tx *sql.Tx, taskID string, decision AckDecision) error {
	var err error
	var storedTask, storedHash, storedSlot, body, delivery string
	var storedRevision int64
	eventTable := "runtime_events"
	if err = tx.QueryRowContext(ctx, `SELECT task_id,payload_hash,event_revision,action_slot,body_json,delivery_status FROM runtime_events WHERE event_id=?`, decision.EventID).Scan(&storedTask, &storedHash, &storedRevision, &storedSlot, &body, &delivery); err != nil || storedTask != taskID || storedRevision != decision.EventRevision || storedSlot != decision.ActionSlot || subtle.ConstantTimeCompare([]byte(storedHash), []byte(strings.ToLower(decision.EventHash))) != 1 {
		if !errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		eventTable = "report_events"
		err = tx.QueryRowContext(ctx, `SELECT c.task_id,e.payload_hash,e.event_revision,e.action_slot,e.body_json,e.delivery_status FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE e.event_id=?`, decision.EventID).Scan(&storedTask, &storedHash, &storedRevision, &storedSlot, &body, &delivery)
		if err != nil || storedTask != taskID || storedRevision != decision.EventRevision || storedSlot != decision.ActionSlot || subtle.ConstantTimeCompare([]byte(storedHash), []byte(strings.ToLower(decision.EventHash))) != 1 {
			return ErrConflict
		}
	}
	var existingDecision, existingHash string
	var existingRevision int64
	err = tx.QueryRowContext(ctx, `SELECT decision,event_hash,event_revision FROM runtime_decisions WHERE action_slot=?`, decision.ActionSlot).Scan(&existingDecision, &existingHash, &existingRevision)
	if err == nil {
		if existingDecision != decision.Decision || existingHash != storedHash || existingRevision != storedRevision {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if delivery != "pending" {
		return ErrConflict
	}
	if decision.Decision == "handled" || decision.Decision == "waiting_user" {
		var event contract.Event
		if json.Unmarshal([]byte(body), &event) != nil {
			return ErrConflict
		}
		var workRevision int
		var currentSegment sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT r.work_revision,(SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1) FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, taskID).Scan(&workRevision, &currentSegment); err != nil || event.WorkRevision != workRevision || !currentSegment.Valid {
			return ErrConflict
		}
		if currentSegment.String != event.SegmentID && (decision.Decision != "handled" || !hasBoundBusinessEffect(ctx, tx, taskID, event, decision)) {
			return ErrConflict
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_decisions(action_slot,event_id,event_revision,event_hash,decision,command_id,created_at) VALUES(?,?,?,?,?,?,?)`, decision.ActionSlot, decision.EventID, decision.EventRevision, strings.ToLower(decision.EventHash), decision.Decision, decision.CommandID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return mapConflict(err)
	}
	statement := `UPDATE runtime_events SET delivery_status='acked' WHERE event_id=? AND delivery_status='pending'`
	if eventTable == "report_events" {
		statement = `UPDATE report_events SET delivery_status='acked' WHERE event_id=? AND delivery_status='pending'`
	}
	if _, err = tx.ExecContext(ctx, statement, decision.EventID); err != nil {
		return err
	}
	return nil
}

func hasBoundBusinessEffect(ctx context.Context, tx *sql.Tx, taskID string, event contract.Event, decision AckDecision) bool {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM retry_decisions WHERE action_slot=? AND task_id=? AND source_event_id=? AND source_event_revision=? AND source_event_hash=? AND source_segment_id=?`, decision.ActionSlot, taskID, decision.EventID, decision.EventRevision, strings.ToLower(decision.EventHash), event.SegmentID).Scan(&found)
	if err == nil {
		return true
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM review_decisions WHERE action_slot=? AND task_id=? AND event_id=? AND event_revision=? AND event_hash=?`, decision.ActionSlot, taskID, decision.EventID, decision.EventRevision, strings.ToLower(decision.EventHash)).Scan(&found)
	if err == nil {
		return true
	}
	if event.Kind != contract.EventQuestion || event.QuestionID == "" || event.QuestionRevision < 1 {
		return false
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM runtime_questions WHERE question_id=? AND task_id=? AND segment_id=? AND question_revision=? AND work_revision=? AND answer_hash!='' AND status IN ('resume_queued','resume_started')`, event.QuestionID, taskID, event.SegmentID, event.QuestionRevision, event.WorkRevision).Scan(&found)
	return err == nil
}

func (d *DB) AckDecision(ctx context.Context, taskID string, decision AckDecision) error {
	return d.AckDecisions(ctx, taskID, "delivery-"+decision.CommandID, runtimeHash("proof:"+decision.CommandID), []AckDecision{decision})
}
