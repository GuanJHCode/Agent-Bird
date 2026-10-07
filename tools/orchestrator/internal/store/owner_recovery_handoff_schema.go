package store

import (
	"database/sql"
	"fmt"
)

var ownerRecoverySchema27Hook func(string) error

func ownerRecoverySchema27Stage(stage string) error {
	if ownerRecoverySchema27Hook != nil {
		return ownerRecoverySchema27Hook(stage)
	}
	return nil
}

var ownerRecoveryHandoffTextColumns = []string{"owner_id", "authorization_id", "run_id", "launch_id", "producer_id", "predecessor_owner_id", "prior_scope_hash", "scope_hash", "prior_record_hash", "record_hash", "authority_hash", "prior_binding_hash", "binding_hash", "created_at"}

func migrateOwnerRecoveryHandoffSchema(tx *sql.Tx) error {
	var unproven int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM execution_owners p WHERE p.succession_authorization_id IS NOT NULL AND (
 NOT ((p.phase='intended' AND p.is_current=1 AND p.control_revision=0 AND p.applied_control_revision=0)
 OR (p.phase='bound' AND p.is_current=1 AND p.control_revision=1 AND p.applied_control_revision=0))
 OR EXISTS(SELECT 1 FROM owner_control_changes c WHERE c.owner_id=p.owner_id AND c.applied_hash IS NOT NULL))`).Scan(&unproven); err != nil {
		return err
	}
	if unproven != 0 {
		return CodeError("owner_recovery_handoff_unproven_history")
	}
	if err := ownerRecoverySchema27Stage("validated"); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE TABLE owner_recovery_scope_handoffs(
 owner_id TEXT PRIMARY KEY NOT NULL REFERENCES execution_owners(owner_id),
 authorization_id TEXT NOT NULL UNIQUE REFERENCES owner_succession_authorizations(authorization_id),
 run_id TEXT NOT NULL REFERENCES runs(id), launch_id TEXT NOT NULL REFERENCES host_launches(id),
 producer_id TEXT NOT NULL REFERENCES runtime_hosts(id), predecessor_owner_id TEXT NOT NULL UNIQUE REFERENCES execution_owners(owner_id),
 prior_scope_hash TEXT NOT NULL CHECK(length(prior_scope_hash)=64 AND prior_scope_hash NOT GLOB '*[^0-9a-f]*'),
 scope_hash TEXT NOT NULL CHECK(length(scope_hash)=64 AND scope_hash NOT GLOB '*[^0-9a-f]*' AND scope_hash!=prior_scope_hash),
 prior_record_hash TEXT NOT NULL CHECK(length(prior_record_hash)=64 AND prior_record_hash NOT GLOB '*[^0-9a-f]*'),
 record_hash TEXT NOT NULL CHECK(length(record_hash)=64 AND record_hash NOT GLOB '*[^0-9a-f]*' AND record_hash!=prior_record_hash),
 authority_hash TEXT NOT NULL CHECK(length(authority_hash)=64 AND authority_hash NOT GLOB '*[^0-9a-f]*'),
 prior_binding_hash TEXT NOT NULL, binding_hash TEXT NOT NULL,
 predecessor_control_revision INTEGER NOT NULL CHECK(predecessor_control_revision>0),
 control_revision INTEGER NOT NULL CHECK(control_revision=1),
 created_at TEXT NOT NULL CHECK(length(created_at)>0), coordinator_epoch INTEGER NOT NULL CHECK(coordinator_epoch>0),
 applied INTEGER NOT NULL DEFAULT 0 CHECK(applied IN (0,1)),
 FOREIGN KEY(predecessor_owner_id,predecessor_control_revision) REFERENCES owner_control_changes(owner_id,revision),
 FOREIGN KEY(owner_id,control_revision) REFERENCES owner_control_changes(owner_id,revision)
);
CREATE TRIGGER owner_recovery_handoff_current BEFORE INSERT ON owner_recovery_scope_handoffs
WHEN NOT EXISTS(SELECT 1 FROM execution_owners n
 JOIN owner_succession_authorizations a ON a.authorization_id=n.succession_authorization_id
 JOIN execution_owners p ON p.owner_id=a.predecessor_owner_id
 JOIN owner_control_changes c ON c.owner_id=n.owner_id AND c.revision=1
 WHERE n.owner_id=NEW.owner_id AND n.phase='bound' AND n.is_current=1 AND n.control_revision=1 AND n.applied_control_revision=0
 AND n.original_binding_hash=NEW.binding_hash AND c.applied_hash IS NULL AND NEW.applied=0 AND NEW.control_revision=n.control_revision AND n.succession_authorization_id=NEW.authorization_id
 AND n.run_id=NEW.run_id AND n.launch_id=NEW.launch_id AND n.producer_id=NEW.producer_id
 AND p.owner_id=NEW.predecessor_owner_id AND p.run_id=n.run_id AND p.launch_id=n.launch_id AND p.producer_id=n.producer_id
 AND p.phase='retired' AND p.is_current=0 AND p.incarnation+1=n.incarnation AND p.original_binding_hash=NEW.prior_binding_hash AND p.control_revision=NEW.predecessor_control_revision
 AND p.control_revision=a.predecessor_control_revision AND p.applied_control_revision=p.control_revision
 AND a.run_id=n.run_id AND a.launch_id=n.launch_id AND a.predecessor_incarnation=p.incarnation
 AND NEW.coordinator_epoch=(SELECT epoch FROM coordinator WHERE id=1) AND NEW.coordinator_epoch>=n.coordinator_epoch
 AND NEW.coordinator_epoch>=a.coordinator_epoch)
BEGIN SELECT RAISE(ABORT,'owner_recovery_handoff_scope_mismatch'); END;
CREATE TRIGGER owner_recovery_handoff_applied_current BEFORE UPDATE ON owner_recovery_scope_handoffs
WHEN NOT EXISTS(SELECT 1 FROM execution_owners n
 WHERE n.owner_id=NEW.owner_id AND n.succession_authorization_id=NEW.authorization_id
 AND n.phase='bound' AND n.is_current=1 AND n.control_revision=1 AND n.applied_control_revision=0
 AND n.original_binding_hash=NEW.binding_hash
 AND (SELECT epoch FROM coordinator WHERE id=1)>=n.coordinator_epoch AND (SELECT epoch FROM coordinator WHERE id=1)>=NEW.coordinator_epoch)
BEGIN SELECT RAISE(ABORT,'owner_recovery_handoff_application_mismatch'); END;`)
	if err != nil {
		return err
	}
	if err = ownerRecoverySchema27Stage("objects"); err != nil {
		return err
	}
	for _, table := range []struct {
		name    string
		columns []string
	}{{"owner_recovery_scope_handoffs", ownerRecoveryHandoffTextColumns}} {
		for _, op := range []string{"DELETE"} {
			if _, err = tx.Exec("CREATE TRIGGER " + table.name + "_immutable_" + op + " BEFORE " + op + " ON " + table.name + " BEGIN SELECT RAISE(ABORT,'owner_recovery_handoff_immutable'); END"); err != nil {
				return err
			}
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
				cost := originalOwnerBytesSQL(ref+".", table.columns, 32)
				for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
					if ref == "OLD" {
						body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-(%s) WHERE scope='%s' AND scope_id=%s;", cost, scope.name, scope.id)
					} else {
						body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, cost)
					}
				}
			}
			if _, err = tx.Exec("CREATE TRIGGER quota_" + table.name + "_" + op + " AFTER " + op + " ON " + table.name + " BEGIN " + body + " END"); err != nil {
				return err
			}
		}
	}
	immutable := "OLD.applied!=0 OR NEW.applied!=1"
	for _, column := range append(append([]string{}, ownerRecoveryHandoffTextColumns...), "predecessor_control_revision", "control_revision", "coordinator_epoch") {
		immutable += " OR NEW." + column + " IS NOT OLD." + column
	}
	if _, err = tx.Exec("CREATE TRIGGER owner_recovery_handoff_immutable_update BEFORE UPDATE ON owner_recovery_scope_handoffs WHEN " + immutable + " BEGIN SELECT RAISE(ABORT,'owner_recovery_handoff_immutable'); END"); err != nil {
		return err
	}
	if err = ownerRecoverySchema27Stage("triggers"); err != nil {
		return err
	}
	var bad int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return CodeError("owner_recovery_handoff_foreign_key_check_failed")
	}
	return nil
}
