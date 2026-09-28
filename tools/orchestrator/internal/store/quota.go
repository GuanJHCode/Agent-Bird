package store

import (
	"context"
	"database/sql"
	"path/filepath"
)

func (d *DB) storageAdmission(ctx context.Context, tx *sql.Tx, runID string, incoming int64, diagnostic bool) error {
	if incoming < 0 {
		return CodeError("control_spool_full")
	}
	available, err := d.storageAvailable(filepath.Dir(d.path))
	if err != nil {
		return CodeError("storage_blocked")
	}
	required := d.minFreeBytes
	addRequired := func(value uint64) bool {
		if ^uint64(0)-required < value {
			return false
		}
		required += value
		return true
	}
	if diagnostic && !addRequired(d.criticalDiskReserve) {
		return CodeError("storage_blocked")
	}
	if !addRequired(uint64(incoming)) || !addRequired(d.sqliteWriteOverhead) || available < required {
		return CodeError("storage_blocked")
	}
	var runBytes, runProgressBytes, userBytes, userProgressBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id=?),0),COALESCE((SELECT progress_bytes FROM storage_usage WHERE scope='run' AND scope_id=?),0)`, runID, runID).Scan(&runBytes, &runProgressBytes); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT total_bytes,progress_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&userBytes, &userProgressBytes); err != nil {
		return err
	}
	exceeds := func(used, limit int64) bool { return incoming > limit || used > limit-incoming }
	if exceeds(runBytes, d.runControlLimit) || exceeds(userBytes, d.userControlLimit) {
		return CodeError("control_spool_full")
	}
	if diagnostic && (exceeds(runProgressBytes, d.runProgressLimit) || exceeds(userProgressBytes, d.userProgressLimit)) {
		return CodeError("report_budget_exhausted")
	}
	return nil
}
