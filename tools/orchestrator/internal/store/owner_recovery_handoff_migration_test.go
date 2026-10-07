package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"strings"
	"testing"
)

func removeOwnerRecoveryHandoffSchema27(db *DB) error {
	var version, count int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		return err
	}
	if version < 27 {
		return nil
	}
	if version != 27 {
		return fmt.Errorf("unsupported handoff fixture schema %d", version)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM owner_recovery_scope_handoffs`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("cannot erase owner recovery handoff history")
	}
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DROP TABLE owner_recovery_scope_handoffs; UPDATE schema_meta SET version=26`); err != nil {
		return err
	}
	return tx.Commit()
}

func TestOwnerRecoveryHandoffSchemaMigrationPreservesAndRollsBack(t *testing.T) {
	for _, fault := range []string{"", "validated", "objects", "triggers", "version", "commit"} {
		t.Run("stage_"+fault, func(t *testing.T) {
			f, _ := recoveryHandoffSchemaFixture(t)
			if err := removeOwnerRecoveryHandoffSchema27(f.db); err != nil {
				t.Fatal(err)
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			path := f.db.path
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			ownerRecoverySchema27Hook = func(stage string) error {
				if stage == fault {
					return fmt.Errorf("fault %s", stage)
				}
				return nil
			}
			t.Cleanup(func() { ownerRecoverySchema27Hook = nil })
			opened, err := Open(path)
			ownerRecoverySchema27Hook = nil
			if fault != "" {
				if err == nil {
					opened.Close()
					t.Fatal("fault ignored")
				}
				raw, e := sql.Open("sqlite", path)
				if e != nil {
					t.Fatal(e)
				}
				if originalOwnerSchemaCount(t, raw, `SELECT version FROM schema_meta`) != 26 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM sqlite_master WHERE name='owner_recovery_scope_handoffs'`) != 0 {
					t.Fatal("migration leaked schema")
				}
				raw.Close()
				opened, err = Open(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			if originalOwnerSchemaCount(t, opened.sql, `SELECT version FROM schema_meta`) != 27 || originalOwnerSchemaCount(t, opened.sql, `SELECT COUNT(*) FROM owner_recovery_scope_handoffs`) != 0 || originalOwnerSchemaCount(t, opened.sql, `SELECT COUNT(*) FROM pragma_foreign_key_check`) != 0 {
				t.Fatal("bad migration")
			}
			if ownerSuccessionResumeSnapshot(t, opened) != before {
				t.Fatal("migration changed old history/accounting")
			}
		})
	}
}
func TestOwnerRecoveryHandoffSchemaDowngradePreservesHistory(t *testing.T) {
	f, r := recoveryHandoffSchemaFixture(t)
	if err := insertRecoveryHandoffSchema(f.db.sql, r); err != nil {
		t.Fatal(err)
	}
	before := ownerSuccessionResumeTablesSnapshot(t, f.db, "schema_meta", "storage_usage", "owner_recovery_scope_handoffs")
	if err := removeOwnerRecoveryHandoffSchema27(f.db); err == nil {
		t.Fatal("erased handoff history")
	}
	if before != ownerSuccessionResumeTablesSnapshot(t, f.db, "schema_meta", "storage_usage", "owner_recovery_scope_handoffs") {
		t.Fatal("refusal changed history")
	}
}

func TestOwnerRecoveryHandoffSchemaMigrationRejectsUnprovenSuccessorHistory(t *testing.T) {
	for _, stage := range []string{"applied_hash_only", "applied", "control2", "retired"} {
		t.Run(stage, func(t *testing.T) {
			f, s, _, b := recoveryHandoffAPIFixture(t)
			ctx := context.Background()
			if err := removeOwnerRecoveryHandoffSchema27(f.db); err != nil {
				t.Fatal(err)
			}
			// Construct a schema26 snapshot, before the new API/gate existed. These
			// statements model the old durable transitions; they do not invent FS proof.
			if stage == "applied_hash_only" || stage == "applied" || stage == "retired" {
				if _, err := f.db.sql.Exec(`UPDATE owner_control_changes SET applied_hash=? WHERE owner_id=? AND revision=1`, strings.Repeat("a", 64), s.OwnerID); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "applied" || stage == "retired" {
				if _, err := f.db.sql.Exec(`UPDATE execution_owners SET applied_control_revision=1 WHERE owner_id=?`, s.OwnerID); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "control2" {
				state, err := f.db.InspectOriginalOwner(ctx, s.OwnerID)
				if err != nil {
					t.Fatal(err)
				}
				permit := b.Permit
				permit.ExpectedControlRevision = 1
				permit.ControlRevision = 2
				permit.CoordinatorEpoch = 2
				permit.OldHost = &b.Binding.FirstHost
				permit.RequestID = "historical-renewal"
				permit.PermitID = "historical-permit"
				permit, err = contract.SignOwnerControlPermit(permit, f.key)
				if err != nil {
					t.Fatal(err)
				}
				body, err := originalOwnerBody(permit)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := f.db.sql.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err = tx.Exec(`UPDATE execution_owners SET control_revision=2,coordinator_epoch=2 WHERE owner_id=?`, s.OwnerID); err != nil {
					t.Fatal(err)
				}
				if err = insertOriginalOwnerPermit(ctx, tx, state.Intent, permit, body, originalOwnerHash([]byte(body)), originalOwnerNow()); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "retired" {
				if _, err := f.db.sql.Exec(`UPDATE tasks SET status='interrupted' WHERE id='owner-run-task'`); err != nil {
					t.Fatal(err)
				}
				scope := b.Binding.Scope
				request := OriginalOwnerResumeRequest{TaskID: "owner-run-task", ControllerThread: "controller", ControlToken: "resume-test-control-token", WorkRevision: 1, CommandID: "historical-next-successor"}
				observation := OriginalOwnerSuccessionObservation{OwnerID: s.OwnerID, Owner: contract.OriginalOwnerTransport{PID: b.Binding.Owner.PID, Birth: b.Binding.Owner.Birth}, Host: b.Binding.FirstHost, Origin: RunOwnerBinding{OriginContextID: scope.OriginalOriginContextID, OriginPID: scope.OriginalOriginPID, OriginBirth: scope.OriginalOriginBirth}, Epoch: 1, ExecutableSHA256: scope.ExecutableSHA256, OwnerExitedAt: "2026-10-08T00:00:00Z", HostExitedAt: "2026-10-08T00:00:00Z"}
				if _, err := f.db.ResumeOriginalOwner(ctx, request, observation, f.key.Public().(ed25519.PublicKey)); err != nil {
					t.Fatal(err)
				}
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			path := f.db.path
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(path)
			if err == nil {
				opened.Close()
				t.Fatal("unproven successor migrated")
			}
			if err.Error() != "owner_recovery_handoff_unproven_history" {
				t.Fatal("unexpected migration error", err)
			}
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if originalOwnerSchemaCount(t, raw, `SELECT version FROM schema_meta`) != 26 || originalOwnerSchemaCount(t, raw, `SELECT COUNT(*) FROM sqlite_master WHERE name='owner_recovery_scope_handoffs'`) != 0 || ownerSuccessionResumeSnapshot(t, &DB{sql: raw}) != before {
				t.Fatal("refused migration changed history")
			}
		})
	}
}
