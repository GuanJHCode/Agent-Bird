package store

import (
	"database/sql"
	"fmt"
)

func migrateReworkLedger(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE retry_decisions ADD COLUMN revision_payload TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
		body := ""
		refs := []string{"NEW"}
		if operation == "DELETE" {
			refs = []string{"OLD"}
		}
		if operation == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		}
		for _, ref := range refs {
			bytes := "length(CAST(" + ref + ".revision_payload AS BLOB))"
			for _, scope := range []struct{ kind, id string }{{"global", "''"}, {"run", "(SELECT run_id FROM tasks WHERE id=" + ref + ".task_id)"}} {
				if ref == "OLD" {
					body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-%s WHERE scope='%s' AND scope_id=%s;", bytes, scope.kind, scope.id)
				} else {
					body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,%s,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.kind, scope.id, bytes)
				}
			}
		}
		when := operation
		if operation == "UPDATE" {
			when += " OF revision_payload,task_id"
		}
		if _, err := tx.Exec("CREATE TRIGGER quota_rework_" + operation + " AFTER " + when + " ON retry_decisions BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	return nil
}
