package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// migrateBridgeSchema runs inside migrateRuntime's transaction. All five bridge
// tables and their quota triggers become visible together with schema version 9.
func migrateBridgeSchema(tx *sql.Tx) error {
	// A few legacy migration fixtures lower schema_meta after a newer schema
	// was already installed. Recognize only the complete v9 shape; a partial
	// upgrade still fails and rolls back in migrateRuntime's transaction.
	var tables, triggers, cutover int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('bridge_bindings','bridge_ingress','bridge_batches','bridge_batch_items','bridge_delivery_attempts')`).Scan(&tables); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'quota_bridge_%'`).Scan(&triggers); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('schema_meta') WHERE name='bridge_cutover_at'`).Scan(&cutover); err != nil {
		return err
	}
	if tables == 5 && triggers == 15 && cutover == 1 {
		return nil
	}
	if _, err := tx.Exec(`ALTER TABLE schema_meta ADD COLUMN bridge_cutover_at TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE schema_meta SET bridge_cutover_at=?`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	_, err := tx.Exec(`
CREATE TABLE bridge_bindings(
  binding_id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), provider_id TEXT NOT NULL,
  native_session_id TEXT NOT NULL DEFAULT '', owner_context_id TEXT NOT NULL,
  owner_pid INTEGER NOT NULL, owner_birth TEXT NOT NULL, native_pid INTEGER NOT NULL,
  native_birth TEXT NOT NULL, native_executable_sha256 TEXT NOT NULL, owner_epoch INTEGER NOT NULL CHECK(owner_epoch>0),
  role TEXT NOT NULL CHECK(role='owner'), transport_mode TEXT NOT NULL CHECK(transport_mode='hook_pull'),
  manifest_sha256 TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('PENDING_SESSION','SESSION_OBSERVED','ACTIVE','DETACHED','STALE')),
  candidate_observation_event_id TEXT NOT NULL DEFAULT '', candidate_observation_hash TEXT NOT NULL DEFAULT '',
  hook_token_hash TEXT NOT NULL, capability_file TEXT NOT NULL, request_id TEXT NOT NULL,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX bridge_one_live_binding ON bridge_bindings(run_id,owner_context_id)
  WHERE status IN ('PENDING_SESSION','SESSION_OBSERVED','ACTIVE');
CREATE UNIQUE INDEX bridge_binding_epoch ON bridge_bindings(run_id,owner_context_id,owner_epoch);
CREATE TABLE bridge_ingress(
  binding_id TEXT NOT NULL REFERENCES bridge_bindings(binding_id), event_id TEXT NOT NULL,
  payload_hash TEXT NOT NULL, normalized_json BLOB NOT NULL, kind TEXT NOT NULL,
  native_session_id TEXT NOT NULL, created_at TEXT NOT NULL,
  PRIMARY KEY(binding_id,event_id)
);
CREATE TABLE bridge_batches(
  batch_id TEXT PRIMARY KEY, binding_id TEXT NOT NULL REFERENCES bridge_bindings(binding_id),
  owner_epoch INTEGER NOT NULL, content_bytes BLOB NOT NULL, render_sha256 TEXT NOT NULL,
  events_json BLOB NOT NULL DEFAULT '[]', receipts_json BLOB NOT NULL DEFAULT '[]', created_at TEXT NOT NULL
);
CREATE TABLE bridge_batch_items(
  batch_id TEXT NOT NULL REFERENCES bridge_batches(batch_id), position INTEGER NOT NULL,
  event_id TEXT NOT NULL, event_revision INTEGER NOT NULL, event_hash TEXT NOT NULL,
  action_slot TEXT NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id), work_revision INTEGER NOT NULL,
  PRIMARY KEY(batch_id,position), UNIQUE(batch_id,event_id,event_revision)
);
CREATE TABLE bridge_delivery_attempts(
  attempt_id TEXT PRIMARY KEY, batch_id TEXT NOT NULL REFERENCES bridge_batches(batch_id),
  binding_id TEXT NOT NULL REFERENCES bridge_bindings(binding_id), owner_epoch INTEGER NOT NULL,
  attempt_no INTEGER NOT NULL CHECK(attempt_no>0), claim_token_hash TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('CLAIMED','EMITTING','OFFERED','UNCERTAIN','EXPIRED','RECEIVED','SUPERSEDED_UNCERTAIN','STALE')),
  deadline_at TEXT NOT NULL, sender_pid INTEGER NOT NULL, sender_birth TEXT NOT NULL,
  wire_bytes BLOB, wire_sha256 TEXT NOT NULL DEFAULT '', receipt_id TEXT NOT NULL DEFAULT '',
  received_at TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT '',
  UNIQUE(batch_id,attempt_no)
);
CREATE UNIQUE INDEX bridge_one_unresolved_attempt ON bridge_delivery_attempts(binding_id)
  WHERE state IN ('CLAIMED','EMITTING','OFFERED','UNCERTAIN');
CREATE INDEX bridge_ingress_kind ON bridge_ingress(binding_id,kind,created_at);
CREATE INDEX bridge_batch_binding ON bridge_batches(binding_id,created_at);
`)
	if err != nil {
		return err
	}
	return bridgeQuotaTriggers(tx)
}

type bridgeQuotaTable struct {
	name     string
	text     []string
	numbers  []string
	runID    func(string) string
	progress bool
}

var bridgeQuotaTables = []bridgeQuotaTable{
	{"bridge_bindings", []string{"binding_id", "run_id", "provider_id", "native_session_id", "owner_context_id", "owner_birth", "native_birth", "native_executable_sha256", "role", "transport_mode", "manifest_sha256", "status", "candidate_observation_event_id", "candidate_observation_hash", "hook_token_hash", "capability_file", "request_id", "created_at", "updated_at"}, []string{"owner_pid", "native_pid", "owner_epoch"}, func(ref string) string { return ref + ".run_id" }, false},
	{"bridge_ingress", []string{"binding_id", "event_id", "payload_hash", "normalized_json", "kind", "native_session_id", "created_at"}, nil, bridgeRunForBinding, true},
	{"bridge_batches", []string{"batch_id", "binding_id", "content_bytes", "render_sha256", "events_json", "receipts_json", "created_at"}, []string{"owner_epoch"}, bridgeRunForBinding, false},
	{"bridge_batch_items", []string{"batch_id", "event_id", "event_hash", "action_slot", "task_id"}, []string{"position", "event_revision", "work_revision"}, func(ref string) string {
		return "(SELECT b.run_id FROM bridge_bindings b JOIN bridge_batches x ON x.binding_id=b.binding_id WHERE x.batch_id=" + ref + ".batch_id)"
	}, false},
	{"bridge_delivery_attempts", []string{"attempt_id", "batch_id", "binding_id", "claim_token_hash", "state", "deadline_at", "sender_birth", "wire_bytes", "wire_sha256", "receipt_id", "received_at", "created_at", "updated_at"}, []string{"owner_epoch", "attempt_no", "sender_pid"}, bridgeRunForBinding, false},
}

func bridgeRunForBinding(ref string) string {
	return "(SELECT run_id FROM bridge_bindings WHERE binding_id=" + ref + ".binding_id)"
}

func bridgeRowBytes(ref string, table bridgeQuotaTable) string {
	parts := make([]string, 0, len(table.text)+len(table.numbers))
	for _, col := range table.text {
		parts = append(parts, "COALESCE(length(CAST("+ref+"."+col+" AS BLOB)),0)")
	}
	for _, col := range table.numbers {
		parts = append(parts, "CASE WHEN "+ref+"."+col+" IS NULL THEN 0 ELSE 8 END")
	}
	return "(" + strings.Join(parts, "+") + ")"
}

func bridgeQuotaTriggers(tx *sql.Tx) error {
	return bridgeQuotaTriggersWithTables(tx, bridgeQuotaTables)
}

func bridgeQuotaTriggers23(tx *sql.Tx) error {
	tables := append([]bridgeQuotaTable(nil), bridgeQuotaTables...)
	for i := range tables {
		if tables[i].name == "bridge_delivery_attempts" {
			tables[i].text = append(append([]string(nil), tables[i].text...), "acp_session_id", "acp_response_sha256", "acp_stop_reason")
			tables[i].numbers = append(append([]string(nil), tables[i].numbers...), "acp_turn_ordinal", "acp_rpc_request_id")
		}
	}
	return bridgeQuotaTriggersWithTables(tx, tables)
}

func bridgeQuotaTriggersWithTables(tx *sql.Tx, tables []bridgeQuotaTable, names ...string) error {
	for _, table := range tables {
		if len(names) != 0 {
			found := false
			for _, name := range names {
				if table.name == name {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			var body strings.Builder
			refs := []string{"NEW"}
			if op == "DELETE" {
				refs = []string{"OLD"}
			}
			if op == "UPDATE" {
				refs = []string{"OLD", "NEW"}
			}
			for _, ref := range refs {
				sign := "+"
				if ref == "OLD" {
					sign = "-"
				}
				bytes := bridgeRowBytes(ref, table)
				progress := "0"
				if table.progress {
					progress = bytes
				}
				for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", table.runID(ref)}} {
					if ref == "OLD" {
						fmt.Fprintf(&body, "UPDATE storage_usage SET total_bytes=total_bytes-%s,progress_bytes=progress_bytes-%s WHERE scope='%s' AND scope_id=%s;", bytes, progress, scope.name, scope.id)
					} else {
						fmt.Fprintf(&body, "INSERT INTO storage_usage VALUES('%s',%s,%s%s,%s%s) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes,progress_bytes=progress_bytes+excluded.progress_bytes;", scope.name, scope.id, sign, bytes, sign, progress)
					}
				}
			}
			if _, err := tx.Exec("CREATE TRIGGER quota_" + table.name + "_" + op + " AFTER " + op + " ON " + table.name + " BEGIN " + body.String() + " END"); err != nil {
				return err
			}
		}
	}
	return nil
}
