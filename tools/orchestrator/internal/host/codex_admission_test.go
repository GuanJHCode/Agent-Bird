package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestCodexWorkerRejectsBeforeStartingNativeWithoutRuntimeState(t *testing.T) {
	_, err := prepareCodexCommand(context.Background(), process.Command{}, &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}, t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "codex_runtime_state_required") {
		t.Fatalf("unverified native worker admission must fail before probing: %v", err)
	}
}

func TestCodexAccountTypeDoesNotProveBootstrapAuth(t *testing.T) {
	_, err := codexAuthProjection(map[string]any{"cli_auth_credentials_store": "file"}, map[string]any{"account": map[string]any{"type": "chatgpt"}})
	if err == nil {
		t.Fatal("account type alone accepted as bootstrap proof")
	}
}

// A valid runtime container must not turn a known incompatible native execution
// path into a paid model call, even when submission preflight was bypassed.
func TestCodexWorkerRejectsNestedSandboxBeforePreparingHome(t *testing.T) {
	for _, review := range []bool{false, true} {
		scratch := filepath.Join(t.TempDir(), "scratch")
		state := &codexRuntimeState{}
		_, err := prepareCodexCommand(context.Background(), process.Command{}, &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}, scratch, review, state)
		if err == nil || err.Error() != "codex_nested_sandbox_unsupported" {
			t.Fatalf("unsafe admission: %v", err)
		}
		if state.home != nil {
			t.Fatal("created credential home before refusing incompatible execution")
		}
		if _, err := os.Stat(scratch); !os.IsNotExist(err) {
			t.Fatal("allocated runtime before refusal")
		}
	}
}
