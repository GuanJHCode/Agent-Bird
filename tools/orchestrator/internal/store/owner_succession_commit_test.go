package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestOwnerSuccessionTransitionsDiscardFailedCommit(t *testing.T) {
	for _, stage := range []string{"prepare", "bind", "applied", "activate"} {
		t.Run(stage, func(t *testing.T) {
			f, spec, _ := ownerSuccessionPrepareFixture(t, true)
			ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
			var intent contract.OriginalOwnerIntent
			var binding OriginalOwnerBindResult
			var bindSpec BindOriginalOwnerSpec
			var applied OriginalOwnerAppliedSpec
			var err error
			if stage != "prepare" {
				intent, err = f.db.PrepareSuccessorOwner(ctx, spec)
				if err != nil {
					t.Fatal(err)
				}
				bindSpec = successorOwnerBindSpec(f, spec, intent)
			}
			if stage == "applied" || stage == "activate" {
				binding, err = f.db.BindOriginalOwner(ctx, bindSpec, f.key)
				if err != nil {
					t.Fatal(err)
				}
				var last int64
				if err = f.db.sql.QueryRow(`SELECT MAX(sequence) FROM runtime_events WHERE producer_id=?`, intent.Scope.ProducerID).Scan(&last); err != nil {
					t.Fatal(err)
				}
				applied = successorOwnerApplied(t, bindSpec, binding, last)
			}
			if stage == "activate" {
				if err = f.db.CommitOriginalOwnerApplied(ctx, applied, pub); err != nil {
					t.Fatal(err)
				}
			}
			operation := func() error {
				switch stage {
				case "prepare":
					_, err = f.db.PrepareSuccessorOwner(ctx, spec)
				case "bind":
					_, err = f.db.BindOriginalOwner(ctx, bindSpec, f.key)
				case "applied":
					err = f.db.CommitOriginalOwnerApplied(ctx, applied, pub)
				case "activate":
					err = f.db.ActivateOriginalOwner(ctx, applied, pub)
				}
				return err
			}
			trigger := map[string]string{
				"prepare":  `AFTER INSERT ON execution_owners WHEN NEW.incarnation=2`,
				"bind":     `AFTER UPDATE OF phase ON execution_owners WHEN NEW.incarnation=2 AND NEW.phase='bound'`,
				"applied":  `AFTER UPDATE OF applied_hash ON owner_control_changes WHEN NEW.applied_hash IS NOT NULL AND NEW.owner_id=(SELECT owner_id FROM execution_owners WHERE incarnation=2 AND is_current=1)`,
				"activate": `AFTER UPDATE OF status ON runtime_hosts WHEN NEW.status='ready'`,
			}[stage]
			if _, err = f.db.sql.Exec(`CREATE TABLE successor_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_successor_commit ` + trigger + ` BEGIN INSERT INTO successor_commit_fault VALUES('missing-parent'); END`); err != nil {
				t.Fatal(err)
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			if err = operation(); err == nil {
				t.Fatal("deferred FK did not fail actual COMMIT")
			}
			external, err := sql.Open("sqlite", f.db.path)
			if err != nil {
				t.Fatal(err)
			}
			defer external.Close()
			externalClean := before == ownerSuccessionResumeSnapshot(t, &DB{sql: external})
			poolClean := before == ownerSuccessionResumeSnapshot(t, f.db)
			t.Logf("failed %s COMMIT: external unchanged=%v pooled unchanged=%v", stage, externalClean, poolClean)
			if !externalClean || !poolClean {
				t.Fatal("failed commit exposed uncommitted owner/control/ready state")
			}
			if originalOwnerSchemaCount(t, f.db.sql, `PRAGMA foreign_keys`) != 1 || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM successor_commit_fault`) != 0 {
				t.Fatal("failed connection leaked a row or lost FK enforcement")
			}
			if _, err = f.db.sql.Exec(`DROP TRIGGER fail_successor_commit`); err != nil {
				t.Fatal(err)
			}
			if err = operation(); err != nil {
				t.Fatal("failed commit prevented same-request retry", err)
			}
		})
	}
}
