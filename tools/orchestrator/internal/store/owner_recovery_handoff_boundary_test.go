package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestOwnerRecoveryHandoffChargesFullUTF8OnceAndApplyNeedsNoQuota(t *testing.T) {
	for _, limit := range []string{"run", "global"} {
		for _, deficit := range []int64{0, 1} {
			t.Run(limit+string(rune('0'+deficit)), func(t *testing.T) {
				f, s, facts, b := recoveryHandoffAPIFixture(t)
				ctx := context.Background()
				pub := f.key.Public().(ed25519.PublicKey)
				resolved, err := f.db.ResolveOriginalOwnerRecoveryHandoff(ctx, s, pub)
				if err != nil {
					t.Fatal(err)
				}
				var auth, priorHash, hash string
				var oldControl int
				if err = f.db.sql.QueryRow(`SELECT succession_authorization_id,original_binding_hash FROM execution_owners WHERE owner_id=?`, s.OwnerID).Scan(&auth, &hash); err != nil {
					t.Fatal(err)
				}
				if err = f.db.sql.QueryRow(`SELECT original_binding_hash,control_revision FROM execution_owners WHERE owner_id=?`, resolved.PriorBinding.Scope.OwnerID).Scan(&priorHash, &oldControl); err != nil {
					t.Fatal(err)
				}
				const created = "2026-10-08T01:02:03.123456789Z"
				cost := int64(4 * 8)
				for _, v := range []string{s.OwnerID, auth, b.Binding.Scope.RunID, b.Binding.Scope.LaunchID, b.Binding.Scope.ProducerID, resolved.PriorBinding.Scope.OwnerID, facts.PriorScopeSHA256, facts.ScopeSHA256, facts.PriorRecordSHA256, facts.RecordSHA256, facts.AuthoritySHA256, priorHash, hash, created} {
					cost += int64(len([]byte(v)))
				}
				global, run := collectionUsage(t, f.db.sql, "global", ""), collectionUsage(t, f.db.sql, "run", b.Binding.Scope.RunID)
				if limit == "global" {
					f.db.userControlLimit = global + cost - deficit
				} else {
					f.db.runControlLimit = run + cost - deficit
				}
				before := recoveryHandoffSnapshot(t, f.db)
				p, err := f.db.prepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub, func() string { return created })
				if deficit != 0 {
					if err != CodeError("control_spool_full") || before != recoveryHandoffSnapshot(t, f.db) {
						t.Fatal("deficit admitted/mutated", err)
					}
					return
				}
				if err != nil || p.Applied {
					t.Fatal(err)
				}
				if collectionUsage(t, f.db.sql, "global", "") != global+cost || collectionUsage(t, f.db.sql, "run", b.Binding.Scope.RunID) != run+cost {
					t.Fatal("incorrect full cost")
				}
				// Other operations may consume remaining allowance after Prepare. Applying
				// this reserved marker must still succeed, without admission or growth.
				f.db.userControlLimit = 1
				f.db.runControlLimit = 1
				if _, err = f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
					t.Fatal("already-reserved apply required new quota", err)
				}
				if collectionUsage(t, f.db.sql, "global", "") != global+cost || collectionUsage(t, f.db.sql, "run", b.Binding.Scope.RunID) != run+cost {
					t.Fatal("apply grew quota")
				}
			})
		}
	}
}

func TestOwnerRecoveryHandoffTransactionsDiscardActualFailedCommit(t *testing.T) {
	for _, stage := range []string{"prepare", "apply"} {
		t.Run(stage, func(t *testing.T) {
			f, s, facts, _ := recoveryHandoffAPIFixture(t)
			ctx := context.Background()
			pub := f.key.Public().(ed25519.PublicKey)
			if stage == "apply" {
				if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
					t.Fatal(err)
				}
			}
			op := "AFTER INSERT"
			if stage == "apply" {
				op = "AFTER UPDATE OF applied"
			}
			if _, err := f.db.sql.Exec(`CREATE TABLE recovery_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER recovery_commit_failure ` + op + ` ON owner_recovery_scope_handoffs BEGIN INSERT INTO recovery_commit_fault VALUES('missing-parent'); END`); err != nil {
				t.Fatal(err)
			}
			operation := func() error {
				var err error
				if stage == "prepare" {
					_, err = f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
				} else {
					_, err = f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
				}
				return err
			}
			before := recoveryHandoffSnapshot(t, f.db)
			if err := operation(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
				t.Fatal("not an actual deferred COMMIT failure", err)
			}
			external, err := sql.Open("sqlite", f.db.path)
			if err != nil {
				t.Fatal(err)
			}
			defer external.Close()
			if before != recoveryHandoffSnapshot(t, &DB{sql: external}) || before != recoveryHandoffSnapshot(t, f.db) {
				t.Fatal("failed COMMIT leaked external or pooled state")
			}
			if originalOwnerSchemaCount(t, f.db.sql, `PRAGMA foreign_keys`) != 1 || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM recovery_commit_fault`) != 0 {
				t.Fatal("connection/row leaked")
			}
			if _, err = f.db.sql.Exec(`DROP TRIGGER recovery_commit_failure`); err != nil {
				t.Fatal(err)
			}
			if err = operation(); err != nil {
				t.Fatal("same proof could not retry", err)
			}
		})
	}
}

func TestOwnerRecoveryHandoffRestartBeforePrepareOrApplyPrecedesRenewal(t *testing.T) {
	for _, stage := range []string{"before_prepare", "before_apply"} {
		t.Run(stage, func(t *testing.T) {
			f, s, facts, b := recoveryHandoffAPIFixture(t)
			ctx := context.Background()
			pub := f.key.Public().(ed25519.PublicKey)
			if stage == "before_apply" {
				if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
				t.Fatal(err)
			}
			s.Epoch = 2
			if _, err := f.db.RegisterHost(ctx, s.Host, 2); err != nil {
				t.Fatal(err)
			}
			sh, _ := contract.OriginalOwnerScopeSHA256(b.Binding.Scope)
			ih, _ := contract.OriginalOwnerIdentitySHA256(b.Binding.Owner)
			renewal := OriginalOwnerAttachSpec{OwnerID: s.OwnerID, ScopeSHA256: sh, OwnerIdentitySHA256: ih, CoordinatorKeyID: b.Binding.CoordinatorKeyID, ExpectedControlRevision: 1, OldHost: b.Binding.FirstHost, NewHost: b.Binding.FirstHost, Epoch: 2, RequestID: "handoff-restart-renewal", RequestSHA256: strings.Repeat("d", 64), Authentication: OriginalOwnerAttachAuthentication{Owner: s.Authentication, Host: s.Host, Epoch: 2}}
			before := recoveryHandoffSnapshot(t, f.db)
			if _, err := f.db.CommitOriginalOwnerAttach(ctx, renewal, f.key); err == nil {
				t.Fatal("renewal bypassed pending handoff")
			}
			if before != recoveryHandoffSnapshot(t, f.db) {
				t.Fatal("rejected renewal changed state")
			}
			if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
				t.Fatal("prepare at current epoch", err)
			}
			if _, err := f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
				t.Fatal("apply at current epoch", err)
			}
			permit, err := f.db.CommitOriginalOwnerAttach(ctx, renewal, f.key)
			if err != nil {
				t.Fatal("renewal after completed handoff", err)
			}
			b.Permit = permit
			applied := successorOwnerApplied(t, BindOriginalOwnerSpec{Host: s.Host, Epoch: 2, Authenticated: s.Authentication}, b, 0)
			if err = f.db.CommitOriginalOwnerApplied(ctx, applied, pub); err != nil {
				t.Fatal(err)
			}
			if err = f.db.ActivateOriginalOwner(ctx, applied, pub); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOwnerRecoveryHandoffChangedValidBytesConflictAfterPrepare(t *testing.T) {
	for _, applied := range []bool{false, true} {
		for _, field := range []string{"prior_record", "record", "authority"} {
			t.Run(fmt.Sprintf("%s/applied_%t", field, applied), func(t *testing.T) {
				f, s, facts, _ := recoveryHandoffAPIFixture(t)
				ctx := context.Background()
				pub := f.key.Public().(ed25519.PublicKey)
				if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
					t.Fatal(err)
				}
				if applied {
					if _, err := f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
						t.Fatal(err)
					}
				}
				before := recoveryHandoffSnapshot(t, f.db)
				switch field {
				case "prior_record":
					facts.PriorRecordSHA256 = strings.Repeat("d", 64)
				case "record":
					facts.RecordSHA256 = strings.Repeat("d", 64)
				case "authority":
					facts.AuthoritySHA256 = strings.Repeat("d", 64)
				}
				if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err == nil {
					t.Fatal("changed valid digest prepared")
				}
				if _, err := f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err == nil {
					t.Fatal("changed valid digest applied")
				}
				if before != recoveryHandoffSnapshot(t, f.db) {
					t.Fatal("conflict wrote")
				}
			})
		}
	}
}
func TestOwnerRecoveryHandoffCancellationHasNoLateWrites(t *testing.T) {
	for _, stage := range []string{"prepare", "apply", "prepare_after_resolve"} {
		t.Run(stage, func(t *testing.T) {
			f, s, facts, _ := recoveryHandoffAPIFixture(t)
			pub := f.key.Public().(ed25519.PublicKey)
			if stage == "apply" {
				if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(context.Background(), s, facts, pub); err != nil {
					t.Fatal(err)
				}
			}
			before := recoveryHandoffSnapshot(t, f.db)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var err error
			if stage == "prepare_after_resolve" {
				_, err = f.db.prepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub, func() string { cancel(); return originalOwnerNow() })
			} else {
				cancel()
				if stage == "apply" {
					_, err = f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
				} else {
					_, err = f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
				}
			}
			if err == nil {
				t.Fatal("canceled mutation accepted")
			}
			if before != recoveryHandoffSnapshot(t, f.db) {
				t.Fatal("cancellation wrote state")
			}
			if stage == "apply" {
				_, err = f.db.ApplyOriginalOwnerRecoveryHandoff(context.Background(), s, facts, pub)
			} else {
				_, err = f.db.PrepareOriginalOwnerRecoveryHandoff(context.Background(), s, facts, pub)
			}
			if err != nil {
				t.Fatal("cancelled lease prevented later exact retry", err)
			}
		})
	}
}
