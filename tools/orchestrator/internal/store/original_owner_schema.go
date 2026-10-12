package store

import (
	"database/sql"
	"fmt"
	"strings"
)

var originalOwnerTextColumns = []string{"owner_id", "launch_id", "run_id", "producer_id", "bootstrap_id", "bootstrap_token_hash", "coordinator_key_id", "request_id", "request_hash", "intent_json", "phase", "owner_birth", "owner_public_key", "current_host_birth", "original_binding_json", "original_binding_hash", "created_at", "updated_at"}
var originalOwnerControlTextColumns = []string{"owner_id", "run_id", "request_id", "request_hash", "old_host_birth", "new_host_birth", "scope_hash", "owner_identity_hash", "coordinator_key_id", "permit_id", "permit_json", "permit_hash", "created_at"}

func originalOwnerBytesSQL(ref string, columns []string, fixed int) string {
	parts := []string{fmt.Sprint(fixed)}
	for _, column := range columns {
		parts = append(parts, "length(CAST(COALESCE("+ref+column+",'') AS BLOB))")
	}
	return strings.Join(parts, "+")
}

func migrateOriginalOwnerSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
ALTER TABLE host_launches ADD COLUMN execution_owner_mode TEXT NOT NULL DEFAULT 'legacy' CHECK(execution_owner_mode IN ('legacy','supervised-v1'));
CREATE TABLE execution_owners(
 owner_id TEXT PRIMARY KEY, launch_id TEXT NOT NULL REFERENCES host_launches(id), run_id TEXT NOT NULL REFERENCES runs(id), producer_id TEXT NOT NULL REFERENCES runtime_hosts(id),
 incarnation INTEGER NOT NULL CHECK(incarnation>0), bootstrap_id TEXT NOT NULL UNIQUE, bootstrap_token_hash TEXT NOT NULL CHECK(length(bootstrap_token_hash)=64),
 coordinator_key_id TEXT NOT NULL CHECK(length(coordinator_key_id)=64), request_id TEXT NOT NULL UNIQUE, request_hash TEXT NOT NULL CHECK(length(request_hash)=64),
 intent_json TEXT NOT NULL CHECK(json_valid(intent_json)), phase TEXT NOT NULL CHECK(phase IN ('intended','bound','retired')), is_current INTEGER NOT NULL CHECK(is_current IN (0,1)),
 owner_pid INTEGER, owner_birth TEXT, owner_public_key TEXT, control_revision INTEGER NOT NULL CHECK(control_revision>=0), current_host_pid INTEGER, current_host_birth TEXT,
 coordinator_epoch INTEGER NOT NULL CHECK(coordinator_epoch>0), original_binding_json TEXT, original_binding_hash TEXT,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(launch_id,incarnation),
 CHECK((phase='intended' AND control_revision=0 AND owner_pid IS NULL AND owner_birth IS NULL AND owner_public_key IS NULL AND current_host_pid IS NULL AND current_host_birth IS NULL AND original_binding_json IS NULL AND original_binding_hash IS NULL)
 OR (phase IN ('bound','retired') AND control_revision>0 AND owner_pid IS NOT NULL AND owner_pid>0 AND owner_birth IS NOT NULL AND length(owner_birth)>0 AND owner_public_key IS NOT NULL AND length(owner_public_key)=64 AND current_host_pid IS NOT NULL AND current_host_pid>0 AND current_host_birth IS NOT NULL AND length(current_host_birth)>0 AND original_binding_json IS NOT NULL AND json_valid(original_binding_json) AND original_binding_hash IS NOT NULL AND length(original_binding_hash)=64)),
 CHECK(phase!='retired' OR is_current=0)
);
CREATE UNIQUE INDEX execution_owner_current_launch ON execution_owners(launch_id) WHERE is_current=1;
CREATE TABLE owner_control_changes(
 owner_id TEXT NOT NULL REFERENCES execution_owners(owner_id), run_id TEXT NOT NULL REFERENCES runs(id), revision INTEGER NOT NULL CHECK(revision>0),
 request_id TEXT NOT NULL UNIQUE, request_hash TEXT NOT NULL CHECK(length(request_hash)=64), expected_revision INTEGER NOT NULL CHECK(expected_revision=revision-1),
 old_host_pid INTEGER, old_host_birth TEXT, new_host_pid INTEGER NOT NULL CHECK(new_host_pid>0), new_host_birth TEXT NOT NULL CHECK(length(new_host_birth)>0),
 scope_hash TEXT NOT NULL CHECK(length(scope_hash)=64), owner_identity_hash TEXT NOT NULL CHECK(length(owner_identity_hash)=64), coordinator_epoch INTEGER NOT NULL CHECK(coordinator_epoch>0),
 coordinator_key_id TEXT NOT NULL CHECK(length(coordinator_key_id)=64), permit_id TEXT NOT NULL UNIQUE, permit_json TEXT NOT NULL CHECK(json_valid(permit_json)), permit_hash TEXT NOT NULL CHECK(length(permit_hash)=64), created_at TEXT NOT NULL,
 PRIMARY KEY(owner_id,revision),
 CHECK((expected_revision=0 AND old_host_pid IS NULL AND old_host_birth IS NULL) OR (expected_revision>0 AND old_host_pid IS NOT NULL AND old_host_pid>0 AND old_host_birth IS NOT NULL AND length(old_host_birth)>0))
);`)
	if err != nil {
		return err
	}
	for _, table := range []struct {
		name    string
		columns []string
		fixed   int
	}{{"execution_owners", originalOwnerTextColumns, 64}, {"owner_control_changes", originalOwnerControlTextColumns, 48}} {
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
				cost := originalOwnerBytesSQL(ref+".", table.columns, table.fixed)
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
	return nil
}
