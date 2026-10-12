package store

import (
	"database/sql"
	"fmt"
)

var ownerSuccessionSchema25Hook func(string) error

func ownerSuccessionSchema25Stage(stage string) error {
	if ownerSuccessionSchema25Hook != nil {
		return ownerSuccessionSchema25Hook(stage)
	}
	return nil
}

var ownerSuccessionTextColumns = []string{
	"authorization_id", "command_id", "request_hash", "run_id", "task_id", "launch_id",
	"predecessor_owner_id", "predecessor_scope_hash", "owner_birth", "owner_exit_proof", "owner_exited_at",
	"host_birth", "host_exit_proof", "host_exited_at", "successor_origin_context_id", "successor_origin_birth",
	"successor_host_generation", "successor_executable", "successor_executable_sha256", "created_at",
}

func ownerSuccessionReferenceBytesSQL(prefix string) string {
	return "CASE WHEN " + prefix + "succession_authorization_id IS NULL THEN 0 ELSE 8+length(CAST(" + prefix + "succession_authorization_id AS BLOB)) END"
}

func migrateOwnerSuccessionSchema(tx *sql.Tx) error {
	// No prior implementation could authenticate a successor. Do not fabricate
	// an authorization for manually changed history during migration.
	var invalid int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM execution_owners WHERE incarnation!=1 OR phase='retired' OR is_current!=1`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return CodeError("owner_succession_unproven_history")
	}
	if err := ownerSuccessionSchema25Stage("validated"); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE TABLE owner_succession_authorizations(
 authorization_id TEXT PRIMARY KEY NOT NULL CHECK(length(authorization_id)>0),
 command_id TEXT NOT NULL UNIQUE CHECK(length(command_id)>0 AND length(CAST(command_id AS BLOB))<=128),
 request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
 run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT NOT NULL REFERENCES tasks(id), work_revision INTEGER NOT NULL CHECK(work_revision>0),
 launch_id TEXT NOT NULL REFERENCES host_launches(id), predecessor_owner_id TEXT NOT NULL UNIQUE REFERENCES execution_owners(owner_id),
 predecessor_incarnation INTEGER NOT NULL CHECK(predecessor_incarnation>0), predecessor_control_revision INTEGER NOT NULL CHECK(predecessor_control_revision>0),
 predecessor_scope_hash TEXT NOT NULL CHECK(length(predecessor_scope_hash)=64),
 owner_pid INTEGER NOT NULL CHECK(owner_pid>0), owner_birth TEXT NOT NULL CHECK(length(owner_birth)>0),
 owner_exit_proof TEXT NOT NULL CHECK(owner_exit_proof='identity_exited_v1'), owner_exited_at TEXT NOT NULL CHECK(length(owner_exited_at)>0),
 host_pid INTEGER NOT NULL CHECK(host_pid>0), host_birth TEXT NOT NULL CHECK(length(host_birth)>0),
 host_exit_proof TEXT NOT NULL CHECK(host_exit_proof='identity_exited_v1'), host_exited_at TEXT NOT NULL CHECK(length(host_exited_at)>0),
 successor_origin_context_id TEXT NOT NULL CHECK(length(successor_origin_context_id)>0), successor_origin_pid INTEGER NOT NULL CHECK(successor_origin_pid>0),
 successor_origin_birth TEXT NOT NULL CHECK(length(successor_origin_birth)>0), successor_host_generation TEXT NOT NULL CHECK(length(successor_host_generation)>0),
 successor_executable TEXT NOT NULL CHECK(length(successor_executable)>0), successor_executable_sha256 TEXT NOT NULL CHECK(length(successor_executable_sha256)=64),
 coordinator_epoch INTEGER NOT NULL CHECK(coordinator_epoch>0), ready_sequence INTEGER NOT NULL CHECK(ready_sequence>0), created_at TEXT NOT NULL CHECK(length(created_at)>0),
 FOREIGN KEY(predecessor_owner_id,predecessor_control_revision) REFERENCES owner_control_changes(owner_id,revision)
);
ALTER TABLE execution_owners ADD COLUMN succession_authorization_id TEXT REFERENCES owner_succession_authorizations(authorization_id);
CREATE UNIQUE INDEX execution_owner_succession_authorization ON execution_owners(succession_authorization_id) WHERE succession_authorization_id IS NOT NULL;
CREATE INDEX owner_succession_launch ON owner_succession_authorizations(launch_id,predecessor_incarnation);
CREATE TRIGGER owner_succession_authorizations_immutable_update BEFORE UPDATE ON owner_succession_authorizations BEGIN SELECT RAISE(ABORT,'owner_succession_immutable'); END;
CREATE TRIGGER owner_succession_authorizations_immutable_delete BEFORE DELETE ON owner_succession_authorizations BEGIN SELECT RAISE(ABORT,'owner_succession_immutable'); END;
CREATE TRIGGER owner_control_history_no_delete BEFORE DELETE ON owner_control_changes BEGIN SELECT RAISE(ABORT,'owner_control_history_immutable'); END;
CREATE TRIGGER owner_control_history_immutable BEFORE UPDATE ON owner_control_changes
WHEN OLD.owner_id IS NOT NEW.owner_id OR OLD.run_id IS NOT NEW.run_id OR OLD.revision IS NOT NEW.revision
 OR OLD.request_id IS NOT NEW.request_id OR OLD.request_hash IS NOT NEW.request_hash OR OLD.expected_revision IS NOT NEW.expected_revision
 OR OLD.old_host_pid IS NOT NEW.old_host_pid OR OLD.old_host_birth IS NOT NEW.old_host_birth
 OR OLD.new_host_pid IS NOT NEW.new_host_pid OR OLD.new_host_birth IS NOT NEW.new_host_birth
 OR OLD.scope_hash IS NOT NEW.scope_hash OR OLD.owner_identity_hash IS NOT NEW.owner_identity_hash
 OR OLD.coordinator_epoch IS NOT NEW.coordinator_epoch OR OLD.coordinator_key_id IS NOT NEW.coordinator_key_id
 OR OLD.permit_id IS NOT NEW.permit_id OR OLD.permit_json IS NOT NEW.permit_json OR OLD.permit_hash IS NOT NEW.permit_hash OR OLD.created_at IS NOT NEW.created_at
BEGIN SELECT RAISE(ABORT,'owner_control_history_immutable'); END;
CREATE TRIGGER owner_succession_authorizations_predecessor BEFORE INSERT ON owner_succession_authorizations
WHEN NOT EXISTS(SELECT 1 FROM execution_owners p JOIN owner_control_changes c ON c.owner_id=p.owner_id AND c.revision=p.control_revision JOIN tasks t ON t.id=NEW.task_id JOIN task_runtime tr ON tr.task_id=t.id
 WHERE p.owner_id=NEW.predecessor_owner_id AND p.launch_id=NEW.launch_id AND p.run_id=NEW.run_id AND t.run_id=p.run_id AND tr.host_launch_id=p.launch_id AND tr.work_revision=NEW.work_revision
 AND p.phase='bound' AND p.is_current=1 AND p.incarnation=NEW.predecessor_incarnation AND p.control_revision=NEW.predecessor_control_revision
 AND c.scope_hash=NEW.predecessor_scope_hash AND c.coordinator_epoch=p.coordinator_epoch
 AND NEW.coordinator_epoch=(SELECT epoch FROM coordinator WHERE id=1) AND NEW.coordinator_epoch>=p.coordinator_epoch
 AND t.status='resume_queued' AND tr.ready_sequence=NEW.ready_sequence
 AND p.owner_pid=NEW.owner_pid AND p.owner_birth=NEW.owner_birth AND p.current_host_pid=NEW.host_pid AND p.current_host_birth=NEW.host_birth)
BEGIN SELECT RAISE(ABORT,'owner_succession_predecessor_mismatch'); END;
CREATE TRIGGER execution_owner_history_no_delete BEFORE DELETE ON execution_owners BEGIN SELECT RAISE(ABORT,'owner_history_immutable'); END;
CREATE TRIGGER execution_owner_retired_immutable BEFORE UPDATE ON execution_owners WHEN OLD.phase='retired' BEGIN SELECT RAISE(ABORT,'owner_history_immutable'); END;
CREATE TRIGGER execution_owner_identity_immutable BEFORE UPDATE ON execution_owners
WHEN OLD.owner_id IS NOT NEW.owner_id OR OLD.launch_id IS NOT NEW.launch_id OR OLD.run_id IS NOT NEW.run_id OR OLD.producer_id IS NOT NEW.producer_id
 OR OLD.incarnation IS NOT NEW.incarnation OR OLD.bootstrap_id IS NOT NEW.bootstrap_id OR OLD.bootstrap_token_hash IS NOT NEW.bootstrap_token_hash
 OR OLD.coordinator_key_id IS NOT NEW.coordinator_key_id OR OLD.request_id IS NOT NEW.request_id OR OLD.request_hash IS NOT NEW.request_hash
 OR OLD.intent_json IS NOT NEW.intent_json OR OLD.created_at IS NOT NEW.created_at
 OR (OLD.phase='bound' AND (OLD.owner_pid IS NOT NEW.owner_pid OR OLD.owner_birth IS NOT NEW.owner_birth OR OLD.owner_public_key IS NOT NEW.owner_public_key OR OLD.original_binding_json IS NOT NEW.original_binding_json OR OLD.original_binding_hash IS NOT NEW.original_binding_hash))
BEGIN SELECT RAISE(ABORT,'owner_identity_immutable'); END;
CREATE TRIGGER execution_owner_succession_reference_immutable BEFORE UPDATE OF succession_authorization_id ON execution_owners
WHEN OLD.succession_authorization_id IS NOT NEW.succession_authorization_id BEGIN SELECT RAISE(ABORT,'owner_succession_reference_immutable'); END;
CREATE TRIGGER execution_owner_succession_retirement BEFORE UPDATE OF phase,is_current ON execution_owners
WHEN (OLD.is_current=0 AND NEW.is_current!=0) OR (OLD.is_current=1 AND NEW.is_current=0 AND
 (OLD.phase!='bound' OR NEW.phase!='retired' OR NOT EXISTS(SELECT 1 FROM owner_succession_authorizations a WHERE a.predecessor_owner_id=OLD.owner_id AND a.launch_id=OLD.launch_id AND a.run_id=OLD.run_id AND a.predecessor_incarnation=OLD.incarnation AND a.predecessor_control_revision=OLD.control_revision AND a.owner_pid=OLD.owner_pid AND a.owner_birth=OLD.owner_birth AND a.host_pid=OLD.current_host_pid AND a.host_birth=OLD.current_host_birth)))
BEGIN SELECT RAISE(ABORT,'owner_succession_required'); END;
CREATE TRIGGER execution_owner_succession_insert BEFORE INSERT ON execution_owners
WHEN NEW.phase!='intended' OR NEW.is_current!=1 OR NOT EXISTS(SELECT 1 WHERE
 json_extract(NEW.intent_json,'$.scope.version')=1 AND json_extract(NEW.intent_json,'$.scope.owner_id')=NEW.owner_id
 AND json_extract(NEW.intent_json,'$.scope.run_id')=NEW.run_id AND json_extract(NEW.intent_json,'$.scope.launch_id')=NEW.launch_id
 AND json_extract(NEW.intent_json,'$.scope.producer_id')=NEW.producer_id AND json_extract(NEW.intent_json,'$.scope.incarnation')=NEW.incarnation)
 OR (NEW.incarnation=1 AND NEW.succession_authorization_id IS NOT NULL) OR (NEW.incarnation>1 AND NOT EXISTS(
 SELECT 1 FROM owner_succession_authorizations a JOIN execution_owners p ON p.owner_id=a.predecessor_owner_id
 WHERE a.authorization_id=NEW.succession_authorization_id AND a.run_id=NEW.run_id AND a.launch_id=NEW.launch_id AND p.producer_id=NEW.producer_id
 AND p.is_current=0 AND p.phase='retired' AND p.incarnation=a.predecessor_incarnation AND NEW.incarnation=p.incarnation+1
 AND NEW.phase='intended' AND NEW.is_current=1 AND NEW.coordinator_epoch>=a.coordinator_epoch
 AND NEW.incarnation=(SELECT MAX(o.incarnation)+1 FROM execution_owners o WHERE o.launch_id=NEW.launch_id)
 AND json_extract(NEW.intent_json,'$.scope.run_id')=NEW.run_id AND json_extract(NEW.intent_json,'$.scope.launch_id')=NEW.launch_id
 AND json_extract(NEW.intent_json,'$.scope.producer_id')=NEW.producer_id AND json_extract(NEW.intent_json,'$.scope.owner_id')=NEW.owner_id
 AND json_extract(NEW.intent_json,'$.scope.incarnation')=NEW.incarnation
 AND json_extract(NEW.intent_json,'$.scope.original_origin_context_id')=a.successor_origin_context_id
 AND json_extract(NEW.intent_json,'$.scope.original_origin_pid')=a.successor_origin_pid
 AND json_extract(NEW.intent_json,'$.scope.original_origin_birth')=a.successor_origin_birth
 AND json_extract(NEW.intent_json,'$.scope.host_generation')=a.successor_host_generation
 AND json_extract(NEW.intent_json,'$.scope.executable')=a.successor_executable
 AND json_extract(NEW.intent_json,'$.scope.executable_sha256')=a.successor_executable_sha256))
BEGIN SELECT RAISE(ABORT,'owner_succession_required'); END;`)
	if err != nil {
		return err
	}
	if err = ownerSuccessionSchema25Stage("objects"); err != nil {
		return err
	}
	// Preserve all prior quota triggers. Only the additive nullable reference is
	// charged here; historical NULL rows have zero added bytes and no backfill.
	for _, table := range []struct {
		name, prefix string
		cost         func(string) string
	}{
		{"owner_succession_authorizations", "quota_owner_succession", func(p string) string { return originalOwnerBytesSQL(p, ownerSuccessionTextColumns, 64) }},
		{"execution_owners", "quota_owner_succession_reference", ownerSuccessionReferenceBytesSQL},
	} {
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
				cost := table.cost(ref + ".")
				for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
					if ref == "OLD" {
						body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-(%s) WHERE scope='%s' AND scope_id=%s;", cost, scope.name, scope.id)
					} else {
						body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, cost)
					}
				}
			}
			if _, err = tx.Exec("CREATE TRIGGER " + table.prefix + "_" + op + " AFTER " + op + " ON " + table.name + " BEGIN " + body + " END"); err != nil {
				return err
			}
		}
	}
	if err = ownerSuccessionSchema25Stage("triggers"); err != nil {
		return err
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return CodeError("owner_succession_foreign_key_check_failed")
	}
	return nil
}
