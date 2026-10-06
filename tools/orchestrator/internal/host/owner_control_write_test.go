package host

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func TestOwnerControlBlockedWriteHonorsCancellation(t *testing.T) {
	for _, kind := range []string{"cancel", "deadline", "write_timeout"} {
		t.Run(kind, func(t *testing.T) {
			writer, peer := ownerRuntimeControlPair(t)
			defer writer.Close()
			defer peer.Close()
			// Fill the real FD4-style socketpair. The peer deliberately never
			// reads, so the next framed write must wait for kernel capacity.
			if err := writer.SetWriteDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			written, err := writer.Write(bytes.Repeat([]byte("x"), 4*1024*1024))
			var timeout net.Error
			if written == 0 || !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("socket buffer did not fill: bytes=%d err=%v", written, err)
			}
			if err := writer.SetWriteDeadline(time.Time{}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			want := errors.New("test owner stop")
			if kind == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 50*time.Millisecond)
				defer stop()
				want = context.DeadlineExceeded
			}
			if kind == "write_timeout" {
				want = context.DeadlineExceeded
			}
			done := make(chan error, 1)
			go func() {
				done <- writeOwnerTransportFrame(ctx, writer, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindOwnerTransportControl, RequestID: "blocked", Payload: []byte(`{"version":1,"action":"connect"}`)})
			}()
			if kind == "cancel" {
				select {
				case err := <-done:
					t.Fatalf("write was not blocked before cancellation: %v", err)
				case <-time.After(50 * time.Millisecond):
				}
				cancel(want)
			}
			limit := 500 * time.Millisecond
			if kind == "write_timeout" {
				limit = 6 * time.Second
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("write lost cancellation cause: got=%v want=%v", err, want)
				}
				if kind == "write_timeout" && ctx.Err() != nil {
					t.Fatal("fallback timeout was actually a context cancellation")
				}
				_ = writer.SetWriteDeadline(time.Now().Add(30 * time.Millisecond))
				if _, err := writer.Write([]byte("next-frame")); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("failed framed stream remained writable: %v", err)
				}
			case <-time.After(limit):
				// Unblock and join the exact writer before failing the test.
				_ = peer.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("owned socket writer did not join after peer close")
				}
				t.Fatal("blocked owner control write ignored cancellation")
			}
		})
	}
}
