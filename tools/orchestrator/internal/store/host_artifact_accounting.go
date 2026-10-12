package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// Match the original immutable ref at INSERT, never when refunding old rows.
func managedHostRuntimeRef(column string) string {
	return `EXISTS(SELECT 1 FROM managed_artifact_refs f JOIN managed_artifact_reservations r ON r.id=f.reservation_id WHERE f.manifest_bytes>0 AND f.intake_id IS NULL
 AND f.proof_json=json_extract(` + column + `,'$.artifact.host_artifact')
 AND json_extract(f.proof_json,'$.proof.path')=json_extract(` + column + `,'$.artifact.path')
 AND json_extract(f.proof_json,'$.proof.size')=json_extract(` + column + `,'$.artifact.size')
 AND json_extract(f.proof_json,'$.proof.sha256')=json_extract(` + column + `,'$.artifact.sha256')
 AND r.run_id=json_extract(` + column + `,'$.run_id') AND r.task_id=json_extract(` + column + `,'$.task_id')
 AND r.attempt_id=json_extract(` + column + `,'$.attempt_id') AND r.scope_key=json_extract(` + column + `,'$.segment_id')
 AND r.producer_id=json_extract(` + column + `,'$.producer_id'))`
}

// Freeze the raw-charge decision per row. A future ref cannot retroactively
// change the refund of a historical, unverified artifact claim.
func managedHostRuntimeBytes(column string) string {
	row := strings.TrimSuffix(column, ".body_json")
	return "length(CAST(" + column + " AS BLOB))+CASE WHEN json_valid(" + column + ") THEN CASE WHEN json_extract(" + column + ",'$.kind')='exited' OR " + row + ".host_artifact_verified=1 THEN 0 ELSE COALESCE(json_extract(" + column + ",'$.artifact.size'),0) END ELSE 0 END"
}

func migrateHostRuntimeAccounting(tx *sql.Tx) error {
	if _, err := tx.Exec(`ALTER TABLE runtime_events ADD COLUMN host_artifact_verified INTEGER NOT NULL DEFAULT 0 CHECK(typeof(host_artifact_verified)='integer' AND host_artifact_verified IN (0,1));
 CREATE TRIGGER runtime_host_artifact_charge_immutable BEFORE UPDATE OF host_artifact_verified ON runtime_events BEGIN SELECT RAISE(ABORT,'runtime_host_artifact_charge_immutable'); END;`); err != nil {
		return err
	}
	if _, err := tx.Exec("CREATE TRIGGER runtime_host_artifact_charge_binding BEFORE INSERT ON runtime_events WHEN NEW.host_artifact_verified=1 AND NOT (" + managedHostRuntimeRef("NEW.body_json") + ") BEGIN SELECT RAISE(ABORT,'runtime_host_artifact_charge_binding'); END;"); err != nil {
		return err
	}

	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		name := "quota_runtime_events_" + op
		if _, err := tx.Exec("DROP TRIGGER " + name); err != nil {
			return err
		}
		refs := []string{"NEW"}
		if op == "DELETE" {
			refs = []string{"OLD"}
		} else if op == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		}
		body := ""
		for _, ref := range refs {
			cost := "(" + managedHostRuntimeBytes(ref+".body_json") + ")"
			for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", "(SELECT run_id FROM tasks WHERE id=" + ref + ".task_id)"}} {
				if ref == "OLD" {
					body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-%s WHERE scope='%s' AND scope_id=%s;", cost, scope.name, scope.id)
				} else {
					body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,%s,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, cost)
				}
			}
		}
		when := op
		if op == "UPDATE" {
			when += " OF body_json,task_id"
		}
		if _, err := tx.Exec("CREATE TRIGGER " + name + " AFTER " + when + " ON runtime_events BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	// Host progress ref metadata belongs to the progress control allowance;
	// raw snapshot bytes have their separate fixed slot reservation.
	if _, err := tx.Exec(`DROP TRIGGER quota_managed_artifact_refs_INSERT`); err != nil {
		return err
	}
	cost := "64"
	for _, column := range []string{"id", "reservation_id", "run_id", "slot", "intake_id", "proof_json"} {
		cost += "+COALESCE(length(CAST(NEW." + column + " AS BLOB)),0)"
	}
	progress := "CASE WHEN NEW.manifest_bytes>0 AND EXISTS(SELECT 1 FROM managed_artifact_reservations r WHERE r.id=NEW.reservation_id AND r.class='progress') THEN (" + cost + ") ELSE 0 END"
	body := ""
	for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", "NEW.run_id"}} {
		body += fmt.Sprintf("INSERT OR IGNORE INTO storage_usage VALUES('%s',%s,0,0); UPDATE storage_usage SET total_bytes=total_bytes+(%s),progress_bytes=progress_bytes+(%s) WHERE scope='%s' AND scope_id=%s;", scope.name, scope.id, cost, progress, scope.name, scope.id)
	}
	if _, err := tx.Exec("CREATE TRIGGER quota_managed_artifact_refs_INSERT AFTER INSERT ON managed_artifact_refs BEGIN " + body + " END"); err != nil {
		return err
	}
	return nil
}
