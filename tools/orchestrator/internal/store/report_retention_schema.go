package store

import (
	"database/sql"
	"fmt"
	"strings"
)

var reportDeletionColumns = []string{"intake_id", "reservation_id", "run_id", "task_id", "request_id", "binding_hash", "facts_sha256", "root_json", "plan_json", "created_at"}

func reportDeletionCostSQL(ref string) string {
	out := "256+" + ref + "marker_bytes"
	for _, c := range reportDeletionColumns {
		out += "+length(CAST(" + ref + c + " AS BLOB))"
	}
	return out
}
func migrateReportRetentionSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE report_artifact_resolutions(
 intake_id TEXT NOT NULL REFERENCES managed_artifact_intakes(id),run_id TEXT NOT NULL REFERENCES runs(id),
 controller TEXT NOT NULL,token_hash TEXT NOT NULL CHECK(length(token_hash)=64),binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),
 request_id TEXT NOT NULL,reason TEXT NOT NULL CHECK(length(CAST(reason AS BLOB)) BETWEEN 1 AND 512),
 resolution TEXT NOT NULL CHECK(resolution IN ('resolved','abandoned')),created_at TEXT NOT NULL,PRIMARY KEY(intake_id,binding_hash),UNIQUE(run_id,request_id));
CREATE TRIGGER report_resolution_immutable BEFORE UPDATE ON report_artifact_resolutions BEGIN SELECT RAISE(ABORT,'report_resolution_immutable'); END;
CREATE TRIGGER report_resolution_retained BEFORE DELETE ON report_artifact_resolutions BEGIN SELECT RAISE(ABORT,'report_resolution_immutable'); END;
CREATE TRIGGER report_resolution_binding BEFORE INSERT ON report_artifact_resolutions WHEN NOT EXISTS(SELECT 1 FROM managed_artifact_intakes WHERE id=NEW.intake_id AND run_id=NEW.run_id AND kind='failure' AND phase='committed') BEGIN SELECT RAISE(ABORT,'report_resolution_invalid'); END;
CREATE TABLE report_artifact_pins(
 id TEXT PRIMARY KEY,intake_id TEXT NOT NULL REFERENCES managed_artifact_intakes(id),run_id TEXT NOT NULL REFERENCES runs(id),
 owner TEXT NOT NULL,reason TEXT NOT NULL,release_condition TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('active','released')),
 CHECK(length(CAST(owner AS BLOB)) BETWEEN 1 AND 256),CHECK(length(CAST(reason AS BLOB)) BETWEEN 1 AND 256),CHECK(length(CAST(release_condition AS BLOB)) BETWEEN 1 AND 256));
CREATE TRIGGER report_pin_initial BEFORE INSERT ON report_artifact_pins WHEN NEW.state!='active' OR NOT EXISTS(SELECT 1 FROM managed_artifact_intakes i JOIN managed_artifact_reservations r ON r.id=i.reservation_id WHERE i.id=NEW.intake_id AND i.run_id=NEW.run_id AND i.phase='committed' AND r.state='active') BEGIN SELECT RAISE(ABORT,'report_pin_invalid'); END;
CREATE TRIGGER report_pin_identity BEFORE UPDATE OF id,intake_id,run_id,owner,reason,release_condition ON report_artifact_pins BEGIN SELECT RAISE(ABORT,'report_pin_immutable'); END;
CREATE TRIGGER report_pin_transition BEFORE UPDATE OF state ON report_artifact_pins WHEN NOT(NEW.state=OLD.state OR OLD.state='active' AND NEW.state='released') BEGIN SELECT RAISE(ABORT,'report_pin_invalid'); END;
CREATE TRIGGER report_pin_retained BEFORE DELETE ON report_artifact_pins BEGIN SELECT RAISE(ABORT,'report_pin_immutable'); END;
CREATE TABLE report_artifact_deletions(
 intake_id TEXT PRIMARY KEY REFERENCES managed_artifact_intakes(id),reservation_id TEXT NOT NULL UNIQUE REFERENCES managed_artifact_reservations(id),
 run_id TEXT NOT NULL REFERENCES runs(id),task_id TEXT NOT NULL REFERENCES tasks(id),request_id TEXT NOT NULL,
 binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),facts_sha256 TEXT NOT NULL CHECK(length(facts_sha256)=64),
 root_json TEXT NOT NULL CHECK(length(CAST(root_json AS BLOB)) BETWEEN 1 AND 4096),
 plan_json TEXT NOT NULL CHECK(length(CAST(plan_json AS BLOB)) BETWEEN 1 AND 16384),
 marker_bytes INTEGER NOT NULL CHECK(marker_bytes=length(CAST(plan_json AS BLOB))),
 removed_bytes INTEGER NOT NULL CHECK(removed_bytes BETWEEN 0 AND 1048576),
 phase TEXT NOT NULL CHECK(phase IN ('prepared','removed')),created_at TEXT NOT NULL,
 control_bytes INTEGER NOT NULL CHECK(control_bytes=(` + reportDeletionCostSQL("") + `)),UNIQUE(run_id,request_id));
CREATE TRIGGER report_deletion_initial BEFORE INSERT ON report_artifact_deletions WHEN NEW.phase!='prepared' OR NOT EXISTS(
 SELECT 1 FROM managed_artifact_intakes i JOIN managed_artifact_reservations r ON r.id=i.reservation_id WHERE i.id=NEW.intake_id AND i.reservation_id=NEW.reservation_id AND i.run_id=NEW.run_id AND i.task_id=NEW.task_id AND i.kind IN ('result','failure') AND i.phase='committed' AND r.class='report' AND r.state='active' AND r.reserved_bytes=NEW.removed_bytes AND r.retained_bytes=r.reserved_bytes)
 OR EXISTS(SELECT 1 FROM report_artifact_pins WHERE intake_id=NEW.intake_id AND state='active') BEGIN SELECT RAISE(ABORT,'report_deletion_invalid'); END;
CREATE TRIGGER report_deletion_identity BEFORE UPDATE OF ` + strings.Join(reportDeletionColumns, ",") + `,marker_bytes,removed_bytes,control_bytes ON report_artifact_deletions BEGIN SELECT RAISE(ABORT,'report_deletion_immutable'); END;
CREATE TRIGGER report_deletion_retained BEFORE DELETE ON report_artifact_deletions BEGIN SELECT RAISE(ABORT,'report_deletion_immutable'); END;
CREATE TRIGGER report_deletion_transition BEFORE UPDATE OF phase ON report_artifact_deletions WHEN NOT(NEW.phase=OLD.phase OR OLD.phase='prepared' AND NEW.phase='removed' AND EXISTS(SELECT 1 FROM managed_artifact_reservations WHERE id=OLD.reservation_id AND state='deleting')) BEGIN SELECT RAISE(ABORT,'report_deletion_invalid'); END;
CREATE TRIGGER report_reservation_transition BEFORE UPDATE OF state,retained_bytes ON managed_artifact_reservations WHEN OLD.class='report' AND NOT(
 (OLD.state=NEW.state AND NEW.retained_bytes=OLD.retained_bytes) OR (OLD.state='reserved' AND NEW.state='active' AND NEW.retained_bytes=NEW.reserved_bytes)
 OR (OLD.state='active' AND NEW.state='deleting' AND NEW.retained_bytes=OLD.retained_bytes AND EXISTS(SELECT 1 FROM report_artifact_deletions WHERE reservation_id=OLD.id AND phase='prepared'))
 OR (OLD.state='deleting' AND NEW.state='released' AND NEW.retained_bytes=0 AND EXISTS(SELECT 1 FROM report_artifact_deletions WHERE reservation_id=OLD.id AND phase='removed')))
 BEGIN SELECT RAISE(ABORT,'report_reservation_transition'); END;
CREATE TRIGGER report_retention_attempt_fence BEFORE INSERT ON attempts WHEN EXISTS(SELECT 1 FROM report_artifact_deletions WHERE task_id=NEW.task_id AND phase='prepared') BEGIN SELECT RAISE(ABORT,'report_artifact_deleting'); END;
CREATE TRIGGER report_retention_segment_fence BEFORE INSERT ON segments WHEN EXISTS(SELECT 1 FROM report_artifact_deletions d JOIN attempts a ON a.task_id=d.task_id WHERE a.id=NEW.attempt_id AND d.phase='prepared') BEGIN SELECT RAISE(ABORT,'report_artifact_deleting'); END;
CREATE TRIGGER report_retention_intake_fence BEFORE INSERT ON managed_artifact_intakes WHEN EXISTS(SELECT 1 FROM report_artifact_deletions WHERE task_id=NEW.task_id AND phase='prepared') BEGIN SELECT RAISE(ABORT,'report_artifact_deleting'); END;
CREATE TABLE report_gc_policies(
 run_id TEXT PRIMARY KEY REFERENCES runs(id),controller TEXT NOT NULL,token_hash TEXT NOT NULL CHECK(length(token_hash)=64),binding_hash TEXT NOT NULL CHECK(length(binding_hash)=64),
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),revision INTEGER NOT NULL CHECK(revision>=1),cursor TEXT NOT NULL DEFAULT '' CHECK(length(CAST(cursor AS BLOB))<=256),
 plan_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(plan_json AS BLOB))<=8191),last_tick TEXT NOT NULL DEFAULT '' CHECK(length(last_tick)<=32),stopped TEXT NOT NULL DEFAULT '' CHECK(length(stopped)<=64));
CREATE TRIGGER quota_report_deletion_INSERT AFTER INSERT ON report_artifact_deletions BEGIN
 INSERT INTO storage_usage SELECT 'global','',0,0 WHERE NOT EXISTS(SELECT 1 FROM storage_usage WHERE scope='global' AND scope_id='');INSERT INTO storage_usage SELECT 'run',NEW.run_id,0,0 WHERE NOT EXISTS(SELECT 1 FROM storage_usage WHERE scope='run' AND scope_id=NEW.run_id);
 UPDATE storage_usage SET total_bytes=total_bytes+NEW.control_bytes WHERE scope='global' AND scope_id='' OR scope='run' AND scope_id=NEW.run_id;END;
`)
	if err != nil {
		return err
	}
	for _, table := range []struct {
		name    string
		columns []string
		mutable bool
	}{
		{"report_artifact_resolutions", []string{"intake_id", "run_id", "controller", "token_hash", "binding_hash", "request_id", "reason", "resolution", "created_at"}, false},
		{"report_artifact_pins", []string{"id", "intake_id", "run_id", "owner", "reason", "release_condition"}, false},
		{"report_gc_policies", []string{"run_id", "controller", "token_hash", "binding_hash", "plan_json"}, true},
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
