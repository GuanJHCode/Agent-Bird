package host

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/execbridge"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// The test binary supplies the same child entry as the packaged executable.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "codex-exec-bridge" {
		if len(os.Args) != 5 {
			os.Exit(2)
		}
		pid, err := strconv.Atoi(os.Args[3])
		if err != nil {
			os.Exit(2)
		}
		if err := execbridge.Forward(context.Background(), os.Args[2], pid, os.Args[4], os.Stdin, os.Stdout); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestNativeCodexRemoteThreadWithoutModel(t *testing.T) {
	if os.Getenv("AGENT_BIRD_NATIVE_CODEX_REMOTE") != "1" {
		t.Skip("opt-in native remote thread, no model")
	}
	binary := os.Getenv("AGENT_BIRD_NATIVE_CODEX_BINARY")
	if binary == "" {
		t.Fatal("fixed binary required")
	}
	root, err := os.MkdirTemp("/private/tmp", "bird-codex-remote-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native evidence: %s", root)
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	state := &codexRuntimeState{}
	defer state.close()
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 40000}
	cmd, err := prepareCodexWorker(ctx, process.Command{Path: binary, PinnedPath: binary, PinnedSHA256: adapter.CodexSHA256, Dir: work, Args: []string{"exec", "--json", "--ephemeral", "--sandbox", "read-only", "-"}, Stdin: []byte("must not start a model")}, profile, filepath.Join(root, "scratch"), false, state)
	if err != nil {
		t.Fatal(err)
	}
	_ = state.turn.Close()
	_ = state.input.Close()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	state.input = read
	cmd.StdinFile = read
	reached := false
	stopBeforeModel := errors.New("metadata_only_stop_before_model")
	state.turn = codexrpc.NewTurn(codexrpc.TurnConfig{Home: state.home.path, Directory: work, Approval: state.config["approval_policy"], Reviewer: state.config["approvals_reviewer"], VerifyConfig: func(r map[string]any) error { return verifyCodexConfigLayers(r, state.home.runtime) }, EnableTools: func() error {
		if err := state.bridge.EnableTools(); err != nil {
			return err
		}
		reached = true
		return stopBeforeModel
	}}, write)
	cmd.OutputFilter = state.turn.Output
	cmd.Stdout = io.Discard
	child, err := process.Start(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(ctx); err != nil {
		child.Stop(context.Background())
		t.Fatal(err)
	}
	if err := child.ConfirmTreeExited(); err != nil {
		t.Fatal(err)
	}
	t.Logf("turn=%v bridge=%v state=%v", state.turn.Err(), state.bridge.Err(), state.bridge.Diagnostics())
	if err := state.bridge.Finish(); err != nil {
		t.Fatal(err)
	}
	if !reached || !errors.Is(state.turn.Err(), stopBeforeModel) || state.bridge.Err() != nil {
		t.Fatalf("remote startup failed: turn=%v bridge=%v output=%s", state.turn.Err(), state.bridge.Err(), safeCodexMetadataFailure(child.Output()))
	}
	if err := state.home.verifyInputs(); err != nil {
		t.Fatal(err)
	}
	if err := state.close(); err != nil {
		t.Fatal(err)
	}
	t.Log("remote environment connected; stopped before turn/start; both process groups exited")
}

// Only fixed error categories are retained; config/account contents are not logs.
func safeCodexMetadataFailure(raw string) string {
	return "native_output_bytes=" + strconv.Itoa(len(raw))
}
