package codexrpc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestMetadataPinMismatchPreventsSpawn(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "started")
	binary := filepath.Join(root, "provider")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntouch \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, _, err = StartPinnedCommand(context.Background(), process.Command{Path: binary, Args: []string{marker}, PinnedPath: binary, PinnedSHA256: strings.Repeat("0", 64)})
	if err == nil {
		t.Fatal("changed provider pin accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("unverified metadata provider was started")
	}
}

func TestUnidentifiedMetadataCleanupStopsWholeGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command("/bin/sh", "-c", `sleep 30 & printf ready > "$1"; wait`, "fixture", marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := stopUnidentified(cmd); err != nil {
		t.Fatal(err)
	}
	if err := process.ConfirmGroupExited(cmd.Process.Pid); err != nil {
		t.Fatal("metadata process group survived cleanup")
	}
}
