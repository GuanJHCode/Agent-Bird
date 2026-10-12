package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestACPBridgeSchema23RestoreFailuresDiscardOriginalConnection(t *testing.T) {
	for _, mode := range []string{"enable-driver-error", "query-driver-error", "disabled-after-enable"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			downgradeACPBridgeSchema23(t, db)
			insertSchema22BridgeGraph(t, db.sql)
			var original any
			ops := runtimeForeignKeyOps{enable: func(ctx context.Context, conn *sql.Conn) error {
				if err := conn.Raw(func(raw any) error { original = raw; return nil }); err != nil {
					return err
				}
				if mode == "enable-driver-error" {
					_, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=(`)
					return err
				}
				if mode == "disabled-after-enable" {
					return nil
				} // query observes the real OFF state
				_, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
				return err
			}}
			if mode == "query-driver-error" {
				ops.read = func(ctx context.Context, conn *sql.Conn) (int, error) {
					var n int
					err := conn.QueryRowContext(ctx, `SELECT missing_schema23_column`).Scan(&n)
					return n, err
				}
			}
			injected := errors.New("before-commit")
			schema23MigrationHook = func(stage string) error {
				if stage == "commit" {
					return injected
				}
				return nil
			}
			t.Cleanup(func() { schema23MigrationHook = nil })
			err = db.migrateRuntimeWithForeignKeyOps(ops)
			schema23MigrationHook = nil
			if !errors.Is(err, injected) || !strings.Contains(err.Error(), "\n") {
				t.Fatalf("lost migration or actual restore error: %v", err)
			}
			conn, err := db.sql.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var next any
			if err := conn.Raw(func(raw any) error { next = raw; return nil }); err != nil {
				t.Fatal(err)
			}
			var fk, version int
			if err := conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&fk); err != nil {
				t.Fatal(err)
			}
			if err := conn.QueryRowContext(context.Background(), `SELECT version FROM schema_meta`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			if original == nil || original == next || fk != 1 || version != 22 {
				t.Errorf("failed connection reused=%t foreignKeys=%d version=%d", original == next, fk, version)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("reopen FK=%d err=%v", fk, err)
			}
			if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != currentSchemaVersion {
				t.Fatalf("reopen version=%d err=%v", version, err)
			}
		})
	}
}

// A separate read transaction in DELETE-journal mode forces a real driver
// COMMIT SQLITE_BUSY. This fault harness is not the production WAL setting.
func TestACPBridgeSchema23ActualCommitFailureRollsBackAndRestores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	downgradeACPBridgeSchema23(t, db)
	insertSchema22BridgeGraph(t, db.sql)
	before := schema23Usage(t, db.sql, "global", "")
	if _, err := db.sql.Exec(`PRAGMA journal_mode=DELETE; PRAGMA busy_timeout=20`); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readTx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	var count int
	if err := readTx.QueryRow(`SELECT COUNT(*) FROM bridge_bindings`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("reader count=%d err=%v", count, err)
	}
	reachedCommit := false
	schema23MigrationHook = func(stage string) error {
		if stage == "commit" {
			reachedCommit = true
		}
		return nil
	}
	t.Cleanup(func() { schema23MigrationHook = nil })
	err = db.migrateRuntime()
	schema23MigrationHook = nil
	if !reachedCommit || err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Fatalf("not a real COMMIT failure: reached=%t err=%v", reachedCommit, err)
	}
	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var version, fk int
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != 22 {
		t.Fatalf("commit rollback version=%d err=%v", version, err)
	}
	if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("commit rollback FK=%d err=%v", fk, err)
	}
	if got := schema23Usage(t, db.sql, "global", ""); got != before {
		t.Fatalf("commit rollback quota=%d want=%d", got, before)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.sql.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("retry version=%d err=%v", version, err)
	}
}
