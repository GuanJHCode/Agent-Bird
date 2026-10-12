package store

import (
	"database/sql"
	"fmt"
	"strings"
)

const hostSlotPlanLimit = 16384
const hostSlotRetirementPrepaid = 2*hostSlotPlanLimit + 2048

var hostDeletionColumns = []string{"reservation_id", "run_id", "task_id", "attempt_id", "segment_id", "producer_id", "class", "scope_sha256", "request_id", "binding_hash", "facts_sha256", "root_json", "namespace_json", "discovery_mode", "created_at"}

func hostDeletionCostSQL(ref string) string {
	s := "4096+" + ref + "slot_count*34816"
	for _, c := range hostDeletionColumns {
		s += "+length(CAST(" + ref + c + " AS BLOB))"
	}
	return s
}

// Creation authority is recorded only with new dispatch. Migration deliberately
// does not authorize discovery of historical unregistered physical objects.
func migrateHostRetentionSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE host_artifact_cleanup_authorities(
 reservation_id TEXT PRIMARY KEY REFERENCES managed_artifact_reservations(id),run_id TEXT NOT NULL REFERENCES runs(id),
 version INTEGER NOT NULL CHECK(version=1),scope_sha256 TEXT NOT NULL CHECK(length(scope_sha256)=64),
 class TEXT NOT NULL CHECK(class IN ('terminal','progress')),slot_count INTEGER NOT NULL CHECK((class='terminal' AND slot_count=1) OR (class='progress' AND slot_count=21)),created_at TEXT NOT NULL);
CREATE TRIGGER host_cleanup_authority_binding BEFORE INSERT ON host_artifact_cleanup_authorities WHEN NOT EXISTS(SELECT 1 FROM managed_artifact_reservations r JOIN segment_runtime s ON s.segment_id=r.scope_key WHERE r.id=NEW.reservation_id AND r.run_id=NEW.run_id AND r.namespace_sha256=NEW.scope_sha256 AND r.class=NEW.class AND r.state='reserved' AND r.retained_bytes=0 AND s.status='launch_requested') BEGIN SELECT RAISE(ABORT,'host_cleanup_authority_invalid'); END;
CREATE TABLE host_artifact_resolutions(
 reservation_id TEXT NOT NULL REFERENCES managed_artifact_reservations(id),run_id TEXT NOT NULL REFERENCES runs(id),
 controller TEXT NOT NULL,token_hash TEXT NOT NULL CHECK(length(token_hash)=64),binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),
 request_id TEXT NOT NULL,reason TEXT NOT NULL CHECK(length(CAST(reason AS BLOB)) BETWEEN 1 AND 512),
 resolution TEXT NOT NULL CHECK(resolution IN ('resolved','abandoned')),created_at TEXT NOT NULL,PRIMARY KEY(reservation_id,binding_hash),UNIQUE(run_id,request_id));
CREATE TRIGGER host_resolution_binding BEFORE INSERT ON host_artifact_resolutions WHEN NOT EXISTS(SELECT 1 FROM managed_artifact_reservations WHERE id=NEW.reservation_id AND run_id=NEW.run_id AND class IN ('terminal','progress') AND state IN ('reserved','active')) BEGIN SELECT RAISE(ABORT,'host_resolution_invalid'); END;
CREATE TABLE host_artifact_pins(
 id TEXT PRIMARY KEY,reservation_id TEXT NOT NULL REFERENCES managed_artifact_reservations(id),run_id TEXT NOT NULL REFERENCES runs(id),
 owner TEXT NOT NULL,reason TEXT NOT NULL,release_condition TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('active','released')),
 CHECK(length(CAST(owner AS BLOB)) BETWEEN 1 AND 256),CHECK(length(CAST(reason AS BLOB)) BETWEEN 1 AND 256),CHECK(length(CAST(release_condition AS BLOB)) BETWEEN 1 AND 256));
CREATE TRIGGER host_pin_initial BEFORE INSERT ON host_artifact_pins WHEN NEW.state!='active' OR NOT EXISTS(SELECT 1 FROM managed_artifact_reservations WHERE id=NEW.reservation_id AND run_id=NEW.run_id AND class IN ('terminal','progress') AND state IN ('reserved','active')) BEGIN SELECT RAISE(ABORT,'host_pin_invalid'); END;
CREATE TRIGGER host_pin_identity BEFORE UPDATE OF id,reservation_id,run_id,owner,reason,release_condition ON host_artifact_pins BEGIN SELECT RAISE(ABORT,'host_pin_immutable'); END;
CREATE TRIGGER host_pin_transition BEFORE UPDATE OF state ON host_artifact_pins WHEN NOT(NEW.state=OLD.state OR OLD.state='active' AND NEW.state='released') BEGIN SELECT RAISE(ABORT,'host_pin_invalid'); END;
CREATE TABLE host_artifact_deletions(
 reservation_id TEXT PRIMARY KEY REFERENCES managed_artifact_reservations(id),run_id TEXT NOT NULL REFERENCES runs(id),task_id TEXT NOT NULL REFERENCES tasks(id),
 attempt_id TEXT NOT NULL REFERENCES attempts(id),segment_id TEXT NOT NULL REFERENCES segments(id),producer_id TEXT NOT NULL REFERENCES runtime_hosts(id),
 class TEXT NOT NULL CHECK(class IN ('terminal','progress')),scope_sha256 TEXT NOT NULL CHECK(length(scope_sha256)=64),
 slot_count INTEGER NOT NULL CHECK((class='terminal' AND slot_count=1) OR (class='progress' AND slot_count=21)),
 request_id TEXT NOT NULL,binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),facts_sha256 TEXT NOT NULL CHECK(length(facts_sha256)=64),
 root_json TEXT NOT NULL CHECK(length(CAST(root_json AS BLOB)) BETWEEN 1 AND 8192),namespace_json TEXT NOT NULL CHECK(length(CAST(namespace_json AS BLOB)) BETWEEN 1 AND 8192),
 discovery_mode TEXT NOT NULL CHECK(discovery_mode IN ('absent_only','schema40_managed')),
 phase TEXT NOT NULL CHECK(phase IN ('prepared','removed')),created_at TEXT NOT NULL,
 control_bytes INTEGER NOT NULL CHECK(control_bytes=(` + hostDeletionCostSQL("") + `)),UNIQUE(run_id,request_id));
CREATE TABLE host_artifact_deletion_slots(
 reservation_id TEXT NOT NULL REFERENCES host_artifact_deletions(reservation_id),slot INTEGER NOT NULL CHECK(slot BETWEEN 0 AND 20),
 object_id TEXT NOT NULL UNIQUE,plan_json TEXT NOT NULL CHECK(length(CAST(plan_json AS BLOB)) BETWEEN 1 AND 16384),
 removed_bytes INTEGER NOT NULL CHECK(removed_bytes BETWEEN 0 AND 2097152),phase TEXT NOT NULL CHECK(phase IN ('prepared','removed')),
 PRIMARY KEY(reservation_id,slot));
CREATE TRIGGER host_deletion_initial BEFORE INSERT ON host_artifact_deletions WHEN NEW.phase!='prepared' OR NOT EXISTS(SELECT 1 FROM managed_artifact_reservations r WHERE r.id=NEW.reservation_id AND r.run_id=NEW.run_id AND r.task_id=NEW.task_id AND r.attempt_id=NEW.attempt_id AND r.producer_id=NEW.producer_id AND r.scope_key=NEW.segment_id AND r.class=NEW.class AND r.namespace_sha256=NEW.scope_sha256 AND r.state IN ('reserved','active')) OR EXISTS(SELECT 1 FROM host_artifact_pins WHERE reservation_id=NEW.reservation_id AND state='active') OR (NEW.discovery_mode='schema40_managed' AND NOT EXISTS(SELECT 1 FROM host_artifact_cleanup_authorities a WHERE a.reservation_id=NEW.reservation_id AND a.run_id=NEW.run_id AND a.scope_sha256=NEW.scope_sha256 AND a.class=NEW.class AND a.slot_count=NEW.slot_count AND a.version=1)) BEGIN SELECT RAISE(ABORT,'host_deletion_invalid'); END;
CREATE TRIGGER host_deletion_identity BEFORE UPDATE OF ` + strings.Join(hostDeletionColumns, ",") + `,slot_count,control_bytes ON host_artifact_deletions BEGIN SELECT RAISE(ABORT,'host_deletion_immutable'); END;
CREATE TRIGGER host_deletion_transition BEFORE UPDATE OF phase ON host_artifact_deletions WHEN NOT(NEW.phase=OLD.phase OR OLD.phase='prepared' AND NEW.phase='removed' AND EXISTS(SELECT 1 FROM managed_artifact_reservations WHERE id=OLD.reservation_id AND state='deleting') AND (SELECT COUNT(*) FROM host_artifact_deletion_slots WHERE reservation_id=OLD.reservation_id AND phase='removed')=OLD.slot_count) BEGIN SELECT RAISE(ABORT,'host_deletion_invalid'); END;
CREATE TRIGGER host_deletion_slot_initial BEFORE INSERT ON host_artifact_deletion_slots WHEN NEW.phase!='prepared' OR NOT EXISTS(SELECT 1 FROM host_artifact_deletions d JOIN managed_artifact_reservations r ON r.id=d.reservation_id WHERE d.reservation_id=NEW.reservation_id AND d.phase='prepared' AND NEW.slot<d.slot_count AND r.state='deleting') BEGIN SELECT RAISE(ABORT,'host_deletion_slot_invalid'); END;
CREATE TRIGGER host_deletion_slot_identity BEFORE UPDATE OF reservation_id,slot,object_id,plan_json,removed_bytes ON host_artifact_deletion_slots BEGIN SELECT RAISE(ABORT,'host_deletion_slot_immutable'); END;
CREATE TRIGGER host_deletion_slot_transition BEFORE UPDATE OF phase ON host_artifact_deletion_slots WHEN NOT(NEW.phase=OLD.phase OR OLD.phase='prepared' AND NEW.phase='removed') BEGIN SELECT RAISE(ABORT,'host_deletion_slot_invalid'); END;
CREATE TRIGGER host_reservation_transition BEFORE UPDATE OF state,retained_bytes ON managed_artifact_reservations WHEN OLD.class IN ('terminal','progress') AND NOT(
 (OLD.state=NEW.state AND (NEW.retained_bytes=OLD.retained_bytes OR OLD.state IN ('reserved','active') AND NEW.retained_bytes>=OLD.retained_bytes))
 OR (OLD.state='reserved' AND NEW.state='active' AND NEW.retained_bytes>=OLD.retained_bytes)
 OR (OLD.state IN ('reserved','active') AND NEW.state='deleting' AND NEW.retained_bytes=OLD.retained_bytes AND EXISTS(SELECT 1 FROM host_artifact_deletions WHERE reservation_id=OLD.id AND phase='prepared'))
 OR (OLD.state='deleting' AND NEW.state='released' AND NEW.retained_bytes=0 AND EXISTS(SELECT 1 FROM host_artifact_deletions WHERE reservation_id=OLD.id AND phase='removed')))
 BEGIN SELECT RAISE(ABORT,'host_reservation_transition'); END;
CREATE TABLE host_gc_policies(
 run_id TEXT PRIMARY KEY REFERENCES runs(id),controller TEXT NOT NULL,token_hash TEXT NOT NULL CHECK(length(token_hash)=64),binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),revision INTEGER NOT NULL CHECK(revision>=1),cursor TEXT NOT NULL DEFAULT '' CHECK(length(CAST(cursor AS BLOB))<=256),
 plan_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(plan_json AS BLOB))<=8191),last_tick TEXT NOT NULL DEFAULT '' CHECK(length(last_tick)<=32),stopped TEXT NOT NULL DEFAULT '' CHECK(length(stopped)<=64));
CREATE TRIGGER quota_host_deletion_INSERT AFTER INSERT ON host_artifact_deletions BEGIN
 INSERT INTO storage_usage SELECT 'global','',0,0 WHERE NOT EXISTS(SELECT 1 FROM storage_usage WHERE scope='global' AND scope_id='');INSERT INTO storage_usage SELECT 'run',NEW.run_id,0,0 WHERE NOT EXISTS(SELECT 1 FROM storage_usage WHERE scope='run' AND scope_id=NEW.run_id);
 UPDATE storage_usage SET total_bytes=total_bytes+NEW.control_bytes WHERE scope='global' AND scope_id='' OR scope='run' AND scope_id=NEW.run_id; END;
`)
	if err != nil {
		return err
	}
	for _, table := range []string{"host_artifact_cleanup_authorities", "host_artifact_resolutions", "host_artifact_pins", "host_artifact_deletions", "host_artifact_deletion_slots"} {
		if _, err = tx.Exec("CREATE TRIGGER retained_" + table + " BEFORE DELETE ON " + table + " BEGIN SELECT RAISE(ABORT,'host_retention_immutable'); END;"); err != nil {
			return err
		}
		if table == "host_artifact_cleanup_authorities" || table == "host_artifact_resolutions" {
			if _, err = tx.Exec("CREATE TRIGGER immutable_" + table + " BEFORE UPDATE ON " + table + " BEGIN SELECT RAISE(ABORT,'host_retention_immutable'); END;"); err != nil {
				return err
			}
		}
	}
	// Ref acquisition and all routes to a fresh consumer are fenced while a
	// prepared retirement owns the run. Existing immutable history stays intact.
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
		if _, err = tx.Exec("CREATE TRIGGER host_retention_" + f.name + "_fence BEFORE " + f.operation + " ON " + f.table + " WHEN EXISTS(SELECT 1 FROM host_artifact_deletions WHERE run_id=" + f.run + " AND phase='prepared') BEGIN SELECT RAISE(ABORT,'host_artifact_deleting'); END;"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`CREATE TRIGGER host_retention_ref_fence BEFORE INSERT ON managed_artifact_refs WHEN EXISTS(SELECT 1 FROM managed_artifact_reservations WHERE id=NEW.reservation_id AND class IN ('terminal','progress') AND state IN ('deleting','released')) BEGIN SELECT RAISE(ABORT,'host_artifact_deleting'); END;`); err != nil {
		return err
	}
	for _, table := range []struct {
		name    string
		columns []string
		mutable bool
	}{
		{"host_artifact_cleanup_authorities", []string{"reservation_id", "run_id", "scope_sha256", "class", "created_at"}, false},
		{"host_artifact_resolutions", []string{"reservation_id", "run_id", "controller", "token_hash", "binding_hash", "request_id", "reason", "resolution", "created_at"}, false},
		{"host_artifact_pins", []string{"id", "reservation_id", "run_id", "owner", "reason", "release_condition"}, false},
		{"host_gc_policies", []string{"run_id", "controller", "token_hash", "binding_hash", "plan_json"}, true},
	} {
		cost := func(ref string) string {
			s := "1024"
			for _, c := range table.columns {
				s += "+length(CAST(" + ref + "." + c + " AS BLOB))"
			}
			return s
		}
		ops := []string{"INSERT"}
		if table.mutable {
			ops = append(ops, "UPDATE")
		}
		for _, op := range ops {
			delta := cost("NEW")
			if op == "UPDATE" {
				delta = "(" + delta + ")-(" + cost("OLD") + ")"
			}
			if _, err = tx.Exec(fmt.Sprintf(`CREATE TRIGGER quota_%s_%s AFTER %s ON %s BEGIN INSERT INTO storage_usage SELECT 'global','',0,0 WHERE NOT EXISTS(SELECT 1 FROM storage_usage WHERE scope='global' AND scope_id='');INSERT INTO storage_usage SELECT 'run',NEW.run_id,0,0 WHERE NOT EXISTS(SELECT 1 FROM storage_usage WHERE scope='run' AND scope_id=NEW.run_id);UPDATE storage_usage SET total_bytes=total_bytes+(%s) WHERE scope='global' AND scope_id='' OR scope='run' AND scope_id=NEW.run_id;END;`, table.name, op, op, table.name, delta)); err != nil {
				return err
			}
		}
	}
	return nil
}
