package store

import (
	"database/sql"
	"fmt"
)

func migrateDependencyInputs(tx *sql.Tx) error {
	_, err := tx.Exec(`
 ALTER TABLE segment_runtime ADD COLUMN dependency_inputs_json TEXT CHECK(dependency_inputs_json IS NULL OR json_valid(dependency_inputs_json));
 ALTER TABLE segment_runtime ADD COLUMN dependency_inputs_sha256 TEXT CHECK(dependency_inputs_sha256 IS NULL OR length(dependency_inputs_sha256)=64);
 CREATE TRIGGER dependency_inputs_pair_insert BEFORE INSERT ON segment_runtime WHEN (NEW.dependency_inputs_json IS NULL) != (NEW.dependency_inputs_sha256 IS NULL) BEGIN SELECT RAISE(ABORT,'dependency_inputs_pair_invalid'); END;
 CREATE TRIGGER dependency_inputs_pair_update BEFORE UPDATE OF dependency_inputs_json,dependency_inputs_sha256 ON segment_runtime WHEN (NEW.dependency_inputs_json IS NULL) != (NEW.dependency_inputs_sha256 IS NULL) OR (OLD.dependency_inputs_json IS NOT NULL AND (NEW.dependency_inputs_json IS NOT OLD.dependency_inputs_json OR NEW.dependency_inputs_sha256 IS NOT OLD.dependency_inputs_sha256)) BEGIN SELECT RAISE(ABORT,'dependency_inputs_immutable'); END;
 `)
	if err != nil {
		return err
	}
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
			size := fmt.Sprintf("(CASE WHEN %s.dependency_inputs_json IS NULL THEN 0 ELSE 16+length(CAST(%s.dependency_inputs_json AS BLOB))+length(CAST(%s.dependency_inputs_sha256 AS BLOB)) END)", ref, ref, ref)
			run := "(SELECT t.run_id FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE a.id=" + ref + ".attempt_id)"
			for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", run}} {
				if ref == "OLD" {
					body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-%s WHERE scope='%s' AND scope_id=%s;", size, scope.name, scope.id)
				} else {
					body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,%s,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, size)
				}
			}
		}
		when := op
		if op == "UPDATE" {
			when += " OF dependency_inputs_json,dependency_inputs_sha256,attempt_id"
		}
		if _, err = tx.Exec("CREATE TRIGGER quota_dependency_inputs_" + op + " AFTER " + when + " ON segment_runtime BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	return nil
}
