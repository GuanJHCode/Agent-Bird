package coordinator

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// net.Pipe provides real blocking Write and Close-unblocks-I/O semantics.
// The wrapper reports entry into production send's Write call; nobody reads
// its peer, and both ends remain open until disconnect or test cleanup.
type t05ObservedPipe struct {
	net.Conn
	entered chan struct{}
	once    sync.Once
}

func (c *t05ObservedPipe) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	return c.Conn.Write(p)
}
func TestReaderDisconnectUnblocksInFlightDispatch(t *testing.T) {
	s := t05ShortServer(t)
	defer s.Close()
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	pipe := &t05ObservedPipe{Conn: local, entered: make(chan struct{})}
	host := t05RegisterDispatchFixture(t, s, "older", 1, pipe)
	dispatched := make(chan error, 1)
	disconnected := make(chan bool, 1)
	go func() { _, err := s.dispatch(host, false); dispatched <- err }()
	select {
	case <-pipe.entered:
	case err := <-dispatched:
		t.Fatalf("wrong cause: dispatch returned before blocking Write: %v", err)
	case <-time.After(2 * time.Second):
		local.Close()
		t.Fatal("wrong cause: production Write entry barrier not reached")
	}
	active, err := s.db.ActiveTasks(context.Background())
	if err != nil || len(active) != 1 || active[0].TaskID != "older-task" {
		local.Close()
		t.Fatalf("wrong cause: grant not committed before disconnect: active=%+v err=%v", active, err)
	}
	// This is exactly the method invoked by handleHost's reader-exit defer.
	// Calling it must Close the transport and release its in-flight Write.
	go func() { disconnected <- s.disconnectHost(host) }()
	completedBeforeCleanup := false
	select {
	case <-disconnected:
		completedBeforeCleanup = true
	case <-time.After(time.Second):
	}
	// Even the expected RED tears down its own real I/O and awaits both Go
	// goroutines. No numeric PID signals, real service, or protocol rewrite.
	if !completedBeforeCleanup {
		if err := local.Close(); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-dispatched:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("wrong cause: dispatch did not report closed transport: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup failure: dispatch goroutine did not finish")
	}
	if !completedBeforeCleanup {
		select {
		case <-disconnected:
		case <-time.After(time.Second):
			t.Fatal("cleanup failure: disconnect goroutine did not finish")
		}
		t.Fatal("reader disconnect cannot Close blocked Write: dispatchMu held across IPC send; committed_grant=older-task reserved=1; disconnect_done_before_cleanup=false; both_goroutines_joined_after_test_Close=true")
	}
}

// Multiple reader exits and stale scan callers may race a blocked send. Each
// reader closes only its own transport; all failed/claim state remains locked.
func TestConcurrentReaderDisconnectsKeepGrantAndReplacement(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "current_session"
		if replacement {
			name = "replacement_session"
		}
		t.Run(name, func(t *testing.T) {
			s := t05ShortServer(t)
			defer s.Close()
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			observed := &t05ObservedPipe{Conn: local, entered: make(chan struct{})}
			host := t05RegisterDispatchFixture(t, s, "older", 2, observed)
			dispatched := make(chan error, 1)
			go func() { _, err := s.dispatch(host, false); dispatched <- err }()
			select {
			case <-observed.entered:
			case <-time.After(2 * time.Second):
				_ = local.Close()
				t.Fatal("setup Write entry not reached")
			}
			var successor *hostSession
			var successorPeer net.Conn
			if replacement {
				successorLocal, p := net.Pipe()
				successorPeer = p
				defer p.Close()
				defer successorLocal.Close()
				successor = &hostSession{id: host.id, conn: successorLocal}
				s.mu.Lock()
				s.hosts[host.id] = successor
				s.mu.Unlock()
			}
			var all sync.WaitGroup
			for i := 0; i < 4; i++ {
				all.Add(1)
				go func() { defer all.Done(); s.disconnectHost(host) }()
			}
			again := make(chan bool, 2)
			for i := 0; i < 2; i++ {
				all.Add(1)
				go func() { defer all.Done(); progress, err := s.dispatch(host, false); again <- progress || err != nil }()
			}
			joined := make(chan struct{})
			go func() { all.Wait(); close(joined) }()
			beforeCleanup := false
			select {
			case <-joined:
				beforeCleanup = true
			case <-time.After(time.Second):
				_ = local.Close()
			}
			select {
			case <-joined:
			case <-time.After(time.Second):
				t.Fatal("cleanup failed: concurrent disconnect/dispatch not joined")
			}
			select {
			case err := <-dispatched:
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("wrong dispatch error: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cleanup failed: initial dispatch not joined")
			}
			for i := 0; i < 2; i++ {
				if <-again {
					t.Fatal("failed session admitted another claim")
				}
			}
			db := t05ReadOnlyDB(t, s)
			var reserved, used int
			var status string
			if err := db.QueryRow(`SELECT COUNT(*),SUM(used_ms) FROM budget_runtime WHERE status='reserved' AND granted_ms=1000`).Scan(&reserved, &used); err != nil || reserved != 1 || used != 0 {
				t.Fatalf("disconnect lost/refunded/duplicated grant: reserved=%d used=%d err=%v", reserved, used, err)
			}
			if err := db.QueryRow(`SELECT status FROM runtime_hosts WHERE id=?`, host.id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			current := s.hosts[host.id]
			s.mu.Unlock()
			if replacement {
				if current != successor || status != "ready" {
					t.Fatalf("old disconnect changed replacement: current=%p want=%p status=%s", current, successor, status)
				}
				_ = successorPeer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
				_, err := successorPeer.Read(make([]byte, 1))
				if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
					t.Fatalf("old disconnect closed replacement transport: %v", err)
				}
			} else if current != nil || status != "offline" {
				t.Fatalf("failed current session not offline: current=%p status=%s", current, status)
			}
			if !beforeCleanup {
				t.Fatal("concurrent reader exits blocked behind dispatchMu; real Close required test cleanup; goroutines joined; reserved=1 unchanged")
			}
		})
	}
}
