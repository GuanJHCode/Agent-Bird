package host

import (
	"context"
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

// N04 组合判据返修(R3, 2026-09-30):
// 现有 primary/second 用例都不能把「后代是否继承 stdout 管道」识别为必要条件:
// 独立复核在 scratch 实跑发现 primary 把 `( sleep 30 )` 改为
// `( sleep 30 >/dev/null 2>&1 )`(后代不再持管道)后
// TestProgressStaysBoundedWhileDescendantHoldsPipeAndNeverFinalizes 仍 PASS;
// second 删除整个 holder 启动块后 TestPipeHoldingDescendantRecordsSingleStoppingProgress
// 仍 PASS(leader 自身 sleep 已足以阻塞)。二者都走同一保守终态分支, 因此
// 断言与「持管道」这一产品不变量脱钩。本用例把该不变量重建为唯一区别。
//
// 骨架取 primary 的「leader 先退出」形状, 关键改动:
//  1. holder 逃逸到独立进程组(Setpgid), 因此 ConfirmTreeExited 只看 leader 组;
//     唯一区别是 holder 是否继承 stdout 写端(而不是进程树是否残留)。
//  2. leader 写出完整 claude-code 成功协议(init+result/success)后 os.Exit(0)。
//     - 持管道: cmd.Wait 因 EOF 缺失阻塞到 ctx 截止, 走保守停止分支, 绝不 result_ready。
//     - 不继承 stdout(负控制): leader 退出即 EOF, Wait 早于截止返回,
//       leader 组已清空 → ConfirmTreeExited 通过 → protocol.Finish → result_ready。
//
// 关闭标准: 删除 pipe 继承(把 holder.Stdout/Stderr 从 os.Stdout 换成丢弃文件)
// 必须使本用例真实 RED; 保持继承时必须 GREEN。
const pipeSensitiveEnvLeader = "G1_N04_PIPE_SENSITIVE_ROLE"
const pipeSensitiveEnvHolderPID = "G1_N04_PIPE_SENSITIVE_PID_FILE"
const pipeSensitiveEnvHolderNoPipe = "G1_N04_PIPE_SENSITIVE_NOPIPE"

func n04SensitiveKillByPidFile(t *testing.T, pidFile string) {
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

func TestN04PipeSensitiveDescendantHoldingStdoutIsNecessary(t *testing.T) {
	switch os.Getenv(pipeSensitiveEnvLeader) {
	case "holder":
		// Holder: a same test binary that only lives long enough to keep the
		// inherited stdout write-end open, then exits. It never writes.
		time.Sleep(30 * time.Second)
		return
	case "leader":
		holder := exec.Command(os.Args[0], "-test.run=^TestN04PipeSensitiveDescendantHoldingStdoutIsNecessary$")
		holder.Env = append(os.Environ(), pipeSensitiveEnvLeader+"=holder")
		holder.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if os.Getenv(pipeSensitiveEnvHolderNoPipe) == "1" {
			// Negative control: the descendant stays alive but does NOT inherit
			// the stdout pipe, so releasing it lets the leader reap cleanly.
			discard, derr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			if derr != nil {
				os.Exit(2)
			}
			holder.Stdout, holder.Stderr = discard, discard
		} else {
			// Product condition under test: descendant inherits the stdout pipe.
			holder.Stdout, holder.Stderr = os.Stdout, os.Stderr
		}
		if err := holder.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv(pipeSensitiveEnvHolderPID), []byte(strconv.Itoa(holder.Process.Pid)), 0600)
		// Emit a complete claude-code success protocol and exit immediately, so
		// the only thing that can block the host's Wait is the held pipe.
		os.Stdout.WriteString(`{"type":"system","subtype":"init","session_id":"claude-pipe"}` + "\n")
		os.Stdout.WriteString(`{"type":"result","subtype":"success","session_id":"claude-pipe","result":"pipe-sensitive structured answer"}` + "\n")
		os.Stdout.Sync()
		os.Exit(0)
	}

	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pidFile := filepath.Join(root, "holder.pid")
	t.Cleanup(func() { n04SensitiveKillByPidFile(t, pidFile) })
	h, err := NewIPC(filepath.Join(root, "spool"), "n04-pipe-sensitive")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	a := store.Attempt{ID: "a", TaskID: "task", SegmentID: "s"}
	result, runErr := h.execute(ctx, a, "run", "task", process.Command{
		Path: binary,
		Args: []string{"-test.run=^TestN04PipeSensitiveDescendantHoldingStdoutIsNecessary$"},
		Env: []string{
			pipeSensitiveEnvLeader + "=leader",
			pipeSensitiveEnvHolderPID + "=" + pidFile,
			pipeSensitiveEnvHolderNoPipe + "=" + os.Getenv(pipeSensitiveEnvHolderNoPipe),
		},
	}, false, launchMetadata{commandID: "c", workRevision: 1, outputProvider: "claude-code"})
	_ = runErr

	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("holder pid missing: %v", readErr)
	}
	holderPID, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if parseErr != nil || holderPID <= 0 {
		t.Fatalf("holder pid=%q", raw)
	}

	// The descendant is alive and (in the product condition) still holds stdout.
	// Publishing a business result while its output is not yet drained is the
	// unsafe outcome; this must never happen.
	if result.Status == "result_ready" || result.ArtifactPath != "" {
		t.Fatalf("descendant still holds stdout yet host finalized a result: status=%s artifact=%q", result.Status, result.ArtifactPath)
	}

	sink := &durableSink{}
	if err := h.PublishAll(context.Background(), sink, 1); err != nil {
		t.Fatal(err)
	}
	for _, e := range sink.events {
		if e.Kind == contract.EventResult || e.Artifact != nil {
			t.Fatalf("held-pipe run published a business result: kind=%s", e.Kind)
		}
	}
}
