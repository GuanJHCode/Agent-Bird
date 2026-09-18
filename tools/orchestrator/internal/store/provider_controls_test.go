package store

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func providerTestPlan(run, thread, provider string) PlanSpec {
	raw, _ := json.Marshal(map[string]any{"provider": provider})
	return PlanSpec{Run: RunSpec{ID: run, ControllerThread: thread, PlanRevision: 1, OriginContextID: run, OriginPID: 7, OriginBirth: "birth"}, Host: HostLaunchSpec{OriginContextID: run, HostGeneration: run, Executable: "/private/host"}, Tasks: []TaskSpec{{ID: run + "-task", RunID: run, MaxAttempts: 2, AdapterPayload: raw}}}
}
func TestProviderCloseIsDurableAndSessionScoped(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	a := providerTestPlan("a", "thread-a", "grok-build")
	b := providerTestPlan("b", "thread-b", "grok-build")
	ra, err := db.SubmitPlan(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SubmitPlan(ctx, b); err != nil {
		t.Fatal(err)
	}
	host, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: ra.LaunchID, LaunchToken: ra.LaunchToken, OriginContextID: "a", OriginPID: 7, OriginBirth: "birth", HostGeneration: "a", PID: 101, Birth: "host", Executable: "/private/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := db.ClaimReady(ctx, host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.ProviderControl(ctx, "thread-a", "grok-build", "disable", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Enabled || result.Status != "stopping" || result.Active != 1 {
		t.Fatalf("close=%+v", result)
	}
	stops, err := db.PendingStops(ctx, host)
	if err != nil || len(stops) != 1 || stops[0].Command.SegmentID != launch.SegmentID {
		t.Fatalf("stops=%v %v", stops, err)
	}
	pending, err := db.PendingLaunches(ctx, host, 1)
	if err != nil || len(pending) != 0 {
		t.Fatalf("replayed closed launch: %v %v", pending, err)
	}
	var status string
	if err = db.sql.QueryRow(`SELECT status FROM tasks WHERE id='b-task'`).Scan(&status); err != nil || status != "ready" {
		t.Fatalf("other session affected: %s %v", status, err)
	}
	if _, err = db.SubmitPlan(ctx, providerTestPlan("c", "thread-a", "grok-build")); err == nil || err.Error() != "provider_disabled" {
		t.Fatalf("admission=%v", err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SubmitPlan(ctx, providerTestPlan("d", "thread-a", "grok-build")); err == nil || err.Error() != "provider_disabled" {
		t.Fatalf("restart admission=%v", err)
	}
	if _, err = db.ProviderControl(ctx, "thread-a", "grok-build", "enable", nil); err == nil || err.Error() != "provider_stop_pending" {
		t.Fatalf("reopen before exit=%v", err)
	}
}
func TestProviderCloseCancelsQueuedAndRejectsFallback(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.SubmitPlan(ctx, providerTestPlan("a", "a", "grok-build")); err != nil {
		t.Fatal(err)
	}
	r, err := db.ProviderControl(ctx, "a", "grok-build", "disable", nil)
	if err != nil || r.Status != "disabled" {
		t.Fatalf("%+v %v", r, err)
	}
	p := providerTestPlan("b", "a", "claude-code")
	p.Tasks[0].FallbackPayloads = []json.RawMessage{json.RawMessage(`{"provider":"grok-build"}`)}
	if _, err = db.SubmitPlan(ctx, p); err == nil || err.Error() != "provider_disabled" {
		t.Fatalf("fallback=%v", err)
	}
	if _, err = db.ProviderControl(ctx, "a", "grok-build", "enable", nil); err != nil {
		t.Fatal(err)
	}
	var status string
	db.sql.QueryRow(`SELECT status FROM tasks WHERE id='a-task'`).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("old task restarted: %s", status)
	}
	if _, err = db.SubmitPlan(ctx, providerTestPlan("new", "a", "grok-build")); err != nil {
		t.Fatal(err)
	}
}
func TestProviderModelDefaultsAndCLIDefault(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	model := "global-model"
	if _, err = db.ProviderControl(ctx, "a", "claude-code", "default-model", &model); err != nil {
		t.Fatal(err)
	}
	r, err := db.ProviderControl(ctx, "b", "claude-code", "status", nil)
	if err != nil || r.Model != "global-model" {
		t.Fatalf("%+v %v", r, err)
	}
	model = "session-model"
	if _, err = db.ProviderControl(ctx, "a", "claude-code", "model", &model); err != nil {
		t.Fatal(err)
	}
	r, _ = db.ProviderControl(ctx, "a", "claude-code", "status", nil)
	if r.Model != "session-model" {
		t.Fatal(r)
	}
	model = ""
	if _, err = db.ProviderControl(ctx, "a", "claude-code", "model", &model); err != nil {
		t.Fatal(err)
	}
	r, _ = db.ProviderControl(ctx, "a", "claude-code", "status", nil)
	if r.Model != "" {
		t.Fatal("CLI default lost", r)
	}
	r, _ = db.ProviderControl(ctx, "b", "claude-code", "status", nil)
	if r.Model != "global-model" {
		t.Fatal("other session changed", r)
	}
}

func TestProviderDisabledBlocksExplicitReviewRetry(t *testing.T) {
	f := newReviewRetryFixture(t, contract.EventStopped)
	if _, err := f.db.ProviderControl(context.Background(), "owner", "claude-code", "disable", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueRetry(context.Background(), f.spec); err == nil || err.Error() != "provider_disabled" {
		t.Fatalf("retry while disabled: %v", err)
	}
}
func TestProviderDisabledBlocksResume(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.SubmitPlan(ctx, providerTestPlan("a", "a", "grok-build")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ProviderControl(ctx, "a", "grok-build", "disable", nil); err != nil {
		t.Fatal(err)
	}
	if err = db.QueueResume(ctx, "a-task", 1); err == nil || err.Error() != "provider_disabled" {
		t.Fatalf("resume=%v", err)
	}
}

func TestProviderMigrationPreservesVersionSevenRuns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.SubmitPlan(ctx, providerTestPlan("history", "owner", "grok-build")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`DROP TABLE provider_settings; UPDATE schema_meta SET version=7`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	var status string
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lost history %d %v", count, err)
	}
	if err = db.sql.QueryRow(`SELECT status FROM tasks WHERE id='history-task'`).Scan(&status); err != nil || status != "ready" {
		t.Fatalf("changed history %s %v", status, err)
	}
	if _, err = db.ProviderControl(ctx, "owner", "grok-build", "disable", nil); err != nil {
		t.Fatal(err)
	}
}
func TestProviderDisabledBlocksReworkWithoutChangingRevision(t *testing.T) {
	f := newReworkFixture(t)
	ctx := context.Background()
	if _, err := f.db.ProviderControl(ctx, "owner", "claude-code", "disable", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.QueueRework(ctx, f.spec); err == nil || err.Error() != "provider_disabled" {
		t.Fatalf("rework=%v", err)
	}
	var revision int
	f.db.sql.QueryRow(`SELECT work_revision FROM task_runtime WHERE task_id='edit'`).Scan(&revision)
	if revision != 1 {
		t.Fatalf("revised disabled work %d", revision)
	}
}

func TestProviderCloseCancelsWholeProviderDependencyChain(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := providerTestPlan("chain", "owner", "grok-build")
	p.Tasks = append(p.Tasks, TaskSpec{ID: "child", RunID: "chain", Dependencies: []string{"chain-task"}, MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"grok-build"}`)})
	if _, err = db.SubmitPlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ProviderControl(ctx, "owner", "grok-build", "disable", nil); err != nil {
		t.Fatalf("close chain rolled back: %v", err)
	}
	var count int
	db.sql.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status IN ('ready','queued')`).Scan(&count)
	if count != 0 {
		t.Fatalf("ready after close: %d", count)
	}
}
func TestProviderWriteCannotBypassManagedWorkspaceViaSubmit(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := providerTestPlan("write", "owner", "claude-code")
	p.Tasks[0].AdapterPayload = json.RawMessage(`{"provider":"claude-code","profile":{"version":1,"role":"implementer","permission":"workspace-write","timeout_ms":1000},"directory":"/private/arbitrary-linked-worktree"}`)
	if _, err = db.SubmitPlan(ctx, p); err == nil || err.Error() != "managed_workspace_required" {
		t.Fatalf("unmanaged admission: %v", err)
	}
}
