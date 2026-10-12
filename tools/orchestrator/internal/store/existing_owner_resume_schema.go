package store

import (
	"database/sql"
	"fmt"
)

var existingOwnerResumeSchema26Hook func(string) error

func existingOwnerResumeSchema26Stage(stage string) error {
	if existingOwnerResumeSchema26Hook != nil {
		return existingOwnerResumeSchema26Hook(stage)
	}
	return nil
}

var existingOwnerResumeTextColumns = []string{"command_id", "request_hash", "run_id", "task_id", "launch_id", "owner_id", "last_segment_id", "created_at"}

func migrateExistingOwnerResumeSchema(tx *sql.Tx) error {
	if err := existingOwnerResumeSchema26Stage("validated"); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE TABLE existing_owner_resume_commands(
 command_id TEXT PRIMARY KEY NOT NULL CHECK(length(command_id)>0 AND length(CAST(command_id AS BLOB))<=128),
 request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
 run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 launch_id TEXT NOT NULL REFERENCES host_launches(id), owner_id TEXT NOT NULL REFERENCES execution_owners(owner_id),
 last_segment_id TEXT NOT NULL, created_at TEXT NOT NULL CHECK(length(created_at)>0),
 work_revision INTEGER NOT NULL CHECK(work_revision>0), control_revision INTEGER NOT NULL CHECK(control_revision>0),
 coordinator_epoch INTEGER NOT NULL CHECK(coordinator_epoch>0), ready_sequence INTEGER NOT NULL CHECK(ready_sequence>0),
 segment_count INTEGER NOT NULL CHECK(segment_count>=0 AND ((segment_count=0 AND last_segment_id='') OR (segment_count>0 AND length(last_segment_id)>0))),
 UNIQUE(task_id,ready_sequence), FOREIGN KEY(owner_id,control_revision) REFERENCES owner_control_changes(owner_id,revision)
);
CREATE TRIGGER existing_owner_resume_immutable_update BEFORE UPDATE ON existing_owner_resume_commands BEGIN SELECT RAISE(ABORT,'owner_resume_immutable'); END;
CREATE TRIGGER existing_owner_resume_immutable_delete BEFORE DELETE ON existing_owner_resume_commands BEGIN SELECT RAISE(ABORT,'owner_resume_immutable'); END;
CREATE TRIGGER existing_owner_resume_command_unique BEFORE INSERT ON existing_owner_resume_commands
 WHEN EXISTS(SELECT 1 FROM owner_succession_authorizations WHERE command_id=NEW.command_id)
 BEGIN SELECT RAISE(ABORT,'owner_resume_command_conflict'); END;
CREATE TRIGGER owner_succession_resume_command_unique BEFORE INSERT ON owner_succession_authorizations
 WHEN EXISTS(SELECT 1 FROM existing_owner_resume_commands WHERE command_id=NEW.command_id)
 BEGIN SELECT RAISE(ABORT,'owner_resume_command_conflict'); END;
CREATE TRIGGER existing_owner_resume_current BEFORE INSERT ON existing_owner_resume_commands
WHEN NOT EXISTS(SELECT 1 FROM execution_owners p
 JOIN owner_control_changes c ON c.owner_id=p.owner_id AND c.revision=p.control_revision
 JOIN tasks t ON t.id=NEW.task_id JOIN task_runtime tr ON tr.task_id=t.id
 JOIN runs r ON r.id=t.run_id JOIN host_launches l ON l.id=tr.host_launch_id
 JOIN runtime_hosts h ON h.id=p.producer_id AND h.launch_id=l.id
 WHERE p.owner_id=NEW.owner_id AND p.phase='bound' AND p.is_current=1
 AND json_extract(p.intent_json,'$.scope.version')=1
 AND json_extract(p.intent_json,'$.scope.owner_id')=p.owner_id AND json_extract(p.intent_json,'$.scope.run_id')=p.run_id
 AND json_extract(p.intent_json,'$.scope.launch_id')=p.launch_id AND json_extract(p.intent_json,'$.scope.producer_id')=p.producer_id
 AND json_extract(p.intent_json,'$.scope.incarnation')=p.incarnation AND json_extract(p.intent_json,'$.coordinator_key_id')=p.coordinator_key_id
 AND p.run_id=NEW.run_id AND t.run_id=NEW.run_id AND p.launch_id=NEW.launch_id AND l.id=NEW.launch_id
 AND l.execution_owner_mode='supervised-v1' AND l.status!='released' AND l.host_id=p.producer_id AND h.id=l.host_id
 AND p.control_revision=NEW.control_revision AND p.applied_control_revision=p.control_revision AND c.applied_hash IS NOT NULL
 AND c.run_id=p.run_id AND c.coordinator_key_id=p.coordinator_key_id
 AND c.coordinator_epoch=p.coordinator_epoch AND c.new_host_pid=p.current_host_pid AND c.new_host_birth=p.current_host_birth
 AND h.pid=p.current_host_pid AND h.birth=p.current_host_birth AND h.coordinator_epoch=NEW.coordinator_epoch AND h.status IN ('ready','reconciling','offline')
 AND NEW.coordinator_epoch=(SELECT epoch FROM coordinator WHERE id=1) AND NEW.coordinator_epoch>=p.coordinator_epoch
 AND t.status='resume_queued' AND tr.work_revision=NEW.work_revision AND tr.ready_sequence=NEW.ready_sequence
 AND r.origin_context_id=json_extract(p.intent_json,'$.scope.original_origin_context_id')
 AND r.origin_pid=json_extract(p.intent_json,'$.scope.original_origin_pid') AND r.origin_birth=json_extract(p.intent_json,'$.scope.original_origin_birth')
 AND l.origin_context_id=r.origin_context_id AND h.origin_context_id=r.origin_context_id
 AND l.host_generation=json_extract(p.intent_json,'$.scope.host_generation') AND h.host_generation=l.host_generation
 AND l.executable=json_extract(p.intent_json,'$.scope.executable') AND h.executable=l.executable
 AND NEW.segment_count=(SELECT COUNT(*) FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id)
 AND NEW.last_segment_id=COALESCE((SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1),'')
)
BEGIN SELECT RAISE(ABORT,'owner_resume_current_mismatch'); END;`)
	if err != nil {
		return err
	}
	if err = existingOwnerResumeSchema26Stage("objects"); err != nil {
		return err
	}
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		refs := []string{"NEW"}
		if op == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		}
		if op == "DELETE" {
			refs = []string{"OLD"}
		}
		body := ""
		for _, ref := range refs {
			cost := originalOwnerBytesSQL(ref+".", existingOwnerResumeTextColumns, 40)
			for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
				if ref == "OLD" {
					body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-(%s) WHERE scope='%s' AND scope_id=%s;", cost, scope.name, scope.id)
				} else {
					body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, cost)
				}
			}
		}
		if _, err = tx.Exec("CREATE TRIGGER quota_existing_owner_resume_" + op + " AFTER " + op + " ON existing_owner_resume_commands BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	if err = existingOwnerResumeSchema26Stage("triggers"); err != nil {
		return err
	}
	var bad int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return CodeError("owner_resume_foreign_key_check_failed")
	}
	return nil
}
