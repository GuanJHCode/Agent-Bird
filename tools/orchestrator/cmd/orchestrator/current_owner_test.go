package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentOwnerRejectsUntrustedManagedContextWithoutCodexFallback(t *testing.T) {
	for _, kind := range []string{"missing", "unknown-field", "public-file", "symlink", "self-launcher", "unsupported-provider", "wrong-scope"} {
		t.Run(kind, func(t *testing.T) {
			root := privateTaskTemp(t)
			path := filepath.Join(root, "owner.json")
			req := managedOwnerRequest{Version: 1, Provider: "claude", Scope: "managed_process", ControllerThread: "managed-claude-test", LauncherPID: os.Getpid(), LauncherBirth: "birth", LauncherPath: "/private/orchestrator", LauncherDigest: strings.Repeat("a", 64)}
			if kind == "unsupported-provider" {
				req.Provider = "unknown"
				req.ControllerThread = "managed-unknown-test"
			}
			if kind == "wrong-scope" {
				req.Scope = "native_conversation"
			}
			if kind != "missing" {
				if err := writeExclusiveJSON(path, req); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "unknown-field":
				if err := os.WriteFile(path, []byte(`{"version":1,"native_chat":"forged"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "public-file":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := filepath.Join(root, "link.json")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			t.Setenv(managedOwnerRequestEnv, path)
			t.Setenv("CODEX_THREAD_ID", "00000000-0000-0000-0000-000000000001")
			if _, err := currentOwner(context.Background()); err == nil || err.Error() != "current_owner_unavailable" {
				t.Fatalf("managed context fell through or was accepted: %v", err)
			}
		})
	}
}
