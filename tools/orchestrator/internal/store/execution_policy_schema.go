package store

import (
	"database/sql"
	"fmt"
)

func policyStoredBytes(ref string) string {
	return "16+length(CAST(" + ref + ".task_id AS BLOB))+length(CAST(" + ref + ".run_id AS BLOB))+length(CAST(" + ref + ".body_json AS BLOB))+length(CAST(" + ref + ".policy_sha256 AS BLOB))"
}
func migrateExecutionPolicies(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE task_execution_policies(task_id TEXT PRIMARY KEY REFERENCES tasks(id),run_id TEXT NOT NULL REFERENCES runs(id),body_json TEXT NOT NULL,policy_sha256 TEXT NOT NULL); CREATE TRIGGER execution_policy_immutable BEFORE UPDATE ON task_execution_policies BEGIN SELECT RAISE(ABORT,'execution_policy_immutable'); END; ALTER TABLE retry_decisions ADD COLUMN origin TEXT NOT NULL DEFAULT 'owner'; ALTER TABLE retry_decisions ADD COLUMN policy_sha256 TEXT NOT NULL DEFAULT ''; ALTER TABLE retry_decisions ADD COLUMN eligible_at_unix_ms INTEGER NOT NULL DEFAULT 0; ALTER TABLE retry_decisions ADD COLUMN backoff_index INTEGER NOT NULL DEFAULT 0;`)
	if err != nil {
		return err
	}
	for _, op := range []string{"INSERT", "DELETE"} {
		ref, sign := "NEW", "+"
		if op == "DELETE" {
			ref, sign = "OLD", "-"
		}
		cost := policyStoredBytes(ref)
		body := ""
		for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
			body += fmt.Sprintf("INSERT OR IGNORE INTO storage_usage VALUES('%s',%s,0,0); UPDATE storage_usage SET total_bytes=total_bytes%s(%s) WHERE scope='%s' AND scope_id=%s;", scope.name, scope.id, sign, cost, scope.name, scope.id)
		}
		if _, err = tx.Exec("CREATE TRIGGER quota_execution_policy_" + op + " AFTER " + op + " ON task_execution_policies BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	for _, op := range []string{"INSERT", "DELETE"} {
		ref, sign := "NEW", "+"
		if op == "DELETE" {
			ref, sign = "OLD", "-"
		}
		cost := "24+length(CAST(" + ref + ".origin AS BLOB))+length(CAST(" + ref + ".command_id AS BLOB))+length(CAST(" + ref + ".policy_sha256 AS BLOB))"
		body := ""
		for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", "(SELECT run_id FROM tasks WHERE id=" + ref + ".task_id)"}} {
			body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes%s(%s) WHERE scope='%s' AND scope_id=%s;", sign, cost, scope.name, scope.id)
		}
		if _, err = tx.Exec("CREATE TRIGGER quota_policy_retry_" + op + " AFTER " + op + " ON retry_decisions WHEN " + ref + ".origin='policy' BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	return nil
}
