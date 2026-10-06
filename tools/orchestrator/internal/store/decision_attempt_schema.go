package store

import (
	"database/sql"
	"fmt"
)

// The fixed16 is conservative logical accounting overhead, not SQLite bytes.
func decisionBindingBytesSQL(ref string) string {
	return "16+length(CAST(" + ref + ".action_slot AS BLOB))+length(CAST(" + ref + ".task_id AS BLOB))+length(CAST(COALESCE(" + ref + ".attempt_id,'') AS BLOB))"
}

func decisionBindingBytes(slot, task, attempt string) int64 {
	return 16 + int64(len(slot)) + int64(len(task)) + int64(len(attempt))
}

func migrateDecisionAttemptBindings(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE decision_attempt_bindings(
 action_slot TEXT NOT NULL REFERENCES retry_decisions(action_slot),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 target_work_revision INTEGER NOT NULL CHECK(target_work_revision>0),
 attempt_id TEXT UNIQUE REFERENCES attempts(id),
 PRIMARY KEY(action_slot,task_id), CHECK(attempt_id IS NULL OR length(attempt_id)>0)
);
CREATE UNIQUE INDEX decision_attempt_pending_task ON decision_attempt_bindings(task_id) WHERE attempt_id IS NULL;`)
	if err != nil {
		return err
	}
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		refs := []string{"NEW"}
		if op == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		} else if op == "DELETE" {
			refs = []string{"OLD"}
		}
		body := ""
		for _, ref := range refs {
			cost := decisionBindingBytesSQL(ref)
			for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", "(SELECT run_id FROM tasks WHERE id=" + ref + ".task_id)"}} {
				if ref == "OLD" {
					body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-(%s) WHERE scope='%s' AND scope_id=%s;", cost, scope.name, scope.id)
				} else {
					body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, cost)
				}
			}
		}
		if _, err = tx.Exec("CREATE TRIGGER quota_decision_attempt_" + op + " AFTER " + op + " ON decision_attempt_bindings BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	return nil
}
