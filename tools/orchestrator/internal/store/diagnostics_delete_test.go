package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
)

func TestDiagnosticsDeletionRequiresPhysicalIntent(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	for _, state := range []string{"deleting", "released", "legacy_pending"} {
		t.Run(state, func(t *testing.T) {
			tx, err := f.db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(`UPDATE managed_artifact_reservations SET state=? WHERE id=?`, state, f.grant.Diagnostics.ReservationID); err == nil {
				t.Fatalf("diagnostics entered %s without durable physical deletion intent", state)
			}
		})
	}
}

// Test-only historical fixtures must never erase real deletion history or its
// physical tombstone charge just to manufacture an older schema.
func removeDiagnosticDeletionSchema31(db *DB) error {
	var version, n int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		return err
	}
	if version < 31 {
		return nil
	}
	if version != 31 {
		return fmt.Errorf("unsupported deletion schema %d", version)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostic_deletions`).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return errors.New("cannot erase diagnostics deletion history")
	}
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`DROP TRIGGER diagnostic_reservation_initial; DROP TRIGGER diagnostic_reservation_transition; DROP TRIGGER diagnostic_delivery_deleting; DROP TABLE diagnostic_deletions; UPDATE schema_meta SET version=30`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func completedDiagnosticsDeleteFixture(t *testing.T) (*diagnosticsReadFixture, DiagnosticsDeleteRequest) {
	t.Helper()
	f := newDiagnosticsReadFixture(t)
	f.append(t, 0, []byte("original detailed bytes"))
	if err := f.writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := diagnosticOutcomeEvent(f.grant, contract.EventResult, 1)
	if _, err := f.db.CommitHostEvent(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	exited := diagnosticOutcomeEvent(f.grant, contract.EventExited, 2)
	code := 0
	exited.ExitCode = &code
	exited.ActiveMS = 1
	if _, err := f.db.CommitHostEvent(context.Background(), exited); err != nil {
		t.Fatal(err)
	}
	return f, DiagnosticsDeleteRequest{Version: 1, TaskID: f.grant.TaskID, AttemptID: f.grant.AttemptID, RequestID: "delete-1", ExpectedRevision: 1}
}
func deletionUsage(t *testing.T, db *DB) string {
	t.Helper()
	var s string
	if err := db.sql.QueryRow(`SELECT json_group_array(json_array(scope,scope_id,total_bytes,progress_bytes)) FROM (SELECT * FROM storage_usage ORDER BY scope,scope_id)`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}
func deletionState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT json_array(r.state,COALESCE(x.phase,''),COALESCE(x.plan_json,''),COALESCE(x.completion_json,''),(SELECT COUNT(*) FROM deletion_commit_fault)) FROM managed_artifact_reservations r LEFT JOIN diagnostic_deletions x ON x.reservation_id=r.id WHERE r.class='diagnostics'`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestDiagnosticsDeleteLifecycleAndHistoricalProof(t *testing.T) {
	ctx := context.Background()
	f, req := completedDiagnosticsDeleteFixture(t)
	raw, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	if err != nil {
		t.Fatal(err)
	}
	var page DiagnosticsEnvelope
	if err = json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.DeleteDiagnostics(ctx, "controller", f.token, req); err == nil {
		t.Fatal("pending page was deleted")
	}
	ack, err := f.db.AckDiagnostics(ctx, "controller", f.token, page.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	req.ExpectedRevision = ack.NextRevision
	result, err := f.db.DeleteDiagnostics(ctx, "controller", f.token, req)
	if err != nil {
		t.Fatal(err)
	}
	var got DiagnosticsDeleteResult
	if err = json.Unmarshal(result, &got); err != nil || got.Status != "deleted" || got.RemovedLogicalBytes <= int64(len("original detailed bytes")) || got.ReleasedReservationBytes != detailedArtifactReservationBytes || got.RetainedMarkerBytes <= 0 {
		t.Fatalf("result=%s err=%v", result, err)
	}
	var state, phase string
	var retained, cost, exactCost int64
	if err = f.db.sql.QueryRow(`SELECT r.state,r.retained_bytes,x.phase,x.control_bytes,`+diagnosticDeletionCostSQL("x.")+` FROM managed_artifact_reservations r JOIN diagnostic_deletions x ON x.reservation_id=r.id`).Scan(&state, &retained, &phase, &cost, &exactCost); err != nil || state != "released" || retained != 0 || phase != "removed" || cost != exactCost {
		t.Fatalf("release/accounting %s %s %d/%d err=%v", state, phase, cost, exactCost, err)
	}
	var charged int64
	if err = f.db.sql.QueryRow(`SELECT COALESCE(SUM(reserved_bytes),0) FROM managed_artifact_reservations WHERE class='diagnostics' AND state!='released'`).Scan(&charged); err != nil || charged != 0 {
		t.Fatal("reservation not released", charged, err)
	}
	namespace, _ := (diagnostics.Scope{Version: 1, RunID: f.grant.RunID, TaskID: f.grant.TaskID, AttemptID: f.grant.AttemptID, ProducerID: f.grant.HostID, PolicySHA256: f.grant.Diagnostics.PolicySHA256}).Hash()
	files, err := os.ReadDir(filepath.Join(f.root, namespace))
	if err != nil || len(files) != 3 {
		t.Fatalf("expected retained locks+marker, entries=%v err=%v", files, err)
	}
	if _, err = f.db.ReadDiagnostics(ctx, "controller", f.token, f.request); !errors.Is(err, CodeError("diagnostics_expired")) {
		t.Fatal("read did not expire", err)
	}
	if cost != independentlyMeasuredDeletionCost(t, f.db) {
		t.Fatalf("deletion metadata charge=%d independently measured=%d", cost, independentlyMeasuredDeletionCost(t, f.db))
	}
	before := deletionUsage(t, f.db)
	path := f.db.path
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.db.DeleteDiagnostics(ctx, "controller", f.token, req)
	if err != nil || !bytes.Equal(result, again) || before != deletionUsage(t, f.db) {
		t.Fatal("reopen exact replay changed quota/response", err)
	}
	changed := req
	changed.RequestID = "delete-2"
	if _, err = f.db.DeleteDiagnostics(ctx, "controller", f.token, changed); err == nil {
		t.Fatal("retired reservation adopted new command")
	}
	tx, err := f.db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	historical := f.grant
	historical.Diagnostics = nil
	if err = bindDiagnosticsForUse(ctx, tx, &historical, candidateArtifactHistoricalProof); err != nil || historical.Diagnostics == nil || *historical.Diagnostics != *f.grant.Diagnostics {
		t.Fatal("retired original proof lost", err)
	}
	live := f.grant
	live.Diagnostics = nil
	if err = bindExistingDiagnostics(ctx, tx, &live); err == nil || live.Diagnostics != nil {
		t.Fatal("retired proof granted live dispatch", err)
	}
}

func TestDiagnosticsDeleteRejectsBeforeFilesystem(t *testing.T) {
	for _, kind := range []string{"foreign_token", "foreign_controller", "stale_revision", "unknown", "active", "not_completed", "foreign_attempt"} {
		t.Run(kind, func(t *testing.T) {
			f, req := completedDiagnosticsDeleteFixture(t)
			controller, token := "controller", f.token
			switch kind {
			case "foreign_token":
				token = "foreign"
			case "foreign_controller":
				controller = "foreign"
			case "stale_revision":
				req.ExpectedRevision++
			case "unknown", "active":
				status := "unknown"
				if kind == "active" {
					status = "running"
				}
				if _, err := f.db.sql.Exec(`UPDATE segment_runtime SET status=? WHERE segment_id=?`, status, f.grant.SegmentID); err != nil {
					t.Fatal(err)
				}
			case "not_completed":
				if _, err := f.db.sql.Exec(`UPDATE tasks SET status='result_ready'`); err != nil {
					t.Fatal(err)
				}
			case "foreign_attempt":
				req.AttemptID = "foreign"
			}
			touched := false
			_, err := f.db.deleteDiagnostics(context.Background(), controller, token, req, func(context.Context, string, diagnostics.Scope, string, []byte) (diagnosticDeletionLease, error) {
				touched = true
				return nil, errors.New("unexpected filesystem")
			})
			if err == nil || touched {
				t.Fatalf("err=%v filesystem=%v", err, touched)
			}
			var n int
			if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostic_deletions`).Scan(&n); err != nil || n != 0 {
				t.Fatal("rejected request created intent", n, err)
			}
		})
	}
}

func TestDiagnosticsDeleteFailedCommitsRetainExactRecovery(t *testing.T) {
	for _, stage := range []string{"prepare", "finalize"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			f, req := completedDiagnosticsDeleteFixture(t)
			trigger := `AFTER INSERT ON diagnostic_deletions`
			if stage == "finalize" {
				trigger = `AFTER UPDATE OF phase ON diagnostic_deletions WHEN NEW.phase='removed'`
			}
			if _, err := f.db.sql.Exec(`CREATE TABLE deletion_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_deletion_commit ` + trigger + ` BEGIN INSERT INTO deletion_commit_fault VALUES('missing-parent'); END`); err != nil {
				t.Fatal(err)
			}
			before := deletionState(t, f.db.sql)
			usageBefore := deletionUsage(t, f.db)
			if _, err := f.db.DeleteDiagnostics(ctx, "controller", f.token, req); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
				t.Fatal("expected actual deferred FK COMMIT failure", err)
			}
			after := deletionState(t, f.db.sql)
			external, err := sql.Open("sqlite", f.db.path)
			if err != nil {
				t.Fatal(err)
			}
			externalAfter := deletionState(t, external)
			external.Close()
			if after != externalAfter {
				t.Fatal("failed physical COMMIT leaked pooled state")
			}
			var reservation, phase string
			var faults int
			if err = f.db.sql.QueryRow(`SELECT r.state,COALESCE(x.phase,''),(SELECT COUNT(*) FROM deletion_commit_fault) FROM managed_artifact_reservations r LEFT JOIN diagnostic_deletions x ON x.reservation_id=r.id WHERE r.class='diagnostics'`).Scan(&reservation, &phase, &faults); err != nil || faults != 0 {
				t.Fatal("failed commit survived", reservation, phase, faults, err)
			}
			if stage == "prepare" {
				if before != after || usageBefore != deletionUsage(t, f.db) {
					t.Fatal("failed prepare changed state/accounting")
				}
				page, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
				if err != nil {
					t.Fatal("failed prepare removed data", err)
				}
				var decoded DiagnosticsEnvelope
				if json.Unmarshal(page, &decoded) != nil || len(decoded.Body.Records) != 1 || string(decoded.Body.Records[0].Data) != "original detailed bytes" {
					t.Fatal("original bytes changed")
				}
				ack, err := f.db.AckDiagnostics(ctx, "controller", f.token, decoded.Receipt)
				if err != nil {
					t.Fatal(err)
				}
				req.ExpectedRevision = ack.NextRevision
			} else {
				if reservation != "deleting" || phase != "prepared" {
					t.Fatal("released quota on failed final commit", reservation, phase)
				}
				var intentPlan string
				if err = f.db.sql.QueryRow(`SELECT plan_json FROM diagnostic_deletions`).Scan(&intentPlan); err != nil {
					t.Fatal(err)
				}
				snapshot, err := f.db.diagnosticDeletionSnapshot(ctx, "controller", f.token, req, string(mustJSONDelete(t, req)))
				if err != nil {
					t.Fatal(err)
				}
				lease, err := diagnostics.OpenDeletion(ctx, f.root, snapshot.row.scope, snapshot.commandID(), []byte(intentPlan))
				if err != nil {
					t.Fatal(err)
				}
				if err = lease.VerifyRemoved(ctx); err != nil {
					t.Fatal("final COMMIT did not follow physical removal", err)
				}
				lease.Close()
				// Once admitted, a legitimate later task revision must not strand original
				// deletion recovery. This is a Store revision fixture, not a new execution.
				if _, err = f.db.sql.Exec(`UPDATE tasks SET status='ready'; UPDATE task_runtime SET work_revision=work_revision+1`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.db.sql.Exec(`DROP TRIGGER fail_deletion_commit`); err != nil {
				t.Fatal(err)
			}
			path := f.db.path
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.DeleteDiagnostics(ctx, "controller", f.token, req); err != nil {
				t.Fatal("reopened same intent did not recover", err)
			}
		})
	}
}
func mustJSONDelete(t *testing.T, r DiagnosticsDeleteRequest) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDiagnosticsDeleteReadWinsAdmissionRace(t *testing.T) {
	ctx := context.Background()
	f, req := completedDiagnosticsDeleteFixture(t)
	var page []byte
	_, err := f.db.deleteDiagnostics(ctx, "controller", f.token, req, func(ctx context.Context, root string, scope diagnostics.Scope, id string, plan []byte) (diagnosticDeletionLease, error) {
		var err error
		// Read wins between the deletion authorization snapshot and prepare CAS.
		page, err = f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
		if err != nil {
			return nil, err
		}
		return diagnostics.OpenDeletion(ctx, root, scope, id, plan)
	})
	if !errors.Is(err, ErrConflict) || len(page) == 0 {
		t.Fatal("read/delete did not serialize", err)
	}
	var n int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostic_deletions`).Scan(&n); err != nil || n != 0 {
		t.Fatal("read race left deletion intent", n, err)
	}
	again, err := f.db.ReadDiagnostics(ctx, "controller", f.token, f.request)
	if err != nil || !bytes.Equal(page, again) {
		t.Fatal("read was invalidated by losing delete", err)
	}
}

func TestDiagnosticsDeletionSchemaTransitionsAndMigration(t *testing.T) {
	f, req := completedDiagnosticsDeleteFixture(t)
	ctx := context.Background()
	x, err := f.db.diagnosticDeletionSnapshot(ctx, "controller", f.token, req, string(mustJSONDelete(t, req)))
	if err != nil {
		t.Fatal(err)
	}
	// Keep a real persisted physical plan while inspecting direct-SQL transition
	// guards. No filesystem deletion is performed by this schema-only fixture.
	lease, err := diagnostics.OpenDeletion(ctx, f.root, x.row.scope, x.commandID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	x.planJSON = string(lease.Plan())
	x.planHash = runtimeHash(x.planJSON)
	x.markerBytes = lease.MarkerBytes()
	x.removedBytes = lease.RemovedBytes()
	x.created = "schema-fixture"
	body, _ := json.Marshal(x.result())
	x.completion = string(body)
	x.controlBytes = x.cost()
	// A premature removed intent must abort the whole prepare transaction,
	// before its reservation has entered deleting.
	if _, err = f.db.sql.Exec(`CREATE TRIGGER premature_delete_removed AFTER INSERT ON diagnostic_deletions BEGIN UPDATE diagnostic_deletions SET phase='removed' WHERE reservation_id=NEW.reservation_id; END`); err != nil {
		t.Fatal(err)
	}
	if err = f.db.prepareDiagnosticDeletion(ctx, "controller", f.token, x); err == nil {
		t.Fatal("removed intent accepted before deleting reservation")
	}
	if _, err = f.db.sql.Exec(`DROP TRIGGER premature_delete_removed`); err != nil {
		t.Fatal(err)
	}
	if err = f.db.prepareDiagnosticDeletion(ctx, "controller", f.token, x); err != nil {
		t.Fatal(err)
	}
	reject := func(name, q string, args ...any) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			tx, err := f.db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(q, args...); err == nil {
				t.Fatal("schema accepted forbidden transition")
			}
		})
	}
	reject("release_before_removed", `UPDATE managed_artifact_reservations SET state='released' WHERE id=?`, x.row.reservation)
	reject("reservation_regression", `UPDATE managed_artifact_reservations SET state='active' WHERE id=?`, x.row.reservation)
	reject("reading_while_deleting", `UPDATE diagnostic_delivery_state SET phase='reading',reading_revision=next_revision,next_revision=next_revision+1,reading_request_json='{}'`)
	reject("intent_mutation", `UPDATE diagnostic_deletions SET plan_json='{}'`)
	reject("erase_intent", `DELETE FROM diagnostic_deletions`)
	tx, err := f.db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE diagnostic_deletions SET phase='removed'; UPDATE managed_artifact_reservations SET state='released',retained_bytes=0 WHERE class='diagnostics'`); err != nil {
		tx.Rollback()
		t.Fatal("legal two-phase SQL rejected", err)
	}
	if _, err = tx.Exec(`UPDATE diagnostic_deletions SET phase='prepared'`); err == nil {
		tx.Rollback()
		t.Fatal("removed intent regressed")
	}
	if _, err = tx.Exec(`UPDATE managed_artifact_reservations SET state='reserved' WHERE class='diagnostics'`); err == nil {
		tx.Rollback()
		t.Fatal("released reservation resurrected")
	}
	tx.Rollback()
	if err = removeDiagnosticDeletionSchema31(f.db); err == nil {
		t.Fatal("migration fixture erased real deletion history")
	}
	for _, state := range []string{"reserved", "deleting", "released"} {
		t.Run("migration_"+state, func(t *testing.T) {
			old := newDiagnosticsReadFixture(t)
			if err := removeDiagnosticDeletionSchema31(old.db); err != nil {
				t.Fatal(err)
			}
			if _, err := old.db.sql.Exec(`UPDATE managed_artifact_reservations SET state=? WHERE class='diagnostics'`, state); err != nil {
				t.Fatal(err)
			}
			err := old.db.migrateRuntime()
			var version, objects int
			if e := old.db.sql.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE name='diagnostic_deletions')`).Scan(&version, &objects); e != nil {
				t.Fatal(e)
			}
			if state == "reserved" {
				if err != nil || version != 31 || objects != 1 {
					t.Fatal("valid upgrade", version, objects, err)
				}
			} else if !errors.Is(err, CodeError("diagnostics_deletion_history_invalid")) || version != 30 || objects != 0 {
				t.Fatal("orphan adopted or partial schema leaked", version, objects, err)
			}
		})
	}
}

func TestDiagnosticsDeletedCandidateRetainsDAGReworkProof(t *testing.T) {
	f := newDAGReworkFixture(t, adapter.ProviderClaude, adapter.ProviderAGY, func(tasks []TaskSpec) []TaskSpec {
		for i := range tasks {
			if tasks[i].ID == "validate" {
				tasks[i].Diagnostics = &contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"}
			}
		}
		return tasks
	})
	ctx := context.Background()
	g := f.grants["validate"]
	if g.Diagnostics == nil {
		t.Fatal("missing detailed validator grant")
	}
	root := filepath.Join(filepath.Dir(f.db.path), "host-spool", g.HostID, g.AttemptID, ".diagnostics")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	scope := diagnostics.Scope{Version: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, ProducerID: g.HostID, PolicySHA256: g.Diagnostics.PolicySHA256}
	w, err := diagnostics.OpenWriter(ctx, root, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Append(ctx, diagnostics.Record{SegmentID: g.SegmentID, Stream: diagnostics.Stdout, Data: []byte("validation logs")}); err != nil {
		w.Abort(ctx, err)
		t.Fatal(err)
	}
	if err = w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	req := DiagnosticsDeleteRequest{Version: 1, TaskID: g.TaskID, AttemptID: g.AttemptID, RequestID: "delete-validation", ExpectedRevision: 1}
	if _, err = f.db.DeleteDiagnostics(ctx, "owner", f.controlToken, req); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.db.QueueRework(ctx, reworkVersionSpec(t, f.spec, 1))
	if err != nil || receipt.TaskRevisions["validate"] != 2 {
		t.Fatal("deleted diagnostics broke historical DAG proof", receipt, err)
	}
	tx, err := f.db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = bindCandidateInput(ctx, tx, &g, true); err == nil {
		t.Fatal("historical deletion proof authorized live dispatch")
	}
}

func TestDiagnosticsDeletionMigrationCommitFailureIsAtomic(t *testing.T) {
	f := newDiagnosticsReadFixture(t)
	if err := removeDiagnosticDeletionSchema31(f.db); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec(`CREATE TABLE deletion_migration_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_deletion_migration AFTER UPDATE OF version ON schema_meta WHEN NEW.version=31 BEGIN INSERT INTO deletion_migration_fault VALUES('missing-parent'); END`); err != nil {
		t.Fatal(err)
	}
	before := deletionUsage(t, f.db)
	if err := f.db.migrateRuntime(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatal("missing actual migration COMMIT failure", err)
	}
	check := func(db *sql.DB) {
		t.Helper()
		var version, objects, faults int
		if err := db.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE name='diagnostic_deletions'),(SELECT COUNT(*) FROM deletion_migration_fault)`).Scan(&version, &objects, &faults); err != nil || version != 30 || objects != 0 || faults != 0 {
			t.Fatal("partial migration persisted", version, objects, faults, err)
		}
	}
	check(f.db.sql)
	external, err := sql.Open("sqlite", f.db.path)
	if err != nil {
		t.Fatal(err)
	}
	check(external)
	external.Close()
	if before != deletionUsage(t, f.db) {
		t.Fatal("failed migration changed accounting")
	}
	if _, err = f.db.sql.Exec(`DROP TRIGGER fail_deletion_migration`); err != nil {
		t.Fatal(err)
	}
	if err = f.db.migrateRuntime(); err != nil {
		t.Fatal("retry migration failed", err)
	}
}

func TestDiagnosticsDeleteRejectsStoreRootReplacement(t *testing.T) {
	f, req := completedDiagnosticsDeleteFixture(t)
	// Replacement has a different SQLite inode even when all logical rows match.
	replacement := filepath.Join(filepath.Dir(f.db.path), "replacement.db")
	if _, err := f.db.sql.Exec(`VACUUM INTO ?`, replacement); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(replacement, 0600); err != nil {
		t.Fatal(err)
	}
	original := f.db.path + ".original"
	if err := os.Rename(f.db.path, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, f.db.path); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(f.db.path); _ = os.Rename(original, f.db.path) }()
	touched := false
	_, err := f.db.deleteDiagnostics(context.Background(), "controller", f.token, req, func(context.Context, string, diagnostics.Scope, string, []byte) (diagnosticDeletionLease, error) {
		touched = true
		return nil, errors.New("unexpected namespace access")
	})
	if !errors.Is(err, CodeError("diagnostics_store_root_invalid")) || touched {
		t.Fatal("replacement Store reached deletion", err, touched)
	}
}

// Enumerate the actual SQL table, not either production cost formula. Every
// TEXT value is charged in UTF-8 bytes; phase reserves its longest legal value
// so completing a deletion cannot require more admission after file removal.
func independentlyMeasuredDeletionCost(t *testing.T, db *DB) int64 {
	t.Helper()
	rows, err := db.sql.Query(`PRAGMA table_info(diagnostic_deletions)`)
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var index, notNull, pk int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&index, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if kind == "TEXT" {
			columns = append(columns, name)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(columns) == 0 {
		t.Fatal("no text fields found")
	}
	values := make([]string, len(columns))
	targets := make([]any, len(columns))
	for i := range targets {
		targets[i] = &values[i]
	}
	if err = db.sql.QueryRow("SELECT " + strings.Join(columns, ",") + " FROM diagnostic_deletions").Scan(targets...); err != nil {
		t.Fatal(err)
	}
	var marker int64
	if err = db.sql.QueryRow(`SELECT marker_bytes FROM diagnostic_deletions`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	total := int64(64) + marker
	for i, name := range columns {
		if name == "phase" {
			total += int64(len("prepared"))
		} else {
			total += int64(len([]byte(values[i])))
		}
	}
	return total
}
