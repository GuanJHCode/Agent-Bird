package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestProviderLockRequiresExactObservedDigestAndPreviousPin(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "provider")
	source := []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.0; else echo --output-format --input-format --permission-mode --disallowed-tools --model --effort --resume; fi\n")
	if err := os.WriteFile(binary, source, 0700); err != nil {
		t.Fatal(err)
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

func TestProfileRunsThroughPinnedCapabilityProbe(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	binary := filepath.Join(root, "provider")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.0; else echo --output-format --input-format --permission-mode --disallowed-tools --model --effort; fi\n"), 0700); err != nil {
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
