package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestBridgeV9MigrationCreatesFiveTablesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"bridge_bindings", "bridge_ingress", "bridge_batches", "bridge_batch_items", "bridge_delivery_attempts"} {
		var count int
		if err := db.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s missing: %d %v", table, count, err)
		}
	}
	var version int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("reopen version=%d err=%v", version, err)
	}
}

func TestBridgeV8UpgradeFailureRollsBackDDLAndKeepsOldRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES('old','c',1,'o',1,'b','queued','2026-01-01T00:00:00Z','collect')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`DROP TABLE config_preparation_abandons; DROP TABLE decision_attempt_bindings; DROP TABLE owner_control_changes; DROP TABLE execution_owners; ALTER TABLE host_launches DROP COLUMN execution_owner_mode; ALTER TABLE owner_rebinds DROP COLUMN old_host_generation; DROP TABLE continuation_decisions; DROP TABLE attempt_provider_bindings; DROP TABLE bridge_delivery_attempts; DROP TABLE bridge_batch_items; DROP TABLE bridge_batches; DROP TABLE bridge_ingress; DROP TABLE bridge_bindings; ALTER TABLE schema_meta DROP COLUMN bridge_cutover_at; UPDATE schema_meta SET version=8; CREATE TABLE bridge_batches(blocker TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path); err == nil {
		reopened.Close()
		t.Fatal("partial upgrade succeeded")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version, count, columns int
	if err = raw.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != 8 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err = raw.QueryRow(`SELECT COUNT(*) FROM runs WHERE id='old'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("old row=%d err=%v", count, err)
	}
	if err = raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bridge_bindings'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial table=%d err=%v", count, err)
	}
	if err = raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('schema_meta') WHERE name='bridge_cutover_at'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("partial column=%d err=%v", columns, err)
	}
	if _, err = raw.Exec(`DROP TABLE bridge_batches`); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("recovered version=%d err=%v", version, err)
	}
}

func TestBridgeFiveTableQuotaTriggersCountRawUTF8AndRollback(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.sql.Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES('r','c',1,'o',1,'b','queued','2026-01-01T00:00:00Z','collect')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO tasks(id,run_id,status,dependencies_json,max_attempts,created_at) VALUES('task','r','queued','[]',1,'now')`); err != nil {
		t.Fatal(err)
	}
	before := bridgeUsage(t, db.sql, "global", "")
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO bridge_bindings(binding_id,run_id,provider_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES('b','r','claude','o',1,'b',2,'n',?,1,'owner','hook_pull',?,'ACTIVE',?,'/tmp/x','request','now','now')`, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	last := bridgeUsageTx(t, tx, "global", "")
	if last <= before {
		t.Fatal("binding uncounted")
	}
	if _, err := tx.Exec(`INSERT INTO bridge_ingress(binding_id,event_id,payload_hash,normalized_json,kind,native_session_id,created_at) VALUES('b','e',?,'{"summary":"界"}','tool_observed','s','now')`, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	current := bridgeUsageTx(t, tx, "global", "")
	if current <= last || current-last < int64(len([]byte("界"))) {
		t.Fatal("ingress UTF8 uncounted")
	}
	last = current
	if _, err := tx.Exec(`INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,created_at) VALUES('batch','b',1,x'010203',?,'now')`, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	current = bridgeUsageTx(t, tx, "global", "")
	if current <= last {
		t.Fatal("batch uncounted")
	}
	last = current
	if _, err := tx.Exec(`INSERT INTO bridge_batch_items(batch_id,position,event_id,event_revision,event_hash,action_slot,task_id,work_revision) VALUES('batch',0,'event',1,?,'slot','task',1)`, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	current = bridgeUsageTx(t, tx, "global", "")
	if current <= last {
		t.Fatal("item uncounted")
	}
	last = current
	if _, err := tx.Exec(`INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,wire_bytes,wire_sha256,created_at) VALUES('attempt','batch','b',1,1,?,'CLAIMED','later',2,'n',x'ff',?,'now')`, strings.Repeat("0", 64), strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	current = bridgeUsageTx(t, tx, "global", "")
	if current <= last {
		t.Fatal("attempt uncounted")
	}
	if _, err := tx.Exec(`UPDATE bridge_delivery_attempts SET wire_bytes=? WHERE attempt_id='attempt'`, []byte("界界")); err != nil {
		t.Fatal(err)
	}
	if got := bridgeUsageTx(t, tx, "global", ""); got != current+int64(len([]byte("界界")))-1 {
		t.Fatalf("mutable wire count=%d before=%d", got, current)
	}
	after := bridgeUsageTx(t, tx, "global", "")
	if after-before < int64(len([]byte("界"))) {
		t.Fatalf("UTF8 not counted: %d", after-before)
	}
	run := bridgeUsageTx(t, tx, "run", "r")
	if run != after-before {
		t.Fatalf("run/global delta differs: run=%d global=%d", run, after-before)
	}
	var progress int64
	if err := tx.QueryRow(`SELECT progress_bytes FROM storage_usage WHERE scope='run' AND scope_id='r'`).Scan(&progress); err != nil {
		t.Fatal(err)
	}
	db.runProgressLimit = progress + 1
	if err := db.storageAdmission(context.Background(), tx, "r", 1, true); err != nil {
		t.Fatalf("quota boundary exact rejected: %v", err)
	}
	if err := db.storageAdmission(context.Background(), tx, "r", 2, true); err == nil {
		t.Fatal("quota boundary +1 admitted")
	}
	for _, deletion := range []struct {
		table, predicate string
	}{
		{"bridge_delivery_attempts", "attempt_id='attempt'"},
		{"bridge_batch_items", "batch_id='batch'"},
		{"bridge_batches", "batch_id='batch'"},
		{"bridge_ingress", "binding_id='b'"},
		{"bridge_bindings", "binding_id='b'"},
	} {
		previous := bridgeUsageTx(t, tx, "global", "")
		if _, err := tx.Exec("DELETE FROM " + deletion.table + " WHERE " + deletion.predicate); err != nil {
			t.Fatal(err)
		}
		if got := bridgeUsageTx(t, tx, "global", ""); got >= previous {
			t.Fatalf("%s deletion did not refund quota: before=%d after=%d", deletion.table, previous, got)
		}
	}
	if got := bridgeUsageTx(t, tx, "global", ""); got != before {
		t.Fatalf("five-table deletion left quota=%d before=%d", got, before)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := bridgeUsage(t, db.sql, "global", ""); got != before {
		t.Fatalf("rollback usage=%d before=%d", got, before)
	}
}

func bridgeUsage(t *testing.T, db *sql.DB, scope, id string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope=? AND scope_id=?`, scope, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func bridgeUsageTx(t *testing.T, tx *sql.Tx, scope, id string) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope=? AND scope_id=?`, scope, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
