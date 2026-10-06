package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
)

func TestDecisionBindingsConcurrentDecisionAndClaimExactlyOnce(t *testing.T) {
	db, host, spec := newOrdinaryDecisionFixture(t)
	ctx := context.Background()
	before := continuationQuotaState(t, db)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			r, err := db.QueueRetry(ctx, spec)
			if err == nil && (r.NextAttemptNo != 2 || r.ActionSlot != spec.ActionSlot) {
				err = errors.New("replay changed receipt")
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if continuationQuotaState(t, db) != before {
		t.Fatal("exact decision replay changed ledger")
	}
	claimed := make(chan string, 2)
	errs = make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() { g, err := db.ClaimReady(ctx, host, 1, 2); claimed <- g.AttemptID; errs <- err })
	}
	wg.Wait()
	close(claimed)
	close(errs)
	var success int
	for id := range claimed {
		if id != "" {
			success++
		}
	}
	for err := range errs {
		if err != nil && !errors.Is(err, ErrNoReady) {
			t.Fatal(err)
		}
	}
	var attempts, consumed, pending int
	if err := db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM attempts),(SELECT COUNT(attempt_id) FROM decision_attempt_bindings),(SELECT COUNT(*) FROM decision_attempt_bindings WHERE attempt_id IS NULL)`).Scan(&attempts, &consumed, &pending); err != nil || success != 1 || attempts != 2 || consumed != 1 || pending != 0 {
		t.Fatalf("concurrent result %d/%d/%d/%d err=%v", success, attempts, consumed, pending, err)
	}
}

func TestDecisionBindingsCASFailureRollsBackAttemptAndReservation(t *testing.T) {
	db, host, _ := newOrdinaryDecisionFixture(t)
	if _, err := db.sql.Exec(`CREATE TRIGGER injected_binding_cas BEFORE UPDATE OF attempt_id ON decision_attempt_bindings BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	before := continuationQuotaState(t, db)
	if _, err := db.ClaimReady(context.Background(), host, 1, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("zero-row CAS err=%v", err)
	}
	if continuationQuotaState(t, db) != before {
		t.Fatal("failed CAS advanced durable ledger")
	}
	if _, err := db.sql.Exec(`DROP TRIGGER injected_binding_cas`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimReady(context.Background(), host, 1, 2); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionBindingsQuotaChecksCombinedRowsAndConsumeGrowth(t *testing.T) {
	for _, scope := range []string{"run", "global"} {
		t.Run(scope, func(t *testing.T) {
			calibration := newReworkFixture(t)
			if _, err := calibration.db.QueueRework(context.Background(), calibration.spec); err != nil {
				t.Fatal(err)
			}
			var payloadBytes, bindingBytes int64
			if err := calibration.db.sql.QueryRow(`SELECT length(CAST(revision_payload AS BLOB)) FROM retry_decisions`).Scan(&payloadBytes); err != nil {
				t.Fatal(err)
			}
			// Independent accounting from the actual persisted row columns.
			if err := calibration.db.sql.QueryRow(`SELECT SUM(16+length(CAST(action_slot AS BLOB))+length(CAST(task_id AS BLOB))+length(CAST(COALESCE(attempt_id,'') AS BLOB))) FROM decision_attempt_bindings`).Scan(&bindingBytes); err != nil {
				t.Fatal(err)
			}
			f := newReworkFixture(t)
			setContinuationQuotaRemaining(t, f.db, scope, payloadBytes+bindingBytes-1)
			before := continuationQuotaState(t, f.db)
			if _, err := f.db.QueueRework(context.Background(), f.spec); !errors.Is(err, CodeError("control_spool_full")) {
				t.Fatalf("combined quota err=%v", err)
			}
			if continuationQuotaState(t, f.db) != before {
				t.Fatal("combined quota refusal partially committed four targets")
			}
			setContinuationQuotaRemaining(t, f.db, scope, payloadBytes+bindingBytes)
			if _, err := f.db.QueueRework(context.Background(), f.spec); err != nil {
				t.Fatal("tight positive quota", err)
			}
			setContinuationQuotaRemaining(t, f.db, scope, 31)
			before = continuationQuotaState(t, f.db)
			if _, err := f.db.ClaimReady(context.Background(), f.host, 1, 2); !errors.Is(err, CodeError("control_spool_full")) {
				t.Fatalf("consume growth err=%v", err)
			}
			if continuationQuotaState(t, f.db) != before {
				t.Fatal("consume quota refusal advanced attempts/reservations")
			}
			setContinuationQuotaRemaining(t, f.db, scope, 32)
			if _, err := f.db.ClaimReady(context.Background(), f.host, 1, 2); err != nil {
				t.Fatal("exact consume growth", err)
			}
		})
	}
}

func TestTaskLocalOrdinalOverflowAndGapDoNotGrantCapacity(t *testing.T) {
	for _, mode := range []string{"gap", "overflow", "negative"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.CreateRun(ctx, RunSpec{ID: "run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateTask(ctx, TaskSpec{ID: "task", RunID: "run", MaxAttempts: 3}); err != nil {
				t.Fatal(err)
			}
			first, err := db.StartAttempt(ctx, "task", "first")
			if err != nil {
				t.Fatal(err)
			}
			if err = db.FinishAttempt(ctx, first.ID, "failed"); err != nil {
				t.Fatal(err)
			}
			ordinal := int64(9)
			if mode == "overflow" {
				ordinal = math.MaxInt64
			} else if mode == "negative" {
				ordinal = -1
			}
			if _, err = db.sql.Exec(`UPDATE attempts SET attempt_no=? WHERE id=?`, ordinal, first.ID); err != nil {
				t.Fatal(err)
			}
			if mode != "gap" {
				before := continuationQuotaState(t, db)
				if _, err = db.StartAttempt(ctx, "task", "overflow"); !errors.Is(err, CodeError("attempt_ordinal_overflow")) {
					t.Fatal(err)
				}
				if continuationQuotaState(t, db) != before {
					t.Fatal("overflow wrote ledger")
				}
				return
			}
			for i := 0; i < 2; i++ {
				a, err := db.StartAttempt(ctx, "task", []string{"second", "third"}[i])
				if err != nil || a.AttemptNo != 10+i {
					t.Fatalf("gap ordinal=%d err=%v", a.AttemptNo, err)
				}
				if err = db.FinishAttempt(ctx, a.ID, "failed"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.StartAttempt(ctx, "task", "fourth"); !errors.Is(err, ErrAttemptLimit) {
				t.Fatalf("gap capacity err=%v", err)
			}
		})
	}
}

func TestDecisionBindingV12MigrationKeepsHistoricalReceiptWithoutBackfill(t *testing.T) {
	db, _, spec := newOrdinaryDecisionFixture(t)
	if _, err := db.sql.Exec(`DELETE FROM decision_attempt_bindings; DROP TABLE config_preparation_abandons; DROP TABLE decision_attempt_bindings; UPDATE retry_decisions SET next_attempt_no=9; UPDATE attempts SET attempt_no=7; UPDATE schema_meta SET version=12`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.sql.QueryRow(`SELECT action_slot||':'||next_attempt_no||':'||(SELECT attempt_no FROM attempts) FROM retry_decisions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	path := db.path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var after string
	var version, bindings int
	if err = upgraded.sql.QueryRow(`SELECT action_slot||':'||next_attempt_no||':'||(SELECT attempt_no FROM attempts) FROM retry_decisions`).Scan(&after); err != nil || before != after {
		t.Fatal("historical receipt renumbered", err)
	}
	if err = upgraded.sql.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM decision_attempt_bindings)`).Scan(&version, &bindings); err != nil || version != currentSchemaVersion || bindings != 0 {
		t.Fatal("migration manufactured binding", version, bindings, err)
	}
	// Historical exact replay returns its unchanged promise, without backfill.
	spec.NextAttemptNo = 9
	r, err := upgraded.QueueRetry(context.Background(), spec)
	if err != nil || r.NextAttemptNo != 9 {
		t.Fatal(r, err)
	}
	if originalOwnerSchemaCount(t, upgraded.sql, `SELECT COUNT(*) FROM decision_attempt_bindings`) != 0 {
		t.Fatal("receipt replay enrolled legacy authority")
	}
}

func TestDecisionBindingV12MigrationDDLFailureRollsBack(t *testing.T) {
	db, _, _ := newOrdinaryDecisionFixture(t)
	if _, err := db.sql.Exec(`DELETE FROM decision_attempt_bindings; DROP TABLE config_preparation_abandons; DROP TABLE decision_attempt_bindings; UPDATE schema_meta SET version=12; CREATE INDEX decision_attempt_pending_task ON retry_decisions(task_id)`); err != nil {
		t.Fatal(err)
	}
	path := db.path
	db.Close()
	if opened, err := Open(path); err == nil {
		opened.Close()
		t.Fatal("conflicting DDL accepted")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if originalOwnerSchemaCount(t, raw, `SELECT version FROM schema_meta`) != 12 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='decision_attempt_bindings'`) != 0 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM retry_decisions`) != 1 {
		t.Fatal("failed migration changed DDL/version/receipt")
	}
}

func TestLegacyDecisionMalformedPayloadCannotBecomeInitialAdmission(t *testing.T) {
	for _, raw := range []string{`{"kind":"unknown"}`, `{"request":null}`, `{"kind":"candidate_review_retry","kind":"candidate_review_retry"}`, `{"KIND":"candidate_review_retry"}`} {
		t.Run(raw, func(t *testing.T) {
			f := newReworkFixture(t)
			ctx := context.Background()
			if _, err := f.db.QueueRework(ctx, f.spec); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.sql.Exec(`DELETE FROM decision_attempt_bindings; UPDATE retry_decisions SET revision_payload=?`, raw); err != nil {
				t.Fatal(err)
			}
			before := continuationQuotaState(t, f.db)
			if _, err := f.db.ClaimReady(ctx, f.host, 1, 2); !errors.Is(err, errLegacyDecisionAmbiguous) {
				t.Fatal("malformed decision admitted", err)
			}
			if continuationQuotaState(t, f.db) != before {
				t.Fatal("malformed decision changed ledger")
			}
		})
	}
}

func TestDecisionBindingsQuotaChargesUTF8BytesInBothScopes(t *testing.T) {
	db, _, spec := newOrdinaryDecisionTaskFixture(t, "任务🎵")
	var expected int64
	if err := db.sql.QueryRow(`SELECT SUM(length(CAST(body_json AS BLOB))) FROM runtime_events`).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	expected += 16 + int64(len(spec.ActionSlot)) + int64(len(spec.TaskID))
	run, global := continuationQuotaUsage(t, db)
	if run != expected || global != expected {
		t.Fatalf("UTF8 charge run=%d global=%d expected=%d", run, global, expected)
	}
}
