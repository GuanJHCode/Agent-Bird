package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestACPBridgeSchema23RequiresActualPositiveIntegers(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insertSchema22BridgeGraph(t, db.sql)
	for _, col := range []string{"acp_turn_ordinal", "acp_rpc_request_id"} {
		t.Run(col, func(t *testing.T) {
			for _, value := range []string{"0", "-1", "1.5", "'not-integer'", "x'01'"} {
				if _, err := db.sql.Exec("UPDATE bridge_delivery_attempts SET " + col + "=" + value + " WHERE attempt_id='attempt-界'"); err == nil {
					t.Errorf("%s accepted %s", col, value)
				}
			}
			if _, err := db.sql.Exec("UPDATE bridge_delivery_attempts SET " + col + "=9223372036854775807 WHERE attempt_id='attempt-界'"); err != nil {
				t.Fatal(err)
			}
			var kind string
			var value int64
			if err := db.sql.QueryRow("SELECT typeof("+col+"),"+col+" FROM bridge_delivery_attempts WHERE attempt_id='attempt-界'").Scan(&kind, &value); err != nil || kind != "integer" || value != 9223372036854775807 {
				t.Fatalf("positive integer kind=%s value=%d err=%v", kind, value, err)
			}
		})
	}
}

func TestACPBridgeSchema23UniqueIndexesActuallyRejectDuplicateReceipts(t *testing.T) {
	for _, pair := range []struct{ column, index string }{{"acp_turn_ordinal", "bridge_acp_delivery_ordinal"}, {"acp_rpc_request_id", "bridge_acp_delivery_request"}} {
		t.Run(pair.column, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			insertSchema22BridgeGraph(t, db.sql)
			if _, err := db.sql.Exec("UPDATE bridge_delivery_attempts SET " + pair.column + "=1 WHERE attempt_id='attempt-界'"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.sql.Exec(`INSERT INTO bridge_batches(batch_id,binding_id,owner_epoch,content_bytes,render_sha256,created_at) VALUES('batch-other','binding-界',1,x'01','other-render','now')`); err != nil {
				t.Fatal(err)
			}
			stmt := `INSERT INTO bridge_delivery_attempts(attempt_id,batch_id,binding_id,owner_epoch,attempt_no,claim_token_hash,state,deadline_at,sender_pid,sender_birth,created_at,` + pair.column + `) VALUES('attempt-other','batch-other','binding-界',1,1,'hash','RECEIVED','now',12,'birth','now',1)`
			if _, err := db.sql.Exec(stmt); err == nil || !strings.Contains(err.Error(), "bridge_delivery_attempts."+pair.column) {
				t.Fatalf("wrong uniqueness rejection: %v", err)
			}
			// Remove only the target index in a rolled-back transaction. The same
			// statement must now succeed, ruling out an unrelated constraint.
			tx, err := db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec("DROP INDEX " + pair.index); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(stmt); err != nil {
				t.Fatalf("target index was not the only blocker: %v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
