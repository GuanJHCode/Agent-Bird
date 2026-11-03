package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
)

func TestGrokProbeReportsSeparateWriteAuthorization(t *testing.T) {
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_CAPABILITY_FIXTURE", "full")
	var out bytes.Buffer
	if err := taskProbe(context.Background(), []string{"--provider", "grok", "--binary", binary}, &out); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Supported     bool `json:"profile_supported"`
		WriteRequired bool `json:"requires_session_write_authorization"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Supported || !got.WriteRequired {
		t.Fatalf("wrong capability/authorization: %+v", got)
	}
}

func TestGrokSessionFlagReachesCapabilityCheckWithoutAllocation(t *testing.T) {
	root := privateTaskTemp(t)
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_CAPABILITY_FIXTURE", "missing")
	lock, err := adapter.Probe(context.Background(), adapter.ProviderGrok, binary)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, "lock.json")
	brief := filepath.Join(root, "brief.txt")
	if err := writeExclusiveJSON(lockPath, lock); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brief, []byte("Read only"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		handle := filepath.Join(root, "handle.json")
		state := filepath.Join(root, "state")
		args := []string{"--provider", "grok", "--run-id", "r", "--directory", root, "--prompt-file", brief, "--provider-lock", lockPath, "--timeout-ms", "1000", "--handle", handle, "--state-dir", state}
		want := "grok_session_write_required"
		if enabled {
			args = append(args, "--grok-session-write")
			want = "provider_capability_unsupported"
		}
		err := taskSubmit(context.Background(), "run", args, &bytes.Buffer{})
		if err == nil || err.Error() != want {
			t.Fatalf("enabled=%v err=%v", enabled, err)
		}
		for _, p := range []string{handle, state} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("allocated %s", p)
			}
		}
	}
}
