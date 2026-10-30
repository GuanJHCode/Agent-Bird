package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestProbeCauseDecompositionObservation is an opt-in diagnostic companion for
// the S1/S3 probe family. ProbeOutput keeps its error STRING stable (F6) and
// exposes causes through the errors.Is/As chain, so the gate's failure text
// alone cannot decompose phases. This test calls ProbeOutput directly against
// fresh first-exec provider scripts (the S1/S3 call shape) and records a
// cause-decomposed classification per iteration:
//
//	start-failure | deadline+kill | exited-error(kill/exit-code) | cancelled | ok
//
// plus the measured ProbeOutput wall time. Assertions are only the
// deterministic controls (pre-cancelled context must classify as cancelled,
// warm stable path must not fail to start). Warm-path deadlines are recorded
// as diagnostic observations; no assertion requires a particular latency.
//
// Enable with ORCHESTRATOR_PROBE_CAUSE_OBSERVATION=1 and append JSONL to
// ORCHESTRATOR_PROBE_CAUSE_OUT.
func TestProbeCauseDecompositionObservation(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_PROBE_CAUSE_OBSERVATION") != "1" {
		t.Skip("opt-in diagnostic probe cause observation")
	}
	outPath := os.Getenv("ORCHESTRATOR_PROBE_CAUSE_OUT")
	runID := os.Getenv("ORCHESTRATOR_PROBE_CAUSE_RUN")
	var out *os.File
	if outPath != "" {
		f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		out = f
	}
	record := func(o map[string]any) {
		o["run_id"] = runID
		b, _ := json.Marshal(o)
		t.Log(string(b))
		if out != nil {
			if _, err := out.Write(append(b, '\n')); err != nil {
				t.Fatal(err)
			}
		}
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())

	classify := func(err error) map[string]any {
		if err == nil {
			return map[string]any{"class": "ok"}
		}
		var startErr *os.PathError
		var exitErr *exec.ExitError
		isDeadline := errors.Is(err, context.DeadlineExceeded)
		isCancelled := errors.Is(err, context.Canceled)
		isStart := errors.As(err, &startErr) && !errors.As(err, &exitErr)
		isExit := errors.As(err, &exitErr)
		var signal string
		if isExit {
			signal = strings.TrimSpace(exitErr.Error())
		}
		return map[string]any{"class": map[bool]string{true: "classified", false: "unclassified"}[isDeadline || isCancelled || isStart || isExit],
			"deadline": isDeadline, "cancelled": isCancelled, "start_failure": isStart,
			"exited": isExit, "exit_text": signal, "text": err.Error()}
	}

	// Deterministic negative control: a pre-cancelled context must classify
	// as cancelled, proving the classifier observes the cause chain, not text.
	// This control does not depend on exec speed, so it is safe to assert even
	// under a fresh-exec storm.
	{
		provider := filepath.Join(root, "warm.sh")
		if err := os.WriteFile(provider, []byte("#!/bin/sh\necho 1.0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		// Absorb cold first-exec by retrying until the file is evaluated; under
		// a fresh-exec storm the very first attempt is itself a cold exec and
		// may exceed the unchanged 2s probe budget. We record that observation
		// (informative) but do not let it abort the storm measurement.
		var warmErr error
		for attempt := 0; attempt < 5; attempt++ {
			if _, warmErr = ProbeOutput(context.Background(), provider, "--version"); warmErr == nil {
				break
			}
		}
		record(map[string]any{"shape": "control_warm_absorb", "result": classify(warmErr)})

		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := ProbeOutput(cancelled, provider, "--version")
		c := classify(err)
		record(map[string]any{"shape": "control_cancelled", "result": c})
		if err == nil || c["class"] != "classified" || c["cancelled"] != true {
			t.Fatalf("cancelled negative control failed: %v %+v", err, c)
		}
		ok := classify(func() error { _, e := ProbeOutput(context.Background(), provider, "--version"); return e }())
		record(map[string]any{"shape": "control_warm_ok", "result": ok})
		// A warm stable-path exec must not fail to START; under load it may hit
		// the unchanged deadline, which is itself the mechanism under study, so
		// this is recorded rather than asserted fatal except for a start failure.
		if ok["start_failure"] == true {
			t.Fatalf("warm stable path failed to start (unexpected): %+v", ok)
		}
	}

	// S1/S3-shaped iterations: FRESH provider script, first exec, 2s internal
	// probe budget unchanged. Classification per attempt (no timing asserts).
	freshAttempts := 10
	if v := os.Getenv("ORCHESTRATOR_PROBE_CAUSE_ITERATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			freshAttempts = n
		}
	}
	for i := 0; i < freshAttempts; i++ {
		provider := filepath.Join(root, fmt.Sprintf("fresh-%d.sh", i))
		content := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 1.0; else echo flags; fi\n# fresh=%d\n", i)
		if err := os.WriteFile(provider, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
		t0 := time.Now()
		_, err := ProbeOutput(context.Background(), provider, "--version")
		elapsed := time.Since(t0)
		c := classify(err)
		c["elapsed_ms"] = float64(elapsed.Microseconds()) / 1000
		record(map[string]any{"shape": "fresh_probe_iteration", "iteration": i, "result": c})
	}
}
