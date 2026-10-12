//go:build darwin || linux

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func diagnosticGrant() contract.LaunchCommand {
	p := contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"}
	body, _ := p.CanonicalJSON()
	return contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", HostID: "producer", Diagnostics: &contract.DiagnosticsGrant{Policy: p, PolicySHA256: hashBytes(body), ReservationID: "artifact-reservation", SegmentOrdinal: 1}}
}

func diagnosticHost(t *testing.T) *Host {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(root, "producer")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDiagnosticDriverPrivacyScopeAndOriginalBytes(t *testing.T) {
	h, g := diagnosticHost(t), diagnosticGrant()
	nilGrant := g
	nilGrant.Diagnostics = nil
	d, err := h.openDetailedDiagnostics(context.Background(), nilGrant, nil)
	if err != nil || d != nil {
		t.Fatalf("default: %v %v", d, err)
	}
	entries, err := os.ReadDir(h.spoolRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("default wrote raw namespace: %v %v", entries, err)
	}
	d, err = h.openDetailedDiagnostics(context.Background(), g, func(error) { t.Error("unexpected failure") })
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("original stdout")
	d.Observe(process.OutputStdout, body)
	copy(body, "changed payload")
	d.Observe(process.OutputStderr, []byte("original stderr"))
	if err = d.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
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
	if err != nil || !p.Snapshot.Closed || p.Snapshot.Incomplete || len(p.Records) != 2 {
		t.Fatalf("page: %#v %v", p, err)
	}
	if string(p.Records[0].Data) != "original stdout" || string(p.Records[1].Data) != "original stderr" || p.Records[0].SegmentID != "00000000000000000001:segment" {
		t.Fatalf("records: %#v", p.Records)
	}
	spools, err := h.spoolEntries()
	if err != nil || len(spools) != 0 {
		t.Fatalf("namespace entered publisher: %d %v", len(spools), err)
	}
}

func TestDiagnosticDriverRejectsAuthorityAndPathsBeforeCapture(t *testing.T) {
	for _, name := range []string{"producer", "policy", "attempt_path", "segment_length", "summary", "symlink", "permissions", "child_symlink", "child_permissions"} {
		t.Run(name, func(t *testing.T) {
			h, g := diagnosticHost(t), diagnosticGrant()
			switch name {
			case "producer":
				g.HostID = "foreign"
			case "policy":
				g.Diagnostics.PolicySHA256 = strings.Repeat("0", 64)
			case "attempt_path":
				g.AttemptID = "../foreign"
			case "segment_length":
				g.SegmentID = strings.Repeat("x", 108)
			case "summary":
				g.Diagnostics.Policy.Mode = "summary"
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(h.spoolRoot, g.AttemptID)); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Mkdir(filepath.Join(h.spoolRoot, g.AttemptID), 0755); err != nil {
					t.Fatal(err)
				}
			case "child_symlink", "child_permissions":
				attempt := filepath.Join(h.spoolRoot, g.AttemptID)
				if err := os.Mkdir(attempt, 0700); err != nil {
					t.Fatal(err)
				}
				var err error
				if name == "child_symlink" {
					err = os.Symlink(t.TempDir(), filepath.Join(attempt, ".diagnostics"))
				} else {
					err = os.Mkdir(filepath.Join(attempt, ".diagnostics"), 0755)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if d, err := h.openDetailedDiagnostics(context.Background(), g, func(error) {}); err == nil || d != nil {
				t.Fatalf("accepted %s: %v %v", name, d, err)
			}
		})
	}
}

type diagnosticPreacceptWriter struct{ *diagnostics.Writer }

func (w diagnosticPreacceptWriter) Append(_ context.Context, record diagnostics.Record) error {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return w.Writer.Append(ctx, record)
}

func TestDiagnosticDriverActualPreacceptAbortRemainsIncomplete(t *testing.T) {
	h, g := diagnosticHost(t), diagnosticGrant()
	scope, segment, err := diagnosticLaunchScope(h.producerID, g)
	if err != nil {
		t.Fatal(err)
	}
	w, err := diagnostics.OpenWriter(context.Background(), h.spoolRoot, scope)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := diagnostics.NewCapture(segment)
	failed := make(chan error, 1)
	d := startDiagnosticDriver(c, diagnosticPreacceptWriter{w}, func(err error) { failed <- err })
	d.Observe(process.OutputStdout, []byte("leased but not accepted"))
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("owner not woken")
	}
	if err = d.Finish(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("preaccept cause lost: %v", err)
	}
	r, err := diagnostics.OpenReader(context.Background(), h.spoolRoot, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p, err := r.Read(context.Background(), diagnostics.PageCursor{}, diagnostics.RecordBytes)
	if err != nil || p.Snapshot.Closed || !p.Snapshot.Incomplete || len(p.Records) != 0 {
		t.Fatalf("false complete: %+v %v", p, err)
	}
	reopened, err := diagnostics.OpenWriter(context.Background(), h.spoolRoot, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err = r.Read(context.Background(), p.Next, diagnostics.RecordBytes)
	if err != nil || !p.Snapshot.Closed || !p.Snapshot.Incomplete {
		t.Fatalf("reopen lost incomplete: %+v %v", p, err)
	}
}

type diagnosticTestWriter struct {
	mu                      sync.Mutex
	records                 []diagnostics.Record
	entered                 chan struct{}
	release                 chan struct{}
	first                   sync.Once
	err                     error
	flushErr, closeErr      error
	flushed                 chan struct{}
	flushOnce               sync.Once
	ignoreContext           bool
	closes, aborts, flushes int
}

func (w *diagnosticTestWriter) Append(ctx context.Context, r diagnostics.Record) error {
	w.first.Do(func() {
		if w.entered != nil {
			close(w.entered)
		}
		if w.release != nil {
			if w.ignoreContext {
				<-w.release
			} else {
				select {
				case <-w.release:
				case <-ctx.Done():
				}
			}
		}
	})
	if w.err != nil {
		return w.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	r.Data = append([]byte(nil), r.Data...)
	w.records = append(w.records, r)
	return nil
}
func (w *diagnosticTestWriter) Flush(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushes++
	if w.flushed != nil {
		w.flushOnce.Do(func() { close(w.flushed) })
	}
	return w.flushErr
}
func (w *diagnosticTestWriter) Close(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closes++
	return w.closeErr
}
func (w *diagnosticTestWriter) Abort(_ context.Context, cause error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.aborts++
	return cause
}

func TestDiagnosticDriverBlockedStorageBoundsQueueAndDrainsFinalLoss(t *testing.T) {
	c, _ := diagnostics.NewCapture("segment")
	w := &diagnosticTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
	d := startDiagnosticDriver(c, w, func(error) { t.Error("unexpected failure") })
	d.Observe(process.OutputStdout, []byte("first"))
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("append not entered")
	}
	// The leased first slot leaves seven queue slots, irrespective of input size.
	body := []byte(strings.Repeat("x", diagnostics.RecordBytes*12))
	d.Observe(process.OutputStderr, body)
	close(w.release)
	if err := d.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.records) != 9 || w.closes != 1 || w.aborts != 0 {
		t.Fatalf("records=%d closes=%d aborts=%d", len(w.records), w.closes, w.aborts)
	}
	last := w.records[len(w.records)-1]
	if len(last.Data) != 0 || last.Stream != diagnostics.Stderr || last.LostBefore != 5*diagnostics.RecordBytes || last.Offset != uint64(len(body)) {
		t.Fatalf("final loss: %#v", last)
	}
}

func TestDiagnosticDriverPreacceptFailureAbortsWithoutClosing(t *testing.T) {
	c, _ := diagnostics.NewCapture("segment")
	want := errors.New("controlled preaccept failure")
	w := &diagnosticTestWriter{err: want}
	failed := make(chan error, 1)
	d := startDiagnosticDriver(c, w, func(err error) { failed <- err })
	d.Observe(process.OutputStdout, []byte("lost"))
	select {
	case err := <-failed:
		if !errors.Is(err, want) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("failure did not wake owner")
	}
	d.Observe(process.OutputStdout, []byte("pipe remains drainable"))
	if err := d.Finish(context.Background()); !errors.Is(err, want) {
		t.Fatalf("failure lost: %v", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closes != 0 || w.aborts != 1 || len(w.records) != 0 {
		t.Fatalf("closes=%d aborts=%d records=%d", w.closes, w.aborts, len(w.records))
	}
}

func TestDiagnosticDriverPeriodicFlushAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			c, _ := diagnostics.NewCapture("segment")
			want := errors.New("flush failure")
			w := &diagnosticTestWriter{flushed: make(chan struct{})}
			if failed {
				w.flushErr = want
			}
			failure := make(chan error, 1)
			d := startDiagnosticDriver(c, w, func(err error) { failure <- err })
			d.Observe(process.OutputStdout, []byte("partial chunk"))
			select {
			case <-w.flushed:
			case <-time.After(3 * time.Second):
				t.Fatal("partial flush starved")
			}
			err := d.Finish(context.Background())
			if failed {
				if !errors.Is(err, want) {
					t.Fatalf("lost flush failure: %v", err)
				}
				select {
				case <-failure:
				default:
					t.Fatal("no owner notification")
				}
			} else if err != nil {
				t.Fatalf("idle tick became failure: %v", err)
			}
		})
	}
}

func TestDiagnosticDriverFinishTimeoutKeepsOriginalJoin(t *testing.T) {
	c, _ := diagnostics.NewCapture("segment")
	w := &diagnosticTestWriter{entered: make(chan struct{}), release: make(chan struct{}), ignoreContext: true}
	d := startDiagnosticDriver(c, w, func(error) { t.Error("unexpected failure") })
	d.Observe(process.OutputStdout, []byte("owned in-flight lease"))
	<-w.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Finish(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-d.done:
		t.Fatal("deadline fabricated consumer join")
	default:
	}
	// Disabled observation must remain nonblocking while the Writer is blocked.
	d.Observe(process.OutputStderr, []byte("late ignored bytes"))
	close(w.release)
	if err := d.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Abort(context.Background(), errors.New("late defer")); err != nil {
		t.Fatalf("second terminal call revoked chosen finish: %v", err)
	}
}

func TestDiagnosticDriverAbortAndCloseFailureNotifyOnce(t *testing.T) {
	for _, closeFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(closeFailure), func(t *testing.T) {
			c, _ := diagnostics.NewCapture("segment")
			want := errors.New("requested failure")
			w := &diagnosticTestWriter{}
			if closeFailure {
				w.closeErr = want
			}
			failed := make(chan error, 2)
			d := startDiagnosticDriver(c, w, func(err error) { failed <- err })
			var err error
			if closeFailure {
				err = d.Finish(context.Background())
			} else {
				err = d.Abort(context.Background(), want)
			}
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
			if len(failed) != 1 {
				t.Fatalf("owner callbacks=%d", len(failed))
			}
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.aborts != 1 || w.closes != map[bool]int{false: 0, true: 1}[closeFailure] {
				t.Fatalf("closes=%d aborts=%d", w.closes, w.aborts)
			}
		})
	}
}

func TestDiagnosticDriverConcurrentOffersAndFinish(t *testing.T) {
	c, _ := diagnostics.NewCapture("segment")
	w := &diagnosticTestWriter{entered: make(chan struct{}), release: make(chan struct{})}
	d := startDiagnosticDriver(c, w, func(error) { t.Error("close raced accepted Offer") })
	// Establish a real accepted, leased record before racing the remaining
	// offers against Finish; the test cannot pass with all input ignored.
	d.Observe(process.OutputStdout, []byte("first"))
	<-w.entered
	var writers sync.WaitGroup
	ready := make(chan struct{})
	for i := 0; i < 8; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-ready
			for j := 0; j < 100; j++ {
				d.Observe(process.OutputStdout, []byte("data"))
			}
		}()
	}
	close(ready)
	close(w.release)
	if err := d.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	writers.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.records) == 0 || string(w.records[0].Data) != "first" {
		t.Fatal("test did not preserve its original accepted lease")
	}
}

func TestDiagnosticDriverResumeUsesOneNamespace(t *testing.T) {
	h, g := diagnosticHost(t), diagnosticGrant()
	for ordinal := 1; ordinal <= 2; ordinal++ {
		g.Diagnostics.SegmentOrdinal = ordinal
		g.SegmentID = fmt.Sprintf("segment-%d", ordinal)
		d, err := h.openDetailedDiagnostics(context.Background(), g, func(error) { t.Error("resume failed") })
		if err != nil {
			t.Fatal(err)
		}
		d.Observe(process.OutputStdout, []byte(fmt.Sprint(ordinal)))
		if err = d.Finish(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(h.spoolRoot, g.AttemptID, ".diagnostics")
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("namespace forked: %v %v", entries, err)
	}
	scope, _, _ := diagnosticLaunchScope(h.producerID, g)
	r, err := diagnostics.OpenReader(context.Background(), root, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p, err := r.Read(context.Background(), diagnostics.PageCursor{}, diagnostics.RecordBytes)
	if err != nil || len(p.Records) != 2 || p.Snapshot.Incomplete || p.Records[0].SegmentID != "00000000000000000001:segment-1" || p.Records[1].SegmentID != "00000000000000000002:segment-2" {
		t.Fatalf("resume page: %+v %v", p, err)
	}
}
