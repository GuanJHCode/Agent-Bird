package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

type ProviderState struct {
	Provider string `json:"provider"`
	Enabled  bool   `json:"enabled"`
	Status   string `json:"status"`
	Model    string `json:"model"`
	Active   int    `json:"active"`
}

// No row preserves legacy admission. Explicit disable survives restart. Empty
// thread is reserved for saved model defaults and never holds an enable switch.
func (d *DB) ProviderControl(ctx context.Context, thread, provider, action string, model *string) (ProviderState, error) {
	empty := ProviderState{}
	if thread == "" || (provider != string(adapter.ProviderClaude) && provider != string(adapter.ProviderGrok) && provider != string(adapter.ProviderAGY) && provider != string(adapter.ProviderCodex)) {
		return empty, CodeError("provider_control_invalid")
	}
	if action != "status" && action != "enable" && action != "disable" && action != "model" && action != "default-model" {
		return empty, CodeError("provider_control_invalid")
	}
	if (action == "model" || action == "default-model") != (model != nil) {
		return empty, CodeError("provider_control_invalid")
	}
	if model != nil && *model != "" && !adapter.ValidModelID(*model) {
		return empty, CodeError("model_invalid")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	before, err := providerStateTx(ctx, tx, thread, provider)
	if err != nil {
		return empty, err
	}
	switch action {
	case "enable":
		if !before.Enabled && before.Active > 0 {
			return empty, CodeError("provider_stop_pending")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO provider_settings(controller_thread,provider,enabled) VALUES(?,?,1) ON CONFLICT(controller_thread,provider) DO UPDATE SET enabled=1`, thread, provider)
	case "disable":
		_, err = tx.ExecContext(ctx, `INSERT INTO provider_settings(controller_thread,provider,enabled) VALUES(?,?,0) ON CONFLICT(controller_thread,provider) DO UPDATE SET enabled=0`, thread, provider)
		if err != nil {
			return empty, err
		}
		rows, e := tx.QueryContext(ctx, `SELECT t.id,r.work_revision FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN runs p ON p.id=t.run_id WHERE p.controller_thread=? AND json_extract(r.adapter_payload,'$.provider')=? AND (t.status IN ('queued','ready','resume_queued') OR EXISTS(SELECT 1 FROM attempts a JOIN segment_runtime s ON s.attempt_id=a.id WHERE a.task_id=t.id AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')))`, thread, provider)
		if e != nil {
			return empty, e
		}
		var tasks []ActiveTask
		for rows.Next() {
			var task ActiveTask
			if e = rows.Scan(&task.TaskID, &task.WorkRevision); e != nil {
				rows.Close()
				return empty, e
			}
			tasks = append(tasks, task)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return empty, e
		}
		for _, task := range tasks {
			var status string
			if err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, task.TaskID).Scan(&status); err != nil {
				return empty, err
			}
			if status == "blocked_dependency" || status == "cancelled" {
				continue
			}
			if _, err = requestStopTx(ctx, tx, task.TaskID, task.WorkRevision, "owner_stop_requested"); err != nil {
				return empty, err
			}
		}
	case "model", "default-model":
		target := thread
		if action == "default-model" {
			target = ""
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO provider_settings(controller_thread,provider,model) VALUES(?,?,?) ON CONFLICT(controller_thread,provider) DO UPDATE SET model=excluded.model`, target, provider, *model)
	}
	if err != nil {
		return empty, err
	}
	result, err := providerStateTx(ctx, tx, thread, provider)
	if err != nil {
		return empty, err
	}
	return result, tx.Commit()
}
func providerStateTx(ctx context.Context, tx *sql.Tx, thread, provider string) (ProviderState, error) {
	state := ProviderState{Provider: provider, Enabled: true, Status: "enabled"}
	err := tx.QueryRowContext(ctx, `SELECT enabled FROM provider_settings WHERE controller_thread=? AND provider=?`, thread, provider).Scan(&state.Enabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT model FROM provider_settings WHERE controller_thread=? AND provider=?),(SELECT model FROM provider_settings WHERE controller_thread='' AND provider=?),'')`, thread, provider, provider).Scan(&state.Model)
	if err != nil {
		return state, err
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN runs p ON p.id=t.run_id JOIN task_runtime r ON r.task_id=t.id WHERE p.controller_thread=? AND json_extract(r.adapter_payload,'$.provider')=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, thread, provider).Scan(&state.Active)
	if !state.Enabled {
		state.Status = "disabled"
		if state.Active > 0 {
			state.Status = "stopping"
		}
	}
	return state, err
}
func checkProviderEnabled(ctx context.Context, tx *sql.Tx, thread string, raw []byte) error {
	var payload struct {
		Provider string `json:"provider"`
	}
	if len(raw) == 0 {
		return nil
	}
	if json.Unmarshal(raw, &payload) != nil {
		return CodeError("invalid_adapter_payload")
	}
	var enabled bool
	err := tx.QueryRowContext(ctx, `SELECT enabled FROM provider_settings WHERE controller_thread=? AND provider=?`, thread, payload.Provider).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !enabled {
		return CodeError("provider_disabled")
	}
	return nil
}
func checkTaskProvider(ctx context.Context, tx *sql.Tx, taskID string, replacement []byte) error {
	var thread, raw string
	if err := tx.QueryRowContext(ctx, `SELECT p.controller_thread,r.adapter_payload FROM tasks t JOIN runs p ON p.id=t.run_id JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, taskID).Scan(&thread, &raw); err != nil {
		return err
	}
	if replacement != nil {
		raw = string(replacement)
	}
	return checkProviderEnabled(ctx, tx, thread, []byte(raw))
}
