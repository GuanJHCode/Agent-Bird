package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestProfileInterruptionNeverOffersOrQueuesResume(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	receipt, err := db.SubmitPlan(ctx, PlanSpec{Run: RunSpec{ID: "r", ControllerThread: "thread", PlanRevision: 1, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: "origin", HostGeneration: "g", Executable: "/private/bin/orchestrator"}, Tasks: []TaskSpec{{ID: "t", RunID: "r", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000}}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "origin", OriginPID: 1, OriginBirth: "birth", HostGeneration: "g", PID: 10, Birth: "host", Executable: "/private/bin/orchestrator"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	g, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	exit := -9
	progress := contract.Event{Version: 1, ProducerID: host, EventID: host + ":progress", RunID: "r", TaskID: "t", AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: 1, Kind: "progress", PayloadHash: strings.Repeat("b", 64), Artifact: &contract.ArtifactRef{ID: "provider-progress", Path: "/private/progress.json", Size: 10, SHA256: strings.Repeat("b", 64)}}
	_, err = db.CommitHostEvent(ctx, contract.Event{Version: 1, ProducerID: host, EventID: host + ":prepared", RunID: "r", TaskID: "t", AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: 1, Kind: contract.EventPrepared, PayloadHash: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CommitHostProgress(ctx, progress, receipt.LaunchID, receipt.LaunchToken, 10, "host"); err != nil {
		t.Fatal(err)
	}
	page, err := db.CollectPending(ctx, "t", "", 0, true)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("progress not collectable: %+v %v", page, err)
	}
	plain, err := db.CollectPending(ctx, "t", "", 0, false)
	if err != nil || len(plain.Events) != 0 {
		t.Fatal("progress requires business ACK")
	}
	live, err := db.SummarizeRun(ctx, "t", "thread", receipt.ControlToken)
	if err != nil || live.Tasks[0].LatestProgress == nil {
		t.Fatalf("missing live progress: %+v %v", live, err)
	}
	var used int64
	if err = db.sql.QueryRow(`SELECT progress_bytes FROM storage_usage WHERE scope='run' AND scope_id='r'`).Scan(&used); err != nil || used <= 10 {
		t.Fatalf("progress not charged: %d %v", used, err)
	}
	second := progress
	second.EventID = host + ":progress-two"
	second.Sequence = 2
	var total int64
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='r'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	originalLimit := db.runControlLimit
	db.runControlLimit = total + 2*1024*1024
	if err = db.CommitHostProgress(ctx, second, receipt.LaunchID, receipt.LaunchToken, 10, "host"); err == nil {
		t.Fatal("progress consumed terminal reserve")
	}
	db.runControlLimit = originalLimit
	db.runProgressLimit = used
	if err = db.CommitHostProgress(ctx, second, receipt.LaunchID, receipt.LaunchToken, 10, "host"); err == nil || err.Error() != "report_budget_exhausted" {
		t.Fatalf("quota: %v", err)
	}
	db.runProgressLimit = 12 * 1024 * 1024
	db.storageAvailable = func(string) (uint64, error) { return db.minFreeBytes + db.sqliteWriteOverhead + 1024*1024, nil }
	if err = db.CommitHostProgress(ctx, second, receipt.LaunchID, receipt.LaunchToken, 10, "host"); err == nil || err.Error() != "storage_blocked" {
		t.Fatalf("critical reserve: %v", err)
	}
	if err = db.CommitHostProgress(ctx, second, receipt.LaunchID, receipt.LaunchToken, 11, "host"); err == nil {
		t.Fatal("foreign peer registered progress")
	}
	// Failed progress has no lifecycle sequence; stopped/exited must still commit.
	for i, kind := range []string{contract.EventStopped, contract.EventExited} {
		reason := ""
		if kind == contract.EventStopped {
			reason = "profile_timeout"
		}
		_, err = db.CommitHostEvent(ctx, contract.Event{Version: 1, ProducerID: host, EventID: host + ":" + kind, RunID: "r", TaskID: "t", AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: int64(i + 2), Kind: kind, PayloadHash: strings.Repeat("a", 64), ExitCode: &exit, StopReason: reason})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = db.CommitHostProgress(ctx, second, receipt.LaunchID, receipt.LaunchToken, 10, "host"); err == nil {
		t.Fatal("post-exit progress accepted")
	}
	summary, err := db.SummarizeRun(ctx, "t", "thread", receipt.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Tasks) != 1 || slices.Contains(summary.Tasks[0].AllowedActions, "resume") {
		t.Fatalf("misleading summary %+v", summary)
	}
	if summary.Tasks[0].StopReason != "profile_timeout" || summary.Tasks[0].ResumeBlockedReason != "profile_resume_not_verified" {
		t.Fatalf("missing diagnostic %+v", summary)
	}
	if err = db.QueueResume(ctx, "t", 1); err == nil || err.Error() != "profile_resume_not_verified" {
		t.Fatalf("resume guard: %v", err)
	}
}
