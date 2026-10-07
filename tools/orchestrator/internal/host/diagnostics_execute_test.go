//go:build darwin || linux

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestDiagnosticExecutionChild(t *testing.T) {
	mode := os.Getenv("DIAGNOSTIC_EXECUTION_CHILD")
	if mode == "" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv("DIAGNOSTIC_EXECUTION_RELEASE")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(92)
		}
		time.Sleep(time.Millisecond)
	}
	if mode == "structured" {
		fmt.Println(`{"type":"system","subtype":"init","session_id":"native-session"}`)
		fmt.Println(`{"type":"result","subtype":"success","result":"public result"}`)
	} else {
		fmt.Fprint(os.Stdout, "native stdout\n")
	}
	fmt.Fprint(os.Stderr, "native stderr\n")
	if mode == "cancel" || mode == "failure" {
		if err := os.WriteFile(os.Getenv("DIAGNOSTIC_EXECUTION_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(93)
		}
		select {
		case <-signals:
			fmt.Fprint(os.Stdout, "termination tail\n")
		case <-time.After(15 * time.Second):
			os.Exit(94)
		}
	}
	os.Exit(0)
}

type diagnosticStructuredInvocation struct{ launchInvocation }

func (diagnosticStructuredInvocation) OutputProvider() string { return "claude-code" }

func nativeDiagnosticLaunch(t *testing.T, mode string) (*Host, contract.LaunchCommand, launchInvocation, string, string) {
	t.Helper()
	h, g := diagnosticHost(t), diagnosticGrant()
	g.CommandID = "command"
	g.ReservationID = "execution-reservation"
	g.WorkRevision = 1
	g.PlanRevision = 1
	g.GrantedActiveMS = 15000
	bin, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	release, ready := filepath.Join(h.spoolRoot, ".release"), filepath.Join(h.spoolRoot, ".ready")
	inv := launchInvocation{args: []string{bin, "-test.run=^TestDiagnosticExecutionChild$"}, dir: h.spoolRoot, env: map[string]string{"DIAGNOSTIC_EXECUTION_CHILD": mode, "DIAGNOSTIC_EXECUTION_RELEASE": release, "DIAGNOSTIC_EXECUTION_READY": ready}}
	return h, g, inv, release, ready
}

func nativeDiagnosticObserver(t *testing.T, h *Host, g contract.LaunchCommand, release string, beforeRelease func()) **process.Handle {
	t.Helper()
	var original *process.Handle
	h.afterSpoolAppend = func(e contract.Event) {
		if e.Kind != contract.EventRunning {
			return
		}
		h.mu.Lock()
		original = h.activeBySeg[g.SegmentID]
		h.mu.Unlock()
		if original == nil {
			t.Error("lost original handle before Running")
			return
		}
		if beforeRelease != nil {
			beforeRelease()
		}
		if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
			t.Error(err)
		}
	}
	return &original
}

func assertDiagnosticOriginalExit(t *testing.T, original **process.Handle) {
	t.Helper()
	if *original == nil {
		t.Fatal("no original handle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := (*original).Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := (*original).ConfirmTreeExited(); err != nil {
		t.Fatal(err)
	}
	if !(*original).Identity().BirthKnown {
		t.Fatal("missing original birth")
	}
}

func readNativeDiagnosticPage(t *testing.T, h *Host, g contract.LaunchCommand) diagnostics.Page {
	t.Helper()
	scope, _, err := diagnosticLaunchScope(h.producerID, g)
	if err != nil {
		t.Fatal(err)
	}
	r, err := diagnostics.OpenReader(context.Background(), filepath.Join(h.spoolRoot, g.AttemptID, ".diagnostics"), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p, err := r.Read(context.Background(), diagnostics.PageCursor{}, diagnostics.RecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiagnosticExecuteLaunchCapturesBothStreamsAndPreservesDefault(t *testing.T) {
	for _, detailed := range []bool{false, true} {
		t.Run(fmt.Sprint(detailed), func(t *testing.T) {
			h, g, inv, release, _ := nativeDiagnosticLaunch(t, "emit")
			if !detailed {
				g.Diagnostics = nil
			}
			original := nativeDiagnosticObserver(t, h, g, release, nil)
			result, err := h.ExecuteLaunch(context.Background(), g, inv)
			if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
				t.Fatalf("launch=%+v %v", result, err)
			}
			assertDiagnosticOriginalExit(t, original)
			if !detailed {
				if _, err = os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, ".diagnostics")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("default captured raw data: %v", err)
				}
				return
			}
			p := readNativeDiagnosticPage(t, h, g)
			var out, stderr string
			for _, r := range p.Records {
				if r.Stream == diagnostics.Stdout {
					out += string(r.Data)
				} else {
					stderr += string(r.Data)
				}
			}
			if !p.Snapshot.Closed || p.Snapshot.Incomplete || out != "native stdout\n" || stderr != "native stderr\n" {
				t.Fatalf("captured %+v out=%q stderr=%q", p.Snapshot, out, stderr)
			}
		})
	}
}

func TestDiagnosticExecuteSummaryWriteFailureKeepsExitAndIsVisible(t *testing.T) {
	for _, detailed := range []bool{false, true} {
		t.Run(fmt.Sprint(detailed), func(t *testing.T) {
			h, g, inv, release, _ := nativeDiagnosticLaunch(t, "structured")
			if !detailed {
				g.Diagnostics = nil
			}
			original := nativeDiagnosticObserver(t, h, g, release, func() {
				if err := os.Mkdir(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "provider-diagnostics.json"), 0700); err != nil {
					t.Error(err)
				}
			})
			result, err := h.ExecuteLaunch(context.Background(), g, diagnosticStructuredInvocation{inv})
			if err == nil || result.Status != "result_ready" || result.ExitCode != 0 {
				t.Fatalf("summary failure hidden: %+v %v", result, err)
			}
			assertDiagnosticOriginalExit(t, original)
			events, err := h.Collect(context.Background(), store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
			if err != nil {
				t.Fatal(err)
			}
			results, exited := 0, 0
			for _, e := range events {
				switch e.Kind {
				case contract.EventResult:
					results++
				case contract.EventExited:
					exited++
					if e.ExitCode == nil || *e.ExitCode != 0 {
						t.Fatal("lost original exit")
					}
					if e.DiagnosticsFailure == nil || !e.DiagnosticsFailure.Summary || e.DiagnosticsFailure.Detailed != detailed {
						t.Fatal("summary failure not persisted on original exit")
					}
				case contract.EventFailed, contract.EventUnknown:
					t.Fatal("observational failure changed business outcome")
				}
			}
			if results != 1 || exited != 1 {
				t.Fatalf("terminal counts: results=%d exited=%d", results, exited)
			}
			body, err := os.ReadFile(result.ArtifactPath)
			if err != nil || string(body) != "public result" {
				t.Fatalf("business output changed: %q %v", body, err)
			}
			if !detailed {
				if _, err := os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, ".diagnostics")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("summary-only failure created raw namespace", err)
				}
				return
			}
			p := readNativeDiagnosticPage(t, h, g)
			if p.Snapshot.Closed && !p.Snapshot.Incomplete {
				t.Fatal("summary failure finalized complete raw capture")
			}
		})
	}
}

func TestDiagnosticExecuteCancellationCapturesTerminationTail(t *testing.T) {
	h, g, inv, release, ready := nativeDiagnosticLaunch(t, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := nativeDiagnosticObserver(t, h, g, release, nil)
	observe := h.afterSpoolAppend
	h.afterSpoolAppend = func(e contract.Event) {
		observe(e)
		if e.Kind != contract.EventRunning {
			return
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Error("child readiness absent")
				break
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
	}
	result, err := h.ExecuteLaunch(ctx, g, inv)
	if !errors.Is(err, context.Canceled) || result.Status != "interrupted" || result.ExitCode != 0 {
		t.Fatalf("cancel=%+v %v", result, err)
	}
	assertDiagnosticOriginalExit(t, original)
	p := readNativeDiagnosticPage(t, h, g)
	var out string
	for _, r := range p.Records {
		if r.Stream == diagnostics.Stdout {
			out += string(r.Data)
		}
	}
	if !p.Snapshot.Closed || p.Snapshot.Incomplete || out != "native stdout\ntermination tail\n" {
		t.Fatalf("premature capture close: %+v out=%q", p.Snapshot, out)
	}
	events, err := h.Collect(context.Background(), store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	stopped, exited := false, false
	for _, e := range events {
		if e.Kind == contract.EventStopped {
			stopped = e.StopReason == "caller_cancelled"
		}
		if e.Kind == contract.EventExited {
			exited = true
			if e.DiagnosticsFailure != nil {
				t.Fatal("ordinary cancellation became storage failure")
			}
		}
	}
	if !stopped || !exited {
		t.Fatal("lost original stop/exit")
	}
}

func TestDiagnosticExecuteStorageFailureStillDrainsOriginalProcess(t *testing.T) {
	h, g, inv, release, _ := nativeDiagnosticLaunch(t, "failure")
	scope, _, err := diagnosticLaunchScope(h.producerID, g)
	if err != nil {
		t.Fatal(err)
	}
	namespace, _ := scope.Hash()
	fault := filepath.Join(h.spoolRoot, g.AttemptID, ".diagnostics", namespace, "manifest.stage")
	original := nativeDiagnosticObserver(t, h, g, release, func() {
		if err := os.Mkdir(fault, 0700); err != nil {
			t.Error(err)
		}
	})
	result, err := h.ExecuteLaunch(context.Background(), g, inv)
	if !errors.Is(err, errDiagnosticsUnavailable) || result.Status != "interrupted" || result.ExitCode != 0 {
		t.Fatalf("storage=%+v %v", result, err)
	}
	assertDiagnosticOriginalExit(t, original)
	if !strings.Contains((*original).Output(), "termination tail") {
		t.Fatal("storage failure stopped original pipe drain")
	}
	events, err := h.Collect(context.Background(), store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	stopped, exited := false, 0
	id := (*original).Identity()
	for _, e := range events {
		if e.Kind == contract.EventStopped || e.Kind == contract.EventExited {
			if e.Process == nil || e.Process.PID != id.PID || e.Process.PGID != id.PGID || e.Process.Birth != id.Birth {
				t.Fatal("terminal detached from original process identity")
			}
		}
		if e.Kind == contract.EventStopped {
			stopped = e.StopReason == "diagnostics_unavailable"
		}
		if e.Kind == contract.EventExited {
			exited++
			if e.DiagnosticsFailure == nil || !e.DiagnosticsFailure.Detailed || e.DiagnosticsFailure.Summary {
				t.Fatal("storage failure missing original exit receipt")
			}
		}
	}
	if !stopped || exited != 1 {
		t.Fatalf("stop=%v exited=%d", stopped, exited)
	}
	// Remove only the owned, deliberately invalid empty directory. This is a
	// controlled filesystem failure, not physical ENOSPC or a disk timing claim.
	if err = os.Remove(fault); err != nil {
		t.Fatal(err)
	}
	p := readNativeDiagnosticPage(t, h, g)
	if p.Snapshot.Closed || !p.Snapshot.Incomplete {
		t.Fatal("failed capture became complete")
	}
}

func TestDiagnosticExecuteStartFailureAbortsWithoutExit(t *testing.T) {
	h, g, inv, _, _ := nativeDiagnosticLaunch(t, "emit")
	inv.args = []string{filepath.Join(h.spoolRoot, "absent-executable")}
	result, err := h.ExecuteLaunch(context.Background(), g, inv)
	if err == nil || result.Status != "unknown" {
		t.Fatalf("start failure=%+v %v", result, err)
	}
	p := readNativeDiagnosticPage(t, h, g)
	if p.Snapshot.Closed || !p.Snapshot.Incomplete || len(p.Records) != 0 {
		t.Fatal("failed Start marked raw complete")
	}
	events, err := h.Collect(context.Background(), store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == contract.EventSpawned || e.Kind == contract.EventExited {
			t.Fatal("Start failure invented process receipt")
		}
	}
}

func TestDiagnosticExecuteReturnedHandleErrorRetainsOriginalCleanup(t *testing.T) {
	h, g, inv, release, ready := nativeDiagnosticLaunch(t, "cancel")
	var original *process.Handle
	want := errors.New("controlled post-Start observation error")
	start := func(ctx context.Context, cmd process.Command) (*process.Handle, error) {
		p, err := process.Start(ctx, cmd)
		original = p
		if err != nil {
			return p, err
		}
		if err = os.WriteFile(release, []byte("release"), 0600); err != nil {
			return p, err
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err = os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return p, errors.New("fixture ready timeout")
			}
			time.Sleep(time.Millisecond)
		}
		return p, want
	}
	a := store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID}
	result, err := h.execute(context.Background(), a, g.RunID, g.TaskID, invocationCommand(inv), false, launchMetadata{commandID: g.CommandID, workRevision: g.WorkRevision, diagnosticsGrant: &g, startProcess: start})
	if !errors.Is(err, want) || result.Status != "unknown" {
		t.Fatalf("post-Start error changed evidence: %+v %v", result, err)
	}
	assertDiagnosticOriginalExit(t, &original)
	if original.ExitCode() != 0 || !strings.Contains(original.Output(), "termination tail") {
		t.Fatal("failed-start cleanup lost original drain")
	}
	p := readNativeDiagnosticPage(t, h, g)
	if p.Snapshot.Closed || !p.Snapshot.Incomplete {
		t.Fatal("unproved launch capture became complete")
	}
}
