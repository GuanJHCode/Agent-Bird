package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// This catches a migration that only changes the version number or adds fields
// without preserving the schema22 bridge graph and its accounted rows.
func TestACPBridgeSchema23MigratesSchema22BridgeGraph(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeACPBridgeSchema23(t, db)
	insertSchema22BridgeGraph(t, db.sql)
	before := schema23Usage(t, db.sql, "global", "")
	objects := schema23ChildObjects(t, db.sql)
	var rowHash string
	if err := db.sql.QueryRow(`SELECT hex(content_bytes)||':'||render_sha256||':'||hex(wire_bytes)||':'||wire_sha256 FROM bridge_batches b JOIN bridge_delivery_attempts a ON a.batch_id=b.batch_id WHERE a.attempt_id='attempt-界'`).Scan(&rowHash); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen through Open so the actual modernc-backed runtime path upgrades
	// the persisted schema22 fixture.
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != 23 {
		t.Fatalf("version=%d err=%v, want schema23", version, err)
	}
	for _, column := range []string{"acp_session_id", "acp_turn_ordinal", "acp_rpc_request_id", "acp_response_sha256", "acp_stop_reason"} {
		var found int
		if err := db.sql.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('bridge_delivery_attempts') WHERE name=?`, column).Scan(&found); err != nil || found != 1 {
			t.Fatalf("attempt column %s found=%d err=%v", column, found, err)
		}
	}
	var transport, session, ordinal sql.NullString
	if err := db.sql.QueryRow(`SELECT b.transport_mode,a.acp_session_id,CAST(a.acp_turn_ordinal AS TEXT) FROM bridge_bindings b JOIN bridge_delivery_attempts a ON a.binding_id=b.binding_id WHERE a.attempt_id='attempt-界'`).Scan(&transport, &session, &ordinal); err != nil {
		t.Fatal(err)
	}
	if transport.String != "hook_pull" || session.String != "" || ordinal.Valid {
		t.Fatalf("legacy row changed transport=%q session=%q ordinal=%+v", transport.String, session.String, ordinal)
	}
	if got := schema23Usage(t, db.sql, "global", ""); got != before {
		t.Fatalf("migration changed storage usage=%d want=%d", got, before)
	}
	if got := schema23ChildObjects(t, db.sql); !equalSchema23Objects(objects, got) {
		t.Fatalf("child schema objects changed: got=%v want=%v", got, objects)
	}
	var migratedHash string
	if err := db.sql.QueryRow(`SELECT hex(content_bytes)||':'||render_sha256||':'||hex(wire_bytes)||':'||wire_sha256 FROM bridge_batches b JOIN bridge_delivery_attempts a ON a.batch_id=b.batch_id WHERE a.attempt_id='attempt-界'`).Scan(&migratedHash); err != nil || migratedHash != rowHash {
		t.Fatalf("preserved row hash=%q want=%q err=%v", migratedHash, rowHash, err)
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_bindings(binding_id,run_id,provider_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES('acp','run-界','grok','acp-owner',11,'owner-birth',12,'native-birth',?,2,'owner','grok_acp_push_v1',?,'ACTIVE','','','request-acp','now','now')`, strings.Repeat("d", 64), strings.Repeat("e", 64)); err != nil {
		t.Fatalf("acp transport rejected: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,created_at) VALUES('batch-acp','acp',2,x'01',?,'now')`, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,created_at) VALUES('batch-acp-2','acp',2,x'02',?,'now')`, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	globalBeforeAttempt := schema23Usage(t, db.sql, "global", "")
	runBeforeAttempt := schema23Usage(t, db.sql, "run", "run-界")
	if _, err := db.sql.Exec(`INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,created_at) VALUES('acp-attempt-1','batch-acp','acp',2,1,?,'RECEIVED','later',12,'native-birth','now')`, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	globalBeforeACP := schema23Usage(t, db.sql, "global", "")
	runBeforeACP := schema23Usage(t, db.sql, "run", "run-界")
	if _, err := db.sql.Exec(`UPDATE bridge_delivery_attempts SET acp_turn_ordinal=1 WHERE attempt_id='acp-attempt-1'`); err != nil {
		t.Fatal(err)
	}
	if got := schema23Usage(t, db.sql, "global", ""); got != globalBeforeACP+8 {
		t.Fatalf("ordinal global growth=%d want=%d", got, globalBeforeACP+8)
	}
	if got := schema23Usage(t, db.sql, "run", "run-界"); got != runBeforeACP+8 {
		t.Fatalf("ordinal run growth=%d want=%d", got, runBeforeACP+8)
	}
	if _, err := db.sql.Exec(`UPDATE bridge_delivery_attempts SET acp_turn_ordinal=NULL WHERE attempt_id='acp-attempt-1'`); err != nil {
		t.Fatal(err)
	}
	if got := schema23Usage(t, db.sql, "global", ""); got != globalBeforeACP {
		t.Fatalf("ordinal global reset=%d want=%d", got, globalBeforeACP)
	}
	if got := schema23Usage(t, db.sql, "run", "run-界"); got != runBeforeACP {
		t.Fatalf("ordinal run reset=%d want=%d", got, runBeforeACP)
	}
	if _, err := db.sql.Exec(`UPDATE bridge_delivery_attempts SET acp_session_id='会',acp_rpc_request_id=9,acp_response_sha256='响应',acp_stop_reason='结束' WHERE attempt_id='acp-attempt-1'`); err != nil {
		t.Fatal(err)
	}
	// UTF-8 text is 3+6+6 bytes; the nullable RPC integer adds 8 bytes.
	if got := schema23Usage(t, db.sql, "global", ""); got != globalBeforeACP+23 {
		t.Fatalf("global ACP update=%d want=%d", got, globalBeforeACP+23)
	}
	if got := schema23Usage(t, db.sql, "run", "run-界"); got != runBeforeACP+23 {
		t.Fatalf("run ACP update=%d want=%d", got, runBeforeACP+23)
	}
	if _, err := db.sql.Exec(`UPDATE bridge_delivery_attempts SET acp_session_id='',acp_rpc_request_id=NULL,acp_response_sha256='',acp_stop_reason='' WHERE attempt_id='acp-attempt-1'`); err != nil {
		t.Fatal(err)
	}
	if got := schema23Usage(t, db.sql, "global", ""); got != globalBeforeACP {
		t.Fatalf("global ACP reset=%d want=%d", got, globalBeforeACP)
	}
	if got := schema23Usage(t, db.sql, "run", "run-界"); got != runBeforeACP {
		t.Fatalf("run ACP reset=%d want=%d", got, runBeforeACP)
	}
	if _, err := db.sql.Exec(`UPDATE bridge_delivery_attempts SET acp_turn_ordinal=1,acp_rpc_request_id=9 WHERE attempt_id='acp-attempt-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE bridge_delivery_attempts SET acp_turn_ordinal=0 WHERE attempt_id='acp-attempt-1'`); err == nil {
		t.Fatal("non-positive ACP ordinal admitted")
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,created_at,acp_turn_ordinal) VALUES('acp-attempt-duplicate','batch-acp-2','acp',2,1,?,'CLAIMED','later',12,'native-birth','now',1)`, strings.Repeat("f", 64)); err == nil {
		t.Fatal("duplicate ACP ordinal admitted")
	}
	if _, err := db.sql.Exec(`INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,created_at,acp_turn_ordinal,acp_rpc_request_id) VALUES('acp-attempt-duplicate-rpc','batch-acp-2','acp',2,1,?,'CLAIMED','later',12,'native-birth','now',2,9)`, strings.Repeat("f", 64)); err == nil {
		t.Fatal("duplicate ACP request ID admitted")
	}
	if _, err := db.sql.Exec(`DELETE FROM bridge_delivery_attempts WHERE attempt_id='acp-attempt-1'`); err != nil {
		t.Fatal(err)
	}
	if got := schema23Usage(t, db.sql, "global", ""); got != globalBeforeAttempt {
		t.Fatalf("global ACP delete=%d want=%d", got, globalBeforeAttempt)
	}
	if got := schema23Usage(t, db.sql, "run", "run-界"); got != runBeforeAttempt {
		t.Fatalf("run ACP delete=%d want=%d", got, runBeforeAttempt)
	}
	var fk int
	if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys=%d err=%v", fk, err)
	}
}

func TestACPBridgeSchema23FailureRollsBackEveryStageAndRestoresForeignKeys(t *testing.T) {
	for _, stage := range []string{"create", "copy", "drop", "rename", "recreate", "trigger", "version", "fkcheck", "commit"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if db != nil {
					_ = db.Close()
				}
			}()
			downgradeACPBridgeSchema23(t, db)
			insertSchema22BridgeGraph(t, db.sql)
			wantUsage := schema23Usage(t, db.sql, "global", "")
			schema23MigrationHook = func(got string) error {
				if got == stage {
					return errors.New("schema23 injected " + stage)
				}
				return nil
			}
			t.Cleanup(func() { schema23MigrationHook = nil })
			if err := db.migrateRuntime(); err == nil {
				t.Fatal("injected migration succeeded")
			}
			var version, foreignKeys, acpColumns int
			if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != 22 {
				t.Fatalf("version=%d err=%v", version, err)
			}
			if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
				t.Fatalf("foreign_keys=%d err=%v", foreignKeys, err)
			}
			if err := db.sql.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('bridge_delivery_attempts') WHERE name LIKE 'acp_%'`).Scan(&acpColumns); err != nil || acpColumns != 0 {
				t.Fatalf("ACP columns=%d err=%v", acpColumns, err)
			}
			var rows int
			if err := db.sql.QueryRow(`SELECT COUNT(*) FROM bridge_bindings WHERE binding_id='binding-界'`).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("binding rows=%d err=%v", rows, err)
			}
			if got := schema23Usage(t, db.sql, "global", ""); got != wantUsage {
				t.Fatalf("usage=%d want=%d", got, wantUsage)
			}
			schema23MigrationHook = nil
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db = nil
			reopened, err := Open(path)
			if err != nil {
				t.Fatalf("retry Open() = %v", err)
			}
			defer reopened.Close()
			var retriedVersion int
			if err := reopened.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&retriedVersion); err != nil || retriedVersion != 23 {
				t.Fatalf("retry version=%d err=%v", retriedVersion, err)
			}
		})
	}
}

func TestACPBridgeSchema23JoinsRestoreHookDiagnosticAfterRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	downgradeACPBridgeSchema23(t, db)
	insertSchema22BridgeGraph(t, db.sql)
	schema23MigrationHook = func(stage string) error {
		if stage == "commit" || stage == "restore" {
			return errors.New("schema23 injected " + stage)
		}
		return nil
	}
	t.Cleanup(func() { schema23MigrationHook = nil })
	err = db.migrateRuntime()
	if err == nil || !strings.Contains(err.Error(), "schema23 injected commit") || !strings.Contains(err.Error(), "schema23 injected restore") {
		t.Fatalf("joined migration/restore error = %v", err)
	}
	var version, foreignKeys int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != 22 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys=%d err=%v", foreignKeys, err)
	}
}

func TestACPBridgeSchema23DeferForeignKeysCannotReplacePreBeginDisable(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "defer.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE parent(id TEXT PRIMARY KEY); CREATE TABLE child(id TEXT PRIMARY KEY,parent_id TEXT NOT NULL REFERENCES parent(id)); INSERT INTO parent VALUES('old'); INSERT INTO child VALUES('child','old')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys=ON; CREATE TABLE parent_new(id TEXT PRIMARY KEY); INSERT INTO parent_new SELECT * FROM parent; DROP TABLE parent; ALTER TABLE parent_new RENAME TO parent`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("setup unexpectedly failed before commit: %v", err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("defer_foreign_keys allowed parent replacement commit")
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM parent WHERE id='old'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rollback parent rows=%d err=%v", rows, err)
	}
}

func insertSchema22BridgeGraph(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES('run-界','controller',1,'origin',10,'birth','queued','now','collect')`,
		`INSERT INTO tasks(id,run_id,status,dependencies_json,max_attempts,created_at) VALUES('task-界','run-界','queued','[]',1,'now')`,
		`INSERT INTO bridge_bindings(binding_id,run_id,provider_id,native_session_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,candidate_observation_event_id,candidate_observation_hash,hook_token_hash,capability_file,request_id,created_at,updated_at) VALUES('binding-界','run-界','claude','session','owner',11,'owner-birth',12,'native-birth','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,'owner','hook_pull','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','ACTIVE','event','cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc','token','/tmp/cap','request','now','now')`,
		`INSERT INTO bridge_ingress(binding_id,event_id,payload_hash,normalized_json,kind,native_session_id,created_at) VALUES('binding-界','event-界','dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',x'E7958C','tool_observed','session','now')`,
		`INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,events_json,receipts_json,created_at) VALUES('batch-界','binding-界',1,x'E7958C','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',x'5B5D',x'5B5D','now')`,
		`INSERT INTO bridge_batch_items(batch_id,position,event_id,event_revision,event_hash,action_slot,task_id,work_revision) VALUES('batch-界',0,'event-界',1,'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff','slot','task-界',1)`,
		`INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,wire_bytes,wire_sha256,receipt_id,received_at,created_at,updated_at) VALUES('attempt-界','batch-界','binding-界',1,1,'0000000000000000000000000000000000000000000000000000000000000000','RECEIVED','later',12,'native-birth',x'E7958C','1111111111111111111111111111111111111111111111111111111111111111','receipt','then','now','later')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("insert schema22 graph: %v; statement=%s", err, stmt)
		}
	}
}

func schema23Usage(t *testing.T, db *sql.DB, scope, id string) int64 {
	t.Helper()
	var usage int64
	if err := db.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope=? AND scope_id=?`, scope, id).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	return usage
}

func schema23ChildObjects(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name,sql FROM sqlite_master WHERE (type='table' AND name IN ('bridge_ingress','bridge_batches','bridge_batch_items')) OR (type='trigger' AND name IN ('quota_bridge_ingress_INSERT','quota_bridge_ingress_UPDATE','quota_bridge_ingress_DELETE','quota_bridge_batches_INSERT','quota_bridge_batches_UPDATE','quota_bridge_batches_DELETE','quota_bridge_batch_items_INSERT','quota_bridge_batch_items_UPDATE','quota_bridge_batch_items_DELETE')) ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	objects := map[string]string{}
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		objects[name] = ddl
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return objects
}

func equalSchema23Objects(want, got map[string]string) bool {
	if len(want) != len(got) {
		return false
	}
	for name, ddl := range want {
		if got[name] != ddl {
			return false
		}
	}
	return true
}

// downgradeACPBridgeSchema23 makes a complete schema22 fixture. It deliberately
// removes every schema23 object; lowering only schema_meta would hide partial
// shape mistakes in migration tests.
func downgradeACPBridgeSchema23(t *testing.T, db *DB) {
	t.Helper()
	var version int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version <= 22 {
		return
	}
	if version != 23 {
		t.Fatalf("cannot make schema22 fixture from version=%d", version)
	}
	if _, err := db.sql.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	fail := func(err error) {
		_ = tx.Rollback()
		_, _ = db.sql.Exec(`PRAGMA foreign_keys=ON`)
		t.Fatal(err)
	}
	for _, table := range bridgeQuotaTables {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			if _, err := tx.Exec(`DROP TRIGGER quota_` + table.name + `_` + op); err != nil {
				fail(err)
			}
		}
	}
	if _, err := tx.Exec(`DROP INDEX bridge_acp_delivery_request;
DROP INDEX bridge_acp_delivery_ordinal;
CREATE TABLE bridge_bindings_schema22(
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
INSERT INTO bridge_bindings_schema22 SELECT binding_id,run_id,provider_id,native_session_id,owner_context_id,owner_pid,owner_birth,native_pid,native_birth,native_executable_sha256,owner_epoch,role,transport_mode,manifest_sha256,status,candidate_observation_event_id,candidate_observation_hash,hook_token_hash,capability_file,request_id,created_at,updated_at FROM bridge_bindings;
DROP TABLE bridge_bindings;
ALTER TABLE bridge_bindings_schema22 RENAME TO bridge_bindings;
CREATE UNIQUE INDEX bridge_one_live_binding ON bridge_bindings(run_id,owner_context_id) WHERE status IN ('PENDING_SESSION','SESSION_OBSERVED','ACTIVE');
CREATE UNIQUE INDEX bridge_binding_epoch ON bridge_bindings(run_id,owner_context_id,owner_epoch);
ALTER TABLE bridge_delivery_attempts DROP COLUMN acp_stop_reason;
ALTER TABLE bridge_delivery_attempts DROP COLUMN acp_response_sha256;
ALTER TABLE bridge_delivery_attempts DROP COLUMN acp_rpc_request_id;
ALTER TABLE bridge_delivery_attempts DROP COLUMN acp_turn_ordinal;
ALTER TABLE bridge_delivery_attempts DROP COLUMN acp_session_id;
UPDATE schema_meta SET version=22`); err != nil {
		fail(err)
	}
	if err := bridgeQuotaTriggers(tx); err != nil {
		fail(err)
	}
	if err := tx.Commit(); err != nil {
		fail(err)
	}
	if _, err := db.sql.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	var foreignKeys int
	if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys=%d err=%v", foreignKeys, err)
	}
}
