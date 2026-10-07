package store

import (
	"context"
	"database/sql"
	"errors"
)

const currentSchemaVersion = 23

// Runtime migrations run under the coordinator instance lock (NewServer) and
// one SQLite transaction. DDL, backfills and version publication commit together;
// rollback preserves the prior schema and data rather than guessing recovery.
func (d *DB) migrateRuntime() (err error) {
	// bridge_bindings is the parent of four existing bridge tables. SQLite only
	// permits the parent rebuild required by schema23 when FK enforcement is
	// disabled before BEGIN; defer_foreign_keys is not a substitute.
	ctx := context.Background()
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var initialVersion int
	if err = conn.QueryRowContext(ctx, `SELECT version FROM schema_meta`).Scan(&initialVersion); err != nil {
		return err
	}
	if initialVersion > currentSchemaVersion {
		return CodeError("schema_newer_than_binary")
	}
	if initialVersion < 1 {
		return CodeError("schema_invalid")
	}
	needsSchema23 := initialVersion < 23
	foreignKeysDisabled := false
	var tx *sql.Tx
	committed := false
	defer func() {
		if tx != nil && !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = errors.Join(err, rollbackErr)
			}
		}
		if foreignKeysDisabled {
			err = errors.Join(err, restoreRuntimeForeignKeys(ctx, conn))
		}
	}()
	if needsSchema23 {
		if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
			return err
		}
		foreignKeysDisabled = true
		var foreignKeys int
		if err = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
			if err == nil {
				err = CodeError("schema23_foreign_keys_not_disabled")
			}
			return err
		}
	}
	tx, err = conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var version int
	if err = tx.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		return err
	}
	if version > currentSchemaVersion {
		return CodeError("schema_newer_than_binary")
	}
	if version < 1 {
		return CodeError("schema_invalid")
	}
	if version == 1 {
		if err = ensureRuntimeSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=2`); err != nil {
			return err
		}
	}
	if version < 3 {
		if _, err = tx.Exec(`CREATE TABLE owner_grants(id TEXT PRIMARY KEY,token_hash TEXT NOT NULL,controller_thread TEXT NOT NULL,origin_pid INTEGER NOT NULL,origin_birth TEXT NOT NULL,created_at TEXT NOT NULL); ALTER TABLE runs ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'native'; CREATE TABLE collection_receipts(id TEXT PRIMARY KEY,task_id TEXT NOT NULL,proof_hash TEXT NOT NULL,bindings_json TEXT NOT NULL); UPDATE schema_meta SET version=3`); err != nil {
			return err
		}
	}
	if version < 4 {
		if _, err = tx.Exec(`CREATE TABLE scheduler_config(id INTEGER PRIMARY KEY CHECK(id=1),body_json TEXT NOT NULL); INSERT INTO scheduler_config VALUES(1,'{"version":1,"global":2}'); UPDATE schema_meta SET version=4`); err != nil {
			return err
		}
	}
	if version < 5 {
		if err = migrateStorageUsage(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=5`); err != nil {
			return err
		}
	}
	if version < 6 {
		if err = migrateReworkLedger(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=6`); err != nil {
			return err
		}
	}
	if version < 7 {
		if err = migrateHostProgress(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=7`); err != nil {
			return err
		}
	}
	if version < 8 {
		if _, err = tx.Exec(`CREATE TABLE provider_settings(controller_thread TEXT NOT NULL, provider TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)), model TEXT, PRIMARY KEY(controller_thread,provider)); UPDATE schema_meta SET version=8`); err != nil {
			return err
		}
	}
	if version < 9 {
		if err = migrateBridgeSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=9`); err != nil {
			return err
		}
	}
	if version < 10 {
		if err = migrateContinuationSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=10`); err != nil {
			return err
		}
	}
	if version < 11 {
		// Legacy rows have no proof of the old generation. Never guess it.
		if _, err = tx.Exec(`ALTER TABLE owner_rebinds ADD COLUMN old_host_generation TEXT NOT NULL DEFAULT ''; UPDATE schema_meta SET version=11`); err != nil {
			return err
		}
	}
	if version < 12 {
		if err = migrateOriginalOwnerSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=12`); err != nil {
			return err
		}
	}
	if version < 13 {
		if err = migrateDecisionAttemptBindings(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=13`); err != nil {
			return err
		}
	}
	if version < 14 {
		if err = migrateConfigAbandons(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=14`); err != nil {
			return err
		}
	}
	if version < 15 {
		if err = migrateExecutionPolicies(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=15`); err != nil {
			return err
		}
	}
	if version < 16 {
		if err = migrateDependencyInputs(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=16`); err != nil {
			return err
		}
	}
	if version < 17 {
		if err = migrateOwnerApplied(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=17`); err != nil {
			return err
		}
	}
	if version < 18 {
		if err = migrateBudgetRevisionSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=18`); err != nil {
			return err
		}
	}
	if version < 19 {
		if err = migrateDiagnosticsSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=19`); err != nil {
			return err
		}
	}
	if version < 20 {
		if err = migrateReportIntakeSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=20`); err != nil {
			return err
		}
	}
	if version < 21 {
		if err = migrateHostArtifactMetadata(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=21`); err != nil {
			return err
		}
	}
	if version < 22 {
		if err = migrateLegacyArtifactAccounting(tx, d); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=22`); err != nil {
			return err
		}
	}
	if version < 23 {
		if err = migrateACPBridgeSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=23`); err != nil {
			return err
		}
		if err = schema23MigrationStage("version"); err != nil {
			return err
		}
		if err = schema23MigrationStage("fkcheck"); err != nil {
			return err
		}
		var violations int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
			return err
		}
		if violations != 0 {
			return CodeError("schema23_foreign_key_check_failed")
		}
	}
	if needsSchema23 {
		if err = schema23MigrationStage("commit"); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func restoreRuntimeForeignKeys(ctx context.Context, conn *sql.Conn) error {
	hookErr := schema23MigrationStage("restore")
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return errors.Join(hookErr, err)
	}
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return errors.Join(hookErr, err)
	}
	if foreignKeys != 1 {
		return errors.Join(hookErr, CodeError("schema23_foreign_keys_not_restored"))
	}
	return hookErr
}
