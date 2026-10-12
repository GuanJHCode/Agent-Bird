package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerGrantRollbackEvidence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Run("publisher_failure", func(t *testing.T) {
		fault := errors.New("publisher failed after its owned file creation")
		grant, rolledBack, err := db.PublishOwnerGrant(ctx, "test", 7, "birth", func(g OwnerGrant, a StoreRootAuthority) error {
			if g.Token == "" || a.Validate() != nil {
				t.Fatal("unbound publisher callback")
			}
			return fault
		})
		if !errors.Is(err, fault) || !rolledBack || grant.ID != "" {
			t.Fatal("missing live rollback proof", rolledBack, err)
		}
		var n int
		if err = db.SQL().QueryRow(`SELECT COUNT(*) FROM owner_grants`).Scan(&n); err != nil || n != 0 {
			t.Fatal("publisher error authorized owner", n, err)
		}
	})
	t.Run("already_done_is_not_proof", func(t *testing.T) {
		db.mu.Lock()
		defer db.mu.Unlock()
		owned, err := db.beginOwnerTransaction(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer owned.close()
		// The underlying transaction is already done, as can happen via database/sql
		// cancellation. A successful rollback by someone else is not this call's proof.
		if err = owned.tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if owned.rollback() || owned.state != ownerTransactionDiscarded {
			t.Fatal("ErrTxDone became unlink authority")
		}
	})
}
