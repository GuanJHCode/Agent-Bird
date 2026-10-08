package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Never erase a derived proof to manufacture a historical fixture. Ordinary
// rebind history remains, with only schema30's added accounting subtracted.
func removeOwnerAuthorizationRebindSchema30(db *DB) error {
	var version, count int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		return err
	}
	if version < 30 {
		return nil
	}
	if version != 30 {
		return fmt.Errorf("unsupported authorization rebind schema %d", version)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM owner_succession_rebind_proofs`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("cannot erase authorization rebind history")
	}
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var body string
	if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE name='execution_owner_succession_insert'`).Scan(&body); err != nil {
		return err
	}
	old, replacement := ownerAuthorizationRebindTriggerChange()
	for _, c := range [][2]string{{replacement, old},
		{"AND NEW.coordinator_epoch>=a.coordinator_epoch AND NEW.coordinator_epoch>=target.derivation_epoch", "AND NEW.coordinator_epoch>=a.coordinator_epoch"},
		{"=target.origin_context_id", "=a.successor_origin_context_id"}, {"=target.origin_pid", "=a.successor_origin_pid"},
		{"=target.origin_birth", "=a.successor_origin_birth"}, {"=target.host_generation", "=a.successor_host_generation"}} {
		if strings.Count(body, c[0]) != 1 {
			return errors.New("unexpected authorization rebind trigger")
		}
		body = strings.Replace(body, c[0], c[1], 1)
	}
	if _, err = tx.Exec(`DROP TRIGGER execution_owner_succession_insert;` + body); err != nil {
		return err
	}
	cost := originalOwnerBytesSQL("r.", ownerRebindTextColumns, 8)
	if _, err = tx.Exec(`UPDATE storage_usage SET total_bytes=total_bytes-COALESCE((SELECT SUM(` + cost + `) FROM owner_rebinds r WHERE storage_usage.scope='global' OR r.run_id=storage_usage.scope_id),0)`); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND (name LIKE 'owner_succession_rebind_%' OR name LIKE 'quota_owner_succession_rebind_%' OR name LIKE 'quota_owner_rebind_%')`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		names = append(names, name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err = tx.Exec("DROP TRIGGER " + name); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DROP VIEW owner_succession_effective_targets; DROP TABLE owner_succession_rebind_proofs; DROP INDEX owner_succession_run; DROP INDEX owner_rebind_old_generation; UPDATE schema_meta SET version=29`); err != nil {
		return err
	}
	return tx.Commit()
}

func TestPendingAuthorizationRebindMigrationFailedCommitDiscardsLease(t *testing.T) {
	f, _, _ := pendingAuthorizationRebindFixture(t, false)
	if err := removeOwnerAuthorizationRebindSchema30(f.db); err != nil {
		t.Fatal(err)
	}
	legacyPendingAuthorizationRebind(t, f, 1)
	before := ownerSuccessionResumeSnapshot(t, f.db)
	if _, err := f.db.sql.Exec(`CREATE TABLE schema30_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED);
 CREATE TRIGGER fail_schema30_commit AFTER UPDATE OF version ON schema_meta WHEN NEW.version=30
 BEGIN INSERT INTO schema30_commit_fault VALUES('missing-parent'); END`); err != nil {
		t.Fatal(err)
	}
	err := f.db.migrateRuntime()
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatal("did not reach actual deferred FK COMMIT failure", err)
	}
	external, err := sql.Open("sqlite", "file:"+f.db.path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	inspect := func(db *sql.DB) (int, int, int) {
		var version, objects, faults int
		if err := db.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE name='owner_succession_rebind_proofs'),(SELECT COUNT(*) FROM schema30_commit_fault)`).Scan(&version, &objects, &faults); err != nil {
			t.Fatal(err)
		}
		return version, objects, faults
	}
	ev, eo, ef := inspect(external)
	pv, po, pf := inspect(f.db.sql)
	t.Logf("actual deferred FK COMMIT: external version/objects/fault=%d/%d/%d; pool=%d/%d/%d", ev, eo, ef, pv, po, pf)
	if ev != 29 || eo != 0 || ef != 0 || pv != 29 || po != 0 || pf != 0 {
		t.Fatal("failed COMMIT returned uncommitted schema30 physical connection to pool")
	}
	if before != ownerSuccessionResumeSnapshot(t, f.db) || before != ownerSuccessionResumeTablesSnapshot(t, &DB{sql: external}, "runs", "run_control", "owner_rebinds", "execution_owners", "owner_control_changes", "owner_succession_authorizations", "tasks", "task_runtime", "runtime_meta", "runtime_hosts", "host_launches", "attempts", "segments", "segment_runtime", "stop_runtime", "runtime_questions", "budget_runtime", "task_execution_policies", "storage_usage") {
		t.Fatal("failed COMMIT changed business history or quota")
	}
	var fk int
	if err = f.db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatal("reused connection lost FK", fk, err)
	}
	if _, err = f.db.sql.Exec(`DROP TRIGGER fail_schema30_commit`); err != nil {
		t.Fatal(err)
	}
	if err = f.db.migrateRuntime(); err != nil {
		t.Fatal("same Store could not retry migration", err)
	}
	if originalOwnerSchemaCount(t, f.db.sql, `SELECT version FROM schema_meta`) != 30 || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM owner_succession_rebind_proofs`) != 1 {
		t.Fatal("retry lost exact historical derivation")
	}
}

// Emulate schema29's authenticated Store rebind commit. These are synthetic
// persisted migration inputs, not real attachment or process-death evidence.
func legacyPendingAuthorizationRebind(t *testing.T, f originalOwnerFixture, hop int) {
	t.Helper()
	s := authorizationRebindSpec(f, hop)
	var old string
	if err := f.db.sql.QueryRow(`SELECT host_generation FROM host_launches WHERE id=?`, f.prepare.Host.LaunchID).Scan(&old); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO owner_rebinds(run_id,host_generation,old_host_generation,origin_pid,origin_birth,attachment_proof_sha256,created_at) VALUES(?,?,?,?,?,?,?)`, s.RunID, s.HostGeneration, old, s.OriginPID, s.OriginBirth, s.AttachmentProofSHA256, "2026-10-08T00:05:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE runs SET origin_pid=?,origin_birth=? WHERE id=?`, s.OriginPID, s.OriginBirth, s.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE host_launches SET host_generation=? WHERE id=?`, s.HostGeneration, f.prepare.Host.LaunchID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE runtime_hosts SET host_generation=? WHERE launch_id=?`, s.HostGeneration, f.prepare.Host.LaunchID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPendingAuthorizationRebindMigrationBackfillsOrRollsBack(t *testing.T) {
	for _, mode := range []string{"ordinary", "initial", "missing_link", "fork", "consumed_drift", "quota"} {
		t.Run(mode, func(t *testing.T) {
			f, ps, req := pendingAuthorizationRebindFixture(t, mode == "initial")
			if mode == "consumed_drift" {
				if _, err := f.db.PrepareSuccessorOwner(context.Background(), ps); err != nil {
					t.Fatal(err)
				}
			}
			if err := removeOwnerAuthorizationRebindSchema30(f.db); err != nil {
				t.Fatal(err)
			}
			legacyPendingAuthorizationRebind(t, f, 1)
			legacyPendingAuthorizationRebind(t, f, 2)
			if mode == "fork" {
				if _, err := f.db.sql.Exec(`INSERT INTO owner_rebinds SELECT run_id,'foreign-branch',origin_pid,origin_birth,attachment_proof_sha256,created_at,old_host_generation FROM owner_rebinds WHERE host_generation='replacement-generation-1'`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing_link" {
				if _, err := f.db.sql.Exec(`DELETE FROM owner_rebinds WHERE host_generation='replacement-generation-1'`); err != nil {
					t.Fatal(err)
				}
			}
			receipt := pendingAuthorizationReceipt(t, f, req)
			before := ownerSuccessionResumeSnapshot(t, f.db)
			if mode == "quota" {
				run, _ := initialOwnerUsage(t, f.db)
				f.db.runControlLimit = run
			}
			err := f.db.migrateRuntime()
			if mode == "ordinary" || mode == "initial" {
				if err != nil {
					t.Fatal(err)
				}
				if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM owner_succession_rebind_proofs`) != 2 {
					t.Fatal("missing exact historical chain")
				}
				if receipt != pendingAuthorizationReceipt(t, f, req) {
					t.Fatal("migration rewrote receipt")
				}
				ps.Host.OriginPID, ps.Host.OriginBirth, ps.Host.HostGeneration = 702, "replacement-origin-2", "replacement-generation-2"
				if _, err = f.db.PrepareSuccessorOwner(context.Background(), ps); err != nil {
					t.Fatal("backfilled target cannot prepare", err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid schema29 history accepted")
				}
				if originalOwnerSchemaCount(t, f.db.sql, `SELECT version FROM schema_meta`) != 29 || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM sqlite_master WHERE name='owner_succession_rebind_proofs'`) != 0 || before != ownerSuccessionResumeSnapshot(t, f.db) {
					t.Fatal("failed migration changed schema29 or counters")
				}
			}
		})
	}
}

func TestPendingAuthorizationRebindQuotaHopLimitAndSealedProof(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(fmt.Sprintf("quota_short_%v", short), func(t *testing.T) {
			f, ps, _ := pendingAuthorizationRebindFixture(t, true)
			spec := authorizationRebindSpec(f, 1)
			spec.OriginBirth = "新来源"
			stamp := "2026-10-08T00:05:00Z"
			// Compute the business row cost independently from the SQL triggers.
			want := int64(8 + len(spec.RunID) + len(spec.HostGeneration) + len(f.prepare.Host.HostGeneration) + len(spec.OriginBirth) + len(spec.AttachmentProofSHA256) + len(stamp))
			want += int64(24 + len(ps.AuthorizationID) + len(spec.RunID) + len(ps.Host.LaunchID) + len(f.prepare.Host.HostGeneration) + len(spec.HostGeneration) + len(spec.OriginContextID) + len(spec.OriginBirth) + len(spec.AttachmentProofSHA256) + len(stamp))
			run, global := initialOwnerUsage(t, f.db)
			f.db.runControlLimit = run + want
			f.db.userControlLimit = global + want
			if short {
				f.db.runControlLimit--
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			err := f.db.rebindOwner(context.Background(), spec, func() string { return stamp })
			if short {
				if err != CodeError("control_spool_full") || before != ownerSuccessionResumeSnapshot(t, f.db) {
					t.Fatal("one-byte quota shortage not atomic", err)
				}
				return
			}
			if err != nil {
				t.Fatal("exact quota boundary", err)
			}
			a, b := initialOwnerUsage(t, f.db)
			if a-run != want || b-global != want {
				t.Fatal("actual trigger accounting mismatch", a-run, b-global, want)
			}
			spec.ReplayOnly = true
			if err = f.db.RebindOwner(context.Background(), spec); err != nil {
				t.Fatal("exact replay needed more quota", err)
			}
		})
	}
	t.Run("hop_and_immutable", func(t *testing.T) {
		f, ps, _ := pendingAuthorizationRebindFixture(t, false)
		for hop := 1; hop <= 64; hop++ {
			if err := f.db.RebindOwner(context.Background(), authorizationRebindSpec(f, hop)); err != nil {
				t.Fatal(hop, err)
			}
		}
		before := ownerSuccessionResumeSnapshot(t, f.db)
		proofs := ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_succession_rebind_proofs")
		if err := f.db.RebindOwner(context.Background(), authorizationRebindSpec(f, 65)); err == nil {
			t.Fatal("unbounded chain accepted")
		}
		for _, query := range []string{`UPDATE owner_succession_rebind_proofs SET derivation_epoch=2 WHERE hop=1`, `DELETE FROM owner_succession_rebind_proofs WHERE hop=1`, `UPDATE owner_rebinds SET origin_birth='foreign' WHERE host_generation='replacement-generation-1'`, `DELETE FROM owner_rebinds WHERE host_generation='replacement-generation-1'`} {
			if _, err := f.db.sql.Exec(query); err == nil {
				t.Fatal("sealed provenance changed", query)
			}
		}
		if before != ownerSuccessionResumeSnapshot(t, f.db) || proofs != ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_succession_rebind_proofs") {
			t.Fatal("rejected chain changed history")
		}
		rows, err := f.db.sql.Query(`EXPLAIN QUERY PLAN SELECT origin_pid FROM owner_succession_effective_targets WHERE authorization_id=?`, ps.AuthorizationID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		indexed := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			t.Log(detail)
			if strings.Contains(detail, "SCAN a") {
				t.Fatal("single authorization scans all history", detail)
			}
			if strings.Contains(detail, "SEARCH a USING INDEX") && strings.Contains(detail, "authorization_id=?") {
				indexed = true
			}
		}
		if err = rows.Err(); err != nil || !indexed {
			t.Fatal("missing authorization PK plan", err)
		}
	})
}
