package gitfilter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gateMemoryConn struct {
	in                    *strings.Reader
	mu                    sync.Mutex
	out                   bytes.Buffer
	reads, writes, closes atomic.Int64
	closeStarted          chan struct{}
	closeRelease          <-chan struct{}
	closeErr              error
	onWrite               func([]byte)
}

func gateMemory(input string) *gateMemoryConn {
	return &gateMemoryConn{in: strings.NewReader(input), closeStarted: make(chan struct{}, 1)}
}
func (c *gateMemoryConn) Read(p []byte) (int, error) { c.reads.Add(1); return c.in.Read(p) }
func (c *gateMemoryConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	c.mu.Lock()
	n, e := c.out.Write(p)
	c.mu.Unlock()
	if c.onWrite != nil {
		c.onWrite(p)
	}
	return n, e
}
func (c *gateMemoryConn) Close() error {
	c.closes.Add(1)
	select {
	case c.closeStarted <- struct{}{}:
	default:
	}
	if c.closeRelease != nil {
		<-c.closeRelease
	}
	return c.closeErr
}
func (c *gateMemoryConn) LocalAddr() net.Addr              { return nil }
func (c *gateMemoryConn) RemoteAddr() net.Addr             { return nil }
func (c *gateMemoryConn) SetDeadline(time.Time) error      { return nil }
func (c *gateMemoryConn) SetReadDeadline(time.Time) error  { return nil }
func (c *gateMemoryConn) SetWriteDeadline(time.Time) error { return nil }
func (c *gateMemoryConn) output() string                   { c.mu.Lock(); defer c.mu.Unlock(); return c.out.String() }
func gateWire(body string, request bool) string {
	s := gatePacket("git-filter-client\n") + gatePacket("version=2\n") + "0000" + gatePacket("capability=smudge\n") + "0000"
	if request {
		s += gatePacket("command=smudge\n") + gatePacket("pathname=a.txt\n") + "0000" + gatePacket(body) + "0000"
	}
	return s
}

type gateCountingPeer struct {
	count int
	check func(int) error
}

func (p *gateCountingPeer) Verify(context.Context) error {
	p.count++
	if p.check != nil {
		return p.check(p.count)
	}
	return nil
}
func gateNew(t *testing.T) (*OriginalGitGate, *ExactTransformSession, *exactTestBroker, context.Context) {
	t.Helper()
	b, m, p, j, ctx := exactTestFixture(t)
	s := openExactTest(t, b, m, p, j)
	g, e := NewOriginalGitGate(ctx, s, exactHash([]byte("command")))
	if e != nil {
		t.Fatal(e)
	}
	return g, s, b, ctx
}
func gateAwait(t *testing.T, check func() bool) {
	t.Helper()
	end := time.Now().Add(time.Second)
	for !check() {
		if time.Now().After(end) {
			t.Fatal("bounded observation did not arrive")
		}
		runtime.Gosched()
	}
}
func gateCloseContext(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return c
}
func gateResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case e := <-done:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not drain")
		return nil
	}
}

func TestOriginalGitGateBoundaries(t *testing.T) {
	t.Run("pending_has_zero_reads_and_writes_and_known_drain", func(t *testing.T) {
		g, s, b, _ := gateNew(t)
		c := gateMemory(gateWire("B", true))
		done := make(chan error, 1)
		go func() { done <- g.Serve(c, 17) }()
		gateAwait(t, func() bool { return g.State().Serving })
		if c.reads.Load() != 0 || c.writes.Load() != 0 {
			t.Fatal("codec I/O before activation")
		}
		if e := g.Close(gateCloseContext(t)); e != nil {
			t.Fatal(e)
		}
		if gateResult(t, done) == nil {
			t.Fatal("pending stream accepted")
		}
		st := g.State()
		if !st.Drained || !st.Revoked || st.Unknown || st.Calls != 0 || c.closes.Load() != 1 || b.loads != 0 || b.closes != 0 || s.State().Closed {
			t.Fatalf("state=%+v closes=%d borrowed=%+v", st, c.closes.Load(), s.State())
		}
	})
	t.Run("no_serve_owner_cancellation_and_closed_activation", func(t *testing.T) {
		g, _, _, ctx := gateNew(t)
		owner, cancel := context.WithCancel(ctx)
		g2, e := NewOriginalGitGate(owner, g.session, g.digest)
		if e != nil {
			t.Fatal(e)
		}
		cancel()
		gateAwait(t, func() bool { return g2.State().Drained })
		if e = g2.activateWith(ctx, gateTestOwner{peer: gateTestPeer{}}, g2.digest); e == nil {
			t.Fatal("expired owner activated")
		}
		if e = g2.Close(gateCloseContext(t)); e != nil {
			t.Fatal(e)
		}
		if e = g.Close(gateCloseContext(t)); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("peer_first_pre_and_post_guards", func(t *testing.T) {
		for _, failAt := range []int{1, 2, 3} {
			t.Run(string(rune('0'+failAt)), func(t *testing.T) {
				g, _, b, ctx := gateNew(t)
				c := gateMemory(gateWire("B", true))
				peer := &gateCountingPeer{check: func(n int) error {
					if n == failAt {
						return errors.New("peer changed")
					}
					return nil
				}}
				if e := g.activateWith(ctx, gateTestOwner{peer: peer}, g.digest); e != nil {
					t.Fatal(e)
				}
				if e := g.Serve(c, 17); e == nil {
					t.Fatal("peer drift accepted")
				}
				if peer.count != failAt || !g.State().Unknown || strings.Contains(c.output(), "status=success") {
					t.Fatalf("verify=%d state=%+v", peer.count, g.State())
				}
				if failAt == 1 && (c.reads.Load() != 0 || c.writes.Load() != 0) {
					t.Fatal("first peer check followed codec I/O")
				}
				want := 0
				if failAt == 3 {
					want = 1
				}
				if b.transforms != want {
					t.Fatalf("transforms=%d want=%d", b.transforms, want)
				}
				if e := g.Close(gateCloseContext(t)); e == nil {
					t.Fatal("Unknown Close returned success")
				}
			})
		}
	})
	t.Run("session_closed_in_post_peer_guard_cannot_publish", func(t *testing.T) {
		g, s, _, ctx := gateNew(t)
		c := gateMemory(gateWire("B", true))
		fired := false
		peer := &gateCountingPeer{check: func(n int) error {
			if n == 3 {
				fired = true
				return s.Close()
			}
			return nil
		}}
		if e := g.activateWith(ctx, gateTestOwner{peer: peer}, g.digest); e != nil {
			t.Fatal(e)
		}
		e := g.Serve(c, 17)
		if !fired || e == nil || strings.Contains(c.output(), "status=success") || g.State().ResponsesCommitted != 0 {
			t.Fatalf("fired=%v err=%v state=%+v", fired, e, g.State())
		}
	})
	t.Run("close_before_publication", func(t *testing.T) {
		g, _, _, ctx := gateNew(t)
		c := gateMemory(gateWire("B", true))
		fired := false
		peer := &gateCountingPeer{check: func(n int) error {
			if n == 3 {
				fired = true
				g.revoke()
			}
			return nil
		}}
		if e := g.activateWith(ctx, gateTestOwner{peer: peer}, g.digest); e != nil {
			t.Fatal(e)
		}
		e := g.Serve(c, 17)
		if !fired || e == nil || g.State().ResponsesCommitted != 0 || strings.Contains(c.output(), "status=success") {
			t.Fatalf("fired=%v err=%v state=%+v", fired, e, g.State())
		}
		if e = g.Close(gateCloseContext(t)); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("publication_before_close_is_not_retracted", func(t *testing.T) {
		g, _, _, ctx := gateNew(t)
		c := gateMemory(gateWire("B", true))
		fired := false
		c.onWrite = func(p []byte) {
			if bytes.Contains(p, []byte("status=success")) {
				fired = true
				g.revoke()
			}
		}
		if e := g.activateWith(ctx, gateTestOwner{peer: gateTestPeer{}}, g.digest); e != nil {
			t.Fatal(e)
		}
		if e := g.Serve(c, 17); e == nil {
			t.Fatal("cancelled response write returned success")
		}
		st := g.State()
		if !fired || st.Calls != 1 || st.ResponsesCommitted != 1 {
			t.Fatalf("fired=%v state=%+v", fired, st)
		}
		if e := g.Close(gateCloseContext(t)); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("known_session_rejection_counts_attempt_without_publication", func(t *testing.T) {
		g, s, b, ctx := gateNew(t)
		c := gateMemory(gateWire("wrong", true))
		if e := g.activateWith(ctx, gateTestOwner{peer: gateTestPeer{}}, g.digest); e != nil {
			t.Fatal(e)
		}
		if e := g.Serve(c, 17); e == nil {
			t.Fatal("wrong input accepted")
		}
		st := g.State()
		if !st.Failed || st.Unknown || st.Calls != 1 || st.ResponsesCommitted != 0 || b.transforms != 0 || s.State().Unknown || strings.Contains(c.output(), "status=success") {
			t.Fatalf("state=%+v session=%+v", st, s.State())
		}
		if e := g.Close(gateCloseContext(t)); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("close_error_is_sticky_unknown", func(t *testing.T) {
		g, _, _, ctx := gateNew(t)
		c := gateMemory(gateWire("", false))
		c.closeErr = errors.New("close proof unavailable")
		if e := g.activateWith(ctx, gateTestOwner{peer: gateTestPeer{}}, g.digest); e != nil {
			t.Fatal(e)
		}
		if e := g.Serve(c, 17); e == nil {
			t.Fatal("close error hidden")
		}
		if !g.State().Unknown || g.State().Drained || c.closes.Load() != 1 {
			t.Fatalf("state=%+v", g.State())
		}
		if e := g.Close(gateCloseContext(t)); e == nil {
			t.Fatal("unknown close succeeded")
		}
	})
	t.Run("blocked_close_never_becomes_early_drained", func(t *testing.T) {
		g, _, _, ctx := gateNew(t)
		c := gateMemory(gateWire("", false))
		release := make(chan struct{})
		c.closeRelease = release
		if e := g.activateWith(ctx, gateTestOwner{peer: gateTestPeer{}}, g.digest); e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { done <- g.Serve(c, 17) }()
		<-c.closeStarted
		if g.State().Drained {
			t.Error("drained before endpoint Close")
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if e := g.Close(closeCtx); e == nil {
			t.Error("unproven close succeeded")
		}
		close(release)
		if e := gateResult(t, done); e == nil {
			t.Error("unknown Serve succeeded")
		}
		if !g.State().Unknown {
			t.Error("timeout Unknown not sticky")
		}
		if e := g.Close(gateCloseContext(t)); e == nil {
			t.Error("late completion erased Unknown")
		}
	})
	t.Run("duplicate_serve_revokes_before_rejected_close", func(t *testing.T) {
		g, _, _, _ := gateNew(t)
		a, b := gateMemory(""), gateMemory("")
		release := make(chan struct{})
		b.closeRelease = release
		first, second := make(chan error, 1), make(chan error, 1)
		go func() { first <- g.Serve(a, 17) }()
		gateAwait(t, func() bool { return g.State().Serving })
		go func() { second <- g.Serve(b, 18) }()
		<-b.closeStarted
		if !g.State().Revoked {
			t.Error("rejected Close blocked original revocation")
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if e := g.Close(closeCtx); e == nil {
			t.Error("duplicate endpoint close not included in drain")
		}
		close(release)
		if gateResult(t, first) == nil || gateResult(t, second) == nil {
			t.Error("duplicate serve accepted")
		}
		if a.reads.Load()+b.reads.Load()+a.writes.Load()+b.writes.Load() != 0 || a.closes.Load() != 1 || b.closes.Load() != 1 || !g.State().Unknown {
			t.Fatalf("I/O or drain mismatch state=%+v", g.State())
		}
	})
	t.Run("concurrent_activation_permanently_rejects", func(t *testing.T) {
		g, _, _, ctx := gateNew(t)
		start := make(chan struct{})
		results := make(chan error, 2)
		for range 2 {
			go func() { <-start; results <- g.activateWith(ctx, gateTestOwner{peer: gateTestPeer{}}, g.digest) }()
		}
		close(start)
		a, b := <-results, <-results
		if a == nil && b == nil || !g.State().Revoked || !g.State().Failed {
			t.Fatalf("results=%v,%v state=%+v", a, b, g.State())
		}
		if e := g.Close(gateCloseContext(t)); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("nil_zero_copy_and_typed_nil", func(t *testing.T) {
		g, s, _, ctx := gateNew(t)
		copy := reflect.New(reflect.TypeOf(*g)).Interface().(*OriginalGitGate)
		reflect.ValueOf(copy).Elem().Set(reflect.ValueOf(g).Elem())
		for _, bad := range []*OriginalGitGate{nil, new(OriginalGitGate), copy} {
			if e := bad.activateWith(ctx, gateTestOwner{peer: gateTestPeer{}}, g.digest); e == nil {
				t.Error("nonoriginal activate")
			}
			if e := bad.Close(ctx); e == nil {
				t.Error("nonoriginal Close")
			}
		}
		if g.State().Revoked || s.State().Closed {
			t.Fatal("copy changed original")
		}
		var typedNil *gateMemoryConn
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("typed nil panic: %v", r)
				}
			}()
			if e := g.Serve(typedNil, 17); e == nil {
				t.Error("typed nil accepted")
			}
		}()
		if e := g.Close(gateCloseContext(t)); e != nil {
			t.Fatal(e)
		}
	})
}

func TestOriginalGitGateConstructorOwner(t *testing.T) {
	for _, kind := range []string{"failed", "closed", "removed", "binding", "unknown", "session_copy"} {
		t.Run(kind, func(t *testing.T) {
			b, m, p, j, ctx := exactTestFixture(t)
			s := openExactTest(t, b, m, p, j)
			switch kind {
			case "failed":
				b.failed = true
			case "closed":
				b.closed = true
			case "removed":
				b.removed = true
			case "binding":
				b.binding.ScopeSHA256 = exactHash([]byte("foreign"))
			case "unknown":
				b.state.Unknown = true
			case "session_copy":
				cp := reflect.New(reflect.TypeOf(*s)).Interface().(*ExactTransformSession)
				reflect.ValueOf(cp).Elem().Set(reflect.ValueOf(s).Elem())
				s = cp
			}
			g, e := NewOriginalGitGate(ctx, s, exactHash([]byte("command")))
			if e == nil || g != nil || b.loads != 0 || b.transforms != 0 || b.closes != 0 {
				t.Fatalf("accepted unusable owner: %s %v", kind, e)
			}
		})
	}
}

func TestExactSessionOriginalOwnership(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "closed"}[closed], func(t *testing.T) {
			b, m, p, j, ctx := exactTestFixture(t)
			s := openExactTest(t, b, m, p, j)
			copy := reflect.New(reflect.TypeOf(*s)).Interface().(*ExactTransformSession)
			reflect.ValueOf(copy).Elem().Set(reflect.ValueOf(s).Elem())
			if closed {
				if e := s.Close(); e != nil {
					t.Fatal(e)
				}
			}
			if copy.State().Closed == false || copy.State().Failed == false {
				t.Error("copy appears usable")
			}
			if out, e := copy.Exchange(ctx, 1, "a.txt", "smudge", exactProduce("B")); e == nil || out != nil {
				t.Error("copy Exchange accepted")
			}
			if e := copy.Close(); e == nil {
				t.Error("copy Close accepted")
			}
			if b.loads != 0 || b.transforms != 0 || b.closes != 0 || b.cleanups != 0 || len(j.records) != 0 || s.State().Closed != closed || s.State().Failed {
				t.Fatalf("copy mutated original: state=%+v loads=%d", s.State(), b.loads)
			}
			if !closed {
				if out, e := s.Exchange(ctx, 1, "a.txt", "smudge", exactProduce("B")); e != nil || !bytes.Equal(out, []byte{0x18}) {
					t.Fatalf("original lost authority: %x %v", out, e)
				}
			}
		})
	}
	var s *ExactTransformSession
	if _, e := s.Exchange(context.Background(), 1, "a.txt", "smudge", func(io.Writer) error { return nil }); e == nil {
		t.Fatal("nil Exchange")
	}
}
