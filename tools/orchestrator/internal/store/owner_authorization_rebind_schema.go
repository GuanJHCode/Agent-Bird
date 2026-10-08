package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

var ownerRebindTextColumns = []string{"run_id", "host_generation", "old_host_generation", "origin_birth", "attachment_proof_sha256", "created_at"}
var ownerAuthorizationRebindTextColumns = []string{"authorization_id", "run_id", "launch_id", "old_host_generation", "host_generation", "origin_context_id", "origin_birth", "attachment_proof_sha256", "source_created_at"}

const ownerAuthorizationRebindView = `CREATE VIEW owner_succession_effective_targets AS
WITH RECURSIVE chain(authorization_id,run_id,launch_id,hop,origin_context_id,origin_pid,origin_birth,host_generation,derivation_epoch) AS (
 SELECT authorization_id,run_id,launch_id,0,successor_origin_context_id,successor_origin_pid,successor_origin_birth,successor_host_generation,coordinator_epoch FROM owner_succession_authorizations
 UNION ALL
 SELECT p.authorization_id,p.run_id,p.launch_id,p.hop,p.origin_context_id,p.origin_pid,p.origin_birth,p.host_generation,p.derivation_epoch
 FROM chain c JOIN owner_succession_rebind_proofs p ON p.authorization_id=c.authorization_id AND p.hop=c.hop+1
 JOIN owner_rebinds r ON r.run_id=p.run_id AND r.host_generation=p.host_generation
 WHERE c.hop<64 AND p.run_id=c.run_id AND p.launch_id=c.launch_id AND p.old_host_generation=c.host_generation
 AND p.origin_context_id=c.origin_context_id AND p.derivation_epoch>=c.derivation_epoch
 AND r.old_host_generation=p.old_host_generation AND r.origin_pid=p.origin_pid AND r.origin_birth=p.origin_birth
 AND r.attachment_proof_sha256=p.attachment_proof_sha256 AND r.created_at=p.source_created_at)
SELECT * FROM chain c WHERE c.hop=(SELECT COUNT(*) FROM owner_succession_rebind_proofs p WHERE p.authorization_id=c.authorization_id)`

func ownerAuthorizationRebindTriggerChange() (string, string) {
	old := `SELECT 1 FROM owner_succession_authorizations a JOIN execution_owners p ON p.owner_id=a.predecessor_owner_id`
	return old, old + ` JOIN owner_succession_effective_targets target ON target.authorization_id=a.authorization_id`
}

func (d *DB) migrateOwnerAuthorizationRebindSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE owner_succession_rebind_proofs(
 authorization_id TEXT NOT NULL REFERENCES owner_succession_authorizations(authorization_id),
 run_id TEXT NOT NULL REFERENCES runs(id), launch_id TEXT NOT NULL REFERENCES host_launches(id),
 hop INTEGER NOT NULL CHECK(hop BETWEEN 1 AND 64),
 old_host_generation TEXT NOT NULL CHECK(length(old_host_generation)>0),
 host_generation TEXT NOT NULL CHECK(length(host_generation)>0 AND host_generation!=old_host_generation),
 origin_context_id TEXT NOT NULL CHECK(length(origin_context_id)>0), origin_pid INTEGER NOT NULL CHECK(origin_pid>0),
 origin_birth TEXT NOT NULL CHECK(length(origin_birth)>0),
 attachment_proof_sha256 TEXT NOT NULL CHECK(length(attachment_proof_sha256)=64 AND attachment_proof_sha256 NOT GLOB '*[^0-9a-f]*'),
 derivation_epoch INTEGER NOT NULL CHECK(derivation_epoch>0), source_created_at TEXT NOT NULL CHECK(length(source_created_at)>0),
 PRIMARY KEY(authorization_id,hop), UNIQUE(authorization_id,old_host_generation), UNIQUE(authorization_id,host_generation),
 FOREIGN KEY(run_id,host_generation) REFERENCES owner_rebinds(run_id,host_generation));
 CREATE INDEX owner_succession_run ON owner_succession_authorizations(run_id);
 CREATE INDEX owner_succession_rebind_source ON owner_succession_rebind_proofs(run_id,host_generation);
 CREATE TRIGGER owner_succession_rebind_no_update BEFORE UPDATE ON owner_succession_rebind_proofs BEGIN SELECT RAISE(ABORT,'owner_succession_rebind_immutable'); END;
 CREATE TRIGGER owner_succession_rebind_no_delete BEFORE DELETE ON owner_succession_rebind_proofs BEGIN SELECT RAISE(ABORT,'owner_succession_rebind_immutable'); END;
 CREATE TRIGGER owner_succession_rebind_source_update BEFORE UPDATE ON owner_rebinds WHEN EXISTS(SELECT 1 FROM owner_succession_rebind_proofs p WHERE p.run_id=OLD.run_id AND p.host_generation=OLD.host_generation) BEGIN SELECT RAISE(ABORT,'owner_succession_rebind_immutable'); END;
 CREATE TRIGGER owner_succession_rebind_source_delete BEFORE DELETE ON owner_rebinds WHEN EXISTS(SELECT 1 FROM owner_succession_rebind_proofs p WHERE p.run_id=OLD.run_id AND p.host_generation=OLD.host_generation) BEGIN SELECT RAISE(ABORT,'owner_succession_rebind_immutable'); END;`)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ownerAuthorizationRebindView); err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE TRIGGER owner_succession_rebind_proven BEFORE INSERT ON owner_succession_rebind_proofs
 WHEN EXISTS(SELECT 1 FROM execution_owners o WHERE o.succession_authorization_id=NEW.authorization_id)
 OR NOT EXISTS(SELECT 1 FROM owner_succession_authorizations a JOIN owner_succession_effective_targets t ON t.authorization_id=a.authorization_id
 JOIN owner_rebinds r ON r.run_id=a.run_id AND r.host_generation=NEW.host_generation
 WHERE a.authorization_id=NEW.authorization_id AND a.run_id=NEW.run_id AND a.launch_id=NEW.launch_id
 AND NEW.hop=t.hop+1 AND NEW.old_host_generation=t.host_generation AND NEW.host_generation!=a.successor_host_generation
 AND NEW.origin_context_id=t.origin_context_id AND NEW.derivation_epoch>=t.derivation_epoch
 AND NEW.derivation_epoch=(SELECT epoch FROM coordinator WHERE id=1)
 AND r.old_host_generation=NEW.old_host_generation AND r.origin_pid=NEW.origin_pid AND r.origin_birth=NEW.origin_birth
 AND r.attachment_proof_sha256=NEW.attachment_proof_sha256 AND r.created_at=NEW.source_created_at)
 BEGIN SELECT RAISE(ABORT,'owner_succession_rebind_unproven'); END;`)
	if err != nil {
		return err
	}
	var body string
	if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='execution_owner_succession_insert'`).Scan(&body); err != nil {
		return err
	}
	old, replacement := ownerAuthorizationRebindTriggerChange()
	changes := [][2]string{{old, replacement},
		{"AND NEW.coordinator_epoch>=a.coordinator_epoch", "AND NEW.coordinator_epoch>=a.coordinator_epoch AND NEW.coordinator_epoch>=target.derivation_epoch"},
		{"=a.successor_origin_context_id", "=target.origin_context_id"}, {"=a.successor_origin_pid", "=target.origin_pid"},
		{"=a.successor_origin_birth", "=target.origin_birth"}, {"=a.successor_host_generation", "=target.host_generation"}}
	for _, c := range changes {
		if strings.Count(body, c[0]) != 1 {
			return CodeError("owner_succession_rebind_schema_mismatch")
		}
		body = strings.Replace(body, c[0], c[1], 1)
	}
	if _, err = tx.Exec(`DROP TRIGGER execution_owner_succession_insert;` + body); err != nil {
		return err
	}
	for _, table := range []struct {
		name, prefix string
		columns      []string
		fixed        int
	}{
		{"owner_rebinds", "quota_owner_rebind", ownerRebindTextColumns, 8},
		{"owner_succession_rebind_proofs", "quota_owner_succession_rebind", ownerAuthorizationRebindTextColumns, 24},
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
					cost := originalOwnerBytesSQL(ref+".", table.columns, table.fixed)
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
	return d.backfillOwnerAuthorizationRebinds(context.Background(), tx)
}
