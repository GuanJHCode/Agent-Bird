package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
)

func TestProviderHelpCombinesCodexExecFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	body := []byte("#!/bin/sh\ncase \"$*\" in '--help') echo --strict-config;; 'exec --help') echo '--json --ephemeral --ignore-rules';; *) exit 9;; esac\n")
	if err := os.WriteFile(path, body, 0700); err != nil {
		t.Fatal(err)
	}
	pin := adapter.BinaryPin{Path: path, Version: "fixture", SHA256: fmt.Sprintf("%x", sha256.Sum256(body))}
	help, err := providerHelp(context.Background(), adapter.ProviderCodex, pin)
	if err != nil || !strings.Contains(help, "--strict-config") || !strings.Contains(help, "--ephemeral") {
		t.Fatalf("help=%q err=%v", help, err)
	}
}

func TestNativeAdmissionRejectsUninstalledCodex(t *testing.T) {
	pin := adapter.BinaryPin{Version: "codex-cli 0.166.0", SHA256: adapter.CodexNativePins()["codex-cli 0.166.0"]}
	if err := nativeExecutionAdmission(adapter.ProviderCodex, pin); err == nil || err.Error() != "codex_guard_package_untrusted" {
		t.Fatalf("got %v", err)
	}
	pin.SHA256 = strings.Repeat("a", 64)
	if err := nativeExecutionAdmission(adapter.ProviderCodex, pin); err == nil || err.Error() != "codex_trial_guard_not_ready" {
		t.Fatalf("got %v", err)
	}
}

// Package smoke may target the real receipt-backed installed wrapper. Ordinary
// tests still build an isolated binary; production has no environment override.
func deliveryTestBinary(t *testing.T, target string) string {
	t.Helper()
	if wrapper := os.Getenv("BIRD_TEST_WRAPPER"); wrapper != "" {
		if !filepath.IsAbs(wrapper) {
			t.Fatal("BIRD_TEST_WRAPPER must be absolute")
		}
		if info, err := os.Stat(wrapper); err != nil || info.Mode()&0111 == 0 {
			t.Fatalf("wrapper unavailable: %v", err)
		}
		return wrapper
	}
	if out, err := exec.Command("go", "build", "-o", target, ".").CombinedOutput(); err != nil {
		t.Fatalf("build %s %v", out, err)
	}
	return target
}
