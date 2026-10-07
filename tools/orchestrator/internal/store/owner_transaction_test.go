package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestOwnerTransactionDiscardsCancelledUnknownRollback(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owned, err := f.db.beginOwnerTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.close()
	var original any
	if err = owned.conn.Raw(func(raw any) error { original = raw; return nil }); err != nil {
		t.Fatal(err)
	}
	cancel()
	// Await database/sql's asynchronous terminal state. Its rollback result is
	// unavailable to the owner helper, including a possible driver failure.
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err = owned.tx.ExecContext(context.Background(), `SELECT 1`)
		if errors.Is(err, sql.ErrTxDone) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transaction did not enter asynchronous done state", err)
		}
		time.Sleep(time.Millisecond)
	}
	owned.close()
	conn, err := f.db.sql.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var replacement any
	if err = conn.Raw(func(raw any) error { replacement = raw; return nil }); err != nil {
		t.Fatal(err)
	}
	if replacement == original {
		t.Fatal("ErrTxDone without owned commit proof recycled the original driver connection")
	}
	var enabled int
	if err = conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatal("replacement connection lost FK enforcement", err)
	}
}

func TestOwnerTransactionReusesProvenTerminalConnection(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			f := newOriginalOwnerFixture(t)
			owned, err := f.db.beginOwnerTransaction(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer owned.close()
			var original any
			if err = owned.conn.Raw(func(raw any) error { original = raw; return nil }); err != nil {
				t.Fatal(err)
			}
			if commit {
				if err = owned.commit(); err != nil {
					t.Fatal(err)
				}
			}
			owned.close()
			owned.close() // Repeated deferred cleanup must preserve the outcome.
			conn, err := f.db.sql.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var reused any
			if err = conn.Raw(func(raw any) error { reused = raw; return nil }); err != nil {
				t.Fatal(err)
			}
			if reused != original {
				t.Fatal("proven transaction outcome unnecessarily discarded the physical connection")
			}
		})
	}
}
