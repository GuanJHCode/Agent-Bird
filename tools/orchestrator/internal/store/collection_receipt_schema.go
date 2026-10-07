package store

import (
	"database/sql"
	"strings"
)

var collectionSchema24Hook func(string) error

func collectionSchema24Stage(stage string) error {
	if collectionSchema24Hook != nil {
		return collectionSchema24Hook(stage)
	}
	return nil
}

func collectionReceiptByteExpr(prefix string) string {
	parts := make([]string, 0, 4)
	for _, col := range []string{"id", "task_id", "proof_hash", "bindings_json"} {
		parts = append(parts, "length(CAST("+prefix+col+" AS BLOB))")
	}
	return "(" + strings.Join(parts, "+") + ")"
}

func migrateCollectionReceiptAccounting(tx *sql.Tx) error {
	var invalid int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM collection_receipts c LEFT JOIN tasks t ON t.id=c.task_id LEFT JOIN runs r ON r.id=t.run_id WHERE c.id IS NULL OR t.id IS NULL OR r.id IS NULL`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return CodeError("collection_receipt_invalid_history")
	}
	if err := collectionSchema24Stage("validated"); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE collection_receipts_schema24(id TEXT PRIMARY KEY NOT NULL,task_id TEXT NOT NULL REFERENCES tasks(id),proof_hash TEXT NOT NULL,bindings_json TEXT NOT NULL); INSERT INTO collection_receipts_schema24(id,task_id,proof_hash,bindings_json) SELECT id,task_id,proof_hash,bindings_json FROM collection_receipts`); err != nil {
		return err
	}
	if err := collectionSchema24Stage("copied"); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE collection_receipts; ALTER TABLE collection_receipts_schema24 RENAME TO collection_receipts`); err != nil {
		return err
	}
	if err := collectionSchema24Stage("replaced"); err != nil {
		return err
	}
	for _, query := range []string{
		`SELECT 'global','',COALESCE(SUM(` + collectionReceiptByteExpr("") + `),0),0 FROM collection_receipts`,
		`SELECT 'run',t.run_id,SUM(` + collectionReceiptByteExpr("c.") + `),0 FROM collection_receipts c JOIN tasks t ON t.id=c.task_id GROUP BY t.run_id`,
	} {
		if _, err := tx.Exec(`INSERT INTO storage_usage SELECT * FROM (` + query + `) WHERE true ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes`); err != nil {
			return err
		}
	}
	if err := collectionSchema24Stage("backfilled"); err != nil {
		return err
	}
	newCost, oldCost := collectionReceiptByteExpr("NEW."), collectionReceiptByteExpr("OLD.")
	_, err := tx.Exec(`CREATE TRIGGER quota_collection_receipts_INSERT AFTER INSERT ON collection_receipts BEGIN
 INSERT INTO storage_usage VALUES('global','',` + newCost + `,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;
 INSERT INTO storage_usage VALUES('run',(SELECT run_id FROM tasks WHERE id=NEW.task_id),` + newCost + `,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;
 END;
 CREATE TRIGGER quota_collection_receipts_DELETE AFTER DELETE ON collection_receipts BEGIN
 UPDATE storage_usage SET total_bytes=total_bytes-` + oldCost + ` WHERE scope='global' AND scope_id='';
 UPDATE storage_usage SET total_bytes=total_bytes-` + oldCost + ` WHERE scope='run' AND scope_id=(SELECT run_id FROM tasks WHERE id=OLD.task_id);
 END;
 CREATE TRIGGER collection_receipts_immutable BEFORE UPDATE ON collection_receipts BEGIN SELECT RAISE(ABORT,'collection_receipt_immutable'); END;`)
	if err != nil {
		return err
	}
	if err := collectionSchema24Stage("triggers"); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return CodeError("collection_receipt_foreign_key_check_failed")
	}
	return nil
}
