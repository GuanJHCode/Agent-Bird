package gitfilter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type fpCancelReader struct {
	*bytes.Reader
	ctx     context.Context
	entered chan struct{}
}

func (r *fpCancelReader) Read(p []byte) (int, error) {
	if r.Len() != 0 {
		return r.Reader.Read(p)
	}
	close(r.entered)
	<-r.ctx.Done()
	return 0, io.EOF // What a cancelled/closed transport may return.
}

type fpCancelWriter struct {
	ctx     context.Context
	entered chan struct{}
}

func (w fpCancelWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.ctx.Done()
	return len(p), nil // Completion does not undo cancellation.
}

func TestRunFilterProcessCancelledIO(t *testing.T) {
	for _, kind := range []string{"request_boundary_eof", "completed_blocking_write"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			entered := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				if kind == "request_boundary_eof" {
					r := &fpCancelReader{Reader: bytes.NewReader([]byte(fpGreeting())), ctx: ctx, entered: entered}
					done <- RunFilterProcess(ctx, r, io.Discard, fpConsume)
				} else {
					w := filterProcessWire{ctx: ctx, output: fpCancelWriter{ctx: ctx, entered: entered}}
					done <- w.write([]byte("0000"))
				}
			}()
			select {
			case <-entered:
				cancel()
			case <-ctx.Done():
				t.Fatal("transport was not entered")
			}
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled IO returned %v; must not become a clean completion", err)
			}
		})
	}
}
