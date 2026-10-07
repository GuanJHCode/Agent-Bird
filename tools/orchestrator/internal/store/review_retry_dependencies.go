package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
)

// Plan only the never-started descendant closure. A retired retry/rework
// promise is separate owner authority and must never be revived here.
func reviewRetryDependents(ctx context.Context, tx *sql.Tx, root string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.status,t.dependencies_json,(SELECT COUNT(*) FROM attempts WHERE task_id=t.id),(SELECT COUNT(*) FROM decision_attempt_bindings WHERE task_id=t.id) FROM tasks t WHERE t.run_id=(SELECT run_id FROM tasks WHERE id=?)`, root)
	if err != nil {
		return nil, err
	}
	type node struct {
		status             string
		deps               []string
		attempts, bindings int
	}
	nodes := map[string]node{}
	for rows.Next() {
		var id, raw string
		var v node
		if err = rows.Scan(&id, &v.status, &raw, &v.attempts, &v.bindings); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v.deps); err != nil {
			rows.Close()
			return nil, err
		}
		nodes[id] = v
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rootNode, exists := nodes[root]
	if !exists {
		return nil, ErrConflict
	}
	rootNode.status = "ready" // prospective state in the same QueueRetry TX
	nodes[root] = rootNode
	reachable := map[string]bool{root: true}
	for changed := true; changed; {
		changed = false
		for id, v := range nodes {
			if reachable[id] {
				continue
			}
			for _, dep := range v.deps {
				if reachable[dep] {
					reachable[id], changed = true, true
					break
				}
			}
		}
	}
	visiting, known, possible := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var canComplete func(string) bool
	canComplete = func(id string) bool {
		if known[id] {
			return possible[id]
		}
		if visiting[id] {
			return false
		}
		v, exists := nodes[id]
		if !exists {
			return false
		}
		visiting[id] = true
		ok := true
		switch v.status {
		case "completed", "integrated":
			known[id], possible[id], visiting[id] = true, true, false
			return true
		case "queued", "ready", "running", "result_ready", "resume_queued", "interrupted", "waiting_question", "stopping":
		case "blocked_dependency":
			ok = reachable[id] && v.attempts == 0 && v.bindings == 0
		default:
			ok = false
		}
		for _, dep := range v.deps {
			if !canComplete(dep) {
				ok = false
				break
			}
		}
		known[id], possible[id], visiting[id] = true, ok, false
		return ok
	}
	var out []string
	for id, v := range nodes {
		if id != root && reachable[id] && v.status == "blocked_dependency" && canComplete(id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

func restoreReviewRetryDependents(ctx context.Context, tx *sql.Tx, ids []string) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='queued' WHERE id=? AND status='blocked_dependency'`, id); err != nil {
			return err
		}
	}
	return releaseReady(ctx, tx)
}
