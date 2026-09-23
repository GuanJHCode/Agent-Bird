package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

func TestGrokProbeReportsSeparateWriteAuthorization(t *testing.T) {
	for _, version := range []string{"grok 1.0.34 (3736acbc8658)", "grok 1.0.40 (eb1a2256660d)"} {
		t.Run(version, func(t *testing.T) {
			root := privateTaskTemp(t)
			binary := filepath.Join(root, "grok")
			script := "#!/bin/sh\ncase \"$1\" in\n--version) echo '" + version + "';;\n--help) echo '--output-format --permission-mode --session-id --leader-socket --no-subagents --disable-web-search --tools --deny';;\n*) exit 99;;\nesac\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := taskProbe(context.Background(), []string{"--provider", "grok", "--binary", binary}, &out); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Lock          adapter.ProviderLock `json:"lock"`
				Supported     bool                 `json:"profile_supported"`
				WriteRequired bool                 `json:"requires_session_write_authorization"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !got.Supported || !got.WriteRequired || got.Lock.Binary.Version != version {
				t.Fatalf("wrong capability/authorization: %+v", got)
			}
		})
	}
}

func TestGrokSessionFlagReachesCapabilityCheckWithoutAllocation(t *testing.T) {
	root := privateTaskTemp(t)
	binary := filepath.Join(root, "grok")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo 'grok 1.0.34 (3736acbc8658)';;\n--help) echo '--output-format --permission-mode';;\n*) exit 99;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
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
