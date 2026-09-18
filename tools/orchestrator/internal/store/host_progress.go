package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func migrateHostProgress(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE host_progress(event_id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),segment_id TEXT NOT NULL,sequence INTEGER NOT NULL,body_json TEXT NOT NULL,accounted_bytes INTEGER NOT NULL,UNIQUE(segment_id,sequence));`)
	if err != nil {
		return err
	}
	for _, op := range []string{"INSERT", "DELETE"} {
		ref, sign := "NEW", "+"
		if op == "DELETE" {
			ref, sign = "OLD", "-"
		}
		body := ""
		for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", "(SELECT run_id FROM tasks WHERE id=" + ref + ".task_id)"}} {
			body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes%s%s.accounted_bytes,progress_bytes=progress_bytes%s%s.accounted_bytes WHERE scope='%s' AND scope_id=%s;", sign, ref, sign, ref, scope.name, scope.id)
		}
		if _, err = tx.Exec("CREATE TRIGGER quota_host_progress_" + op + " AFTER " + op + " ON host_progress BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	return nil
}

// A separate authenticated stream: a rejected observation never consumes a
// lifecycle sequence. Quota/epoch failures may be dropped by its producer.
func (d *DB) CommitHostProgress(ctx context.Context, event contract.Event, launchID, token string, pid int, birth string) error {
	if event.Kind != contract.EventProgress || event.Version != 1 || event.Sequence < 1 || event.Sequence > 21 || event.StopReason != "" || event.Artifact == nil || event.Artifact.ID != "provider-progress" || event.Artifact.Size < 1 || event.Artifact.Size > 64*1024 || !validDigest(event.PayloadHash) || event.PayloadHash != event.Artifact.SHA256 {
		return CodeError("invalid_progress")
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hash string
	if err = tx.QueryRowContext(ctx, `SELECT l.token_hash FROM host_launches l JOIN runtime_hosts h ON h.launch_id=l.id JOIN segment_runtime s ON s.host_id=h.id JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime r ON r.task_id=t.id WHERE l.id=? AND h.id=? AND h.pid=? AND h.birth=? AND h.status='ready' AND h.coordinator_epoch=? AND s.segment_id=? AND s.command_id=? AND a.id=? AND a.task_id=? AND t.run_id=? AND r.work_revision=? AND s.status IN ('prepared','spawned','running','stopping')`, launchID, event.ProducerID, pid, birth, event.ExecutionEpoch, event.SegmentID, event.CommandID, event.AttemptID, event.TaskID, event.RunID, event.WorkRevision).Scan(&hash); err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(runtimeHash(token))) != 1 {
		return ErrHostRejected
	}
	var old string
	err = tx.QueryRowContext(ctx, `SELECT body_json FROM host_progress WHERE event_id=?`, event.EventID).Scan(&old)
	if err == nil {
		if old != string(body) {
			return ErrConflict
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	incoming := int64(len(body)) + event.Artifact.Size
	// Reserve control quota for every currently owned segment's terminal
	// artifact (<=1 MiB) plus lifecycle envelopes. Diagnostic quota alone is
	// insufficient when ordinary control records already approach the limit.
	var runBytes, userBytes, runActive, userActive int64
	err = tx.QueryRowContext(ctx, `SELECT
 COALESCE((SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id=?),0),
 COALESCE((SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''),0),
 (SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id=? AND s.status!='exited'),
 (SELECT COUNT(*) FROM segment_runtime WHERE status!='exited')`, event.RunID, event.RunID).Scan(&runBytes, &userBytes, &runActive, &userActive)
	if err != nil {
		return err
	}
	const terminalReserve = 2 * 1024 * 1024
	if incoming > d.runControlLimit-runBytes-runActive*terminalReserve || incoming > d.userControlLimit-userBytes-userActive*terminalReserve {
		return CodeError("report_budget_exhausted")
	}
	if err = d.storageAdmission(ctx, tx, event.RunID, incoming, true); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO host_progress VALUES(?,?,?,?,?,?)`, event.EventID, event.TaskID, event.SegmentID, event.Sequence, string(body), incoming); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_order(event_id,task_id) VALUES(?,?)`, event.EventID, event.TaskID); err != nil {
		return err
	}
	return tx.Commit()
}
