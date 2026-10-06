package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeFailureRetainsExitCauseWithoutOutput(t *testing.T) {
	output, err := ProbeOutput(context.Background(), "/bin/sh", "-c", "printf synthetic-secret >&2; exit 7")
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 7 {
		t.Fatalf("exit cause missing: %v", err)
	}
	if output != "" || err.Error() != "provider_probe_failed" || fmt.Sprintf("%+v", err) != "provider_probe_failed" {
		t.Fatal("probe exposed diagnostics or changed public code")
	}
}

func TestProbeFailureRetainsStartCause(t *testing.T) {
	output, err := ProbeOutput(context.Background(), filepath.Join(t.TempDir(), "synthetic-secret-missing"))
	var pathError *os.PathError
	if !errors.As(err, &pathError) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("start cause missing: %v", err)
	}
	if output != "" || err.Error() != "provider_probe_failed" {
		t.Fatal("start failure changed public code")
	}
}

func TestProbeFailureRetainsCancellationCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := ProbeOutput(ctx, "/bin/echo", "not-started")
	if !errors.Is(err, context.Canceled) || output != "" || err.Error() != "provider_probe_failed" {
		t.Fatalf("cancel cause missing or leaked output: %q %v", output, err)
	}
}

func TestProbeFailureRetainsDeadlineAndExitCauses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	output, err := ProbeOutput(ctx, "/bin/sleep", "5")
	var exitError *exec.ExitError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &exitError) {
		t.Fatalf("deadline/exit causes missing: %v", err)
	}
	if output != "" || err.Error() != "provider_probe_failed" {
		t.Fatal("deadline failure changed public code")
	}
}

func TestProbeVersionFailurePreservesNestedCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = Probe(ctx, ProviderClaude, path)
	if !errors.Is(err, context.Canceled) || err.Error() != "binary_version_probe_failed" {
		t.Fatalf("version failure lost cause or changed code: %v", err)
	}
}
