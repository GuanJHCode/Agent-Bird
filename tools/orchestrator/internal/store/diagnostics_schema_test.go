package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Historical fixtures may strip v19 only when it has no data or accounting to
// preserve. DROP TABLE removes its own triggers as part of SQLite's DDL.
func downgradeDiagnosticsSchema(t *testing.T, db *DB) {
	t.Helper()
	downgradeReportIntakeSchema(t, db)
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, table := range []string{"diagnostic_delivery_state", "managed_artifact_reservations", "task_diagnostics_policies"} {
		var count int
		if err = tx.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cannot downgrade populated diagnostics table %s: count=%d err=%v", table, count, err)
		}
		if _, err = tx.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(`UPDATE schema_meta SET version=18`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func diagnosticsLegacyGrant(t *testing.T, db *DB) contract.LaunchCommand {
	t.Helper()
	ctx := context.Background()
	receipt, err := db.SubmitPlan(ctx, diagnosticsPlan(nil))
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "generation", PID: 101, Birth: "host-birth", Executable: "/private/tmp/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, host, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func diagnosticsLegacySnapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, query := range map[string]string{
		"task":         `SELECT json_array(t.id,t.status,r.adapter_payload) FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id='task'`,
		"attempt":      `SELECT json_array(a.id,a.attempt_no,a.status,s.id,s.segment_no,s.status,r.launch_intent_hash) FROM attempts a JOIN segments s ON s.attempt_id=a.id JOIN segment_runtime r ON r.segment_id=s.id WHERE a.task_id='task'`,
		"event":        `SELECT body_json FROM runtime_events WHERE event_id='legacy-artifact'`,
		"global_usage": `SELECT json_array(total_bytes,progress_bytes) FROM storage_usage WHERE scope='global' AND scope_id=''`,
		"run_usage":    `SELECT json_array(total_bytes,progress_bytes) FROM storage_usage WHERE scope='run' AND scope_id='run'`,
	} {
		var value string
		if err := db.QueryRow(query).Scan(&value); err != nil {
			t.Fatal(name, err)
		}
		out[name] = value
	}
	return out
}

func diagnosticsLegacyArtifact(t *testing.T, db *DB) {
	t.Helper()
	g := diagnosticsLegacyGrant(t, db)
	historicalHostGrantForTest(t, db, &g)
	e := contract.Event{Version: 1, ProducerID: g.HostID, EventID: "legacy-artifact", RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, CommandID: g.CommandID, Sequence: 1, Kind: contract.EventResult, PayloadHash: strings.Repeat("a", 64), Artifact: &contract.ArtifactRef{ID: "legacy", Path: "/private/legacy/result-output.bin", Size: 42, SHA256: strings.Repeat("a", 64)}}
	if _, err := db.CommitHostEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticsV18MigrationPreservesLegacyWithoutConsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsLegacyArtifact(t, db)
	downgradeDiagnosticsSchema(t, db)
	before := diagnosticsLegacySnapshot(t, db.sql)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err = db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != currentSchemaVersion {
			t.Fatal(version, err)
		}
		if got := diagnosticsLegacySnapshot(t, db.sql); !reflect.DeepEqual(got, before) {
			t.Fatalf("migration changed legacy state: %v / %v", got, before)
		}
		for _, table := range []string{"task_diagnostics_policies", "managed_artifact_reservations", "diagnostic_delivery_state"} {
			var count int
			if err = db.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
				t.Fatal("migration fabricated authority", table, count, err)
			}
		}
		var triggers int
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name IN ('task_diagnostics_policies','managed_artifact_reservations','diagnostic_delivery_state')`).Scan(&triggers); err != nil || triggers != 13 {
			t.Fatal("missing diagnostics constraints/accounting", triggers, err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiagnosticsDeliveryStatePreventsPendingReplacementAndRevisionReuse(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g := diagnosticsLegacyGrant(t, db)
	// Direct SQL exercises schema invariants, not consent or read/ACK authority.
	if _, err = db.sql.Exec(`INSERT INTO managed_artifact_reservations(id,run_id,task_id,attempt_id,producer_id,class,scope_key,policy_sha256,namespace_sha256,reserved_bytes,state,created_at) VALUES('reservation',?,?,?,?, 'diagnostics','attempt',?,?,21102592,'reserved','now')`, g.RunID, g.TaskID, g.AttemptID, g.HostID, strings.Repeat("a", 64), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`INSERT INTO diagnostic_delivery_state(attempt_id,run_id,task_id,producer_id,reservation_id) VALUES(?,?,?,?, 'reservation')`, g.AttemptID, g.RunID, g.TaskID, g.HostID); err != nil {
		t.Fatal(err)
	}
	update := func(assignment string) error {
		_, err := db.sql.Exec("UPDATE diagnostic_delivery_state SET "+assignment+" WHERE attempt_id=?", g.AttemptID)
		return err
	}
	reject := func(assignment string) {
		t.Helper()
		if err := update(assignment); err == nil {
			t.Fatalf("invalid durable transition accepted: %s", assignment)
		}
	}
	accept := func(assignment string) {
		t.Helper()
		if err := update(assignment); err != nil {
			t.Fatalf("valid durable transition rejected: %s: %v", assignment, err)
		}
	}
	reject(`next_revision=2`)
	reject(`acked_cursor_json='{"forged":true}'`)
	reject(`phase='reading',reading_revision=2,next_revision=3,reading_request_json='{}'`)
	accept(`phase='reading',reading_revision=1,next_revision=2,reading_request_json='{}'`)
	for _, assignment := range []string{`reading_request_json='{"changed":true}'`, `reading_revision=2,next_revision=3`, `next_revision=3`, `last_ack_json='{"forged":true}'`, `phase='pending',pending_envelope=zeroblob(65537)`, `phase='pending',pending_envelope=CAST('x' AS BLOB),pending_control_bytes=0.5`} {
		reject(assignment)
	}
	// An unexposed failed read burns its allocated revision, without ACK effects.
	accept(`phase='idle',reading_revision=0,reading_request_json=''`)
	reject(`next_revision=1`)
	accept(`phase='reading',reading_revision=2,next_revision=3,reading_request_json='{}'`)
	var before int64
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	accept(`phase='pending',pending_envelope=CAST('page-body' AS BLOB),pending_control_bytes=3`)
	var after int64
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&after); err != nil || after != before+3 {
		t.Fatal("raw page double charged or metadata omitted", before, after, err)
	}
	for _, assignment := range []string{`pending_envelope=CAST('new-page' AS BLOB)`, `pending_control_bytes=4`, `reading_request_json='{"changed":true}'`, `next_revision=4`, `next_revision=1`, `acked_cursor_json='{"forged":true}'`, `phase='reading',pending_envelope=X'',pending_control_bytes=0`, `phase='idle',reading_revision=0,reading_request_json='',pending_envelope=X'',pending_control_bytes=0`} {
		reject(assignment)
	}
	accept(`pending_envelope=pending_envelope`)
	// Schema enforces the shape; future Store APIs must authenticate this ACK and
	// its cursor/receipt against the full pending envelope in the same transaction.
	accept(`phase='idle',reading_revision=0,reading_request_json='',pending_envelope=X'',pending_control_bytes=0,last_ack_json='{"revision":2}',acked_cursor_json='{"chunk":0}'`)
	reject(`next_revision=2`)
	accept(`phase='reading',reading_revision=3,next_revision=4,reading_request_json='{}'`)
}

func TestDiagnosticsDeliveryStartsWithoutHistory(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g := diagnosticsLegacyGrant(t, db)
	if _, err = db.sql.Exec(`INSERT INTO managed_artifact_reservations(id,run_id,task_id,attempt_id,producer_id,class,scope_key,policy_sha256,namespace_sha256,reserved_bytes,state,created_at) VALUES('reservation',?,?,?,?, 'diagnostics','attempt',?,?,21102592,'reserved','now')`, g.RunID, g.TaskID, g.AttemptID, g.HostID, strings.Repeat("a", 64), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, columns, values string }{
		{"reading", "phase,next_revision,reading_revision,reading_request_json", `'reading',2,1,'{}'`},
		{"pending", "phase,next_revision,reading_revision,reading_request_json,pending_envelope", `'pending',2,1,'{}',CAST('page' AS BLOB)`},
		{"revision", "next_revision", `2`},
		{"ack", "last_ack_json", `'{"revision":1}'`},
		{"cursor", "acked_cursor_json", `'{"chunk":1}'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = tx.Exec(`INSERT INTO diagnostic_delivery_state(attempt_id,run_id,task_id,producer_id,reservation_id,`+tc.columns+`) VALUES(?,?,?,?,'reservation',`+tc.values+`)`, g.AttemptID, g.RunID, g.TaskID, g.HostID)
			if err == nil {
				t.Fatal("initial diagnostics history accepted")
			}
		})
	}
}

func TestDiagnosticsLedgerRejectsFractionalAccounting(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g := diagnosticsLegacyGrant(t, db)
	for _, tc := range []struct {
		name               string
		reserved, retained float64
	}{{"reserved", 1.5, 0}, {"retained", 3, 0.5}} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = tx.Exec(`INSERT INTO managed_artifact_reservations(id,run_id,task_id,attempt_id,producer_id,class,scope_key,policy_sha256,namespace_sha256,reserved_bytes,retained_bytes,state,created_at) VALUES('reservation',?,?,?,?,'terminal','segment','','',?,?,'reserved','now')`, g.RunID, g.TaskID, g.AttemptID, g.HostID, tc.reserved, tc.retained)
			if err == nil {
				t.Fatal("fractional artifact accounting accepted")
			}
		})
	}
}

func TestDiagnosticsV18MigrationRollsBackAllDDLAndAccounting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsLegacyArtifact(t, db)
	downgradeDiagnosticsSchema(t, db)
	before := diagnosticsLegacySnapshot(t, db.sql)
	// Fail late, after creating policy/reservation tables and their guards.
	if _, err = db.sql.Exec(`CREATE TABLE diagnostic_delivery_state(broken TEXT); INSERT INTO diagnostic_delivery_state VALUES('preserve')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("corrupt v18 accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if got := diagnosticsLegacySnapshot(t, raw); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed migration changed legacy state: %v / %v", got, before)
	}
	var version, tables, triggers int
	var preserved string
	if err = raw.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('task_diagnostics_policies','managed_artifact_reservations')),(SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name IN ('task_diagnostics_policies','managed_artifact_reservations','diagnostic_delivery_state')),(SELECT broken FROM diagnostic_delivery_state)`).Scan(&version, &tables, &triggers, &preserved); err != nil || version != 18 || tables != 0 || triggers != 0 || preserved != "preserve" {
		t.Fatal("partial diagnostics migration", version, tables, triggers, preserved, err)
	}
}

func TestDiagnosticsPolicyControlAccountingIsTransactional(t *testing.T) {
	ctx := context.Background()
	open := func() *DB {
		t.Helper()
		db, err := Open(filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	legacy := open()
	if _, err := legacy.SubmitPlan(ctx, diagnosticsPlan(nil)); err != nil {
		t.Fatal(err)
	}
	var baseline int64
	if err := legacy.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	policy := &contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"}
	body, _ := json.Marshal(policy)
	additional := int64(64 + len("run") + len("task") + len(body) + 64)
	detailed := open()
	if _, err := detailed.SubmitPlan(ctx, diagnosticsPlan(policy)); err != nil {
		t.Fatal(err)
	}
	var used int64
	if err := detailed.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&used); err != nil || used != baseline+additional {
		t.Fatal("consent unaccounted", used, baseline, additional, err)
	}
	limited := open()
	limited.userControlLimit = baseline + additional - 1
	if _, err := limited.SubmitPlan(ctx, diagnosticsPlan(policy)); err != CodeError("control_spool_full") {
		t.Fatal("consent exceeded control allowance", err)
	}
	var rows int
	if err := limited.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM runs)+(SELECT COUNT(*) FROM tasks)+(SELECT COUNT(*) FROM task_diagnostics_policies)+(SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id='')`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("rejected consent left state/accounting", rows, err)
	}
}
