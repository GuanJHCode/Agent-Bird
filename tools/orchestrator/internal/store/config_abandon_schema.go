package store

import (
	"database/sql"
	"fmt"
)

func migrateConfigAbandons(tx *sql.Tx) error {
	if _, e := tx.Exec(`CREATE TABLE config_preparation_abandons(
run_id TEXT PRIMARY KEY, use_json TEXT NOT NULL CHECK(length(CAST(use_json AS BLOB)) BETWEEN 1 AND 8192),
use_sha256 TEXT NOT NULL CHECK(length(use_sha256)=64),created_at TEXT NOT NULL CHECK(length(created_at) BETWEEN 1 AND 64),
receipt_sha256 TEXT NOT NULL CHECK(length(receipt_sha256)=64));`); e != nil {
		return e
	}
	cost := func(ref string) string {
		return "16+length(CAST(" + ref + ".run_id AS BLOB))+length(CAST(" + ref + ".use_json AS BLOB))+length(CAST(" + ref + ".use_sha256 AS BLOB))+length(CAST(" + ref + ".created_at AS BLOB))+length(CAST(" + ref + ".receipt_sha256 AS BLOB))"
	}
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		refs := []string{"NEW"}
		if op == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		} else if op == "DELETE" {
			refs = []string{"OLD"}
		}
		body := ""
		for _, ref := range refs {
			for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
				if ref == "OLD" {
					body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-(%s) WHERE scope='%s' AND scope_id=%s;", cost(ref), scope.name, scope.id)
				} else {
					body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, cost(ref))
				}
			}
		}
		if _, e := tx.Exec("CREATE TRIGGER quota_config_abandon_" + op + " AFTER " + op + " ON config_preparation_abandons BEGIN " + body + " END"); e != nil {
			return e
		}
	}
	// There is no old table at schema13, hence no historical abandon rows to
	// backfill. Every new row (including first receipt before runs exists) is
	// charged to both scopes by the same transactional trigger.
	return nil
}
