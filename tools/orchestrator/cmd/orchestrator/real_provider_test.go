package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
)

// Opt-in: runs the installed, authenticated CLI and may consume model quota.
// This never rewrites a provider pin or provider authentication/configuration.
func TestRealClaudeReviewerProfile(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_REAL_CLAUDE") != "1" {
		t.Skip("requires explicit real provider trial")
	}
	binary, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal("installed claude unavailable")
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	lock, err := adapter.Probe(ctx, adapter.ProviderClaude, binary)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 30000}
	payload, _ := json.Marshal(invocationPayload{Provider: string(adapter.ProviderClaude), ProviderLock: &lock, Profile: &profile, Directory: root, Prompt: "Reply with exactly ORCHESTRATOR_PROVIDER_OK. Do not use tools."})
	grant := contract.LaunchCommand{RunID: "trial", TaskID: "trial", AttemptID: "attempt", SegmentID: "segment", CommandID: "command", ReservationID: "reservation", WorkRevision: 1, GrantedActiveMS: 30000, AdapterPayload: payload}
	inv, err := invocationForGrant(ctx, grant)
	if err != nil {
		t.Fatal(err)
	}
	h, err := host.NewIPC(filepath.Join(root, "state", "host-spool", "launch"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.ExecuteLaunch(ctx, grant, inv)
	if err != nil {
		t.Fatalf("real provider launch: %v", err)
	}
	if result.Status != "result_ready" {
		t.Fatalf("real provider %s: status=%s exit=%d", lock.Binary.Version, result.Status, result.ExitCode)
	}
	content, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "ORCHESTRATOR_PROVIDER_OK") {
		t.Fatalf("real provider final marker missing (%d bytes)", len(content))
	}
	t.Logf("real %s: final marker verified under reviewer sandbox", lock.Binary.Version)
}
