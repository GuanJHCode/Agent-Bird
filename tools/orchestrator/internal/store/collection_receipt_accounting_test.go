package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestCollectionReceiptAccountingPersistsUsageAndRejectsNewGrowth(t *testing.T) {
	db, _ := quotaFixture(t)
	if _, err := db.sql.Exec(`UPDATE runs SET delivery_mode='collect' WHERE id='run'`); err != nil {
		t.Fatal(err)
	}
	events := []contract.Event{{EventID: "结果", Kind: "result", EventRevision: 1, ActionSlot: "slot", PayloadHash: strings.Repeat("a", 64)}}
	id, proof, raw, err := collectionReceiptParts("task", events)
	if err != nil {
		t.Fatal(err)
	}
	cost := int64(len(id) + len("task") + len(proof) + len(raw))
	global := collectionUsage(t, db.sql, "global", "")
	run := collectionUsage(t, db.sql, "run", "run")
	db.userControlLimit = global + cost
	db.runControlLimit = run + cost
	got, gotProof, err := db.CollectionReceipt(context.Background(), "task", events)
	if err != nil || got != id || gotProof != proof {
		t.Fatalf("receipt=%s proof=%s err=%v", got, gotProof, err)
	}
	if n := collectionUsage(t, db.sql, "global", ""); n != global+cost {
		t.Errorf("global metadata charge=%d want=%d", n, global+cost)
	}
	if n := collectionUsage(t, db.sql, "run", "run"); n != run+cost {
		t.Errorf("run metadata charge=%d want=%d", n, run+cost)
	}
	// Reading the same durable receipt introduces no growth, including when
	// existing historical usage exceeds a subsequently reduced limit.
	db.userControlLimit = global
	db.runControlLimit = run
	if replay, p, err := db.CollectionReceipt(context.Background(), "task", events); err != nil || replay != id || p != proof {
		t.Fatalf("exact replay at limit: %s %s %v", replay, p, err)
	}
	events[0].EventID = "next"
	if _, _, err := db.CollectionReceipt(context.Background(), "task", events); err == nil || err.Error() != "control_spool_full" {
		t.Fatalf("new receipt bypassed admission: %v", err)
	}
	var rows int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM collection_receipts`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("receipt rows=%d err=%v", rows, err)
	}
}

func TestCollectionReceiptAccountingMigratesHistoryExactlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeCollectionSchema24(t, db)
	if _, err := db.sql.Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES('run','controller',1,'origin',1,'birth','queued','now','collect'); INSERT INTO tasks(id,run_id,status,dependencies_json,max_attempts,created_at) VALUES('任务','run','queued','[]',1,'now')`); err != nil {
		t.Fatal(err)
	}
	const id, task, proof, bindings = "receipt-界", "任务", "proof-界", `[{"id":"事件"}]`
	cost := int64(len(id) + len(task) + len(proof) + len(bindings))
	if _, err := db.sql.Exec(`INSERT INTO collection_receipts VALUES(?,?,?,?)`, id, task, proof, bindings); err != nil {
		t.Fatal(err)
	}
	before := collectionUsage(t, db.sql, "global", "")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != currentSchemaVersion {
			t.Errorf("version=%d err=%v", version, err)
		}
		if got := collectionUsage(t, db.sql, "global", ""); got != before+cost {
			t.Errorf("reopen%d global=%d want=%d", i, got, before+cost)
		}
		var run, progress int64
		if err := db.sql.QueryRow(`SELECT total_bytes,progress_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&run, &progress); err != nil || run != cost || progress != 0 {
			t.Errorf("reopen%d run=%d progress=%d err=%v", i, run, progress, err)
		}
		var gotBindings string
		if err := db.sql.QueryRow(`SELECT bindings_json FROM collection_receipts WHERE id=?`, id).Scan(&gotBindings); err != nil || gotBindings != bindings {
			t.Fatalf("history mutated: %q %v", gotBindings, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Downgrade both the physical shape and its accounting for older-version
// fixtures. Never lower only schema_meta and leave new triggers behind.
func downgradeCollectionSchema24(t *testing.T, db *DB) {
	t.Helper()
	downgradeOwnerSuccessionSchema25(t, db)
	var version int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version <= 23 {
		return
	}
	if version != 24 {
		t.Fatalf("cannot remove collection schema from version%d", version)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, sql := range []string{
		`DROP TRIGGER quota_collection_receipts_INSERT`,
		`DROP TRIGGER quota_collection_receipts_DELETE`,
		`DROP TRIGGER collection_receipts_immutable`,
		`UPDATE storage_usage SET total_bytes=total_bytes-(SELECT COALESCE(SUM(length(CAST(id AS BLOB))+length(CAST(task_id AS BLOB))+length(CAST(proof_hash AS BLOB))+length(CAST(bindings_json AS BLOB))),0) FROM collection_receipts) WHERE scope='global' AND scope_id=''`,
		`UPDATE storage_usage SET total_bytes=total_bytes-(SELECT COALESCE(SUM(length(CAST(c.id AS BLOB))+length(CAST(c.task_id AS BLOB))+length(CAST(c.proof_hash AS BLOB))+length(CAST(c.bindings_json AS BLOB))),0) FROM collection_receipts c JOIN tasks t ON t.id=c.task_id WHERE t.run_id=storage_usage.scope_id) WHERE scope='run'`,
		`CREATE TABLE collection_receipts_schema23(id TEXT PRIMARY KEY,task_id TEXT NOT NULL,proof_hash TEXT NOT NULL,bindings_json TEXT NOT NULL)`,
		`INSERT INTO collection_receipts_schema23 SELECT * FROM collection_receipts`,
		`DROP TABLE collection_receipts`,
		`ALTER TABLE collection_receipts_schema23 RENAME TO collection_receipts`,
		`UPDATE schema_meta SET version=23`,
	} {
		if _, err := tx.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionReceiptAccountingTriggersRollbackAndImmutability(t *testing.T) {
	db, _ := quotaFixture(t)
	before := collectionUsage(t, db.sql, "global", "")
	runBefore := collectionUsage(t, db.sql, "run", "run")
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	const id, task, proof, raw = "collection-界", "task", "proof-界", "[]"
	cost := int64(len(id) + len(task) + len(proof) + len(raw))
	if _, err = tx.Exec(`INSERT INTO collection_receipts VALUES(?,?,?,?)`, id, task, proof, raw); err != nil {
		t.Fatal(err)
	}
	if got := collectionUsage(t, tx, "global", ""); got != before+cost {
		t.Fatalf("insert charge=%d want=%d", got, before+cost)
	}
	if _, err = tx.Exec(`UPDATE collection_receipts SET proof_hash='changed' WHERE id=?`, id); err == nil {
		t.Fatal("immutable receipt updated")
	}
	if _, err = tx.Exec(`INSERT INTO collection_receipts VALUES('orphan','missing','proof','[]')`); err == nil {
		t.Fatal("orphan accepted")
	}
	if _, err = tx.Exec(`DELETE FROM collection_receipts WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if got := collectionUsage(t, tx, "global", ""); got != before {
		t.Fatalf("delete refund=%d want=%d", got, before)
	}
	if got := collectionUsage(t, tx, "run", "run"); got != runBefore {
		t.Fatalf("run refund=%d want=%d", got, runBefore)
	}
	if _, err = tx.Exec(`INSERT INTO collection_receipts VALUES(?,?,?,?)`, id, task, proof, raw); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := collectionUsage(t, db.sql, "global", ""); got != before {
		t.Fatalf("rollback leaked usage=%d want=%d", got, before)
	}
	var rows int
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM collection_receipts`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
}

func TestCollectionReceiptAccountingConflictAndConcurrentReplay(t *testing.T) {
	db, _ := quotaFixture(t)
	if _, err := db.sql.Exec(`UPDATE runs SET delivery_mode='collect' WHERE id='run'`); err != nil {
		t.Fatal(err)
	}
	events := []contract.Event{{EventID: "event", Kind: "result", EventRevision: 1, ActionSlot: "slot", PayloadHash: strings.Repeat("a", 64)}}
	id, proof, raw, err := collectionReceiptParts("task", events)
	if err != nil {
		t.Fatal(err)
	}
	before := collectionUsage(t, db.sql, "global", "")
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, p, e := db.CollectionReceipt(context.Background(), "task", events)
			if e == nil && (got != id || p != proof) {
				e = errors.New("different replay")
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cost := int64(len(id) + len("task") + len(proof) + len(raw))
	if got := collectionUsage(t, db.sql, "global", ""); got != before+cost {
		t.Fatalf("concurrent charge=%d want=%d", got, before+cost)
	}
	// Simulate conflicting historical data without disabling production guards.
	if _, err = db.sql.Exec(`DELETE FROM collection_receipts WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`INSERT INTO collection_receipts VALUES(?,'task','wrong','[]')`, id); err != nil {
		t.Fatal(err)
	}
	conflictUsage := collectionUsage(t, db.sql, "global", "")
	if _, _, err = db.CollectionReceipt(context.Background(), "task", events); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
	if got := collectionUsage(t, db.sql, "global", ""); got != conflictUsage {
		t.Fatalf("conflict changed usage=%d want=%d", got, conflictUsage)
	}
}

func TestCollectionReceiptAccountingMigrationFailureAtomic(t *testing.T) {
	for _, stage := range []string{"validated", "copied", "replaced", "backfilled", "triggers", "version", "commit"} {
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
			downgradeCollectionSchema24(t, db)
			if _, err = db.sql.Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at) VALUES('run','controller',1,'origin',1,'birth','queued','now'); INSERT INTO tasks(id,run_id,status,dependencies_json,max_attempts,created_at) VALUES('task','run','queued','[]',1,'now'); INSERT INTO collection_receipts VALUES('receipt','task','proof','[]')`); err != nil {
				t.Fatal(err)
			}
			before := collectionUsage(t, db.sql, "global", "")
			collectionSchema24Hook = func(got string) error {
				if got == stage {
					return errors.New("injected " + stage)
				}
				return nil
			}
			t.Cleanup(func() { collectionSchema24Hook = nil })
			if err = db.migrateRuntime(); err == nil || !strings.Contains(err.Error(), "injected "+stage) {
				t.Fatalf("fault=%v", err)
			}
			var version, fk, objects, rows int
			if err = db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != 23 {
				t.Fatalf("version=%d err=%v", version, err)
			}
			if err = db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("FK=%d err=%v", fk, err)
			}
			if err = db.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'quota_collection_receipts_%' OR name IN ('collection_receipts_immutable','collection_receipts_schema24')`).Scan(&objects); err != nil || objects != 0 {
				t.Fatalf("partial objects=%d err=%v", objects, err)
			}
			if err = db.sql.QueryRow(`SELECT COUNT(*) FROM collection_receipts WHERE id='receipt' AND proof_hash='proof' AND bindings_json='[]'`).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("history=%d err=%v", rows, err)
			}
			if got := collectionUsage(t, db.sql, "global", ""); got != before {
				t.Fatalf("rollback usage=%d want=%d", got, before)
			}
			collectionSchema24Hook = nil
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			db = nil
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if got := collectionUsage(t, reopened.sql, "global", ""); got != before+18 {
				t.Fatalf("retry usage=%d want=%d", got, before+18)
			}
		})
	}
}

func TestCollectionReceiptAccountingInvalidHistoryFailsClosed(t *testing.T) {
	for _, kind := range []string{"orphan", "null_id"} {
		t.Run(kind, func(t *testing.T) {
			db, _ := quotaFixture(t)
			downgradeCollectionSchema24(t, db)
			if kind == "orphan" {
				if _, err := db.sql.Exec(`INSERT INTO collection_receipts VALUES('id','missing','proof','[]')`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.sql.Exec(`INSERT INTO collection_receipts VALUES(NULL,'task','proof','[]')`); err != nil {
					t.Fatal(err)
				}
			}
			before := collectionUsage(t, db.sql, "global", "")
			if err := db.migrateRuntime(); err == nil || err.Error() != "collection_receipt_invalid_history" {
				t.Fatalf("invalid history=%v", err)
			}
			var version, fk int
			if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != 23 {
				t.Fatalf("version=%d err=%v", version, err)
			}
			if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("FK=%d err=%v", fk, err)
			}
			if got := collectionUsage(t, db.sql, "global", ""); got != before {
				t.Fatalf("failed migration changed usage=%d", got)
			}
		})
	}
}

func TestCollectionReceiptAccountingHookReusesExistingAtExactQuota(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			f := newBridgeDeliveryFixture(t)
			tx, err := f.db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			sources, err := bridgeSourceRows(context.Background(), tx, "run", f.binding)
			_ = tx.Rollback()
			if err != nil || len(sources) != 1 {
				t.Fatalf("sources=%v err=%v", sources, err)
			}
			events := []contract.Event{sources[0].event}
			id, proof, _, err := collectionReceiptParts("task", events)
			if err != nil {
				t.Fatal(err)
			}
			if existing {
				if _, _, err = f.db.CollectionReceipt(context.Background(), "task", events); err != nil {
					t.Fatal(err)
				}
			}
			req := f.claimReq()
			candidate, content, eventJSON, receiptJSON, err := bridgeCandidate(contract.BridgeBatch{SchemaVersion: 1, BatchID: "batch-" + strings.Repeat("a", 24), BindingID: f.binding, OwnerEpoch: 1, TransportMode: "hook_pull"}, events)
			if err != nil {
				t.Fatal(err)
			}
			candidate.AttemptID = "attempt-" + strings.Repeat("a", 24)
			wire, hash, err := bridgeWire(candidate, "claude", req.NativeHook)
			if err != nil {
				t.Fatal(err)
			}
			stamp := bridgeTime(time.Now())
			a := bridgeAttempt{id: candidate.AttemptID, batchID: candidate.BatchID, bindingID: f.binding, tokenHash: runtimeHash(req.ClaimToken), state: "CLAIMED", deadline: stamp, senderBirth: f.peerBirth, wire: wire, wireHash: hash}
			growth := bridgeAttemptBytes(a, stamp, "") + bridgeBatchBytes(candidate, content, eventJSON, receiptJSON, stamp)
			for _, e := range candidate.Events {
				growth += bridgeItemBytes(candidate.BatchID, e)
			}
			before := collectionUsage(t, f.db.sql, "global", "")
			f.db.userControlLimit = before + growth
			f.db.runControlLimit = collectionUsage(t, f.db.sql, "run", "run") + growth
			got, err := f.db.ClaimBridge(f.hook, req)
			if !existing {
				if err == nil || err.Error() != "control_spool_full" {
					t.Fatalf("missing receipt bypassed quota: %v", err)
				}
				for _, table := range []string{"collection_receipts", "bridge_batches", "bridge_batch_items", "bridge_delivery_attempts"} {
					var rows int
					if e := f.db.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&rows); e != nil || rows != 0 {
						t.Fatalf("%s rows=%d err=%v", table, rows, e)
					}
				}
				if usage := collectionUsage(t, f.db.sql, "global", ""); usage != before {
					t.Fatalf("quota failure leaked growth=%d", usage-before)
				}
				return
			}
			if err != nil {
				t.Fatalf("existing receipt incorrectly charged at exact quota: %v", err)
			}
			if len(got.DeliveryReceipts) != 1 || got.DeliveryReceipts[0].DeliveryID != id || got.DeliveryReceipts[0].ProofSHA256 != proof {
				t.Fatalf("receipt changed: %+v", got.DeliveryReceipts)
			}
			if usage := collectionUsage(t, f.db.sql, "global", ""); usage != before+growth {
				t.Fatalf("actual growth=%d want=%d", usage-before, growth)
			}
		})
	}
}

func collectionUsage(t *testing.T, q interface{ QueryRow(string, ...any) *sql.Row }, scope, id string) int64 {
	t.Helper()
	var n int64
	if err := q.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope=? AND scope_id=?`, scope, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCollectionReceiptAccountingUpgradeSchema22Graph(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeACPBridgeSchema23(t, db)
	insertSchema22BridgeGraph(t, db.sql)
	const id, task, proof, raw = "receipt-界", "task-界", "proof-界", `[{"id":"事件"}]`
	cost := int64(len(id) + len(task) + len(proof) + len(raw))
	if _, err = db.sql.Exec(`INSERT INTO collection_receipts VALUES(?,?,?,?)`, id, task, proof, raw); err != nil {
		t.Fatal(err)
	}
	before := collectionUsage(t, db.sql, "global", "")
	runBefore := collectionUsage(t, db.sql, "run", "run-界")
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		db, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := collectionUsage(t, db.sql, "global", ""); got != before+cost {
			t.Errorf("reopen%d global=%d want=%d", i, got, before+cost)
		}
		if got := collectionUsage(t, db.sql, "run", "run-界"); got != runBefore+cost {
			t.Errorf("reopen%d run=%d want=%d", i, got, runBefore+cost)
		}
		var version, fk, rows, violations int
		if err = db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != currentSchemaVersion {
			t.Errorf("version=%d err=%v", version, err)
		}
		if err = db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
			t.Errorf("FK=%d err=%v", fk, err)
		}
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
			t.Errorf("violations=%d err=%v", violations, err)
		}
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM collection_receipts WHERE id=? AND task_id=? AND proof_hash=? AND bindings_json=?`, id, task, proof, raw).Scan(&rows); err != nil || rows != 1 {
			t.Errorf("receipt rows=%d err=%v", rows, err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
