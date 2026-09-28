package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"path/filepath"
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
	taskID, runID, status, group string
	revision                     int
	maxActive                    int64
	maxAttempts                  int
	payload                      string
}

func selectReady(ctx context.Context, tx *sql.Tx, launchID string, limits ConcurrencyLimits) (readyTask, error) {
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
	rows, err = tx.QueryContext(ctx, `SELECT t.id,t.run_id,t.status,r.work_revision,r.budget_group_id,r.max_active_ms,t.max_attempts,r.adapter_payload FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE r.host_launch_id=? AND t.status IN ('ready','resume_queued') ORDER BY r.ready_sequence`, launchID)
	if err != nil {
		return readyTask{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var task readyTask
		if err = rows.Scan(&task.taskID, &task.runID, &task.status, &task.revision, &task.group, &task.maxActive, &task.maxAttempts, &task.payload); err != nil {
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
		if providers[p] < pmax && workspaces[w] < wmax {
			return task, nil
		}
	}
	if err = rows.Err(); err != nil {
		return readyTask{}, err
	}
	return readyTask{}, sql.ErrNoRows
}
