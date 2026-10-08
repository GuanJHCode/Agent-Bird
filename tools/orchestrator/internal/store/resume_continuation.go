package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"strings"
)

type ResumeContinuationSpec struct {
	TaskID        string `json:"task_id"`
	WorkRevision  int    `json:"work_revision"`
	SegmentID     string `json:"segment_id"`
	EventID       string `json:"event_id"`
	EventRevision int64  `json:"event_revision"`
	EventHash     string `json:"event_hash"`
	ActionSlot    string `json:"action_slot"`
	CommandID     string `json:"command_id"`
}

type ContinuationReceipt struct {
	Version         int    `json:"version"`
	Status          string `json:"status"`
	DecisionID      string `json:"decision_id"`
	TaskID          string `json:"task_id"`
	AttemptID       string `json:"attempt_id"`
	SourceSegmentID string `json:"source_segment_id"`
}

func (d *DB) QueueContinuation(ctx context.Context, s ResumeContinuationSpec) (ContinuationReceipt, error) {
	if s.TaskID == "" || s.WorkRevision < 1 || s.SegmentID == "" || s.EventID == "" || s.EventRevision < 1 || len(s.EventHash) != 64 || s.ActionSlot == "" || s.CommandID == "" || len(s.CommandID) > 128 {
		return ContinuationReceipt{}, CodeError("invalid_continuation")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginOwnerTransaction(ctx)
	if err != nil {
		return ContinuationReceipt{}, err
	}
	defer owned.close()
	tx := owned.tx
	fail := func(e error) (ContinuationReceipt, error) { owned.close(); return ContinuationReceipt{}, e }
	var r ContinuationReceipt
	var command, event, hash, slot string
	var rev int
	var eventRev int64
	err = tx.QueryRowContext(ctx, `SELECT decision_id,task_id,attempt_id,source_segment_id,command_id,source_event_id,source_event_revision,source_event_sha256,action_slot,work_revision FROM continuation_decisions WHERE action_slot=? OR command_id=? OR source_event_id=?`, s.ActionSlot, s.CommandID, s.EventID).Scan(&r.DecisionID, &r.TaskID, &r.AttemptID, &r.SourceSegmentID, &command, &event, &eventRev, &hash, &slot, &rev)
	if err == nil {
		if r.TaskID != s.TaskID || r.SourceSegmentID != s.SegmentID || command != s.CommandID || event != s.EventID || eventRev != s.EventRevision || hash != strings.ToLower(s.EventHash) || slot != s.ActionSlot || rev != s.WorkRevision {
			return fail(ErrConflict)
		}
		r.Version, r.Status = 1, "resume_queued"
		owned.close()
		return r, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fail(err)
	}
	var status, raw, latest, run string
	var currentRev, active, open int
	err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision,r.adapter_payload,t.run_id FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, s.TaskID).Scan(&status, &currentRev, &raw, &run)
	if err != nil {
		return fail(ErrNotFound)
	}
	if status != "interrupted" || currentRev != s.WorkRevision {
		return fail(ErrConflict)
	}
	p, err := continuationPayload([]byte(raw))
	if err != nil {
		return fail(err)
	}
	if p.Profile == nil {
		return fail(CodeError("typed_continuation_required"))
	}
	if err = checkTaskProvider(ctx, tx, s.TaskID, nil); err != nil {
		return fail(err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1`, s.TaskID).Scan(&latest); err != nil || latest != s.SegmentID {
		return fail(ErrConflict)
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, s.TaskID).Scan(&active); err != nil || active != 0 {
		return fail(ErrConflict)
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_questions WHERE task_id=? AND status IN ('open','resume_queued','resume_started')`, s.TaskID).Scan(&open); err != nil || open != 0 {
		return fail(ErrConflict)
	}
	e, sourceKind, err := readContinuationSource(ctx, tx, s.EventID, s.TaskID, s.SegmentID, false)
	if err != nil {
		return fail(err)
	}
	if e.Kind != contract.EventStopped || e.RunID != run || e.WorkRevision != s.WorkRevision || e.EventRevision != s.EventRevision || e.PayloadHash != strings.ToLower(s.EventHash) || e.ActionSlot != s.ActionSlot {
		return fail(ErrConflict)
	}
	var outcome, host, launch string
	if err = tx.QueryRowContext(ctx, `SELECT outcome,host_id,command_id FROM segment_runtime WHERE segment_id=?`, s.SegmentID).Scan(&outcome, &host, &launch); err != nil || outcome != contract.EventStopped || host != e.ProducerID || launch != e.CommandID {
		return fail(ErrConflict)
	}
	ref, b, err := trustedContinuationRef(ctx, tx, e, contract.ContinuationInterrupted, sourceKind)
	if err != nil {
		return fail(err)
	}
	if err = checkContinuationBudget(ctx, tx, s.TaskID, e.AttemptID); err != nil {
		return fail(err)
	}
	if err = d.insertContinuationDecision(ctx, tx, ref, b, e, s.CommandID, "", ""); err != nil {
		return fail(err)
	}
	seq, err := takeReadySequence(ctx, tx)
	if err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='resume_queued' WHERE id=?`, s.TaskID); err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, seq, s.TaskID); err != nil {
		return fail(err)
	}
	r = ContinuationReceipt{Version: 1, Status: "resume_queued", DecisionID: "continuation-" + runtimeHash(e.EventID + "\x00" + e.ActionSlot)[:32], TaskID: s.TaskID, AttemptID: e.AttemptID, SourceSegmentID: e.SegmentID}
	if err = owned.commit(); err != nil {
		return ContinuationReceipt{}, err
	}
	return r, nil
}
