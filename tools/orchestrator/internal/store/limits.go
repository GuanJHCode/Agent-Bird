package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"path/filepath"
	"time"
)

type ConcurrencyLimits struct {
	Version    int            `json:"version"`
	Global     int            `json:"global"`
	Providers  map[string]int `json:"providers,omitempty"`
	Workspaces map[string]int `json:"workspaces,omitempty"`
}

func workspaceKey(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
func (d *DB) ConfigureConcurrency(ctx context.Context, raw json.RawMessage) error {
	var limits ConcurrencyLimits
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&limits) != nil || limits.Version != 1 || limits.Global < 1 || limits.Global > 32 {
		return CodeError("concurrency_limits_invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return CodeError("concurrency_limits_invalid")
	}
	for key, n := range limits.Providers {
		if key == "" || len(key) > 128 || n < 1 || n > 32 {
			return CodeError("concurrency_limits_invalid")
		}
	}
	normalized := map[string]int{}
	for key, n := range limits.Workspaces {
		if !filepath.IsAbs(key) || n < 1 || n > 32 {
			return CodeError("concurrency_limits_invalid")
		}
		key = workspaceKey(key)
		if _, exists := normalized[key]; exists {
			return CodeError("concurrency_limits_invalid")
		}
		normalized[key] = n
	}
	limits.Workspaces = normalized
	body, err := json.Marshal(limits)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err = d.sql.ExecContext(ctx, `UPDATE scheduler_config SET body_json=? WHERE id=1`, string(body))
	return err
}
func readLimits(ctx context.Context, tx *sql.Tx) (ConcurrencyLimits, error) {
	var raw string
	var limits ConcurrencyLimits
	err := tx.QueryRowContext(ctx, `SELECT body_json FROM scheduler_config WHERE id=1`).Scan(&raw)
	if err != nil {
		return limits, err
	}
	err = json.Unmarshal([]byte(raw), &limits)
	return limits, err
}
func resourceKeys(raw string) (string, string) {
	var payload struct {
		Provider  string `json:"provider"`
		Kind      string `json:"kind"`
		Directory string `json:"directory"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return "", ""
	}
	if payload.Provider == "" {
		payload.Provider = payload.Kind
	}
	return payload.Provider, workspaceKey(payload.Directory)
}

type readyTask struct {
	taskID, runID, status, group, launchID string
	revision                               int
	maxActive                              int64
	maxAttempts                            int
	payload                                string
}

func (d *DB) selectReady(ctx context.Context, tx *sql.Tx, launchID string, epoch uint64, limits ConcurrencyLimits) (readyTask, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.adapter_payload FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN task_runtime r ON r.task_id=a.task_id WHERE s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`)
	if err != nil {
		return readyTask{}, err
	}
	providers, workspaces := map[string]int{}, map[string]int{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return readyTask{}, err
		}
		p, w := resourceKeys(raw)
		providers[p]++
		workspaces[w]++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return readyTask{}, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT t.id,t.run_id,t.status,r.work_revision,r.budget_group_id,r.max_active_ms,t.max_attempts,r.adapter_payload,r.host_launch_id FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN runtime_hosts h ON h.launch_id=r.host_launch_id WHERE h.status='ready' AND h.coordinator_epoch=? AND t.status IN ('ready','resume_queued') ORDER BY r.ready_sequence`, epoch)
	if err != nil {
		return readyTask{}, err
	}
	defer rows.Close()
	var blockedPolicy error
	for rows.Next() {
		var task readyTask
		if err = rows.Scan(&task.taskID, &task.runID, &task.status, &task.revision, &task.group, &task.maxActive, &task.maxAttempts, &task.payload, &task.launchID); err != nil {
			return readyTask{}, err
		}
		if err := checkTaskProvider(ctx, tx, task.taskID, nil); err != nil {
			if err == CodeError("provider_disabled") {
				continue
			}
			return readyTask{}, err
		}
		p, w := resourceKeys(task.payload)
		pmax, wmax := limits.Providers[p], limits.Workspaces[w]
		if pmax == 0 {
			pmax = limits.Global
		}
		if wmax == 0 {
			wmax = limits.Global
		}
		if providers[p] >= pmax || workspaces[w] >= wmax {
			continue
		}
		var eligible int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(d.eligible_at_unix_ms),0) FROM decision_attempt_bindings b JOIN retry_decisions d USING(action_slot) WHERE b.task_id=? AND b.attempt_id IS NULL AND b.retired=0`, task.taskID).Scan(&eligible); err != nil {
			return readyTask{}, err
		}
		if eligible > time.Now().UnixMilli() {
			continue
		}
		allowed, targetErr := integrationDispatchAllowed(ctx, tx, task)
		if targetErr != nil {
			return readyTask{}, targetErr
		}
		if !allowed {
			continue
		}
		var positiveHold, groupHold int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN b.task_id=? THEN 1 ELSE 0 END),0) FROM decision_attempt_bindings b JOIN task_runtime r ON r.task_id=b.task_id WHERE r.budget_group_id=? AND b.attempt_id IS NULL AND b.retired=0 AND b.reserved_active_ms>0`, task.taskID, task.group).Scan(&groupHold, &positiveHold); err != nil {
			return readyTask{}, err
		}
		var binding *decisionAttemptBinding
		if task.status != "resume_queued" {
			budget, readErr := readExecutionBudget(ctx, tx, task.taskID)
			if readErr != nil {
				return readyTask{}, readErr
			}
			if budget.policy != nil || positiveHold > 0 {
				binding, _, err = d.pendingDecisionBinding(ctx, tx, task.taskID, task.revision)
				if err != nil {
					return readyTask{}, err
				}
			}
			if e := executionCapacity(ctx, tx, task.taskID, binding); e != nil {
				if task.launchID == launchID {
					budget, readErr := readExecutionBudget(ctx, tx, task.taskID)
					if readErr != nil {
						return readyTask{}, readErr
					}
					if budget.policy == nil && groupHold == 0 || e != ErrAttemptLimit {
						return readyTask{}, e
					}
					// A pending promise can make an older sibling ineligible.
					// Continue FIFO so its due retry can consume that promise.
					if blockedPolicy == nil {
						blockedPolicy = e
					}
				}
				continue
			}
		}
		attempt := ""
		if task.status == "resume_queued" {
			if err = tx.QueryRowContext(ctx, `SELECT id FROM attempts WHERE task_id=? ORDER BY attempt_no DESC LIMIT 1`, task.taskID).Scan(&attempt); err != nil {
				return readyTask{}, err
			}
		}
		if _, e := executionGrantForBinding(ctx, tx, task.taskID, attempt, binding); e != nil {
			if task.launchID == launchID {
				budget, readErr := readExecutionBudget(ctx, tx, task.taskID)
				if readErr != nil {
					return readyTask{}, readErr
				}
				if budget.policy == nil && groupHold == 0 || e != CodeError("budget_exhausted") {
					return readyTask{}, e
				}
				if blockedPolicy == nil {
					blockedPolicy = e
				}
			}
			continue
		}
		if task.launchID != launchID {
			return readyTask{}, sql.ErrNoRows
		}
		return task, nil
	}
	if err = rows.Err(); err != nil {
		return readyTask{}, err
	}
	if blockedPolicy != nil {
		return readyTask{}, blockedPolicy
	}
	return readyTask{}, sql.ErrNoRows
}
