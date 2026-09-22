package host

import (
	"context"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeCodexPolicyMetadataOnly(t *testing.T) {
	if os.Getenv("AGENT_BIRD_NATIVE_CODEX_POLICY") != "1" {
		t.Skip("opt-in native metadata probe; no model")
	}
	binary := os.Getenv("AGENT_BIRD_NATIVE_CODEX_BINARY")
	var err error
	if binary == "" {
		binary, err = exec.LookPath("codex")
	}
	if err != nil {
		t.Fatal(err)
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp("/private/tmp", "bird-codex-policy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("metadata evidence retained: %s", work)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	state := &codexRuntimeState{}
	defer state.close()
	// Metadata-only research helper; it does not establish production admission.
	cmd, err := prepareCodexRuntime(ctx, process.Command{Path: binary, PinnedPath: binary, PinnedSHA256: adapter.CodexSHA256, Dir: work, Args: []string{"exec", "--json", "--ephemeral", "--sandbox", "read-only", "-"}}, &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 40000}, filepath.Join(work, "scratch"), false, state)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/usr/bin/sandbox-exec" {
		t.Fatal("worker sandbox absent")
	}
}
