package host

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestS4StageTimingObservation is an opt-in diagnostic harness for the S4
// signature (TestGrokHostLaunchUsesAuthorizedSession). It measures the same
// primitives the production launch uses (prepareGrokCommand + process.Start /
// Wait) under a generous non-product context so no 2s/5s product deadline is
// involved, and appends per-stage JSONL as it goes.
//
// Measurer controls (asserted only when ORCHESTRATOR_S4_STAGE_CONTROL_STRICT=1,
// i.e. in the quiet baseline and its repeat, never under a storm):
//   - a same-file warm repeat must be faster than the cold first exec,
//     killing "our clock is broken" and "ambient CPU explains it all";
//   - warm stage separation (first stdout near spawn, exit ~3s later) proves
//     the first-byte and exit clocks observe different events.
//
// Enable with ORCHESTRATOR_S4_STAGE_TIMING=1 and append JSONL to
// ORCHESTRATOR_S4_STAGE_OUT. Without the flag the test skips.
func TestS4StageTimingObservation(t *testing.T) {
	if os.Getenv("ORCHESTRATOR_S4_STAGE_TIMING") != "1" {
		t.Skip("opt-in diagnostic stage timing harness")
	}
	strict := os.Getenv("ORCHESTRATOR_S4_STAGE_CONTROL_STRICT") == "1"
	outPath := os.Getenv("ORCHESTRATOR_S4_STAGE_OUT")
	runID := os.Getenv("ORCHESTRATOR_S4_STAGE_RUN")
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
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	for _, p := range []string{home, work} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GROK_HOME", home)
	if err := os.WriteFile(filepath.Join(work, "INPUT.txt"), []byte("fixture-readonly"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureScript := "#!/bin/sh\nsession=''\nwhile [ $# -gt 0 ]; do\ncase \"$1\" in --session-id) shift; session=$1;; esac\nshift\ndone\n[ -n \"$session\" ] || exit 9\nanswer=$(cat INPUT.txt)\nprintf '%s\\n' \"{\\\"type\\\":\\\"text\\\",\\\"data\\\":\\\"$answer\\\"}\"\nprintf '%s\\n' \"{\\\"type\\\":\\\"end\\\",\\\"stopReason\\\":\\\"end_turn\\\",\\\"sessionId\\\":\\\"$session\\\"}\"\n"

	// runGrokChain executes prepareGrokCommand + Start + Wait over the
	// provider file at reuseProvider. When writeProvider is true the file is
	// freshly created (cold first exec); otherwise the existing file is reused
	// so its evaluation is already warm.
	runGrokChain := func(shape string, iteration int, reuseProvider string, writeProvider bool) map[string]any {
		o := map[string]any{"shape": shape, "iteration": iteration, "strict_control": strict}
		provider := filepath.Join(root, reuseProvider)
		if writeProvider {
			if err := os.WriteFile(provider, []byte(fixtureScript), 0700); err != nil {
				t.Fatal(err)
			}
		}
		pin := adapter.BinaryPin{Path: provider, Version: "grok 1.0.34 (3736acbc8658)", SHA256: hashBytes([]byte(fixtureScript))}
		profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 120_000, GrokSessionWrite: true}
		inv, err := adapter.BuildInvocation(adapter.Request{Provider: adapter.ProviderGrok, Binary: pin, Lock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderGrok, Protocol: adapter.ProtocolID(adapter.ProviderGrok), Binary: pin}, CWD: work, Prompt: "read INPUT.txt", Profile: profile})
		if err != nil {
			t.Fatal(err)
		}
		grant := contract.LaunchCommand{CommandID: fmt.Sprintf("stage-%s-%d", shape, iteration), ReservationID: fmt.Sprintf("stage-res-%s-%d", shape, iteration), RunID: "stage-" + hashText(root), TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, GrantedActiveMS: 120_000}
		socket := filepath.Join("/private/tmp", "codex-grok-"+grokSessionID(grant))
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		base := invocationCommand(reportInvocation{base: inv})
		tPrepare := time.Now()
		prepared, err := prepareGrokCommand(ctx, base, profile, grant, filepath.Join(root, fmt.Sprintf("scratch-%s-%d", shape, iteration)))
		if err != nil {
			t.Fatalf("%s prepare: %v", shape, err)
		}
		// prepare succeeded for this test-unique grant; only now own cleanup.
		defer os.RemoveAll(socket)
		o["session_socket"] = socket
		o["prepare_ms"] = durationMS(time.Since(tPrepare))
		rec := &firstByteRecorder{}
		prepared.Stdout = rec
		tSpawn := time.Now()
		p, err := process.Start(ctx, prepared)
		if err != nil {
			t.Fatalf("%s start: %v", shape, err)
		}
		o["start_return_ms"] = durationMS(time.Since(tSpawn))
		waitErr := p.Wait(ctx)
		o["exit_observed_ms"] = durationMS(time.Since(tSpawn))
		o["first_stdout_ms"] = rec.msSince(tSpawn)
		o["exit_code"] = p.ExitCode()
		o["wait_err"] = fmt.Sprint(waitErr)
		if waitErr != nil {
			t.Fatalf("%s wait: %v", shape, waitErr)
		}
		record(o)
		return o
	}

	// Shape A: full production grok chain over a FRESH provider file (cold
	// first exec), then A2: the identical chain over the same already
	// evaluated file. The A/A2 pair isolates first-exec evaluation from the
	// rest of the production launch flow. A3 reuses the warm chain a second
	// time so the warm baseline is not a single sample.
	coldMS := map[int]float64{}
	warmMS := map[int]float64{}
	for iteration := 0; iteration < 3; iteration++ {
		provider := fmt.Sprintf("provider-a-%d", iteration)
		cold := runGrokChain("A_cold_grok_chain", iteration, provider, true)
		coldMS[iteration] = cold["exit_observed_ms"].(float64)
		warm := runGrokChain("A2_warm_grok_chain", iteration, provider, false)
		warmMS[iteration] = warm["exit_observed_ms"].(float64)
	}
	for iteration := range coldMS {
		if strict && !(warmMS[iteration] < coldMS[iteration]) {
			t.Fatalf("A/A2 control invalid: warm %vms >= cold %vms at pair %d", warmMS[iteration], coldMS[iteration], iteration)
		}
	}

	// Shape B: sandbox-exec with a constant minimal policy over a stable path.
	for iteration := 0; iteration < 3; iteration++ {
		o := map[string]any{"shape": "B_sandbox_stable", "iteration": iteration, "strict_control": strict}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		tSpawn := time.Now()
		p, err := process.Start(ctx, process.Command{Path: "/usr/bin/sandbox-exec", Args: []string{"-p", "(version 1)(allow default)", "/usr/bin/true"}, Dir: work})
		if err != nil {
			cancel()
			t.Fatalf("shape B start: %v", err)
		}
		waitErr := p.Wait(ctx)
		o["exit_observed_ms"] = durationMS(time.Since(tSpawn))
		o["exit_code"] = p.ExitCode()
		cancel()
		if waitErr != nil {
			t.Fatalf("shape B wait: %v", waitErr)
		}
		record(o)
	}

	// Shape C: fresh provider file executed directly (cold, no sandbox), then
	// the same file again (warm). The cold-warm pair is the measurer negative
	// control: the stall must attach to first-exec evaluation, not to clocks.
	for iteration := 0; iteration < 3; iteration++ {
		binary := filepath.Join(root, fmt.Sprintf("provider-c-%d", iteration))
		content := fmt.Sprintf("#!/bin/sh\necho ok\n# fresh=%d-%d\n", iteration, time.Now().UnixNano())
		if err := os.WriteFile(binary, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
		pairCold := 0.0
		for _, pass := range []string{"cold", "warm"} {
			o := map[string]any{"shape": "C_fresh_direct_" + pass, "iteration": iteration, "strict_control": strict}
			rec := &firstByteRecorder{}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			tSpawn := time.Now()
			p, err := process.Start(ctx, process.Command{Path: binary, Dir: work, Stdout: rec})
			if err != nil {
				cancel()
				t.Fatalf("shape C %s start: %v", pass, err)
			}
			waitErr := p.Wait(ctx)
			o["exit_observed_ms"] = durationMS(time.Since(tSpawn))
			o["first_stdout_ms"] = rec.msSince(tSpawn)
			o["exit_code"] = p.ExitCode()
			cancel()
			if waitErr != nil {
				t.Fatalf("shape C %s wait: %v", pass, waitErr)
			}
			if pass == "cold" {
				pairCold = o["exit_observed_ms"].(float64)
			} else if strict {
				warm := o["exit_observed_ms"].(float64)
				if warm > 1000 {
					t.Fatalf("C control invalid: warm repeat %vms (iteration %d)", warm, iteration)
				}
				if !(warm < pairCold) {
					t.Fatalf("C control invalid: warm %vms >= cold %vms at pair %d", warm, pairCold, iteration)
				}
			}
			record(o)
		}
	}

	// Shape D: warm start of a provider that prints quickly then sleeps.
	// Stage separation control: first stdout near spawn while the exit clock
	// shows the sleep; proves the two clocks observe different events.
	o := map[string]any{"shape": "D_warm_stage_separation", "iteration": 0, "strict_control": strict}
	binary := filepath.Join(root, "provider-d")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho quick\nsleep 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	{
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		p, err := process.Start(ctx, process.Command{Path: binary, Dir: work})
		if err != nil {
			cancel()
			t.Fatalf("shape D warm start: %v", err)
		}
		if waitErr := p.Wait(ctx); waitErr != nil {
			cancel()
			t.Fatalf("shape D warm wait: %v", waitErr)
		}
		cancel()
	}
	rec := &firstByteRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	tSpawn := time.Now()
	p, err := process.Start(ctx, process.Command{Path: binary, Dir: work, Stdout: rec})
	if err != nil {
		cancel()
		t.Fatalf("shape D start: %v", err)
	}
	waitErr := p.Wait(ctx)
	o["exit_observed_ms"] = durationMS(time.Since(tSpawn))
	o["first_stdout_ms"] = rec.msSince(tSpawn)
	o["exit_code"] = p.ExitCode()
	cancel()
	if waitErr != nil {
		t.Fatalf("shape D wait: %v", waitErr)
	}
	if strict {
		if v, ok := o["first_stdout_ms"].(float64); !ok || v > 1000 {
			t.Fatalf("shape D control invalid: first stdout %vms", o["first_stdout_ms"])
		}
		if v := o["exit_observed_ms"].(float64); v < 2500 {
			t.Fatalf("shape D control invalid: exit observed %vms (expected sleep visible)", v)
		}
	}
	record(o)
}

func durationMS(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

// firstByteRecorder timestamps the first Write.
type firstByteRecorder struct {
	mu    sync.Mutex
	first time.Time
	seen  bool
}

func (r *firstByteRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	if !r.seen && len(p) > 0 {
		r.first = time.Now()
		r.seen = true
	}
	r.mu.Unlock()
	return len(p), nil
}

func (r *firstByteRecorder) msSince(anchor time.Time) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.seen {
		return -1
	}
	return durationMS(r.first.Sub(anchor))
}
