package store

import (
	"database/sql"
	"fmt"
)

func migrateOwnerApplied(tx *sql.Tx) error {
	_, err := tx.Exec(`
 ALTER TABLE execution_owners ADD COLUMN applied_control_revision INTEGER NOT NULL DEFAULT 0 CHECK(applied_control_revision>=0 AND applied_control_revision<=control_revision);
 ALTER TABLE owner_control_changes ADD COLUMN applied_hash TEXT CHECK(applied_hash IS NULL OR length(applied_hash)=64);
 CREATE TRIGGER owner_applied_hash_immutable BEFORE UPDATE OF applied_hash ON owner_control_changes WHEN OLD.applied_hash IS NOT NULL AND NEW.applied_hash IS NOT OLD.applied_hash BEGIN SELECT RAISE(ABORT,'owner_applied_immutable'); END;
 UPDATE storage_usage SET total_bytes=total_bytes+8*(SELECT COUNT(*) FROM execution_owners e WHERE storage_usage.scope='global' OR (storage_usage.scope='run' AND e.run_id=storage_usage.scope_id));
 `)
	if err != nil {
		return err
	}
	// Keep v12 accounting intact. These additive triggers own only v17 bytes.
	for _, table := range []struct{ name, size string }{{"execution_owners", "8"}, {"owner_control_changes", "CASE WHEN %s.applied_hash IS NULL THEN 0 ELSE 8+length(CAST(%s.applied_hash AS BLOB)) END"}} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			refs := []string{"NEW"}
			if op == "DELETE" {
				refs = []string{"OLD"}
			}
			if op == "UPDATE" {
				refs = []string{"OLD", "NEW"}
			}
			body := ""
			for _, ref := range refs {
				size := table.size
				if table.name == "owner_control_changes" {
					size = fmt.Sprintf(size, ref, ref)
				}
				for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
					if ref == "OLD" {
						body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-(%s) WHERE scope='%s' AND scope_id=%s;", size, scope.name, scope.id)
					} else {
						body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, size)
					}
				}
			}
			if _, err = tx.Exec("CREATE TRIGGER quota_owner_applied_" + table.name + "_" + op + " AFTER " + op + " ON " + table.name + " BEGIN " + body + " END"); err != nil {
				return err
			}
		}
	}
	return nil
}
