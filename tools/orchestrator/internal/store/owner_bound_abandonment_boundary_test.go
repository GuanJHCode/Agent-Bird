package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestBoundOwnerAbandonmentRejectsChangedAuthorityAndReservation(t *testing.T) {
	for _, change := range []string{"token", "command_collision", "task_revision", "host", "origin", "epoch", "digest", "key", "historical_applied", "cancelled", "ready_sequence", "snapshot_after_proof"} {
		t.Run(change, func(t *testing.T) {
			f, req, obs, _, facts := boundAbandonmentFixture(t)
			ctx := context.Background()
			key := f.key.Public().(ed25519.PublicKey)
			switch change {
			case "token":
				req.ControlToken = "wrong"
			case "command_collision":
				req.CommandID = "resume-command-界"
			case "task_revision":
				req.WorkRevision++
			case "host":
				obs.Host.Birth += "x"
			case "origin":
				obs.Origin.OriginBirth += "x"
			case "epoch":
				obs.Epoch++
			case "digest":
				obs.ExecutableSHA256 = strings.Repeat("f", 64)
			case "key":
				key = make(ed25519.PublicKey, ed25519.PublicKeySize)
			case "historical_applied":
				if _, err := f.db.sql.Exec(`UPDATE owner_control_changes SET applied_hash=? WHERE owner_id=?`, strings.Repeat("a", 64), obs.OwnerID); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				if _, err := f.db.sql.Exec(`UPDATE tasks SET status='cancelled' WHERE id=?`, req.TaskID); err != nil {
					t.Fatal(err)
				}
			case "ready_sequence":
				if _, err := f.db.sql.Exec(`UPDATE task_runtime SET ready_sequence=ready_sequence+1 WHERE task_id=?`, req.TaskID); err != nil {
					t.Fatal(err)
				}
			case "snapshot_after_proof":
				if _, err := f.db.PrepareBoundOwnerAbandonment(ctx, req, obs, facts, key); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.sql.Exec(`UPDATE task_runtime SET work_revision=work_revision+1 WHERE task_id=?`, req.TaskID); err != nil {
					t.Fatal(err)
				}
			}
			before := ownerSuccessionResumeSnapshot(t, f.db) + ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_bound_abandonments", "owner_recovery_scope_handoffs")
			if _, err := f.db.PrepareBoundOwnerAbandonment(ctx, req, obs, facts, key); err == nil {
				t.Fatal("unproved abandonment", change)
			}
			if before != ownerSuccessionResumeSnapshot(t, f.db)+ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_bound_abandonments", "owner_recovery_scope_handoffs") {
				t.Fatal("rejection wrote")
			}
		})
	}
}

func TestBoundOwnerAbandonmentAtomicQuotaAndCommit(t *testing.T) {
	for _, stage := range []string{"quota", "prepare", "apply", "resume"} {
		t.Run(stage, func(t *testing.T) {
			f, req, obs, _, facts := boundAbandonmentFixture(t)
			ctx := context.Background()
			key := f.key.Public().(ed25519.PublicKey)
			if stage == "apply" || stage == "resume" {
				if _, err := f.db.PrepareBoundOwnerAbandonment(ctx, req, obs, facts, key); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "resume" {
				if _, err := f.db.ApplyBoundOwnerAbandonment(ctx, req, obs, facts, key); err != nil {
					t.Fatal(err)
				}
			}
			op := func() error {
				var err error
				switch stage {
				case "quota", "prepare":
					_, err = f.db.PrepareBoundOwnerAbandonment(ctx, req, obs, facts, key)
				case "apply":
					_, err = f.db.ApplyBoundOwnerAbandonment(ctx, req, obs, facts, key)
				case "resume":
					_, err = f.db.ResumeBoundOwnerAbandonment(ctx, req, obs, facts, key)
				}
				return err
			}
			if stage == "quota" {
				f.db.runControlLimit = originalOwnerUsed(t, f)
			} else {
				trigger := map[string]string{"prepare": "AFTER INSERT ON owner_bound_abandonments", "apply": "AFTER UPDATE ON owner_recovery_scope_handoffs", "resume": "AFTER UPDATE OF status ON host_launches WHEN NEW.status='offline'"}[stage]
				if _, err := f.db.sql.Exec(`CREATE TABLE abandonment_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER abandonment_commit_fail ` + trigger + ` BEGIN INSERT INTO abandonment_commit_fault VALUES('absent-parent'); END`); err != nil {
					t.Fatal(err)
				}
			}
			snap := func(d *DB) string {
				return ownerSuccessionResumeSnapshot(t, d) + ownerSuccessionResumeTablesSnapshot(t, d, "owner_bound_abandonments", "owner_recovery_scope_handoffs")
			}
			before := snap(f.db)
			err := op()
			if err == nil || (stage != "quota" && !strings.Contains(err.Error(), "FOREIGN KEY")) {
				t.Fatal("wrong failure", err)
			}
			external, e := sql.Open("sqlite", f.db.path)
			if e != nil {
				t.Fatal(e)
			}
			defer external.Close()
			if before != snap(f.db) || before != snap(&DB{sql: external}) {
				t.Fatal("failed commit leaked or left pooled txn")
			}
			if stage != "quota" {
				if _, err = f.db.sql.Exec(`DROP TRIGGER abandonment_commit_fail`); err != nil {
					t.Fatal(err)
				}
				if err = op(); err != nil {
					t.Fatal("replay poisoned", err)
				}
			}
		})
	}
}

func TestBoundOwnerAbandonmentConcurrentFinalization(t *testing.T) {
	f, req, obs, _, facts := boundAbandonmentFixture(t)
	ctx := context.Background()
	key := f.key.Public().(ed25519.PublicKey)
	if _, err := f.db.PrepareBoundOwnerAbandonment(ctx, req, obs, facts, key); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ApplyBoundOwnerAbandonment(ctx, req, obs, facts, key); err != nil {
		t.Fatal(err)
	}
	before := ownerSuccessionResumeTablesSnapshot(t, f.db, "task_runtime", "runtime_meta", "budget_runtime")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for j := range errs {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			r := req
			if j == 1 {
				r.CommandID += "-other"
			}
			_, errs[j] = f.db.ResumeBoundOwnerAbandonment(ctx, r, obs, facts, key)
		}(j)
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatal("expected one winning command", errs)
	}
	if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM owner_succession_authorizations WHERE abandoned_owner_id IS NOT NULL`) != 1 || before != ownerSuccessionResumeTablesSnapshot(t, f.db, "task_runtime", "runtime_meta", "budget_runtime") {
		t.Fatal("duplicate authorization/queue/budget")
	}
}

func TestBoundOwnerAbandonmentExactCombinedQuota(t *testing.T) {
	const now = "2026-10-08T02:00:00Z"
	// Price one real transaction, independently cross-check its two immutable
	// rows through the schema accounting test, then straddle the admission byte.
	f, req, obs, _, facts := boundAbandonmentFixture(t)
	key := f.key.Public().(ed25519.PublicKey)
	ctx := context.Background()
	before := originalOwnerUsed(t, f)
	if _, err := f.db.prepareBoundOwnerAbandonment(ctx, req, obs, facts, key, func() string { return now }); err != nil {
		t.Fatal(err)
	}
	cost := originalOwnerUsed(t, f) - before
	if cost <= 64 {
		t.Fatal("missing combined charge", cost)
	}
	for _, delta := range []int64{-1, 0} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			g, r, o, _, facts := boundAbandonmentFixture(t)
			before := originalOwnerUsed(t, g)
			g.db.runControlLimit = before + cost + delta
			_, err := g.db.prepareBoundOwnerAbandonment(ctx, r, o, facts, g.key.Public().(ed25519.PublicKey), func() string { return now })
			if delta < 0 {
				if err == nil || originalOwnerUsed(t, g) != before || originalOwnerSchemaCount(t, g.db.sql, `SELECT COUNT(*) FROM owner_bound_abandonments`)+originalOwnerSchemaCount(t, g.db.sql, `SELECT COUNT(*) FROM owner_recovery_scope_handoffs`) != 0 {
					t.Fatal("underpriced transaction partially committed", err)
				}
			} else {
				if err != nil || originalOwnerUsed(t, g) != before+cost {
					t.Fatal("exact byte rejected/mischarged", err)
				}
			}
		})
	}
}
