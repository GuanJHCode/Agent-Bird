//go:build darwin

package gitfilter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestBrokerStreamLoadBoundaries(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Run("success_revokes_retained_writer", func(t *testing.T) {
		b := liveBroker(t, ctx, f, brokerRequest(t, f, "copy"))
		var retained io.Writer
		got, err := b.LoadStream(ctx, 0, func(w io.Writer) error {
			retained = w
			_, e := w.Write([]byte("canonical"))
			if e != nil {
				return e
			}
			_, e = w.Write([]byte("-bytes"))
			return e
		})
		if err != nil || got.Bytes != int64(len("canonical-bytes")) || got.SHA256 == "" {
			t.Fatalf("load=%+v err=%v", got, err)
		}
		if _, err := retained.Write([]byte("late")); err == nil {
			t.Fatal("retained writer remained live")
		}
		var out bytes.Buffer
		if err := b.ReadSlot(ctx, 0, &out); err != nil || out.String() != "canonical-bytes" {
			t.Fatalf("slot=%q err=%v", out.String(), err)
		}
		if _, err := b.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("overflow_sticky_even_if_callback_ignores_it", func(t *testing.T) {
		b := liveBroker(t, ctx, f, brokerRequest(t, f, "copy"))
		_, err := b.LoadStream(ctx, 0, func(w io.Writer) error { _, _ = w.Write(bytes.Repeat([]byte("x"), int(MaximumBytes)+1)); return nil })
		if err == nil {
			t.Fatal("overflow succeeded")
		}
		if err := b.ReadSlot(ctx, 0, io.Discard); err == nil {
			t.Fatal("overflow slot readable")
		}
		if _, err := b.Cleanup(ctx); err != nil {
			t.Fatalf("known overflow cleanup: %v", err)
		}
	})
	t.Run("partial_known_error_finalizes_terminal_and_cleans", func(t *testing.T) {
		b := liveBroker(t, ctx, f, brokerRequest(t, f, "copy"))
		known := errors.New("confirmed git exit")
		_, err := b.LoadStream(ctx, 0, func(w io.Writer) error { _, _ = w.Write([]byte("partial")); return known })
		if !errors.Is(err, known) || b.State().Unknown {
			t.Fatalf("err=%v state=%+v", err, b.State())
		}
		if err := b.ReadSlot(ctx, 0, io.Discard); err == nil {
			t.Fatal("partial slot readable")
		}
		if _, err := b.Cleanup(ctx); err != nil {
			t.Fatalf("known partial cleanup: %v", err)
		}
	})
	t.Run("partial_process_unknown_retains_resources", func(t *testing.T) {
		b := liveBroker(t, ctx, f, brokerRequest(t, f, "copy"))
		_, err := b.LoadStream(ctx, 0, func(w io.Writer) error { _, _ = w.Write([]byte("partial")); return process.ErrProcessTreeUnknown })
		if !errors.Is(err, process.ErrProcessTreeUnknown) || !b.State().Unknown {
			t.Fatalf("err=%v state=%+v", err, b.State())
		}
		if r, e := b.Cleanup(ctx); e == nil || r != nil {
			t.Fatal("unknown load cleaned")
		}
		f.retain = true
	})
	t.Run("panic_marks_unknown_before_repanic", func(t *testing.T) {
		b := liveBroker(t, ctx, f, brokerRequest(t, f, "copy"))
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("panic swallowed")
				}
			}()
			_, _ = b.LoadStream(ctx, 0, func(io.Writer) error { panic("producer") })
		}()
		if !b.State().Unknown {
			t.Fatal("panic broker reusable")
		}
		if r, e := b.Cleanup(ctx); e == nil || r != nil {
			t.Fatal("panic load cleaned")
		}
		f.retain = true
	})
}
