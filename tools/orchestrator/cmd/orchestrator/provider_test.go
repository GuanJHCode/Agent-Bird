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

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestProviderLockRequiresExactObservedDigestAndPreviousPin(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "provider")
	source := []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.0; else echo --output-format --input-format --permission-mode --disallowed-tools --model --effort --resume --json-schema; fi\n")
	if err := os.WriteFile(binary, source, 0700); err != nil {
		t.Fatal(err)
	}
	// Prepare the fixture with a separate bounded first exec before testing
	// observed identity and pin confirmation through the product's 2s probe.
	// Preparation failure aborts without retry; it does not extend that probe.
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
	if err := prepareFixtureFirstExec(binary); err != nil {
		t.Fatalf("fixture_updated_exec_not_ready: %v", err)
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
	if string(output) != "1.0\n" {
		return fmt.Errorf("unexpected fixture version output %q", string(output))
	}
	return nil
}

func TestProfileRunsThroughPinnedCapabilityProbe(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	binary := filepath.Join(root, "provider")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.0; else echo --output-format --input-format --permission-mode --disallowed-tools --model --effort --json-schema; fi\n"), 0700); err != nil {
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
