package host

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

// N04 覆盖(2026-09-30):真实后代持 stdout 期间的进度语义必须符合既定协议
// (runtime-protocols §6/§7 与 store/host_progress.go:37-52 信封/状态守卫):
// 活跃期(2s 窗口 <15s ticker)不得出现虚构进度;停止确认期恰一条
// index=20(Sequence 21)快照且信封 StopReason 恒空;终态之后计数不再推进;
// 不完整快照标记 incomplete 且不泄漏私有内容。
// 既有持管道/逃逸孙契约测试(stop_timing_test.go:109/:166)走 protocol==nil
// 路径,进度观察者从未执行,故该组合此前为零覆盖(批次走查确认)。

func n04KillByPidFile(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if parseErr != nil || pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func n04ReadProgressSnapshot(t *testing.T, event contract.Event) map[string]any {
	t.Helper()
	if event.Kind != contract.EventProgress || event.Sequence != 21 || event.StopReason != "" ||
		event.Artifact == nil || event.Artifact.ID != "provider-progress" ||
		event.Artifact.Size < 1 || event.Artifact.Size > 64*1024 {
		t.Fatalf("progress envelope violates protocol: %+v", event)
	}
	body, err := os.ReadFile(event.Artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) != event.Artifact.Size || event.Artifact.SHA256 != hashBytes(body) {
		t.Fatalf("progress artifact identity mismatch: size=%d envelope=%+v", len(body), event.Artifact)
	}
	if strings.Contains(string(body), "PRIVATE") {
		t.Fatalf("private content leaked into progress: %s", body)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatalf("invalid progress snapshot: %s", body)
	}
	return snapshot
}

func TestPipeHoldingDescendantRecordsSingleStoppingProgress(t *testing.T) {
	if os.Getenv("G1_PIPE_PROGRESS_ROLE") == "holder" {
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv("G1_PIPE_PROGRESS_ROLE") == "leader" {
		holder := exec.Command(os.Args[0], "-test.run=^TestPipeHoldingDescendantRecordsSingleStoppingProgress$")
		holder.Env = append(os.Environ(), "G1_PIPE_PROGRESS_ROLE=holder")
		holder.Stdout, holder.Stderr = os.Stdout, os.Stderr
		if err := holder.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("G1_PIPE_PROGRESS_PID_FILE"), []byte(strconv.Itoa(holder.Process.Pid)), 0600)
		os.Stdout.WriteString(`{"type":"system","subtype":"init","session_id":"session-1"}` + "\n")
		os.Stdout.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"partial public line"}]}}`)
		os.Stdout.Sync()
		time.Sleep(20 * time.Second)
		os.Exit(0)
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pidFile := filepath.Join(root, "holder.pid")
	t.Cleanup(func() { n04KillByPidFile(t, pidFile) })
	h, err := NewIPC(filepath.Join(root, "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	var progressEvents []contract.Event
	h.progressSink = func(_ context.Context, e contract.Event) error {
		progressEvents = append(progressEvents, e)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a := store.Attempt{ID: "a", TaskID: "task", SegmentID: "s"}
	result, err := h.execute(ctx, a, "run", "task", process.Command{Path: binary, Args: []string{"-test.run=^TestPipeHoldingDescendantRecordsSingleStoppingProgress$"}, Env: []string{"G1_PIPE_PROGRESS_ROLE=leader", "G1_PIPE_PROGRESS_PID_FILE=" + pidFile}}, false, launchMetadata{commandID: "c", workRevision: 1, outputProvider: "claude-code"})
	if err == nil || result.Status != "interrupted" || result.ArtifactPath != "" {
		t.Fatalf("want interrupted conservative settlement: %+v %v", result, err)
	}
	if len(progressEvents) != 1 {
		t.Fatalf("want exactly one stopping progress, got %d", len(progressEvents))
	}
	if snapshot := n04ReadProgressSnapshot(t, progressEvents[0]); snapshot["status"] != "incomplete" {
		t.Fatalf("stopping snapshot must be incomplete: %+v", snapshot)
	}
	got, err := h.Collect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	terminals, results := 0, 0
	for _, e := range got {
		switch e.Kind {
		case contract.EventExited, contract.EventUnknown:
			terminals++
		case contract.EventResult:
			results++
		}
	}
	if terminals != 1 || results != 0 {
		t.Fatalf("terminal discipline broken: terminals=%d results=%d", terminals, results)
	}
	if len(progressEvents) != 1 {
		t.Fatalf("progress advanced after terminal: %d", len(progressEvents))
	}
}

func TestEscapedPipeHolderProgressDoesNotAdvanceAfterTerminal(t *testing.T) {
	if os.Getenv("G1_PIPE_ESCAPE_PROGRESS_ROLE") == "holder" {
		time.Sleep(30 * time.Second)
		return
	}
	if os.Getenv("G1_PIPE_ESCAPE_PROGRESS_ROLE") == "leader" {
		holder := exec.Command(os.Args[0], "-test.run=^TestEscapedPipeHolderProgressDoesNotAdvanceAfterTerminal$")
		holder.Env = append(os.Environ(), "G1_PIPE_ESCAPE_PROGRESS_ROLE=holder")
		holder.Stdout, holder.Stderr = os.Stdout, os.Stderr
		holder.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := holder.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("G1_PIPE_ESCAPE_PROGRESS_PID_FILE"), []byte(strconv.Itoa(holder.Process.Pid)), 0600)
		os.Stdout.WriteString(`{"type":"system","subtype":"init","session_id":"session-1"}` + "\n")
		os.Stdout.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"partial public line"}]}}`)
		os.Stdout.Sync()
		time.Sleep(20 * time.Second)
		os.Exit(0)
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pidFile := filepath.Join(root, "holder.pid")
	t.Cleanup(func() { n04KillByPidFile(t, pidFile) })
	h, err := NewIPC(filepath.Join(root, "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	var progressEvents []contract.Event
	h.progressSink = func(_ context.Context, e contract.Event) error {
		progressEvents = append(progressEvents, e)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a := store.Attempt{ID: "a", TaskID: "task", SegmentID: "s"}
	result, err := h.execute(ctx, a, "run", "task", process.Command{Path: binary, Args: []string{"-test.run=^TestEscapedPipeHolderProgressDoesNotAdvanceAfterTerminal$"}, Env: []string{"G1_PIPE_ESCAPE_PROGRESS_ROLE=leader", "G1_PIPE_ESCAPE_PROGRESS_PID_FILE=" + pidFile}}, false, launchMetadata{commandID: "c", workRevision: 1, outputProvider: "claude-code"})
	if result.Status == "result_ready" || result.ArtifactPath != "" {
		t.Fatalf("pipe-held escape scenario must never report success: %+v %v", result, err)
	}
	if result.Status != "interrupted" && result.Status != "unknown" {
		t.Fatalf("unexpected settlement: %+v %v", result, err)
	}
	if result.Status == "unknown" && len(progressEvents) != 0 {
		t.Fatalf("UNKNOWN period must not emit progress: %d", len(progressEvents))
	}
	if result.Status == "interrupted" {
		if len(progressEvents) != 1 {
			t.Fatalf("want exactly one stopping progress, got %d", len(progressEvents))
		}
		n04ReadProgressSnapshot(t, progressEvents[0])
	}
	if raw, readErr := os.ReadFile(pidFile); readErr == nil {
		if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); parseErr == nil {
			if killErr := syscall.Kill(pid, 0); killErr != nil {
				t.Fatalf("escapee should still hold the pipe outside the owned group: %v", killErr)
			}
		}
	} else {
		t.Fatalf("holder pid missing: %v", readErr)
	}
	got, err := h.Collect(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	results := 0
	for _, e := range got {
		if e.Kind == contract.EventResult {
			results++
		}
	}
	if results != 0 {
		t.Fatal("late progress pipe release must not publish result")
	}
	before := len(progressEvents)
	n04KillByPidFile(t, pidFile)
	time.Sleep(200 * time.Millisecond)
	if len(progressEvents) != before {
		t.Fatalf("progress advanced after terminal and late pipe drain: %d→%d", before, len(progressEvents))
	}
}
