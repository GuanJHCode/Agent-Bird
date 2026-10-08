package store

import (
	"database/sql"
	"fmt"
	"strings"
)

var ownerBoundAbandonmentSchema28Hook func(string) error

func ownerBoundAbandonmentSchema28Stage(stage string) error {
	if ownerBoundAbandonmentSchema28Hook != nil {
		return ownerBoundAbandonmentSchema28Hook(stage)
	}
	return nil
}

var ownerBoundAbandonmentTextColumns = []string{"owner_id", "inbound_authorization_id", "run_id", "task_id", "launch_id", "producer_id", "binding_hash", "scope_hash", "permit_hash", "owner_birth", "owner_exit_proof", "owner_exited_at", "host_birth", "host_exit_proof", "host_exited_at", "origin_context_id", "origin_birth", "host_generation", "executable", "executable_sha256", "last_segment_id", "created_at"}

// The only substitute for an Applied predecessor is its immutable, exact death
// proof plus the completed adjacent filesystem handoff. Ordinary owners keep
// the existing Applied requirement.
const abandonedPredecessorSQL = `EXISTS(SELECT 1 FROM owner_bound_abandonments d JOIN owner_recovery_scope_handoffs h ON h.owner_id=d.owner_id
 WHERE d.owner_id=p.owner_id AND a.abandoned_owner_id=d.owner_id AND h.applied=1
 AND d.inbound_authorization_id=p.succession_authorization_id AND d.binding_hash=p.original_binding_hash
 AND d.control_revision=p.control_revision AND p.applied_control_revision=0
 AND d.run_id=a.run_id AND d.task_id=a.task_id AND d.launch_id=a.launch_id
 AND d.work_revision=a.work_revision AND d.ready_sequence=a.ready_sequence
 AND d.owner_pid=p.owner_pid AND d.owner_birth=p.owner_birth AND d.host_pid=p.current_host_pid AND d.host_birth=p.current_host_birth
 AND NOT EXISTS(SELECT 1 FROM owner_control_changes c WHERE c.owner_id=p.owner_id AND c.applied_hash IS NOT NULL))`

func migrateOwnerBoundAbandonmentSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE owner_bound_abandonments(
 owner_id TEXT PRIMARY KEY NOT NULL REFERENCES owner_recovery_scope_handoffs(owner_id),
 inbound_authorization_id TEXT NOT NULL UNIQUE REFERENCES owner_succession_authorizations(authorization_id),
 run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 launch_id TEXT NOT NULL REFERENCES host_launches(id), producer_id TEXT NOT NULL REFERENCES runtime_hosts(id),
 binding_hash TEXT NOT NULL, scope_hash TEXT NOT NULL, permit_hash TEXT NOT NULL,
 owner_birth TEXT NOT NULL, owner_exit_proof TEXT NOT NULL CHECK(owner_exit_proof='identity_exited_v1'), owner_exited_at TEXT NOT NULL CHECK(length(owner_exited_at)>0),
 host_birth TEXT NOT NULL, host_exit_proof TEXT NOT NULL CHECK(host_exit_proof='identity_exited_v1'), host_exited_at TEXT NOT NULL CHECK(length(host_exited_at)>0),
 origin_context_id TEXT NOT NULL, origin_birth TEXT NOT NULL, host_generation TEXT NOT NULL, executable TEXT NOT NULL, executable_sha256 TEXT NOT NULL,
 last_segment_id TEXT NOT NULL, created_at TEXT NOT NULL CHECK(length(created_at)>0),
 control_revision INTEGER NOT NULL CHECK(control_revision>0), owner_pid INTEGER NOT NULL CHECK(owner_pid>1), host_pid INTEGER NOT NULL CHECK(host_pid>1), origin_pid INTEGER NOT NULL CHECK(origin_pid>1),
 coordinator_epoch INTEGER NOT NULL CHECK(coordinator_epoch>0), work_revision INTEGER NOT NULL CHECK(work_revision>0), ready_sequence INTEGER NOT NULL CHECK(ready_sequence>0), segment_count INTEGER NOT NULL CHECK(segment_count>=0),
 FOREIGN KEY(owner_id,control_revision) REFERENCES owner_control_changes(owner_id,revision));
 ALTER TABLE owner_succession_authorizations ADD COLUMN abandoned_owner_id TEXT REFERENCES owner_bound_abandonments(owner_id);
 CREATE UNIQUE INDEX owner_succession_abandonment ON owner_succession_authorizations(abandoned_owner_id) WHERE abandoned_owner_id IS NOT NULL;
 CREATE TRIGGER owner_bound_abandonment_current BEFORE INSERT ON owner_bound_abandonments
 WHEN NOT EXISTS(SELECT 1 FROM execution_owners n
 JOIN owner_succession_authorizations a ON a.authorization_id=n.succession_authorization_id
 JOIN owner_recovery_scope_handoffs h ON h.owner_id=n.owner_id
 JOIN owner_control_changes c ON c.owner_id=n.owner_id AND c.revision=n.control_revision
 JOIN runs r ON r.id=n.run_id JOIN host_launches l ON l.id=n.launch_id
 JOIN tasks t ON t.id=a.task_id JOIN task_runtime tr ON tr.task_id=t.id
 WHERE n.owner_id=NEW.owner_id AND n.phase='bound' AND n.is_current=1 AND n.applied_control_revision=0
 AND NOT EXISTS(SELECT 1 FROM owner_control_changes x WHERE x.owner_id=n.owner_id AND x.applied_hash IS NOT NULL)
 AND (n.control_revision=1 OR h.applied=1) AND n.control_revision=NEW.control_revision
 AND a.authorization_id=NEW.inbound_authorization_id AND n.run_id=NEW.run_id AND a.task_id=NEW.task_id
 AND n.launch_id=NEW.launch_id AND n.producer_id=NEW.producer_id AND n.original_binding_hash=NEW.binding_hash
 AND h.scope_hash=NEW.scope_hash AND h.binding_hash=n.original_binding_hash AND h.authorization_id=a.authorization_id
 AND c.permit_hash=NEW.permit_hash AND c.coordinator_epoch=n.coordinator_epoch
 AND n.owner_pid=NEW.owner_pid AND n.owner_birth=NEW.owner_birth AND n.current_host_pid=NEW.host_pid AND n.current_host_birth=NEW.host_birth
 AND r.origin_context_id=NEW.origin_context_id AND r.origin_pid=NEW.origin_pid AND r.origin_birth=NEW.origin_birth
 AND l.host_generation=NEW.host_generation AND l.executable=NEW.executable AND a.successor_executable_sha256=NEW.executable_sha256
 AND NEW.coordinator_epoch=(SELECT epoch FROM coordinator WHERE id=1) AND NEW.coordinator_epoch>=n.coordinator_epoch
 AND t.run_id=n.run_id AND t.status='resume_queued' AND tr.host_launch_id=n.launch_id
 AND tr.work_revision=a.work_revision AND tr.work_revision=NEW.work_revision AND tr.ready_sequence=a.ready_sequence AND tr.ready_sequence=NEW.ready_sequence
 AND NEW.segment_count=(SELECT COUNT(*) FROM segments s JOIN attempts x ON x.id=s.attempt_id WHERE x.task_id=a.task_id)
 AND NEW.last_segment_id=COALESCE((SELECT s.id FROM segments s JOIN attempts x ON x.id=s.attempt_id WHERE x.task_id=a.task_id ORDER BY x.attempt_no DESC,s.segment_no DESC LIMIT 1),''))
 BEGIN SELECT RAISE(ABORT,'owner_bound_abandonment_mismatch'); END;
 CREATE TRIGGER owner_bound_abandonment_immutable_update BEFORE UPDATE ON owner_bound_abandonments BEGIN SELECT RAISE(ABORT,'owner_bound_abandonment_immutable'); END;
 CREATE TRIGGER owner_bound_abandonment_immutable_delete BEFORE DELETE ON owner_bound_abandonments BEGIN SELECT RAISE(ABORT,'owner_bound_abandonment_immutable'); END;
 CREATE TRIGGER owner_bound_abandonment_control_update BEFORE UPDATE ON owner_control_changes WHEN EXISTS(SELECT 1 FROM owner_bound_abandonments WHERE owner_id=OLD.owner_id) BEGIN SELECT RAISE(ABORT,'owner_bound_abandonment_sealed'); END;
 CREATE TRIGGER owner_bound_abandonment_control_insert BEFORE INSERT ON owner_control_changes WHEN EXISTS(SELECT 1 FROM owner_bound_abandonments WHERE owner_id=NEW.owner_id) BEGIN SELECT RAISE(ABORT,'owner_bound_abandonment_sealed'); END;
 CREATE TRIGGER owner_bound_abandonment_owner_update BEFORE UPDATE ON execution_owners
 WHEN EXISTS(SELECT 1 FROM owner_bound_abandonments WHERE owner_id=OLD.owner_id) AND
 (NEW.control_revision IS NOT OLD.control_revision OR NEW.applied_control_revision IS NOT OLD.applied_control_revision OR NEW.coordinator_epoch IS NOT OLD.coordinator_epoch OR NEW.current_host_pid IS NOT OLD.current_host_pid OR NEW.current_host_birth IS NOT OLD.current_host_birth)
 BEGIN SELECT RAISE(ABORT,'owner_bound_abandonment_sealed'); END;
 CREATE TRIGGER owner_bound_abandonment_authorization BEFORE INSERT ON owner_succession_authorizations
 WHEN NEW.abandoned_owner_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM execution_owners p JOIN owner_bound_abandonments d ON d.owner_id=p.owner_id JOIN owner_recovery_scope_handoffs h ON h.owner_id=d.owner_id
 WHERE p.owner_id=NEW.predecessor_owner_id AND d.owner_id=NEW.abandoned_owner_id AND p.phase='bound' AND p.is_current=1 AND h.applied=1
 AND p.applied_control_revision=0 AND d.control_revision=p.control_revision AND d.binding_hash=p.original_binding_hash
 AND d.task_id=NEW.task_id AND d.run_id=NEW.run_id AND d.launch_id=NEW.launch_id AND d.work_revision=NEW.work_revision AND d.ready_sequence=NEW.ready_sequence
 AND d.owner_pid=NEW.owner_pid AND d.owner_birth=NEW.owner_birth AND d.host_pid=NEW.host_pid AND d.host_birth=NEW.host_birth
 AND d.origin_context_id=NEW.successor_origin_context_id AND d.origin_pid=NEW.successor_origin_pid AND d.origin_birth=NEW.successor_origin_birth
 AND d.host_generation=NEW.successor_host_generation AND d.executable=NEW.successor_executable AND d.executable_sha256=NEW.successor_executable_sha256
 AND d.owner_exited_at=NEW.owner_exited_at AND d.host_exited_at=NEW.host_exited_at AND NEW.coordinator_epoch>=d.coordinator_epoch)
 BEGIN SELECT RAISE(ABORT,'owner_bound_abandonment_required'); END;
 CREATE TRIGGER owner_bound_abandonment_authorization_required BEFORE INSERT ON owner_succession_authorizations
 WHEN NEW.abandoned_owner_id IS NULL AND EXISTS(SELECT 1 FROM owner_bound_abandonments WHERE owner_id=NEW.predecessor_owner_id)
 BEGIN SELECT RAISE(ABORT,'owner_bound_abandonment_required'); END;`)
	if err != nil {
		return err
	}
	if err = ownerBoundAbandonmentSchema28Stage("objects"); err != nil {
		return err
	}
	// Upgrade only the known schema27 predicate; never guess at an altered schema.
	var current string
	if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='owner_recovery_handoff_current'`).Scan(&current); err != nil {
		return err
	}
	const old = "p.applied_control_revision=p.control_revision"
	if strings.Count(current, old) != 1 {
		return CodeError("owner_bound_abandonment_schema_mismatch")
	}
	current = strings.Replace(current, old, "((a.abandoned_owner_id IS NULL AND "+old+") OR "+abandonedPredecessorSQL+")", 1)
	if _, err = tx.Exec(`DROP TRIGGER owner_recovery_handoff_current`); err != nil {
		return err
	}
	if _, err = tx.Exec(current); err != nil {
		return err
	}
	for _, table := range []struct{ name, prefix, cost string }{
		{"owner_bound_abandonments", "quota_owner_bound_abandonment", originalOwnerBytesSQL("NEW.", ownerBoundAbandonmentTextColumns, 64)},
		{"owner_succession_authorizations", "quota_owner_abandonment_reference", "CASE WHEN NEW.abandoned_owner_id IS NULL THEN 0 ELSE 8+length(CAST(NEW.abandoned_owner_id AS BLOB)) END"},
	} {
		body := fmt.Sprintf("INSERT INTO storage_usage VALUES('global','',(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes; INSERT INTO storage_usage VALUES('run',NEW.run_id,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", table.cost, table.cost)
		if _, err = tx.Exec("CREATE TRIGGER " + table.prefix + "_INSERT AFTER INSERT ON " + table.name + " BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	if err = ownerBoundAbandonmentSchema28Stage("triggers"); err != nil {
		return err
	}
	var bad int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return CodeError("owner_bound_abandonment_foreign_key_check_failed")
	}

	return nil
}
