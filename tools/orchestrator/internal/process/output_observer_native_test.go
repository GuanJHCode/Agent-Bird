package process

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

type nativeObservedBytes struct {
	mu             sync.Mutex
	stdout, stderr []byte
}

func (o *nativeObservedBytes) observe(stream OutputStream, p []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if stream == OutputStdout {
		o.stdout = append(o.stdout, p...)
	} else if stream == OutputStderr {
		o.stderr = append(o.stderr, p...)
	}
}
func (o *nativeObservedBytes) snapshot() ([]byte, []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return bytes.Clone(o.stdout), bytes.Clone(o.stderr)
}

func TestNativeOutputObserverCapturesBothStreamsPastSummaryLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var observed nativeObservedBytes
	var downstream bytes.Buffer
	spec := duplexCommand(t, "stderr")
	spec.ObserveOutput = observed.observe
	spec.Stdout = &downstream
	h, err := Start(ctx, spec)
	if h != nil {
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := h.Stop(cleanup); err != nil {
				t.Error(err)
			}
			if err := h.Wait(cleanup); err != nil {
				t.Error(err)
			}
		}()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if h.ExitCode() != 0 || h.ConfirmTreeExited() != nil || !h.Identity().BirthKnown {
		t.Fatal("original process proof missing")
	}
	stdout, stderr := observed.snapshot()
	if string(stdout) != "stdout-exclusive-marker" || !bytes.Equal(stderr, bytes.Repeat([]byte("e"), 2<<20)) {
		t.Fatalf("observed stdout=%d stderr=%d", len(stdout), len(stderr))
	}
	if downstream.String() != string(stdout) {
		t.Fatal("observer changed original downstream")
	}
	if summary, truncated := h.OutputSnapshot(); len(summary) != 1<<20 || !truncated {
		t.Fatalf("summary bound changed %d %v", len(summary), truncated)
	}
}

func TestNativeDuplexObserverWaitDoesNotConsumeStdout(t *testing.T) {
	var observed nativeObservedBytes
	spec := duplexCommand(t, "stderr")
	spec.ObserveOutput = observed.observe
	h, stream, ctx := ownDuplex(t, spec)
	if err := h.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := observed.snapshot()
	if len(stdout) != 0 || !bytes.Equal(stderr, bytes.Repeat([]byte("e"), 2<<20)) {
		t.Fatalf("Wait consumed stdout or lost stderr: %d/%d", len(stdout), len(stderr))
	}
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "stdout-exclusive-marker" {
		t.Fatalf("original reader %q %v", data, err)
	}
	stdout, stderr = observed.snapshot()
	if !bytes.Equal(stdout, data) || len(stderr) != 2<<20 {
		t.Fatalf("observer duplicated or lost read: %d/%d", len(stdout), len(stderr))
	}
	if summary, truncated := h.OutputSnapshot(); !bytes.Equal([]byte(summary), bytes.Repeat([]byte("e"), 1<<20)) || !truncated {
		t.Fatal("duplex stdout entered summary")
	}
	if h.ExitCode() != 0 || h.ConfirmTreeExited() != nil {
		t.Fatal("original Wait/tree missing")
	}
}
