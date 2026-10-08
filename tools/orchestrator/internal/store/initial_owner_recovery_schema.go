package store

import (
	"database/sql"
	"fmt"
	"strings"
)

var initialOwnerProofTextColumns = []string{"owner_id", "command_id", "request_hash", "run_id", "task_id", "launch_id", "binding_hash", "permit_hash", "snapshot_json", "snapshot_hash", "facts_json", "stage_name", "authorization_id", "authorization_json"}

// The initial exception applies only to the first adjacent authorization. It
// cannot stand in for Applied on a later owner; that requires schema28's proof.
const initialPredecessorSQL = `EXISTS(SELECT 1 FROM owner_initial_recoveries z
 WHERE z.owner_id=p.owner_id AND a.initial_proof_id=z.owner_id AND a.authorization_id=z.authorization_id
 AND z.phase IN ('finalized','activated') AND p.incarnation=1 AND p.control_revision=1 AND p.applied_control_revision=0
 AND z.binding_hash=p.original_binding_hash AND z.run_id=a.run_id AND z.task_id=a.task_id AND z.launch_id=a.launch_id
 AND a.abandoned_owner_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM owner_control_changes x WHERE x.owner_id=p.owner_id AND x.applied_hash IS NOT NULL))`

func initialOwnerProofBytesSQL(prefix string) string {
	return originalOwnerBytesSQL(prefix, initialOwnerProofTextColumns, 64+9) + "+CASE WHEN " + prefix + "phase='prepared' THEN " + prefix + "reserved_bytes ELSE 0 END"
}

func migrateInitialOwnerRecoverySchema(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE owner_initial_recoveries(
 owner_id TEXT PRIMARY KEY NOT NULL REFERENCES execution_owners(owner_id),
 command_id TEXT NOT NULL UNIQUE, request_hash TEXT NOT NULL,
 run_id TEXT NOT NULL UNIQUE REFERENCES runs(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 launch_id TEXT NOT NULL UNIQUE REFERENCES host_launches(id), binding_hash TEXT NOT NULL, permit_hash TEXT NOT NULL,
 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json) AND length(CAST(snapshot_json AS BLOB))<=1048576),
 snapshot_hash TEXT NOT NULL, snapshot_count INTEGER NOT NULL CHECK(snapshot_count BETWEEN 1 AND 64),
 facts_json TEXT NOT NULL CHECK(json_valid(facts_json)), stage_name TEXT NOT NULL UNIQUE,
 authorization_id TEXT NOT NULL UNIQUE, authorization_json TEXT NOT NULL CHECK(json_valid(authorization_json)),
 reserved_bytes INTEGER NOT NULL CHECK(reserved_bytes>0), phase TEXT NOT NULL CHECK(phase IN ('prepared','finalized','activated')));
 ALTER TABLE owner_succession_authorizations ADD COLUMN initial_proof_id TEXT REFERENCES owner_initial_recoveries(owner_id);
 CREATE TRIGGER owner_initial_proof_current BEFORE INSERT ON owner_initial_recoveries
 WHEN NEW.phase!='prepared' OR NOT EXISTS(SELECT 1 FROM execution_owners p JOIN owner_control_changes c ON c.owner_id=p.owner_id AND c.revision=1 JOIN tasks t ON t.id=NEW.task_id JOIN task_runtime tr ON tr.task_id=t.id
 WHERE p.owner_id=NEW.owner_id AND p.run_id=NEW.run_id AND p.launch_id=NEW.launch_id AND p.incarnation=1 AND p.phase='bound' AND p.is_current=1
 AND p.control_revision=1 AND p.applied_control_revision=0 AND p.succession_authorization_id IS NULL
 AND p.original_binding_hash=NEW.binding_hash AND c.permit_hash=NEW.permit_hash AND c.applied_hash IS NULL
 AND t.run_id=p.run_id AND tr.host_launch_id=p.launch_id AND t.status='ready' AND tr.ready_sequence>0)
 BEGIN SELECT RAISE(ABORT,'owner_initial_recovery_mismatch'); END;
 CREATE TRIGGER owner_initial_proof_no_delete BEFORE DELETE ON owner_initial_recoveries BEGIN SELECT RAISE(ABORT,'owner_initial_recovery_immutable'); END;
 CREATE TRIGGER owner_initial_proof_finalize BEFORE UPDATE OF phase ON owner_initial_recoveries
 WHEN OLD.phase='prepared' AND NOT EXISTS(SELECT 1 FROM execution_owners p JOIN owner_succession_authorizations a ON a.authorization_id=NEW.authorization_id JOIN runtime_hosts h ON h.id=p.producer_id JOIN host_launches l ON l.id=p.launch_id
 WHERE p.owner_id=NEW.owner_id AND p.phase='retired' AND p.is_current=0 AND a.initial_proof_id=NEW.owner_id
 AND a.predecessor_owner_id=p.owner_id AND a.command_id=NEW.command_id AND a.request_hash=NEW.request_hash
 AND h.status='offline' AND l.status='offline' AND h.pid=a.host_pid AND h.birth=a.host_birth)
 BEGIN SELECT RAISE(ABORT,'owner_initial_recovery_finalize_required'); END;
 CREATE TRIGGER owner_initial_proof_activate BEFORE UPDATE OF phase ON owner_initial_recoveries
 WHEN OLD.phase='finalized' AND NOT EXISTS(SELECT 1 FROM execution_owners n JOIN owner_succession_authorizations a ON a.authorization_id=n.succession_authorization_id JOIN owner_control_changes c ON c.owner_id=n.owner_id AND c.revision=n.control_revision
 WHERE a.initial_proof_id=NEW.owner_id AND n.phase='bound' AND n.is_current=1 AND n.applied_control_revision=n.control_revision
 AND n.control_revision>0 AND c.applied_hash IS NOT NULL AND n.run_id=NEW.run_id AND n.launch_id=NEW.launch_id)
 BEGIN SELECT RAISE(ABORT,'owner_initial_recovery_applied_required'); END;
 CREATE TRIGGER owner_initial_proof_sealed_control BEFORE UPDATE ON owner_control_changes
 WHEN EXISTS(SELECT 1 FROM owner_initial_recoveries WHERE owner_id=OLD.owner_id)
 BEGIN SELECT RAISE(ABORT,'owner_initial_recovery_sealed'); END;
 CREATE TRIGGER owner_initial_proof_sealed_control_insert BEFORE INSERT ON owner_control_changes
 WHEN EXISTS(SELECT 1 FROM owner_initial_recoveries WHERE owner_id=NEW.owner_id)
 BEGIN SELECT RAISE(ABORT,'owner_initial_recovery_sealed'); END;
 CREATE TRIGGER owner_initial_proof_sealed_owner BEFORE UPDATE ON execution_owners
 WHEN EXISTS(SELECT 1 FROM owner_initial_recoveries WHERE owner_id=OLD.owner_id) AND
 (NEW.control_revision IS NOT OLD.control_revision OR NEW.applied_control_revision IS NOT OLD.applied_control_revision OR NEW.coordinator_epoch IS NOT OLD.coordinator_epoch OR NEW.current_host_pid IS NOT OLD.current_host_pid OR NEW.current_host_birth IS NOT OLD.current_host_birth)
 BEGIN SELECT RAISE(ABORT,'owner_initial_recovery_sealed'); END;`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE TRIGGER owner_initial_command_reserve BEFORE INSERT ON owner_initial_recoveries
 WHEN EXISTS(SELECT 1 FROM existing_owner_resume_commands WHERE command_id=NEW.command_id)
 OR EXISTS(SELECT 1 FROM owner_succession_authorizations WHERE command_id=NEW.command_id)
 BEGIN SELECT RAISE(ABORT,'owner_resume_command_reserved'); END;
 CREATE TRIGGER owner_initial_command_existing BEFORE INSERT ON existing_owner_resume_commands
 WHEN EXISTS(SELECT 1 FROM owner_initial_recoveries WHERE command_id=NEW.command_id)
 BEGIN SELECT RAISE(ABORT,'owner_resume_command_reserved'); END;
 CREATE TRIGGER owner_initial_command_succession BEFORE INSERT ON owner_succession_authorizations
 WHEN EXISTS(SELECT 1 FROM owner_initial_recoveries p WHERE p.command_id=NEW.command_id
 AND (NEW.authorization_id IS NOT p.authorization_id OR NEW.initial_proof_id IS NOT p.owner_id OR NEW.predecessor_owner_id IS NOT p.owner_id))
 BEGIN SELECT RAISE(ABORT,'owner_resume_command_reserved'); END;`)
	if err != nil {
		return err
	}
	// Freeze every authorization field except its final Coordinator epoch.
	// The integer epoch has fixed quota cost and may advance after a restart.
	match := ""
	for _, pair := range []struct{ column, path string }{
		{"authorization_id", "authorization_id"}, {"command_id", "command_id"}, {"request_hash", "request_sha256"},
		{"run_id", "run_id"}, {"task_id", "task_id"}, {"work_revision", "work_revision"}, {"launch_id", "launch_id"},
		{"predecessor_owner_id", "predecessor_owner_id"}, {"predecessor_incarnation", "predecessor_incarnation"}, {"predecessor_control_revision", "predecessor_control_revision"}, {"predecessor_scope_hash", "predecessor_scope_sha256"},
		{"owner_pid", "owner.pid"}, {"owner_birth", "owner.birth"}, {"owner_exited_at", "owner_exited_at"},
		{"host_pid", "host.pid"}, {"host_birth", "host.birth"}, {"host_exited_at", "host_exited_at"},
		{"successor_origin_context_id", "successor_origin.OriginContextID"}, {"successor_origin_pid", "successor_origin.OriginPID"}, {"successor_origin_birth", "successor_origin.OriginBirth"},
		{"successor_host_generation", "successor_host_generation"}, {"successor_executable", "successor_executable"}, {"successor_executable_sha256", "successor_executable_sha256"},
		{"ready_sequence", "ready_sequence"}, {"created_at", "created_at"}, {"initial_proof_id", "initial_proof_id"},
	} {
		match += " AND NEW." + pair.column + " IS json_extract(z.authorization_json,'$." + pair.path + "')"
	}
	_, err = tx.Exec(`CREATE TRIGGER owner_initial_authorization_exact BEFORE INSERT ON owner_succession_authorizations
 WHEN (EXISTS(SELECT 1 FROM owner_initial_recoveries WHERE owner_id=NEW.predecessor_owner_id) AND NEW.initial_proof_id IS NOT NEW.predecessor_owner_id)
 OR (NEW.initial_proof_id IS NOT NULL AND NEW.abandoned_owner_id IS NULL AND NOT EXISTS(SELECT 1 FROM owner_initial_recoveries z
 WHERE z.owner_id=NEW.initial_proof_id AND z.owner_id=NEW.predecessor_owner_id AND z.phase='prepared'
 AND NEW.coordinator_epoch>=json_extract(z.authorization_json,'$.coordinator_epoch')` + match + `))
 BEGIN SELECT RAISE(ABORT,'owner_initial_recovery_authorization_mismatch'); END;`)
	if err != nil {
		return err
	}
	immutable := "NOT ((OLD.phase='prepared' AND NEW.phase='finalized') OR (OLD.phase='finalized' AND NEW.phase='activated'))"
	for _, column := range append(append([]string{}, initialOwnerProofTextColumns...), "snapshot_count", "reserved_bytes") {
		immutable += " OR NEW." + column + " IS NOT OLD." + column
	}
	if _, err = tx.Exec("CREATE TRIGGER owner_initial_proof_immutable BEFORE UPDATE ON owner_initial_recoveries WHEN " + immutable + " BEGIN SELECT RAISE(ABORT,'owner_initial_recovery_immutable'); END"); err != nil {
		return err
	}
	// Upgrade exact known schema28 predicates; unexpected schemas fail closed.
	changes := initialOwnerTriggerChanges()
	for _, c := range changes {
		var body string
		if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, c.name).Scan(&body); err != nil {
			return err
		}
		if strings.Count(body, c.old) != 1 {
			return CodeError("owner_initial_recovery_schema_mismatch")
		}
		if _, err = tx.Exec("DROP TRIGGER " + c.name); err != nil {
			return err
		}
		if _, err = tx.Exec(strings.Replace(body, c.old, c.replacement, 1)); err != nil {
			return err
		}
	}
	for _, table := range []struct {
		name, prefix string
		cost         func(string) string
	}{
		{"owner_initial_recoveries", "quota_owner_initial_proof", initialOwnerProofBytesSQL},
		{"owner_succession_authorizations", "quota_owner_initial_reference", func(p string) string {
			return "CASE WHEN " + p + "initial_proof_id IS NULL THEN 0 ELSE 8+length(CAST(" + p + "initial_proof_id AS BLOB)) END"
		}},
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
				for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
					cost := table.cost(ref + ".")
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
	var invalid int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return CodeError("owner_initial_recovery_foreign_key_check_failed")
	}
	return nil
}

func initialOwnerTriggerChanges() []struct{ name, old, replacement string } {
	return []struct{ name, old, replacement string }{
		{"owner_succession_authorizations_predecessor", "t.status='resume_queued'", `((NEW.initial_proof_id IS NULL AND t.status='resume_queued') OR (NEW.initial_proof_id IS NOT NULL AND t.status='ready' AND EXISTS(SELECT 1 FROM owner_initial_recoveries z WHERE z.owner_id=NEW.initial_proof_id AND z.run_id=NEW.run_id AND z.task_id=NEW.task_id AND z.launch_id=NEW.launch_id AND ((z.phase='prepared' AND p.owner_id=z.owner_id AND NEW.authorization_id=z.authorization_id AND NEW.command_id=z.command_id AND NEW.request_hash=z.request_hash AND p.incarnation=1 AND p.applied_control_revision=0) OR (z.phase='finalized' AND p.succession_authorization_id IS NOT NULL AND EXISTS(SELECT 1 FROM owner_succession_authorizations inbound WHERE inbound.authorization_id=p.succession_authorization_id AND inbound.initial_proof_id=z.owner_id))))))`},
		{"owner_recovery_handoff_current", "(a.abandoned_owner_id IS NULL AND p.applied_control_revision=p.control_revision)", "(a.abandoned_owner_id IS NULL AND a.initial_proof_id IS NULL AND p.applied_control_revision=p.control_revision) OR " + initialPredecessorSQL},
		{"owner_recovery_handoff_current", "d.inbound_authorization_id=p.succession_authorization_id", "d.inbound_authorization_id=p.succession_authorization_id AND a.initial_proof_id IS (SELECT inbound.initial_proof_id FROM owner_succession_authorizations inbound WHERE inbound.authorization_id=d.inbound_authorization_id)"},
		{"owner_bound_abandonment_current", "t.status='resume_queued'", `((a.initial_proof_id IS NULL AND t.status='resume_queued') OR (a.initial_proof_id IS NOT NULL AND t.status='ready' AND EXISTS(SELECT 1 FROM owner_initial_recoveries z WHERE z.owner_id=a.initial_proof_id AND z.phase='finalized' AND z.run_id=a.run_id AND z.task_id=a.task_id AND z.launch_id=a.launch_id)))`},
		{"owner_bound_abandonment_authorization", "d.task_id=NEW.task_id", "NEW.initial_proof_id IS (SELECT inbound.initial_proof_id FROM owner_succession_authorizations inbound WHERE inbound.authorization_id=d.inbound_authorization_id) AND d.task_id=NEW.task_id"},
	}
}
