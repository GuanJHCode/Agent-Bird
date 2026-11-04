package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

func TestProviderLockRequiresExactObservedDigestAndPreviousPin(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "provider")
	source := []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo '2.1.267 (Claude Code)'; else echo --output-format --input-format --permission-mode --disallowed-tools --tools --model --effort --resume; fi\n")
	if err := os.WriteFile(binary, source, 0700); err != nil {
		t.Fatal(err)
	}
	// This test verifies observed identity, exact-digest confirmation and
	// previous-pin refusal — not cold-start latency. The first exec of a
	// freshly written script pays macOS launch-policy cost inside the
	// product's bounded 2s probe (measured 10/10 over-budget under a
	// controlled churn storm), so that cost is paid here in an explicitly
	// named, separately bounded preparation phase. A preparation failure
	// aborts without retry. Real deadline and cancellation behavior stays
	// covered independently by adapter probe_failure_test.go, and the
	// cold-start shape by the opt-in cause observation test.
	if err := prepareFixtureFirstExec(binary); err != nil {
		t.Fatalf("fixture_first_exec_not_ready: %v", err)
	}
	reqPath := filepath.Join(root, "request.json")
	lockPath := filepath.Join(root, "lock.json")
	req := providerRequest{Provider: adapter.ProviderClaude, BinaryPath: binary, LockFile: lockPath}
	call := func(op string) error {
		t.Helper()
		data, _ := json.Marshal(req)
		if err := os.WriteFile(reqPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		return providerControl(context.Background(), op, []string{"--request", reqPath}, &bytes.Buffer{})
	}
	if err := call("provider-probe"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("probe modified lock")
	}
	if err := call("provider-lock"); err == nil {
		t.Fatal("unconfirmed lock accepted")
	}
	sum := sha256.Sum256(source)
	req.ConfirmSHA256 = hex.EncodeToString(sum[:])
	if err := call("provider-lock"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(lockPath)
	if err := os.WriteFile(binary, append(source, []byte("# upgrade\n")...), 0700); err != nil {
		t.Fatal(err)
	}
	if err := call("provider-lock"); err == nil {
		t.Fatal("upgrade accepted old confirmation")
	}
	after, _ := os.ReadFile(lockPath)
	if !bytes.Equal(before, after) {
		t.Fatal("lock changed without matching confirmation")
	}
}

// prepareFixtureFirstExec pays the first-execution cost of the freshly written
// fixture with its own 10s budget, so the product probe below runs against an
// already-launchable path without changing the 2s contract it enforces.
func prepareFixtureFirstExec(binary string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return fmt.Errorf("first --version exec: %w (ctx.Err=%v)", err, ctx.Err())
	}
	if string(output) != "2.1.267 (Claude Code)\n" {
		return fmt.Errorf("unexpected fixture version output %q", string(output))
	}
	return nil
}

func TestProfileRunsThroughPinnedCapabilityProbe(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	binary := filepath.Join(root, "provider")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo '2.1.267 (Claude Code)'; else echo --output-format --input-format --permission-mode --disallowed-tools --tools --model --effort; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := adapter.Probe(context.Background(), adapter.ProviderClaude, binary)
	if err != nil {
		t.Fatal(err)
	}
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, Model: "test-model", Reasoning: "medium", TimeoutMS: 1000}
	payload := invocationPayload{Provider: string(adapter.ProviderClaude), ProviderLock: &lock, Profile: &profile, Prompt: "inspect", Directory: root}
	body, _ := json.Marshal(payload)
	if _, err := invocationForGrant(context.Background(), contract.LaunchCommand{AdapterPayload: body}); err != nil {
		t.Fatal(err)
	}
	payload.SessionKind = "session-id"
	payload.SessionID = "s"
	body, _ = json.Marshal(payload)
	if _, err := invocationForGrant(context.Background(), contract.LaunchCommand{AdapterPayload: body}); err == nil {
		t.Fatal("unsupported resume silently accepted")
	}
}
