package main

import (
	"bytes"
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

func TestCurrentOwnerMissingManagedContextDoesNotStartRuntime(t *testing.T) {
	t.Setenv("AGENT_BIRD_OWNER_REQUEST", "")
	t.Setenv("CODEX_THREAD_ID", "")
	state := filepath.Join(t.TempDir(), "state")
	err := run(context.Background(), []string{"owner-bind", "--current", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || err.Error() != "current_codex_thread_unavailable" {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("created runtime before validating origin")
	}
}
