//go:build darwin

package gitfilter

import (
	"context"
	"net"
	"os"
	"reflect"
	"testing"
	"time"
)

func transportFixture(t *testing.T) (*carrierTransport, context.Context) {
	t.Helper()
	ctx := carrierTestContext(t)
	o, err := newCarrierSocket(ctx, carrierTestSegment(t), carrierTestBinding(), &carrierSocketTestJournal{})
	if err != nil {
		t.Fatal(err)
	}
	g, _, _, _ := gateNew(t)
	tr, err := newCarrierTransport(ctx, o, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tr.drain(c)
		_ = o.release(c)
		if o.state.directory != "" {
			_ = os.Remove(o.state.socket)
			_ = os.Remove(o.state.directory)
		}
	})
	return tr, ctx
}

func TestCarrierTransportSecondConnectionRevokesPending(t *testing.T) {
	tr, ctx := transportFixture(t)
	done := make(chan error, 1)
	go func() { done <- tr.serve() }()
	c, err := net.Dial("unix", tr.socket.state.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	gateAwait(t, func() bool { return tr.gate.State().Serving })
	c2, err := net.Dial("unix", tr.socket.state.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if gateResult(t, done) == nil {
		t.Fatal("second client accepted")
	}
	if err = tr.drain(ctx); err != nil {
		t.Fatal(err)
	}
	if !tr.gate.State().Revoked || tr.gate.State().Calls != 0 || !tr.gate.State().Drained {
		t.Fatalf("gate=%+v", tr.gate.State())
	}
	if err = tr.removeAfterOriginalGit(ctx, nil); err == nil {
		t.Fatal("removed without original Wait/tree")
	}
}

func TestCarrierTransportCopyAndOwnerError(t *testing.T) {
	tr, ctx := transportFixture(t)
	if duplicate, err := newCarrierTransport(ctx, tr.socket, tr.gate); err == nil || duplicate != nil {
		t.Fatal("B1 borrowed by second transport")
	}
	cp := reflect.New(reflect.TypeOf(tr).Elem()).Interface().(*carrierTransport)
	reflect.ValueOf(cp).Elem().Set(reflect.ValueOf(tr).Elem())
	if cp.drain(ctx) == nil || cp.serve() == nil {
		t.Fatal("copied transport accepted")
	}
	if err := tr.activate(ctx, nil, tr.gate.digest); err == nil {
		t.Fatal("nil original Git activated")
	}
	if tr.gate.State().Activated {
		t.Fatal("invalid activation")
	}
	o, err := newCarrierSocket(ctx, carrierTestSegment(t), carrierTestBinding(), &carrierSocketTestJournal{hook: func(carrierSocketRecord) error { return os.ErrPermission }})
	if err == nil || o == nil {
		t.Fatal("missing original error owner")
	}
	bad, e := newCarrierTransport(ctx, o, nil)
	if e == nil || bad == nil {
		t.Fatal("discarded original B1 owner")
	}
	if !o.state.released || !o.state.unknown || o.state.parent != nil || o.state.segment != nil {
		t.Fatal("errored owner descriptors retained")
	}
}
