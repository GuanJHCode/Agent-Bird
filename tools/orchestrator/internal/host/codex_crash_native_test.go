package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/execbridge"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func nativeCrashFixture(args []string) int {
	if len(args) != 2 {
		return 2
	}
	segment, binary := args[0], args[1]
	work := filepath.Join(segment, "work")
	helper, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		return 2
	}
	spec := process.Command{Path: "/usr/bin/sandbox-exec", PinnedPath: binary, PinnedSHA256: adapter.CodexSHA256, Dir: work, ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + segment, "CODEX_HOME=" + segment}, Args: []string{"-p", `(version 1)(deny default)(allow process-exec process-info* sysctl-read file-read* signal)(allow mach-lookup)(allow file-write* (subpath ` + strconv.Quote(segment) + `))(deny network*)(deny process-fork)`, binary, "exec-server", "--listen", "stdio"}}
	broker, err := execbridge.Start(context.Background(), spec, helper, filepath.Join(segment, "codex-executor"), nil, false)
	if err != nil {
		return 3
	}
	defer broker.Close()
	fmt.Println("ready")
	select {}
}

func TestNativeExecutorHostCrashKeepsUnreceiptedLaunchUnknown(t *testing.T) {
	if os.Getenv("AGENT_BIRD_NATIVE_CODEX_REMOTE") != "1" {
		t.Skip("opt-in native executor crash; no model")
	}
	binary := os.Getenv("AGENT_BIRD_NATIVE_CODEX_BINARY")
	if binary == "" {
		t.Fatal("fixed binary required")
	}
	root, err := os.MkdirTemp("/private/tmp", "bird-codex-crash-")
	if err != nil {
		t.Fatal(err)
	}
	segment := filepath.Join(root, "attempt", "segment")
	if err := os.MkdirAll(filepath.Join(segment, "work"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "codex-native-crash-fixture", segment, binary)
	child.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "ready\n" {
		_ = child.Wait()
		t.Fatalf("crash fixture did not start: %v", err)
	}
	var spawned struct {
		Process process.Identity `json:"process"`
	}
	raw, err := os.ReadFile(filepath.Join(segment, "codex-executor", "spawned.json"))
	if err != nil || json.Unmarshal(raw, &spawned) != nil {
		t.Fatal("missing native executor identity")
	}
	if err := process.VerifyRunningExecutable(ctx, spawned.Process, binary, adapter.CodexSHA256); err != nil {
		t.Fatal("wrong executor image:", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("fixture Host was not killed")
	}
	deadline := time.Now().Add(3 * time.Second)
	for process.ConfirmGroupExited(spawned.Process.PGID) != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := process.ConfirmGroupExited(spawned.Process.PGID); err != nil {
		t.Fatal("native executor survived Host loss:", err)
	}
	if err := execbridge.VerifyExecutorExit(filepath.Join(segment, "codex-executor")); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatal("missing Host receipt was accepted:", err)
	}
	h, err := NewIPC(root, "crash-recovery")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{CommandID: "command", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1}
	if err := h.recoverDuplicateGrant(grant, 2); err != nil {
		t.Fatal(err)
	}
	spool, err := h.spool(store.Attempt{ID: "attempt", SegmentID: "segment", TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	records, err := spool.Read()
	if err != nil || len(records) != 1 || records[0].Kind != contract.EventUnknown {
		t.Fatalf("crashed launch was released or rerun: %+v %v", records, err)
	}
	t.Log("native executor exited after fixture Host SIGKILL; absent receipt kept recovery UNKNOWN; no model/account used")
}
