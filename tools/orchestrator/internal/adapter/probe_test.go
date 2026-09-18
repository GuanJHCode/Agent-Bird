package adapter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Reuse the already-running test executable instead of a newly created shell
// script. Direct script launch can exceed the bounded probe on macOS even when
// the same script via /bin/sh completes in milliseconds. The test covers stream
// selection, not first-execution policy latency for an unsigned script.
func TestMain(m *testing.M) {
	if os.Getenv("ORCHESTRATOR_PROBE_TEST_HELPER") == "grok-auto-update" {
		if os.Getenv("GROK_DISABLE_AUTOUPDATER") != "1" {
			os.Exit(2)
		}
		_, _ = os.Stdout.WriteString("grok probe without updater\n")
		os.Exit(0)
	}
	if os.Getenv("ORCHESTRATOR_PROBE_TEST_HELPER") == "1" {
		if len(os.Args) == 2 && os.Args[1] == "--help" {
			_, _ = os.Stderr.WriteString("--input-format --output-format --mode\n")
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
	if err != nil || help != "--input-format --output-format --mode" {
		t.Fatalf("stderr help lost: %q %v", help, err)
	}
	version, err := ProbeOutput(context.Background(), path, "--version")
	if err != nil || version != "1.2.0" {
		t.Fatalf("version mixed with diagnostics: %q %v", version, err)
	}
}
