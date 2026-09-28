package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Narrow read-only upgrade diagnostic. No migrations, capabilities, task
// payloads, provider state, or automatic process actions are exposed.
func runtimeStatus(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("runtime-status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateArg := fs.String("state-dir", "", "runtime state")
	runID := fs.String("run-id", "", "optional exact run ID for read-only reconciliation")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return codeError("invalid_args")
	}
	state, err := resolveState(*stateArg)
	if err != nil {
		return err
	}
	path := filepath.Join(state, "state.db")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		return codeError("runtime_unavailable")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return codeError("runtime_unavailable")
	}
	defer db.Close()
	queryCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var active, unknown, stops int
	err = db.QueryRowContext(queryCtx, `SELECT (SELECT COUNT(*) FROM segment_runtime WHERE status IN ('launch_requested','prepared','spawned','running','stopping','unknown')),(SELECT COUNT(*) FROM segment_runtime WHERE status='unknown'),(SELECT COUNT(*) FROM stop_runtime WHERE status IN ('pending','accepted'))`).Scan(&active, &unknown, &stops)
	if err != nil {
		return codeError("runtime_unavailable")
	}
	var queued, hosts int
	err = db.QueryRowContext(queryCtx, `SELECT (SELECT COUNT(*) FROM tasks WHERE status IN ('queued','ready','resume_queued')),(SELECT COUNT(*) FROM host_launches WHERE status!='released')`).Scan(&queued, &hosts)
	if err != nil {
		return codeError("runtime_unavailable")
	}
	result := map[string]any{"queued_tasks": queued, "unreleased_hosts": hosts, "version": 1, "active_segments": active, "unknown_segments": unknown, "pending_stops": stops, "observation_only": true}
	// Aggregate lifecycle metadata only; this remains an observation, never an upgrade lock.
	for name, query := range map[string]string{
		"host_states": `SELECT status,COUNT(*) FROM host_launches WHERE status!='released' GROUP BY status`,
		"task_states": `SELECT status,COUNT(*) FROM tasks WHERE status NOT IN ('completed','cancelled','failed','budget_exhausted','blocked_dependency') GROUP BY status`,
	} {
		rows, e := db.QueryContext(queryCtx, query)
		if e != nil {
			return codeError("runtime_unavailable")
		}
		counts := map[string]int{}
		for rows.Next() {
			var status string
			var count int
			if e = rows.Scan(&status, &count); e != nil {
				rows.Close()
				return codeError("runtime_unavailable")
			}
			counts[status] = count
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return codeError("runtime_unavailable")
		}
		result[name] = counts
	}
	var readyHosts, pendingEvents, pendingReports int
	err = db.QueryRowContext(queryCtx, `SELECT (SELECT COUNT(*) FROM runtime_hosts WHERE status='ready'),(SELECT COUNT(*) FROM runtime_events WHERE delivery_status='pending'),(SELECT COUNT(*) FROM report_events WHERE delivery_status IN ('pending','pending_session'))`).Scan(&readyHosts, &pendingEvents, &pendingReports)
	if err != nil {
		return codeError("runtime_unavailable")
	}
	result["ready_hosts"] = readyHosts
	result["pending_events"] = pendingEvents
	result["pending_reports"] = pendingReports
	if *runID != "" {
		var runs, tasks, attempts int
		err = db.QueryRowContext(queryCtx, `SELECT (SELECT COUNT(*) FROM runs WHERE id=?),(SELECT COUNT(*) FROM tasks WHERE run_id=?),(SELECT COUNT(*) FROM attempts WHERE task_id IN (SELECT id FROM tasks WHERE run_id=?))`, *runID, *runID, *runID).Scan(&runs, &tasks, &attempts)
		if err != nil {
			return codeError("runtime_unavailable")
		}
		result["run_id"] = *runID
		result["matching_runs"] = runs
		result["matching_tasks"] = tasks
		result["matching_attempts"] = attempts
	}
	return json.NewEncoder(out).Encode(result)
}
