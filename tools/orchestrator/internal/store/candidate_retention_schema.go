package store

import (
	"database/sql"
	"fmt"
)

func migrateCandidateRetentionSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE candidate_resources(
 id TEXT PRIMARY KEY CHECK(length(id)=64),run_id TEXT NOT NULL REFERENCES runs(id),task_id TEXT NOT NULL REFERENCES tasks(id),attempt_id TEXT NOT NULL REFERENCES attempts(id),segment_id TEXT NOT NULL REFERENCES segments(id),host_id TEXT NOT NULL REFERENCES runtime_hosts(id),command_id TEXT NOT NULL,
 source_event_id TEXT NOT NULL UNIQUE REFERENCES runtime_events(event_id),header_json TEXT NOT NULL CHECK(length(CAST(header_json AS BLOB)) BETWEEN 1 AND 8192),receipt_sha256 TEXT NOT NULL CHECK(length(receipt_sha256)=64),projection_sha256 TEXT NOT NULL CHECK(length(projection_sha256)=64),created_at TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('available','prepared','running','pending','removed')),state_revision INTEGER NOT NULL CHECK(state_revision>=1),generation INTEGER NOT NULL CHECK(generation>=0),UNIQUE(host_id,command_id));
CREATE TABLE candidate_run_resolutions(
 run_id TEXT NOT NULL REFERENCES runs(id),binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),controller TEXT NOT NULL,token_hash TEXT NOT NULL CHECK(length(token_hash)=64),request_id TEXT NOT NULL,reason TEXT NOT NULL CHECK(length(CAST(reason AS BLOB)) BETWEEN 1 AND 512),resolution TEXT NOT NULL CHECK(resolution IN ('resolved','abandoned')),created_at TEXT NOT NULL,PRIMARY KEY(run_id,binding_hash),UNIQUE(run_id,request_id));
CREATE TABLE candidate_run_retirements(
 run_id TEXT PRIMARY KEY REFERENCES runs(id),binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),request_id TEXT NOT NULL,facts_sha256 TEXT NOT NULL CHECK(length(facts_sha256)=64),created_at TEXT NOT NULL);
CREATE TABLE candidate_maintenance_operations(
 operation_id TEXT PRIMARY KEY CHECK(length(operation_id)=64),resource_id TEXT NOT NULL REFERENCES candidate_resources(id),run_id TEXT NOT NULL REFERENCES runs(id),generation INTEGER NOT NULL CHECK(generation>=1),binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),request_id TEXT NOT NULL,grant_json TEXT NOT NULL CHECK(length(CAST(grant_json AS BLOB)) BETWEEN 1 AND 24576),grant_sha256 TEXT NOT NULL CHECK(length(grant_sha256)=64),created_at TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('prepared','running','pending','removed')),child_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(child_json AS BLOB))<=4096),outcome_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(outcome_json AS BLOB))<=8192),UNIQUE(resource_id,generation),UNIQUE(run_id,request_id));
CREATE TABLE candidate_gc_policies(
 run_id TEXT PRIMARY KEY REFERENCES runs(id),controller TEXT NOT NULL,token_hash TEXT NOT NULL CHECK(length(token_hash)=64),binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),revision INTEGER NOT NULL CHECK(revision>=1),cursor TEXT NOT NULL DEFAULT '' CHECK(length(cursor)<=64),last_tick TEXT NOT NULL DEFAULT '' CHECK(length(last_tick)<=32),stopped TEXT NOT NULL DEFAULT '' CHECK(length(stopped)<=64));
CREATE TRIGGER candidate_resource_identity BEFORE UPDATE OF id,run_id,task_id,attempt_id,segment_id,host_id,command_id,source_event_id,header_json,receipt_sha256,projection_sha256,created_at ON candidate_resources BEGIN SELECT RAISE(ABORT,'candidate_resource_immutable');END;
CREATE TRIGGER candidate_resource_initial BEFORE INSERT ON candidate_resources WHEN NEW.state!='available' OR NEW.generation!=0 OR NEW.state_revision!=1 OR EXISTS(SELECT 1 FROM candidate_run_retirements WHERE run_id=NEW.run_id) BEGIN SELECT RAISE(ABORT,'candidate_resource_retired');END;
CREATE TRIGGER candidate_operation_identity BEFORE UPDATE OF operation_id,resource_id,run_id,generation,binding_hash,request_id,grant_json,grant_sha256,created_at ON candidate_maintenance_operations BEGIN SELECT RAISE(ABORT,'candidate_operation_immutable');END;
CREATE TRIGGER candidate_operation_initial BEFORE INSERT ON candidate_maintenance_operations WHEN NEW.state!='prepared' OR NEW.child_json!='' OR NEW.outcome_json!='' OR NOT EXISTS(SELECT 1 FROM candidate_resources c JOIN candidate_run_retirements r ON r.run_id=c.run_id WHERE c.id=NEW.resource_id AND c.run_id=NEW.run_id AND c.state IN ('available','pending') AND NEW.generation=c.generation+1) BEGIN SELECT RAISE(ABORT,'candidate_operation_invalid');END;
CREATE TRIGGER candidate_resource_transition BEFORE UPDATE OF state,state_revision,generation ON candidate_resources WHEN NOT(
 NEW.state_revision=OLD.state_revision+1 AND (
 OLD.state IN ('available','pending') AND NEW.state='prepared' AND NEW.generation=OLD.generation+1 AND EXISTS(SELECT 1 FROM candidate_maintenance_operations WHERE resource_id=OLD.id AND generation=NEW.generation AND state='prepared')
 OR OLD.state='prepared' AND NEW.state IN ('running','pending') AND NEW.generation=OLD.generation
 OR OLD.state='running' AND NEW.state IN ('pending','removed') AND NEW.generation=OLD.generation)) BEGIN SELECT RAISE(ABORT,'candidate_resource_transition');END;
CREATE TRIGGER candidate_operation_transition BEFORE UPDATE OF state,child_json,outcome_json ON candidate_maintenance_operations WHEN NOT(
 OLD.state='prepared' AND NEW.state='running' AND OLD.child_json='' AND NEW.child_json!='' AND NEW.outcome_json=''
 OR OLD.state='prepared' AND NEW.state='pending' AND NEW.child_json='' AND NEW.outcome_json!=''
 OR OLD.state='running' AND NEW.state IN ('pending','removed') AND NEW.child_json=OLD.child_json AND NEW.outcome_json!='') BEGIN SELECT RAISE(ABORT,'candidate_operation_transition');END;
`)
	if err != nil {
		return err
	}
	for _, table := range []string{"candidate_resources", "candidate_run_resolutions", "candidate_run_retirements", "candidate_maintenance_operations"} {
		if _, err = tx.Exec("CREATE TRIGGER retained_" + table + " BEFORE DELETE ON " + table + " BEGIN SELECT RAISE(ABORT,'candidate_retention_immutable');END;"); err != nil {
			return err
		}
		if table == "candidate_run_resolutions" || table == "candidate_run_retirements" {
			if _, err = tx.Exec("CREATE TRIGGER immutable_" + table + " BEFORE UPDATE ON " + table + " BEGIN SELECT RAISE(ABORT,'candidate_retention_immutable');END;"); err != nil {
				return err
			}
		}
	}
	fences := []struct{ name, table, operation, run string }{
		{"attempt", "attempts", "INSERT", "(SELECT run_id FROM tasks WHERE id=NEW.task_id)"},
		{"segment", "segments", "INSERT", "(SELECT t.run_id FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE a.id=NEW.attempt_id)"},
		{"task", "tasks", "INSERT", "NEW.run_id"},
		{"task_update", "tasks", "UPDATE OF dependencies_json,status", "NEW.run_id"},
		{"task_runtime", "task_runtime", "UPDATE OF work_revision,adapter_payload", "(SELECT run_id FROM tasks WHERE id=NEW.task_id)"},
		{"continuation", "continuation_decisions", "INSERT", "NEW.run_id"},
		{"retry", "retry_decisions", "INSERT", "(SELECT run_id FROM tasks WHERE id=NEW.task_id)"},
		{"new_base", "new_base_lineage", "INSERT", "NEW.source_run_id"},
	}
	for _, f := range fences {
		if _, err = tx.Exec("CREATE TRIGGER candidate_retention_" + f.name + "_fence BEFORE " + f.operation + " ON " + f.table + " WHEN EXISTS(WITH RECURSIVE sources(run_id) AS (SELECT " + f.run + " UNION SELECT l.source_run_id FROM new_base_lineage l JOIN sources s ON l.run_id=s.run_id) SELECT 1 FROM candidate_run_retirements r JOIN sources s ON s.run_id=r.run_id) BEGIN SELECT RAISE(ABORT,'candidate_resource_retired');END;"); err != nil {
			return err
		}
	}
	// Header and operation history stay charged after collection. The operation
	// prepays bounded child/outcome metadata, so final accounting cannot fail
	// after the filesystem effect merely because this row grew.
	for _, q := range []struct{ table, cost string }{
		{"candidate_resources", "4096+length(CAST(NEW.header_json AS BLOB))"},
		{"candidate_run_resolutions", "2048+length(CAST(NEW.reason AS BLOB))"},
		{"candidate_run_retirements", "2048"},
		{"candidate_maintenance_operations", "16384+length(CAST(NEW.grant_json AS BLOB))"},
		{"candidate_gc_policies", "4096"},
	} {
		if _, err = tx.Exec(fmt.Sprintf(`CREATE TRIGGER quota_%s_INSERT AFTER INSERT ON %s BEGIN INSERT INTO storage_usage SELECT 'global','',0,0 WHERE NOT EXISTS(SELECT 1 FROM storage_usage WHERE scope='global' AND scope_id='');INSERT INTO storage_usage SELECT 'run',NEW.run_id,0,0 WHERE NOT EXISTS(SELECT 1 FROM storage_usage WHERE scope='run' AND scope_id=NEW.run_id);UPDATE storage_usage SET total_bytes=total_bytes+(%s) WHERE scope='global' AND scope_id='' OR scope='run' AND scope_id=NEW.run_id;END;`, q.table, q.table, q.cost)); err != nil {
			return err
		}
	}
	return nil
}
