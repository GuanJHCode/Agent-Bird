package store

const currentSchemaVersion = 8

// Runtime migrations run under the coordinator instance lock (NewServer) and
// one SQLite transaction. DDL, backfills and version publication commit together;
// rollback preserves the prior schema and data rather than guessing recovery.
func (d *DB) migrateRuntime() error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	return tx.Commit()
}
