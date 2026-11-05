package store

import (
	"database/sql"
	"fmt"
)

func migrateReworkObjects(tx *sql.Tx) error {
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('runtime_hosts') WHERE name='owner_rework_refs_v3'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		if _, err := tx.Exec(`ALTER TABLE runtime_hosts ADD COLUMN owner_rework_refs_v3 INTEGER NOT NULL DEFAULT 0 CHECK(owner_rework_refs_v3 IN (0,1))`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS rework_objects (
 id TEXT PRIMARY KEY, owner TEXT NOT NULL,run_id TEXT NOT NULL REFERENCES runs(id),task_id TEXT NOT NULL REFERENCES tasks(id),work_revision INTEGER NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('brief','condition')),size INTEGER NOT NULL CHECK(size BETWEEN 1 AND 262144),sha256 TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('reserved','published','pinned','abandoned','deleting')),written_bytes INTEGER NOT NULL DEFAULT 0,
 device INTEGER NOT NULL DEFAULT 0,inode INTEGER NOT NULL DEFAULT 0,charged_bytes INTEGER NOT NULL CHECK(charged_bytes=size+4096));
 CREATE TABLE IF NOT EXISTS rework_uploads(id TEXT PRIMARY KEY,owner TEXT NOT NULL,task_id TEXT NOT NULL REFERENCES tasks(id),command_id TEXT NOT NULL UNIQUE,source_json TEXT NOT NULL,
 object_id TEXT NOT NULL,brief_id TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('active','committed','abandoned')));
 CREATE TABLE IF NOT EXISTS rework_pins(object_id TEXT NOT NULL REFERENCES rework_objects(id),command_id TEXT NOT NULL REFERENCES retry_decisions(command_id),PRIMARY KEY(object_id,command_id));`); err != nil {
		return err
	}
	for _, op := range []string{"INSERT", "DELETE"} {
		ref, sign := "NEW", "+"
		if op == "DELETE" {
			ref, sign = "OLD", "-"
		}
		body := ""
		for _, scope := range []struct{ k, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
			if op == "INSERT" {
				body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,%s.charged_bytes,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.k, scope.id, ref)
			} else {
				body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes%s%s.charged_bytes WHERE scope='%s' AND scope_id=%s;", sign, ref, scope.k, scope.id)
			}
		}
		if _, err := tx.Exec("CREATE TRIGGER IF NOT EXISTS quota_rework_objects_" + op + " AFTER " + op + " ON rework_objects BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	for _, op := range []string{"INSERT", "DELETE"} {
		ref := "NEW"
		if op == "DELETE" {
			ref = "OLD"
		}
		body := ""
		for _, scope := range []struct{ k, id string }{{"global", "''"}, {"run", "(SELECT run_id FROM tasks WHERE id=" + ref + ".task_id)"}} {
			if op == "INSERT" {
				body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,4096,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.k, scope.id)
			} else {
				body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-4096 WHERE scope='%s' AND scope_id=%s;", scope.k, scope.id)
			}
		}
		if _, err := tx.Exec("CREATE TRIGGER IF NOT EXISTS quota_rework_uploads_" + op + " AFTER " + op + " ON rework_uploads BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	return nil
}
