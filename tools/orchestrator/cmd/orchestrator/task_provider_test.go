package main

import (
	"bytes"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskProbeProviderSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		provider  adapter.Provider
		supported bool
	}{
		{"claude", adapter.ProviderClaude, true}, {"agy", adapter.ProviderAGY, true}, {"grok", adapter.ProviderGrok, false},
		{"antigravity-cli", adapter.ProviderAGY, true}, {"grok-build", adapter.ProviderGrok, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTaskTemp(t)
			binary := filepath.Join(root, "provider")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo fixture-v1;;\n--help) echo '--output-format --input-format --permission-mode --mode --disallowed-tools';;\n*) exit 99;;\nesac\n"), 0700); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := taskProbe(context.Background(), []string{"--provider", tc.name, "--binary", binary}, &out)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Lock      adapter.ProviderLock `json:"lock"`
				Supported bool                 `json:"profile_supported"`
				Reason    string               `json:"reason"`
			}
			if err = json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Lock.Provider != tc.provider || got.Supported != tc.supported {
				t.Fatalf("unexpected probe: %+v", got)
			}
			if !tc.supported && got.Reason != "grok_version_not_verified" {
				t.Fatalf("lost guard: %+v", got)
			}
		})
	}
}

func TestTaskRunProviderLockMismatchBeforeAllocation(t *testing.T) {
	root := privateTaskTemp(t)
	brief := filepath.Join(root, "brief.txt")
	if err := os.WriteFile(brief, []byte("Read only; no delegation"), 0600); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, "lock.json")
	if err := writeExclusiveJSON(lock, adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude}); err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(root, "handle.json")
	state := filepath.Join(root, "state")
	err := taskSubmit(context.Background(), "run", []string{"--provider", "agy", "--run-id", "r", "--directory", root, "--prompt-file", brief, "--provider-lock", lock, "--timeout-ms", "1000", "--handle", handle, "--state-dir", state}, &bytes.Buffer{})
	if err == nil || err.Error() != "provider_lock_invalid" {
		t.Fatalf("got %v", err)
	}
	for _, p := range []string{handle, state} {
		if _, err = os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("allocated %s", p)
		}
	}
}

func TestTaskProviderRejectsUnknown(t *testing.T) {
	err := taskProbe(context.Background(), []string{"--provider", "other", "--binary", "/nonexistent"}, &bytes.Buffer{})
	if err == nil || err.Error() != "task_provider_unsupported" {
		t.Fatalf("got %v", err)
	}
}

func TestTaskRunGrokGuardBeforeAllocation(t *testing.T) {
	root := privateTaskTemp(t)
	brief := filepath.Join(root, "brief.txt")
	if err := os.WriteFile(brief, []byte("Read only"), 0600); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, "lock.json")
	if err := writeExclusiveJSON(lock, adapter.ProviderLock{Version: 1, Provider: adapter.ProviderGrok, Protocol: adapter.ProtocolID(adapter.ProviderGrok), Binary: adapter.BinaryPin{Version: "grok 1.0.34 (3736acbc8658)"}}); err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(root, "handle.json")
	state := filepath.Join(root, "state")
	err := taskSubmit(context.Background(), "run", []string{"--provider", "grok", "--run-id", "r", "--directory", root, "--prompt-file", brief, "--provider-lock", lock, "--timeout-ms", "1000", "--handle", handle, "--state-dir", state}, &bytes.Buffer{})
	if err == nil || err.Error() != "grok_session_write_required" {
		t.Fatalf("got %v", err)
	}
	for _, p := range []string{handle, state} {
		if _, err = os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("allocated %s", p)
		}
	}
}

func TestTaskRunAGYReachesCapabilityCheck(t *testing.T) {
	root := privateTaskTemp(t)
	binary := filepath.Join(root, "agy")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo fixture-v1;;\n--help) echo '--output-format --input-format';;\n*) exit 99;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	pin, err := adapter.Probe(context.Background(), adapter.ProviderAGY, binary)
	if err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.txt")
	if err = os.WriteFile(brief, []byte("Read only"), 0600); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, "lock.json")
	if err = writeExclusiveJSON(lock, pin); err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(root, "handle.json")
	state := filepath.Join(root, "state")
	err = taskSubmit(context.Background(), "run", []string{"--provider", "agy", "--run-id", "r", "--directory", root, "--prompt-file", brief, "--provider-lock", lock, "--timeout-ms", "1000", "--handle", handle, "--state-dir", state}, &bytes.Buffer{})
	if err == nil || err.Error() != "provider_capability_unsupported" {
		t.Fatalf("got %v", err)
	}
	for _, p := range []string{handle, state} {
		if _, err = os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("allocated %s", p)
		}
	}
}
