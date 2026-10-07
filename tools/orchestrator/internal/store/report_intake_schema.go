package store

import (
	"database/sql"
	"fmt"
)

// No legacy path/size claim becomes a managed object during migration.
func migrateReportIntakeSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE managed_artifact_intakes(
 id TEXT PRIMARY KEY,reservation_id TEXT NOT NULL UNIQUE REFERENCES managed_artifact_reservations(id),
 run_id TEXT NOT NULL REFERENCES runs(id),task_id TEXT NOT NULL REFERENCES tasks(id),
 capability_id TEXT NOT NULL REFERENCES report_capabilities(capability_id),
 sequence INTEGER NOT NULL CHECK(typeof(sequence)='integer' AND sequence>=1),event_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('result','question','failure')),
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 descriptor_json TEXT NOT NULL CHECK(length(CAST(descriptor_json AS BLOB))<=16384),
 phase TEXT NOT NULL CHECK(phase IN ('prepared','committed')),created_at TEXT NOT NULL,
 UNIQUE(capability_id,sequence),UNIQUE(capability_id,event_id));
CREATE TRIGGER artifact_intake_immutable BEFORE UPDATE OF id,reservation_id,run_id,task_id,capability_id,sequence,event_id,kind,payload_hash,descriptor_json,created_at ON managed_artifact_intakes BEGIN SELECT RAISE(ABORT,'artifact_intake_immutable'); END;
CREATE TRIGGER artifact_intake_retained BEFORE DELETE ON managed_artifact_intakes BEGIN SELECT RAISE(ABORT,'artifact_intake_immutable'); END;
CREATE TRIGGER artifact_intake_initial BEFORE INSERT ON managed_artifact_intakes WHEN NEW.phase!='prepared' BEGIN SELECT RAISE(ABORT,'artifact_intake_initial'); END;
CREATE TRIGGER artifact_intake_transition BEFORE UPDATE OF phase ON managed_artifact_intakes WHEN NOT (NEW.phase=OLD.phase OR (OLD.phase='prepared' AND NEW.phase='committed')) BEGIN SELECT RAISE(ABORT,'artifact_intake_transition'); END;
CREATE TABLE managed_artifact_refs(
 id TEXT PRIMARY KEY,reservation_id TEXT NOT NULL REFERENCES managed_artifact_reservations(id),
 run_id TEXT NOT NULL REFERENCES runs(id),slot TEXT NOT NULL,
 intake_id TEXT UNIQUE REFERENCES managed_artifact_intakes(id),
 proof_json TEXT NOT NULL CHECK(length(CAST(proof_json AS BLOB))<=4096),
 UNIQUE(reservation_id,slot));
CREATE TRIGGER artifact_ref_immutable BEFORE UPDATE ON managed_artifact_refs BEGIN SELECT RAISE(ABORT,'artifact_ref_immutable'); END;
CREATE TRIGGER artifact_ref_retained BEFORE DELETE ON managed_artifact_refs BEGIN SELECT RAISE(ABORT,'artifact_ref_immutable'); END;
CREATE TRIGGER artifact_report_ref_binding BEFORE INSERT ON managed_artifact_refs
WHEN NEW.intake_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM managed_artifact_intakes i JOIN managed_artifact_reservations r ON r.id=i.reservation_id
 WHERE i.id=NEW.intake_id AND NEW.id=i.id AND NEW.reservation_id=i.reservation_id AND NEW.run_id=i.run_id AND NEW.slot='report'
 AND i.phase='prepared' AND r.state='reserved' AND r.retained_bytes=0 AND r.class='report'
 AND json_valid(NEW.proof_json) AND json_extract(NEW.proof_json,'$.intake_id')=i.id
 AND json_extract(NEW.proof_json,'$.descriptor_sha256')=r.namespace_sha256
 AND json_type(NEW.proof_json,'$.size')='integer' AND json_extract(NEW.proof_json,'$.size')=r.reserved_bytes
 AND json_extract(NEW.proof_json,'$.sha256')=json_extract(i.descriptor_json,'$.sha256'))
BEGIN SELECT RAISE(ABORT,'artifact_ref_binding'); END;
CREATE TRIGGER artifact_report_intake_completion BEFORE UPDATE OF phase ON managed_artifact_intakes
WHEN NEW.phase='committed' AND NOT EXISTS(
 SELECT 1 FROM managed_artifact_refs f JOIN managed_artifact_reservations r ON r.id=f.reservation_id
 JOIN report_events e ON e.capability_id=NEW.capability_id AND e.sequence=NEW.sequence AND e.event_id=NEW.event_id AND e.kind=NEW.kind AND e.payload_hash=NEW.payload_hash
 WHERE f.intake_id=NEW.id AND f.id=NEW.id AND f.slot='report' AND f.reservation_id=NEW.reservation_id
 AND r.state='active' AND r.retained_bytes=r.reserved_bytes
 AND json_extract(e.body_json,'$.artifact.path')=json_extract(f.proof_json,'$.path')
 AND json_extract(e.body_json,'$.artifact.sha256')=json_extract(f.proof_json,'$.sha256')
 AND json_extract(e.body_json,'$.artifact.size')=r.retained_bytes)
BEGIN SELECT RAISE(ABORT,'artifact_intake_completion'); END;
`)
	if err != nil {
		return err
	}
	for _, table := range []struct {
		name     string
		columns  []string
		physical string
		mutable  bool
	}{
		{"managed_artifact_intakes", []string{"id", "reservation_id", "run_id", "task_id", "capability_id", "event_id", "kind", "payload_hash", "descriptor_json", "phase", "created_at"}, "descriptor_json", true},
		{"managed_artifact_refs", []string{"id", "reservation_id", "run_id", "slot", "intake_id", "proof_json"}, "", false},
	} {
		cost := func(ref string) string {
			out := "64"
			for _, column := range table.columns {
				out += "+COALESCE(length(CAST(" + ref + "." + column + " AS BLOB)),0)"
			}
			// The immutable descriptor exists both in SQLite and on disk.
			if table.physical != "" {
				out += "+length(CAST(" + ref + "." + table.physical + " AS BLOB))"
			}
			return out
		}
		operations := []string{"INSERT"}
		if table.mutable {
			operations = append(operations, "UPDATE")
		}
		for _, op := range operations {
			delta := cost("NEW")
			if op == "UPDATE" {
				delta = "(" + cost("NEW") + ")-(" + cost("OLD") + ")"
			}
			body := ""
			for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", "NEW.run_id"}} {
				body += fmt.Sprintf("INSERT OR IGNORE INTO storage_usage VALUES('%s',%s,0,0); UPDATE storage_usage SET total_bytes=total_bytes+(%s) WHERE scope='%s' AND scope_id=%s;", scope.name, scope.id, delta, scope.name, scope.id)
			}
			if _, err = tx.Exec("CREATE TRIGGER quota_" + table.name + "_" + op + " AFTER " + op + " ON " + table.name + " BEGIN " + body + " END"); err != nil {
				return err
			}
		}
	}
	return nil
}
