package adapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Reuse the already-running test executable instead of a newly created shell
// script. Direct script launch can exceed the bounded probe on macOS even when
// the same script via /bin/sh completes in milliseconds. The test covers stream
// selection, not first-execution policy latency for an unsigned script.
func TestMain(m *testing.M) {
	if mode := os.Getenv("ORCHESTRATOR_GROK_AUTH_HELPER"); mode != "" {
		if len(os.Args) != 2 || os.Args[1] != "models" || os.Getenv("GROK_DISABLE_AUTOUPDATER") != "1" {
			os.Exit(71)
		}
		if mode == "fail" {
			_, _ = os.Stdout.WriteString("private-auth-detail")
			os.Exit(9)
		}
		if mode == "slow" {
			time.Sleep(time.Minute)
		}
		_, _ = os.Stdout.WriteString("grok-model\n")
		os.Exit(0)
	}

	if os.Getenv("ORCHESTRATOR_PROBE_TEST_HELPER") == "grok-auto-update" {
		if os.Getenv("GROK_DISABLE_AUTOUPDATER") != "1" {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString("grok probe without updater\n")
		os.Exit(0)
	}
	if os.Getenv("ORCHESTRATOR_PROBE_TEST_HELPER") == "1" {
		if len(os.Args) == 2 && os.Args[1] == "--help" {
			_, _ = os.Stderr.WriteString("--print --input-format --output-format --mode\n")
		} else if len(os.Args) == 2 && os.Args[1] == "--version" {
			_, _ = os.Stdout.WriteString("1.2.0\n")
			_, _ = os.Stderr.WriteString("diagnostic\n")
		} else {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if os.Getenv("ORCHESTRATOR_PROBE_TEST_HELPER") == "auto-update" {
		if os.Getenv("AGY_CLI_DISABLE_AUTO_UPDATE") != "true" {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString("1.2.3\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestProbeDisablesGrokAutoUpdateInChildOnly(t *testing.T) {
	t.Setenv("ORCHESTRATOR_PROBE_TEST_HELPER", "grok-auto-update")
	t.Setenv("GROK_DISABLE_AUTOUPDATER", "")
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--version", "--help"} {
		t.Run(flag, func(t *testing.T) {
			output, err := ProbeOutput(context.Background(), path, flag)
			if err != nil || output != "grok probe without updater" {
				t.Fatalf("probe allowed auto-update: %q %v", output, err)
			}
		})
	}
	if os.Getenv("GROK_DISABLE_AUTOUPDATER") != "" {
		t.Fatal("probe changed parent environment")
	}
}

func TestProbeDisablesAGYAutoUpdateInChildOnly(t *testing.T) {
	t.Setenv("ORCHESTRATOR_PROBE_TEST_HELPER", "auto-update")
	t.Setenv("AGY_CLI_DISABLE_AUTO_UPDATE", "false")
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	version, err := ProbeOutput(context.Background(), path, "--version")
	if err != nil || version != "1.2.3" {
		t.Fatalf("probe allowed auto-update: %q %v", version, err)
	}
	if os.Getenv("AGY_CLI_DISABLE_AUTO_UPDATE") != "false" {
		t.Fatal("probe changed parent environment")
	}
}

func TestProbeAcceptsHelpOnStderrWithoutMixingVersionDiagnostics(t *testing.T) {
	t.Setenv("ORCHESTRATOR_PROBE_TEST_HELPER", "1")
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	help, err := ProbeOutput(context.Background(), path, "--help")
	if err != nil || help != "--print --input-format --output-format --mode" {
		t.Fatalf("stderr help lost: %q %v", help, err)
	}
	version, err := ProbeOutput(context.Background(), path, "--version")
	if err != nil || version != "1.2.0" {
		t.Fatalf("version mixed with diagnostics: %q %v", version, err)
	}
}

func TestProbeCacheReusesSuccessfulPinnedOutputButNotErrors(t *testing.T) {
	t.Setenv("ORCHESTRATOR_PROBE_TEST_HELPER", "1")
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	original := probeCommand
	calls := 0
	probeCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls++
		return exec.CommandContext(ctx, name, args...)
	}
	t.Cleanup(func() { probeCommand = original })

	oldPin := BinaryPin{Path: path, SHA256: strings.Repeat("a", 64)}
	ctx := WithProbeCache(context.Background())
	for range 2 {
		got, err := ProbeOutputPinned(ctx, oldPin, "--version")
		if err != nil || got != "1.2.0" {
			t.Fatalf("ProbeOutputPinned() = %q, %v", got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("successful pinned probe executions = %d, want 1", calls)
	}

	// A replacement at the same path must not reuse the old binary's help.
	newPin := BinaryPin{Path: path, SHA256: strings.Repeat("b", 64)}
	for _, pin := range []BinaryPin{oldPin, newPin} {
		if _, err := ProbeOutputPinned(ctx, pin, "--help"); err != nil {
			t.Fatalf("ProbeOutputPinned(help) = %v", err)
		}
	}
	if calls != 3 {
		t.Fatalf("same-path replacement reused a cached help probe: calls = %d, want 3", calls)
	}

	probeCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		calls++
		return exec.CommandContext(ctx, "/bin/false")
	}
	failed := WithProbeCache(context.Background())
	for range 2 {
		if _, err := ProbeOutputPinned(failed, newPin, "--help"); err == nil {
			t.Fatal("ProbeOutputPinned() error = nil, want probe failure")
		}
	}
	if calls != 5 {
		t.Fatalf("failed probe executions were cached: calls = %d, want 5", calls)
	}
}

func TestProbeOutputDoesNotCacheWithoutPinnedIdentity(t *testing.T) {
	t.Setenv("ORCHESTRATOR_PROBE_TEST_HELPER", "1")
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	original := probeCommand
	calls := 0
	probeCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls++
		return exec.CommandContext(ctx, name, args...)
	}
	t.Cleanup(func() { probeCommand = original })
	ctx := WithProbeCache(context.Background())
	for range 2 {
		if _, err := ProbeOutput(ctx, path, "--version"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("unpinned probe executions = %d, want 2", calls)
	}
}

func TestProbeCacheDoesNotCrossContextsOrCanceledContext(t *testing.T) {
	t.Setenv("ORCHESTRATOR_PROBE_TEST_HELPER", "1")
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	original := probeCommand
	calls := 0
	probeCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls++
		return exec.CommandContext(ctx, name, args...)
	}
	t.Cleanup(func() { probeCommand = original })
	pin := BinaryPin{Path: path, SHA256: strings.Repeat("c", 64)}
	for _, ctx := range []context.Context{WithProbeCache(context.Background()), WithProbeCache(context.Background())} {
		if _, err := ProbeOutputPinned(ctx, pin, "--version"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("probe output crossed context boundary: calls = %d, want 2", calls)
	}

	base, cancel := context.WithCancel(context.Background())
	cached := WithProbeCache(base)
	if _, err := ProbeOutputPinned(cached, pin, "--help"); err != nil {
		t.Fatal(err)
	}
	cancel()
	if output, err := ProbeOutputPinned(cached, pin, "--help"); err == nil || output != "" {
		t.Fatalf("canceled context returned cached output %q, %v", output, err)
	}
}
