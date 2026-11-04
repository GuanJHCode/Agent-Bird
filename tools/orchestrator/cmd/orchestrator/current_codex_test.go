package main

import (
	"bytes"
	"codex-cli-orchestration-design/tools/orchestrator/internal/coordinator"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentCodexMissingThreadDoesNotStartRuntime(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	state := filepath.Join(t.TempDir(), "state")
	err := run(context.Background(), []string{"owner-bind", "--current-codex", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "current_codex_thread_unavailable" {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("created runtime before validating origin")
	}
}

func TestCurrentCodexRejectsIdentityOverrides(t *testing.T) {
	for _, extra := range [][]string{{"--request", "/tmp/request.json"}, {"--origin-pid", "1"}, {"--controller-thread", "other"}} {
		args := append([]string{"owner-bind", "--current-codex"}, extra...)
		err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || err.Error() != "invalid_args" {
			t.Fatalf("%v: %v", extra, err)
		}
	}
}

func TestCurrentCodexOwnerExcludesOnlyExactObservedDaemon(t *testing.T) {
	for _, daemonFirst := range []bool{false, true} {
		var req coordinator.OwnerBindRequest
		var err error
		if daemonFirst {
			req, err = observeCodexOwner(req, "thread", 10, "daemon-birth", true)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err = observeCodexOwner(req, "thread", 20, "session-birth", false)
		if err != nil {
			t.Fatal(err)
		}
		if !daemonFirst {
			req, err = observeCodexOwner(req, "thread", 10, "daemon-birth", true)
			if err != nil {
				t.Fatal(err)
			}
		}
		if req.OriginPID != 20 || req.OriginBirth != "session-birth" {
			t.Fatalf("selected=%+v", req)
		}
		if _, err = observeCodexOwner(req, "thread", 30, "another-session", false); err == nil || err.Error() != "current_codex_origin_ambiguous" {
			t.Fatalf("multiple sessions=%v", err)
		}
	}
}
