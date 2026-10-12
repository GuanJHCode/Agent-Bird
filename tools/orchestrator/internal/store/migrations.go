package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
)

const currentSchemaVersion = 41

// SchemaVersion reports the schema this binary writes, without opening state.
func SchemaVersion() int { return currentSchemaVersion }

// Runtime migrations run under the coordinator instance lock (NewServer) and
// one SQLite transaction. DDL, backfills and version publication commit together;
// rollback preserves the prior schema and data rather than guessing recovery.
func (d *DB) migrateRuntime() (err error) {
	return d.migrateRuntimeWithForeignKeyOps(runtimeForeignKeyOps{})
}

// Per-invocation dependencies let fault tests exercise failed driver operations
// without global function replacement or a simulated successful PRAGMA.
type runtimeForeignKeyOps struct {
	enable func(context.Context, *sql.Conn) error
	read   func(context.Context, *sql.Conn) (int, error)
}

func (d *DB) migrateRuntimeWithForeignKeyOps(ops runtimeForeignKeyOps) (err error) {
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
	var rebindRoot *StoreRootAuthority
	if initialVersion == 29 {
		var pendingInitial int
		if err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owner_succession_authorizations a WHERE a.initial_proof_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM execution_owners o WHERE o.succession_authorization_id=a.authorization_id))`).Scan(&pendingInitial); err != nil {
			return err
		}
		if pendingInitial != 0 {
			a, e := d.initialOwnerRootAuthority()
			if e != nil {
				return e
			}
			rebindRoot = &a
		}
	}
	needsSchema23 := initialVersion < 23
	foreignKeysDisabled := false
	var tx *sql.Tx
	committed := false
	connectionDiscarded := false
	defer func() {
		if tx != nil && !committed && !connectionDiscarded {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = errors.Join(err, rollbackErr)
			}
		}
		if foreignKeysDisabled && !connectionDiscarded {
			restoreErr := restoreRuntimeForeignKeys(ctx, conn, ops)
			err = errors.Join(err, restoreErr)
			if restoreErr != nil {
				// Never return a connection with unverified FK enforcement to the
				// pool. ErrBadConn makes database/sql discard this physical lease.
				discardErr := conn.Raw(func(any) error { return driver.ErrBadConn })
				if discardErr != nil && !errors.Is(discardErr, driver.ErrBadConn) && !errors.Is(discardErr, sql.ErrConnDone) {
					err = errors.Join(err, discardErr)
				}
			}
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
	if version < 24 {
		if err = migrateCollectionReceiptAccounting(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=24`); err != nil {
			return err
		}
		if err = collectionSchema24Stage("version"); err != nil {
			return err
		}
	}
	if version < 25 {
		if err = migrateOwnerSuccessionSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=25`); err != nil {
			return err
		}
		if err = ownerSuccessionSchema25Stage("version"); err != nil {
			return err
		}
	}
	if version < 26 {
		if err = migrateExistingOwnerResumeSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=26`); err != nil {
			return err
		}
		if err = existingOwnerResumeSchema26Stage("version"); err != nil {
			return err
		}
	}
	if version < 27 {
		if err = migrateOwnerRecoveryHandoffSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=27`); err != nil {
			return err
		}
		if err = ownerRecoverySchema27Stage("version"); err != nil {
			return err
		}
	}
	if version < 28 {
		if err = migrateOwnerBoundAbandonmentSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=28`); err != nil {
			return err
		}
		if err = ownerBoundAbandonmentSchema28Stage("version"); err != nil {
			return err
		}
	}

	if version < 29 {
		if err = migrateInitialOwnerRecoverySchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=29`); err != nil {
			return err
		}
	}
	if version < 30 {
		if err = d.migrateOwnerAuthorizationRebindSchema(tx, rebindRoot); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=30`); err != nil {
			return err
		}
	}

	if version < 31 {
		if err = migrateDiagnosticDeletionSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=31`); err != nil {
			return err
		}
	}
	if version < 32 {
		if err = migrateOwnerLaunchSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=32`); err != nil {
			return err
		}
	}

	if version < 33 {
		if err = migrateNewBaseSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=33`); err != nil {
			return err
		}
	}
	if version < 34 {
		if err = migrateValidationToolReservations(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=34`); err != nil {
			return err
		}
	}
	if version < 35 {
		if err = migrateOwnerPublicationSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=35`); err != nil {
			return err
		}
	}

	if version < 36 {
		if err = migrateGitFilterResourceReservations(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=36`); err != nil {
			return err
		}
	}

	if version < 37 {
		if err = migrateDiagnosticsGC(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=37`); err != nil {
			return err
		}
	}

	if version < 38 {
		if err = migrateGitFilterCarrierReservations(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=38`); err != nil {
			return err
		}
	}

	if version < 39 {
		if err = migrateReportRetentionSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=39`); err != nil {
			return err
		}
	}

	if version < 40 {
		if err = migrateHostRetentionSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=40`); err != nil {
			return err
		}
	}

	if version < 41 {
		if err = migrateCandidateRetentionSchema(tx); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE schema_meta SET version=41`); err != nil {
			return err
		}
	}

	if needsSchema23 {
		if err = schema23MigrationStage("commit"); err != nil {
			return err
		}
	}
	if version < 24 {
		if err = collectionSchema24Stage("commit"); err != nil {
			return err
		}
	}
	if version < 25 {
		if err = ownerSuccessionSchema25Stage("commit"); err != nil {
			return err
		}
	}
	if version < 26 {
		if err = existingOwnerResumeSchema26Stage("commit"); err != nil {
			return err
		}
	}
	if version < 27 {
		if err = ownerRecoverySchema27Stage("commit"); err != nil {
			return err
		}
	}
	if version < 28 {
		if err = ownerBoundAbandonmentSchema28Stage("commit"); err != nil {
			return err
		}
	}

	// sql.Tx is done after a failed COMMIT even when SQLite keeps the physical
	// transaction open. Reuse the audited rollback/discard path; a discarded
	// lease needs no FK restoration and must never return to the pool.
	owned := ownerTransaction{conn: conn, tx: tx}
	if err = owned.commit(); err != nil {
		connectionDiscarded = owned.state == ownerTransactionDiscarded
		return err
	}
	committed = true
	if rebindRoot != nil {
		if err = rebindRoot.Validate(); err != nil {
			return CodeError("owner_initial_recovery_uncertain")
		}
	}
	return nil
}

func restoreRuntimeForeignKeys(ctx context.Context, conn *sql.Conn, ops runtimeForeignKeyOps) error {
	hookErr := schema23MigrationStage("restore")
	if ops.enable == nil {
		ops.enable = func(ctx context.Context, conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
			return err
		}
	}
	if ops.read == nil {
		ops.read = func(ctx context.Context, conn *sql.Conn) (int, error) {
			var fk int
			err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
			return fk, err
		}
	}
	if err := ops.enable(ctx, conn); err != nil {
		return errors.Join(hookErr, err)
	}
	foreignKeys, err := ops.read(ctx, conn)
	if err != nil {
		return errors.Join(hookErr, err)
	}
	if foreignKeys != 1 {
		return errors.Join(hookErr, CodeError("schema23_foreign_keys_not_restored"))
	}
	return hookErr
}
