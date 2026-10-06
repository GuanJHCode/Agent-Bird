package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// BudgetRevisionTask is one member of a complete, absolute group snapshot.
// None of its fields is a delta: each successful revision replaces the
// effective vector used for future admission and grants.
type BudgetRevisionTask struct {
	TaskID             string `json:"task_id"`
	WorkRevision       int    `json:"work_revision"`
	MaxAttempts        int    `json:"max_attempts"`
	AttemptMaxActiveMS int64  `json:"attempt_max_active_ms"`
	TaskMaxActiveMS    int64  `json:"task_max_active_ms"`
	SegmentMaxActiveMS int64  `json:"segment_max_active_ms"`
}

type BudgetRevisionRequest struct {
	BudgetGroupID    string               `json:"budget_group_id"`
	ExpectedRevision int                  `json:"expected_revision"`
	CommandID        string               `json:"command_id"`
	GroupMaxAttempts int                  `json:"group_max_attempts"`
	GroupMaxActiveMS int64                `json:"group_max_active_ms"`
	Tasks            []BudgetRevisionTask `json:"tasks"`
}

type BudgetRevisionCommand struct {
	AnchorTaskID string
	Controller   string
	ControlToken string
	Request      BudgetRevisionRequest
}

type BudgetRevisionReceipt struct {
	RunID         string `json:"run_id"`
	BudgetGroupID string `json:"budget_group_id"`
	Revision      int    `json:"revision"`
	CommandID     string `json:"command_id"`
	RequestJSON   string `json:"request_json"`
}

func canonicalBudgetRevisionRequest(req BudgetRevisionRequest) (BudgetRevisionRequest, string, error) {
	if req.BudgetGroupID == "" || req.ExpectedRevision < 0 || req.CommandID == "" || len(req.CommandID) > 128 || len(req.Tasks) == 0 || len(req.Tasks) > 64 {
		return req, "", CodeError("budget_revision_invalid")
	}
	req.Tasks = append([]BudgetRevisionTask(nil), req.Tasks...)
	sort.Slice(req.Tasks, func(i, j int) bool { return req.Tasks[i].TaskID < req.Tasks[j].TaskID })
	for i, task := range req.Tasks {
		if task.TaskID == "" || task.WorkRevision < 1 || task.MaxAttempts < 1 || task.AttemptMaxActiveMS < 1 || task.TaskMaxActiveMS < 1 || task.SegmentMaxActiveMS < 1 || (i > 0 && task.TaskID == req.Tasks[i-1].TaskID) {
			return req, "", CodeError("budget_revision_invalid")
		}
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return req, "", err
	}
	return req, string(raw), nil
}

func budgetRevisionStoredBytes(runID string, request BudgetRevisionRequest, requestJSON, receiptJSON string) int64 {
	// The SQL triggers use the same fixed accounting model for the two rows.
	return int64(56 + 2*len(runID) + 2*len(request.CommandID) + 2*len(requestJSON) + len(request.BudgetGroupID) + len(receiptJSON))
}

func (d *DB) ReviseBudget(ctx context.Context, command BudgetRevisionCommand) (BudgetRevisionReceipt, error) {
	empty := BudgetRevisionReceipt{}
	req, requestJSON, err := canonicalBudgetRevisionRequest(command.Request)
	if err != nil || command.AnchorTaskID == "" || command.Controller == "" || command.ControlToken == "" {
		if err != nil {
			return empty, err
		}
		return empty, CodeError("budget_revision_invalid")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, mapBudgetRevisionConflict(err)
	}
	rollback := func(e error) (BudgetRevisionReceipt, error) {
		_ = tx.Rollback()
		return empty, mapBudgetRevisionConflict(e)
	}
	var runID, controller, tokenHash, anchorGroup string
	err = tx.QueryRowContext(ctx, `SELECT t.run_id,r.controller_thread,c.token_hash,tr.budget_group_id FROM tasks t JOIN runs r ON r.id=t.run_id JOIN run_control c ON c.run_id=r.id JOIN task_runtime tr ON tr.task_id=t.id WHERE t.id=?`, command.AnchorTaskID).Scan(&runID, &controller, &tokenHash, &anchorGroup)
	if errors.Is(err, sql.ErrNoRows) {
		return rollback(ErrNotFound)
	}
	if err != nil {
		return rollback(err)
	}
	if controller != command.Controller || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(runtimeHash(command.ControlToken))) != 1 || anchorGroup != req.BudgetGroupID {
		return rollback(CodeError("owner_mismatch"))
	}
	// Replay is evaluated before CAS. A later revision must not invalidate the
	// original command's exact idempotent receipt.
	var savedRequest, savedReceipt string
	err = tx.QueryRowContext(ctx, `SELECT request_json,receipt_json FROM budget_revision_receipts WHERE run_id=? AND command_id=?`, runID, req.CommandID).Scan(&savedRequest, &savedReceipt)
	if err == nil {
		if savedRequest != requestJSON {
			return rollback(ErrConflict)
		}
		var receipt BudgetRevisionReceipt
		if json.Unmarshal([]byte(savedReceipt), &receipt) != nil {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	var collision int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM budget_revisions WHERE run_id=? AND command_id=?`, runID, req.CommandID).Scan(&collision); err != nil {
		return rollback(err)
	}
	if collision != 0 {
		return rollback(ErrConflict)
	}
	var current int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM budget_revisions WHERE run_id=? AND budget_group_id=?`, runID, req.BudgetGroupID).Scan(&current); err != nil {
		return rollback(err)
	}
	if current != req.ExpectedRevision {
		return rollback(ErrConflict)
	}
	if err = validateBudgetRevisionSnapshot(ctx, tx, runID, req, current); err != nil {
		return rollback(err)
	}
	receipt := BudgetRevisionReceipt{RunID: runID, BudgetGroupID: req.BudgetGroupID, Revision: current + 1, CommandID: req.CommandID, RequestJSON: requestJSON}
	receiptJSONBytes, err := json.Marshal(receipt)
	if err != nil {
		return rollback(err)
	}
	receiptJSON := string(receiptJSONBytes)
	if err = d.storageAdmission(ctx, tx, runID, budgetRevisionStoredBytes(runID, req, requestJSON, receiptJSON), false); err != nil {
		return rollback(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_revisions(run_id,budget_group_id,revision,command_id,request_json,created_at) VALUES(?,?,?,?,?,?)`, runID, req.BudgetGroupID, receipt.Revision, req.CommandID, requestJSON, now); err != nil {
		return rollback(mapConflict(err))
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_revision_receipts(run_id,command_id,request_json,receipt_json,created_at) VALUES(?,?,?,?,?)`, runID, req.CommandID, requestJSON, receiptJSON, now); err != nil {
		return rollback(mapConflict(err))
	}
	if err = tx.Commit(); err != nil {
		return empty, mapBudgetRevisionConflict(err)
	}
	return receipt, nil
}

func mapBudgetRevisionConflict(err error) error {
	if err != nil && strings.Contains(err.Error(), "database is locked") {
		return ErrConflict
	}
	return err
}

func validateBudgetRevisionSnapshot(ctx context.Context, tx *sql.Tx, runID string, req BudgetRevisionRequest, current int) error {
	var count, policies int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COUNT(p.task_id) FROM task_runtime tr JOIN tasks t ON t.id=tr.task_id LEFT JOIN task_execution_policies p ON p.task_id=t.id WHERE t.run_id=? AND tr.budget_group_id=?`, runID, req.BudgetGroupID).Scan(&count, &policies); err != nil {
		return err
	}
	if count != len(req.Tasks) || policies != count {
		return CodeError("budget_revision_group_invalid")
	}
	if req.GroupMaxAttempts < 1 || req.GroupMaxAttempts > 256 || req.GroupMaxActiveMS < 1 || req.GroupMaxActiveMS > 86_400_000 {
		return CodeError("budget_revision_invalid")
	}
	increased := false
	for _, task := range req.Tasks {
		var revision, baselineAttempts int
		var baselineAttemptMS, defaultSegment int64
		var found string
		if err := tx.QueryRowContext(ctx, `SELECT tr.work_revision,t.max_attempts,tr.max_active_ms,p.body_json FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN task_execution_policies p ON p.task_id=t.id WHERE t.run_id=? AND tr.budget_group_id=? AND t.id=?`, runID, req.BudgetGroupID, task.TaskID).Scan(&revision, &baselineAttempts, &baselineAttemptMS, &found); err != nil {
			return CodeError("budget_revision_group_invalid")
		}
		if revision != task.WorkRevision {
			return ErrConflict
		}
		b, err := readExecutionBudgetBaseline(baselineAttempts, baselineAttemptMS, found)
		if err != nil {
			return err
		}
		if current > 0 {
			latest, err := readBudgetRevisionTask(ctx, tx, runID, req.BudgetGroupID, current, task.TaskID)
			if err != nil {
				return err
			}
			b.taskAttempts, b.attemptMS, b.taskMS, b.segmentMS = latest.MaxAttempts, latest.AttemptMaxActiveMS, latest.TaskMaxActiveMS, latest.SegmentMaxActiveMS
			b.groupAttempts, b.groupMS = latest.GroupMaxAttempts, latest.GroupMaxActiveMS
		}
		defaultSegment = min(task.AttemptMaxActiveMS, defaultSegmentActiveMS)
		if req.GroupMaxAttempts < b.groupAttempts || req.GroupMaxActiveMS < b.groupMS || task.MaxAttempts > 16 || task.MaxAttempts < b.taskAttempts || task.AttemptMaxActiveMS < b.attemptMS || task.TaskMaxActiveMS < b.taskMS || task.SegmentMaxActiveMS < b.segmentMS || task.MaxAttempts > req.GroupMaxAttempts || task.AttemptMaxActiveMS > task.TaskMaxActiveMS || task.TaskMaxActiveMS > req.GroupMaxActiveMS || task.SegmentMaxActiveMS > defaultSegment {
			return CodeError("budget_revision_invalid")
		}
		increased = increased || task.MaxAttempts > b.taskAttempts || task.AttemptMaxActiveMS > b.attemptMS || task.TaskMaxActiveMS > b.taskMS || task.SegmentMaxActiveMS > b.segmentMS || req.GroupMaxAttempts > b.groupAttempts || req.GroupMaxActiveMS > b.groupMS
	}
	if !increased {
		return CodeError("budget_revision_no_increase")
	}
	return nil
}

type effectiveBudgetTask struct {
	BudgetRevisionTask
	GroupMaxAttempts int
	GroupMaxActiveMS int64
}

func readBudgetRevisionTask(ctx context.Context, tx *sql.Tx, runID, group string, revision int, taskID string) (effectiveBudgetTask, error) {
	var raw string
	var out effectiveBudgetTask
	if err := tx.QueryRowContext(ctx, `SELECT request_json FROM budget_revisions WHERE run_id=? AND budget_group_id=? AND revision=?`, runID, group, revision).Scan(&raw); err != nil {
		return out, err
	}
	var request BudgetRevisionRequest
	if json.Unmarshal([]byte(raw), &request) != nil || request.BudgetGroupID != group {
		return out, CodeError("budget_revision_invalid")
	}
	for _, task := range request.Tasks {
		if task.TaskID == taskID {
			out.BudgetRevisionTask, out.GroupMaxAttempts, out.GroupMaxActiveMS = task, request.GroupMaxAttempts, request.GroupMaxActiveMS
			return out, nil
		}
	}
	return out, CodeError("budget_revision_invalid")
}
