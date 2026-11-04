package adapter

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeProbeUsesPrivateStateAndCannotTouchCallerHome(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "NODE_OPTIONS", "HTTPS_PROXY", "DYLD_INSERT_LIBRARIES"} {
		t.Setenv(name, "SYNTHETIC_UNTRUSTED")
	}
	secret := filepath.Join(home, "source-auth")
	if err := os.WriteFile(secret, []byte("SYNTHETIC_AUTH"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	pin := BinaryPin{Path: "/bin/sh", Version: "probe", SHA256: fmt.Sprintf("%x", sha256.Sum256(body))}
	script := `test "$HOME" != "$1" && test "$CLAUDE_CONFIG_DIR" = "$HOME" && test "$DISABLE_AUTOUPDATER" = 1 && test -z "${ANTHROPIC_API_KEY+x}${ANTHROPIC_AUTH_TOKEN+x}${CLAUDE_CODE_OAUTH_TOKEN+x}${NODE_OPTIONS+x}${HTTPS_PROXY+x}${DYLD_INSERT_LIBRARIES+x}" && test ! -r "$1/source-auth" && ! touch "$1/probe-write" 2>/dev/null && printf '%s' "$2"`
	for _, flag := range []string{"--version", "--help"} {
		output, err := ProbePinnedOutput(context.Background(), ProviderClaude, pin, "-c", script, "probe", home, flag)
		if err != nil || output != flag {
			t.Fatalf("unsafe %s probe: %q %v", flag, output, err)
		}
	}
	if got, err := os.ReadFile(secret); err != nil || string(got) != "SYNTHETIC_AUTH" {
		t.Fatal("source changed")
	}
}

func TestClaudeNodeRequiresExplicitOfficialPinBeforeExecution(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "node")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":/usr/bin:/bin")
	t.Setenv("ORCHESTRATOR_CLAUDE_NODE_BINARY", "")
	if _, err := ClaudeNodePin(); err == nil || err.Error() != "claude_node_pin_missing" {
		t.Fatal("ambient PATH selected Node")
	}
	t.Setenv("ORCHESTRATOR_CLAUDE_NODE_BINARY", fake)
	if _, err := ClaudeNodePin(); err == nil || err.Error() != "claude_node_runtime_untrusted" {
		t.Fatal("fake Node accepted")
	}
}
