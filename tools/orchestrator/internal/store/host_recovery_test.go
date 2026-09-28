package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestHostRecoveryInspectionPreservesLedgerAndRejectsUnsafeRuns(t *testing.T) {
	for _, mode := range []string{"paused", "wrong_controller", "wrong_token", "stale_revision", "unknown", "prepared", "launch_requested", "running", "stopping", "unexpected_segment", "ready_task", "resume_queued", "pending_stop", "reconciling"} {
		t.Run(mode, func(t *testing.T) {
			f := newReworkFixture(t)
			ctx := context.Background()
			if _, err := f.db.sql.Exec(`UPDATE run_control SET token_hash=?`, runtimeHash("test-control")); err != nil {
				t.Fatal(err)
			}
			if err := f.db.MarkHostOffline(ctx, f.host, 1); err != nil {
				t.Fatal(err)
			}
			controller, token, revision := "owner", "test-control", 1
			want := "host_recovery_not_quiescent"
			switch mode {
			case "paused":
				want = ""
			case "wrong_controller":
				controller = "other"
				want = "owner_mismatch"
			case "wrong_token":
				token = "other"
				want = "owner_mismatch"
			case "stale_revision":
				revision = 2
				want = string(ErrConflict)
			case "unknown", "prepared", "launch_requested", "running", "stopping", "unexpected_segment":
				if _, err := f.db.sql.Exec(`UPDATE segment_runtime SET status=? WHERE segment_id=?`, mode, f.edit.SegmentID); err != nil {
					t.Fatal(err)
				}
			case "ready_task", "resume_queued":
				status := "ready"
				if mode == "resume_queued" {
					status = mode
				}
				if _, err := f.db.sql.Exec(`UPDATE tasks SET status=? WHERE id='integrate'`, status); err != nil {
					t.Fatal(err)
				}
			case "pending_stop":
				if _, err := f.db.sql.Exec(`INSERT INTO stop_runtime(command_id,host_id,segment_id,reason,deadline_unix_ms,status,created_at) VALUES('stop',?,?,'fixture',1,'pending','now')`, f.host, f.edit.SegmentID); err != nil {
					t.Fatal(err)
				}
			case "reconciling":
				if _, err := f.db.sql.Exec(`UPDATE runtime_hosts SET status='reconciling'`); err != nil {
					t.Fatal(err)
				}
				want = "host_recovery_unavailable"
			}
			before := hostRecoveryLedger(t, f.db)
			result, err := f.db.InspectHostRecovery(ctx, "review", controller, token, revision)
			if want == "" {
				if err != nil || result.RunID != "run" || result.Status != "offline" || result.Executable != "/private/bin/orchestrator" {
					t.Fatalf("paused recovery: %+v %v", result, err)
				}
			} else if err == nil || err.Error() != want {
				t.Fatalf("error=%v want=%s", err, want)
			}
			if after := hostRecoveryLedger(t, f.db); after != before {
				t.Fatal("read-only recovery inspection changed ledger")
			}
		})
	}
}

func hostRecoveryLedger(t *testing.T, db *DB) string {
	t.Helper()
	var out strings.Builder
	for _, table := range []string{"runs", "tasks", "task_runtime", "attempts", "segments", "segment_runtime", "budget_runtime", "runtime_events", "review_decisions", "delivery_receipts", "host_launches", "runtime_hosts", "stop_runtime"} {
		rows, err := db.sql.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&out, table)
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			out.Write(raw)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return out.String()
}
