package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

// N04 覆盖(2026-09-30):host_progress 通道信封守卫(store/host_progress.go:37)
// 与 UNKNOWN 段进度拒收(:52 状态白名单不含 unknown)此前无专项用例。
// 合法信封、重放幂等与 post-exit 拒收已由 interruption_test.go:14 覆盖,
// 本文件只补缺失分支,不重复既有身份。

func TestHostProgressRejectsUnknownSegmentAndEnvelopeBranches(t *testing.T) {
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
	if _, err = db.CommitHostEvent(ctx, contract.Event{Version: 1, ProducerID: host, EventID: host + ":prepared", RunID: "r", TaskID: "t", AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: 1, Kind: contract.EventPrepared, PayloadHash: strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("b", 64)
	base := contract.Event{Version: 1, ProducerID: host, EventID: host + ":progress", RunID: "r", TaskID: "t", AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: 1, Kind: contract.EventProgress, PayloadHash: digest, Artifact: &contract.ArtifactRef{ID: "provider-progress", Path: "/private/progress.json", Size: 10, SHA256: digest}}
	if err := db.CommitHostProgress(ctx, base, receipt.LaunchID, receipt.LaunchToken, 10, "host"); err != nil {
		t.Fatalf("valid progress rejected: %v", err)
	}
	if err := db.CommitHostProgress(ctx, base, receipt.LaunchID, receipt.LaunchToken, 10, "host"); err != nil {
		t.Fatalf("identical replay not idempotent: %v", err)
	}
	conflict := base
	conflict.EventID = host + ":other"
	conflict.Sequence = 2
	conflict.Artifact = &contract.ArtifactRef{ID: "provider-progress", Path: "/private/other.json", Size: 11, SHA256: strings.Repeat("d", 64)}
	conflict.PayloadHash = strings.Repeat("d", 64)
	if err := db.CommitHostProgress(ctx, conflict, receipt.LaunchID, receipt.LaunchToken, 10, "host"); err != nil {
		t.Fatalf("distinct valid progress rejected: %v", err)
	}
	// 信封分支:每一分支都必须以 invalid_progress 拒绝,不得进入生命周期。
	type badCase struct {
		name   string
		mutate func(*contract.Event)
	}
	for _, bad := range []badCase{
		{"sequence-zero", func(e *contract.Event) { e.Sequence = 0 }},
		{"sequence-over-21", func(e *contract.Event) { e.Sequence = 22 }},
		{"stop-reason-leak", func(e *contract.Event) { e.StopReason = "deadline_exceeded" }},
		{"missing-artifact", func(e *contract.Event) { e.Artifact = nil }},
		{"wrong-artifact-id", func(e *contract.Event) {
			e.Artifact = &contract.ArtifactRef{ID: "other", Path: "/private/x.json", Size: 10, SHA256: digest}
		}},
		{"zero-size", func(e *contract.Event) {
			e.Artifact = &contract.ArtifactRef{ID: "provider-progress", Path: "/private/x.json", Size: 0, SHA256: digest}
		}},
		{"oversize", func(e *contract.Event) {
			e.Artifact = &contract.ArtifactRef{ID: "provider-progress", Path: "/private/x.json", Size: 64*1024 + 1, SHA256: digest}
		}},
		{"payload-mismatch", func(e *contract.Event) { e.PayloadHash = strings.Repeat("e", 64) }},
		{"unregistered-kind", func(e *contract.Event) { e.Kind = "progress-spoof" }},
	} {
		event := base
		event.EventID = host + ":bad-" + bad.name
		event.Sequence = 3
		event.Artifact = &contract.ArtifactRef{ID: "provider-progress", Path: "/private/bad.json", Size: 10, SHA256: digest}
		event.PayloadHash = digest
		bad.mutate(&event)
		err := db.CommitHostProgress(ctx, event, receipt.LaunchID, receipt.LaunchToken, 10, "host")
		if err == nil || err.Error() != "invalid_progress" {
			t.Fatalf("%s accepted with %v", bad.name, err)
		}
	}
	// UNKNOWN 段:进度通道必须整段拒收(store/host_progress.go:52 白名单)。
	if _, err = db.CommitHostEvent(ctx, contract.Event{Version: 1, ProducerID: host, EventID: host + ":unknown", RunID: "r", TaskID: "t", AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: 2, Kind: contract.EventUnknown, PayloadHash: strings.Repeat("f", 64)}); err != nil {
		t.Fatalf("unknown terminal rejected: %v", err)
	}
	late := base
	late.EventID = host + ":late-unknown"
	late.Sequence = 4
	late.PayloadHash = strings.Repeat("9", 64)
	late.Artifact = &contract.ArtifactRef{ID: "provider-progress", Path: "/private/late.json", Size: 10, SHA256: strings.Repeat("9", 64)}
	err = db.CommitHostProgress(ctx, late, receipt.LaunchID, receipt.LaunchToken, 10, "host")
	if !errors.Is(err, ErrHostRejected) {
		t.Fatalf("progress accepted during UNKNOWN segment: %v", err)
	}
	// UNKNOWN 期间的迟到快照不得入库,也不得进入汇总投影。
	var rows int
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM host_progress`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("UNKNOWN-period progress row leaked: rows=%d err=%v", rows, err)
	}
	summary, err := db.SummarizeRun(ctx, "t", "thread", receipt.ControlToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range summary.Tasks {
		if task.LatestProgress != nil && task.LatestProgress.SHA256 == late.PayloadHash {
			t.Fatal("UNKNOWN-period progress leaked into summary")
		}
	}
}
