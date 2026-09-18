package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func quotaFixture(t testing.TB) (*DB, contract.LaunchCommand) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "run", ControllerThread: "master", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "gen", Executable: "/private/bin/host"}, Tasks: []TaskSpec{{ID: "task", RunID: "run", MaxAttempts: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "gen", PID: 9, Birth: "host", Executable: "/private/bin/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	return db, launch
}

const quotaInsert = `INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,delivery_status,created_at) VALUES(?,?,?,'task',?,?,'hash',?,'internal','now')`

func BenchmarkQuotaAdmission(b *testing.B) {
	db, launch := quotaFixture(b)
	tx, err := db.sql.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i := 1; i <= 5000; i++ {
		if _, err = tx.Exec(quotaInsert, fmt.Sprint(i), launch.HostID, i, launch.AttemptID, launch.SegmentID, strings.Repeat("x", 512)); err != nil {
			b.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	tx, err = db.sql.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err = db.storageAdmission(context.Background(), tx, "run", 100, false); err != nil {
			b.Fatal(err)
		}
	}
}
func TestQuotaCountersTrackUpdateDeleteAndRollback(t *testing.T) {
	db, launch := quotaFixture(t)
	var initial int64
	if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&initial); err != nil {
		t.Fatalf("missing transactional counters: %v", err)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(quotaInsert, "q1", launch.HostID, 1, launch.AttemptID, launch.SegmentID, "你好"); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err = tx.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&n); err != nil || n != 6 {
		t.Fatalf("byte quota=%d %v", n, err)
	}
	tx.Rollback()
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&n); err != nil || n != initial {
		t.Fatalf("rollback leaked quota: %d %v", n, err)
	}
	if _, err = db.sql.Exec(quotaInsert, "q1", launch.HostID, 1, launch.AttemptID, launch.SegmentID, "data"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`UPDATE runtime_events SET body_json='more-data' WHERE event_id='q1'`); err != nil {
		t.Fatal(err)
	}
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&n); err != nil || n != initial+9 {
		t.Fatalf("update quota=%d %v", n, err)
	}
	if _, err = db.sql.Exec(`DELETE FROM runtime_events WHERE event_id='q1'`); err != nil {
		t.Fatal(err)
	}
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&n); err != nil || n != initial {
		t.Fatalf("delete quota=%d %v", n, err)
	}
}

func TestQuotaMigrationBackfillsReportAndRuntimeUsage(t *testing.T) {
	db, launch := quotaFixture(t)
	if _, err := db.sql.Exec(quotaInsert, "history", launch.HostID, 1, launch.AttemptID, launch.SegmentID, "runtime"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO report_events(event_id,capability_id,sequence,kind,payload_hash,body_json,event_revision,action_slot,delivery_status,accounted_bytes,created_at) VALUES('progress',?,1,'progress','hash','report',0,'','internal',6,'now')`, launch.ReportCapabilityID); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		for _, scope := range []struct {
			kind, id        string
			total, progress int
		}{{"global", "", 13, 6}, {"run", "run", 13, 6}, {"capability", launch.ReportCapabilityID, 6, 6}} {
			var total, progress int
			if err := db.sql.QueryRow(`SELECT total_bytes,progress_bytes FROM storage_usage WHERE scope=? AND scope_id=?`, scope.kind, scope.id).Scan(&total, &progress); err != nil || total != scope.total || progress != scope.progress {
				t.Fatalf("%s counters %d/%d %v", scope.kind, total, progress, err)
			}
		}
	}
	check()
	for _, table := range []string{"runtime_events", "report_events"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			if _, err := db.sql.Exec("DROP TRIGGER quota_" + table + "_" + op); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Recreate the actual v4 schema, including removal of the later v6 ledger.
	if _, err := db.sql.Exec(`DROP TABLE provider_settings; DROP TABLE host_progress; DROP TRIGGER quota_rework_INSERT; DROP TRIGGER quota_rework_UPDATE; DROP TRIGGER quota_rework_DELETE; ALTER TABLE retry_decisions DROP COLUMN revision_payload; DROP TABLE storage_usage; UPDATE schema_meta SET version=4`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateRuntime(); err != nil {
		t.Fatal(err)
	}
	check()
	if _, err := db.sql.Exec(`UPDATE report_events SET kind='result',accounted_bytes=7 WHERE event_id='progress'`); err != nil {
		t.Fatal(err)
	}
	var total, progress int
	if err := db.sql.QueryRow(`SELECT total_bytes,progress_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&total, &progress); err != nil || total != 14 || progress != 0 {
		t.Fatalf("report update %d/%d %v", total, progress, err)
	}
}
