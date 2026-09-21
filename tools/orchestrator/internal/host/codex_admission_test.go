package host

import (
	"context"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestCodexWorkerRejectsBeforeStartingNativeWithoutVerifiedAdmission(t *testing.T) {
	_, err := prepareCodexCommand(context.Background(), process.Command{}, &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}, t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "codex_trial_guard_not_ready") {
		t.Fatalf("unverified native worker admission must fail before probing: %v", err)
	}
}

func TestCodexAccountTypeDoesNotProveBootstrapAuth(t *testing.T) {
	_, err := codexAuthProjection(map[string]any{"cli_auth_credentials_store": "file"}, map[string]any{"account": map[string]any{"type": "chatgpt"}})
	if err == nil {
		t.Fatal("account type alone accepted as bootstrap proof")
	}
}
