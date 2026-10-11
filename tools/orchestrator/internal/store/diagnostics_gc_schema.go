package store

import (
	"database/sql"
	"fmt"
)

func migrateDiagnosticsGC(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE diagnostics_gc_policies(
 run_id TEXT PRIMARY KEY REFERENCES runs(id), controller TEXT NOT NULL, token_hash TEXT NOT NULL CHECK(length(token_hash)=64), binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)), revision INTEGER NOT NULL CHECK(revision>=1), cursor TEXT NOT NULL DEFAULT '' CHECK(length(CAST(cursor AS BLOB))<=256),
 plan_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(plan_json AS BLOB))<=8192), plan_revision INTEGER NOT NULL DEFAULT 0,
 last_tick TEXT NOT NULL DEFAULT '' CHECK(length(last_tick)<=32), stopped TEXT NOT NULL DEFAULT '' CHECK(length(stopped)<=64));
 CREATE TABLE diagnostics_consumptions(
 attempt_id TEXT NOT NULL REFERENCES attempts(id),run_id TEXT NOT NULL REFERENCES runs(id),task_id TEXT NOT NULL REFERENCES tasks(id),
 work_revision INTEGER NOT NULL CHECK(work_revision>0),event_id TEXT NOT NULL,event_revision INTEGER NOT NULL CHECK(event_revision>0),event_hash TEXT NOT NULL CHECK(length(event_hash)=64), consumed_at TEXT NOT NULL CHECK(length(consumed_at)<=32), PRIMARY KEY(attempt_id,event_id));
 CREATE TRIGGER diagnostics_consumption_completed AFTER UPDATE OF status ON tasks WHEN NEW.status='completed' AND OLD.status!='completed' BEGIN
 UPDATE diagnostics_consumptions SET consumed_at=rtrim(rtrim(strftime('%Y-%m-%dT%H:%M:%f','now'),'0'),'.')||'Z' WHERE task_id=NEW.id AND consumed_at='' AND work_revision=(SELECT work_revision FROM task_runtime WHERE task_id=NEW.id) AND attempt_id=(SELECT id FROM attempts WHERE task_id=NEW.id ORDER BY attempt_no DESC LIMIT 1) AND EXISTS(SELECT 1 FROM attempts a WHERE a.id=attempt_id AND a.status='result_ready') AND EXISTS(SELECT 1 FROM (SELECT event_id,body_json FROM runtime_events UNION ALL SELECT event_id,body_json FROM report_events) e WHERE e.event_id=diagnostics_consumptions.event_id AND json_extract(e.body_json,'$.segment_id')=(SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=NEW.id ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1)); END;
 CREATE TRIGGER diagnostics_consumption_binding BEFORE UPDATE OF attempt_id,run_id,task_id,work_revision,event_id,event_revision,event_hash ON diagnostics_consumptions BEGIN SELECT RAISE(ABORT,'diagnostics_consumption_immutable'); END;
 CREATE TRIGGER diagnostics_consumption_retained BEFORE DELETE ON diagnostics_consumptions BEGIN SELECT RAISE(ABORT,'diagnostics_consumption_immutable'); END;`)
	if err != nil {
		return err
	}
	for _, table := range []struct {
		name    string
		columns []string
	}{{"diagnostics_gc_policies", []string{"run_id", "controller", "token_hash", "binding_hash", "plan_json"}}, {"diagnostics_consumptions", []string{"attempt_id", "run_id", "task_id", "event_id", "event_hash"}}} {
		cost := func(ref string) string {
			s := "160"
			if table.name == "diagnostics_gc_policies" {
				s = "512"
			}
			for _, c := range table.columns {
				s += "+length(CAST(" + ref + "." + c + " AS BLOB))"
			}
			return s
		}
		for _, op := range []string{"INSERT", "UPDATE"} {
			delta := cost("NEW")
			if op == "UPDATE" {
				delta = "(" + delta + ")-(" + cost("OLD") + ")"
			}
			_, err = tx.Exec(fmt.Sprintf(`CREATE TRIGGER quota_%s_%s AFTER %s ON %s BEGIN INSERT INTO storage_usage SELECT 'global','',0,0 WHERE NOT EXISTS(SELECT 1 FROM storage_usage WHERE scope='global' AND scope_id='');INSERT INTO storage_usage SELECT 'run',NEW.run_id,0,0 WHERE NOT EXISTS(SELECT 1 FROM storage_usage WHERE scope='run' AND scope_id=NEW.run_id);UPDATE storage_usage SET total_bytes=total_bytes+(%s) WHERE scope='global' AND scope_id='' OR scope='run' AND scope_id=NEW.run_id;END;`, table.name, op, op, table.name, delta))
			if err != nil {
				return err
			}
		}
	}
	return nil
}
