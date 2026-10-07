package store

import (
	"database/sql"
	"fmt"
)

// migrateBudgetRevisionSchema installs an append-only audit trail. Cap vectors
// are stored as canonical JSON so baseline policy records remain immutable.
func migrateBudgetRevisionSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE budget_revisions(
 run_id TEXT NOT NULL REFERENCES runs(id), budget_group_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>=1), command_id TEXT NOT NULL,
 request_json TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(run_id,budget_group_id,revision), UNIQUE(run_id,command_id));
CREATE TABLE budget_revision_receipts(
 run_id TEXT NOT NULL REFERENCES runs(id), command_id TEXT NOT NULL,
 request_json TEXT NOT NULL, receipt_json TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(run_id,command_id));
CREATE TRIGGER budget_revisions_immutable BEFORE UPDATE ON budget_revisions BEGIN SELECT RAISE(ABORT,'budget_revision_immutable'); END;
CREATE TRIGGER budget_revisions_append_only BEFORE DELETE ON budget_revisions BEGIN SELECT RAISE(ABORT,'budget_revision_immutable'); END;
CREATE TRIGGER budget_revision_receipts_immutable BEFORE UPDATE ON budget_revision_receipts BEGIN SELECT RAISE(ABORT,'budget_revision_receipt_immutable'); END;
CREATE TRIGGER budget_revision_receipts_append_only BEFORE DELETE ON budget_revision_receipts BEGIN SELECT RAISE(ABORT,'budget_revision_receipt_immutable'); END;`)
	if err != nil {
		return err
	}
	// Ledger rows are control-plane data.  Charge both the immutable command and
	// the immutable receipt in the same transaction that admits the command.
	for _, table := range []string{"budget_revisions", "budget_revision_receipts"} {
		for _, op := range []string{"INSERT", "DELETE"} {
			ref, sign := "NEW", "+"
			if op == "DELETE" {
				ref, sign = "OLD", "-"
			}
			cost := "24+length(CAST(" + ref + ".run_id AS BLOB))+length(CAST(" + ref + ".command_id AS BLOB))+length(CAST(" + ref + ".request_json AS BLOB))"
			if table == "budget_revisions" {
				cost += "+8+length(CAST(" + ref + ".budget_group_id AS BLOB))"
			} else {
				cost += "+length(CAST(" + ref + ".receipt_json AS BLOB))"
			}
			body := ""
			for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
				body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,0,0) ON CONFLICT(scope,scope_id) DO NOTHING; UPDATE storage_usage SET total_bytes=total_bytes%s(%s) WHERE scope='%s' AND scope_id=%s;", scope.name, scope.id, sign, cost, scope.name, scope.id)
			}
			if _, err = tx.Exec("CREATE TRIGGER quota_" + table + "_" + op + " AFTER " + op + " ON " + table + " BEGIN " + body + " END"); err != nil {
				return err
			}
		}
	}
	return nil
}
