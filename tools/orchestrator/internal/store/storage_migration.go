package store

import (
	"database/sql"
	"fmt"
)

func runtimeStoredBytes(column string) string {
	return "length(CAST(" + column + " AS BLOB)) + CASE WHEN json_valid(" + column + ") THEN CASE WHEN json_extract(" + column + ",'$.kind')='exited' THEN 0 ELSE COALESCE(json_extract(" + column + ",'$.artifact.size'),0) END ELSE 0 END"
}

// Triggers keep every counter in the same transaction as its source rows,
// including rollback, diagnostic compaction and future retention deletes.
func migrateStorageUsage(tx *sql.Tx) error {
	_, err := tx.Exec(`UPDATE report_events SET accounted_bytes=MAX(accounted_bytes,length(CAST(body_json AS BLOB)))+CASE WHEN json_valid(body_json) THEN COALESCE(json_extract(body_json,'$.artifact.size'),0) ELSE 0 END; UPDATE report_capabilities SET used_bytes=MAX(used_bytes, (SELECT COALESCE(SUM(accounted_bytes),0) FROM report_events WHERE capability_id=report_capabilities.capability_id))`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE TABLE storage_usage(scope TEXT NOT NULL,scope_id TEXT NOT NULL,total_bytes INTEGER NOT NULL CHECK(total_bytes>=0),progress_bytes INTEGER NOT NULL CHECK(progress_bytes>=0 AND progress_bytes<=total_bytes),PRIMARY KEY(scope,scope_id));
 INSERT INTO storage_usage VALUES('global','',0,0);
 INSERT INTO storage_usage SELECT 'run',id,0,0 FROM runs;
 INSERT INTO storage_usage SELECT 'capability',capability_id,0,0 FROM report_capabilities;`)
	if err != nil {
		return err
	}
	// Backfill through the same aggregates once, only during migration.
	for _, query := range []string{
		"SELECT 'global','',COALESCE(SUM(" + runtimeStoredBytes("body_json") + "),0),0 FROM runtime_events",
		"SELECT 'run',t.run_id,SUM(" + runtimeStoredBytes("e.body_json") + "),0 FROM runtime_events e JOIN tasks t ON t.id=e.task_id GROUP BY t.run_id",
		`SELECT 'global','',COALESCE(SUM(accounted_bytes),0),COALESCE(SUM(CASE WHEN kind='progress' THEN accounted_bytes ELSE 0 END),0) FROM report_events`,
		`SELECT 'run',c.run_id,SUM(e.accounted_bytes),SUM(CASE WHEN e.kind='progress' THEN e.accounted_bytes ELSE 0 END) FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id GROUP BY c.run_id`,
		`SELECT 'capability',capability_id,SUM(accounted_bytes),SUM(CASE WHEN kind='progress' THEN accounted_bytes ELSE 0 END) FROM report_events GROUP BY capability_id`,
	} {
		// An outer WHERE avoids SQLite's INSERT SELECT / ON parsing ambiguity.
		if _, err = tx.Exec(`INSERT INTO storage_usage SELECT * FROM (` + query + `) WHERE true ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes,progress_bytes=progress_bytes+excluded.progress_bytes`); err != nil {
			return err
		}
	}
	for _, table := range []string{"runtime_events", "report_events"} {
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
				sign := ""
				if ref == "OLD" {
					sign = "-"
				}
				total := "(" + runtimeStoredBytes(ref+".body_json") + ")"
				progress := "0"
				run := "(SELECT run_id FROM tasks WHERE id=" + ref + ".task_id)"
				scopes := []struct{ scope, id string }{{"global", "''"}, {"run", run}}
				if table == "report_events" {
					total = ref + ".accounted_bytes"
					progress = "(CASE WHEN " + ref + ".kind='progress' THEN " + total + " ELSE 0 END)"
					scopes[1].id = "(SELECT run_id FROM report_capabilities WHERE capability_id=" + ref + ".capability_id)"
					scopes = append(scopes, struct{ scope, id string }{"capability", ref + ".capability_id"})
				}
				for _, scope := range scopes {
					if ref == "OLD" {
						body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-%s,progress_bytes=progress_bytes-%s WHERE scope='%s' AND scope_id=%s;", total, progress, scope.scope, scope.id)
					} else {
						body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,%s%s,%s%s) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes,progress_bytes=progress_bytes+excluded.progress_bytes;", scope.scope, scope.id, sign, total, sign, progress)
					}
				}
			}
			when := operation
			if operation == "UPDATE" {
				if table == "runtime_events" {
					when += " OF body_json,task_id"
				} else {
					when += " OF accounted_bytes,kind,capability_id"
				}
			}
			if _, err = tx.Exec("CREATE TRIGGER quota_" + table + "_" + operation + " AFTER " + when + " ON " + table + " BEGIN " + body + " END"); err != nil {
				return err
			}
		}
	}
	return nil
}
