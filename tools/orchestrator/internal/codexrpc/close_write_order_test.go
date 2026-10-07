package codexrpc

import (
	"errors"
	"io"
	"testing"
	"testing/synctest"
)

// These tests control the CloseWrite state machine, not the OS reader schedule.
// Real-pipe tests separately establish that only original Read EOF sets readEOF.
type failedHalfCloseStream struct {
	closeEntered chan struct{}
	closeRelease <-chan struct{}
}

func (*failedHalfCloseStream) Read([]byte) (int, error)  { panic("no reader in state test") }
func (*failedHalfCloseStream) Write([]byte) (int, error) { panic("no writer in state test") }
func (*failedHalfCloseStream) CloseWrite() error         { return io.ErrClosedPipe }
func (s *failedHalfCloseStream) Close() error {
	if s.closeEntered != nil {
		close(s.closeEntered)
		<-s.closeRelease
	}
	return nil
}

func TestClientCloseWriteWaitsForLatchedEOFClassification(t *testing.T) {
	for _, final := range []struct {
		name string
		err  error
	}{
		{"eof", io.EOF},
		{"partial", ErrIncompleteFrame},
		{"oversize", ErrFrameLimit},
	} {
		t.Run(final.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				// Snapshot the reachable interval between the original reader's
				// EOF latch and its final-frame classification. No reader or EOF
				// is fabricated by production CloseWrite itself.
				c := &Client{stream: &failedHalfCloseStream{}, readEOF: true, done: make(chan struct{})}
				defer c.fail(final.err)
				results := make(chan error, 2)
				go func() { results <- c.CloseWrite() }()
				synctest.Wait()
				go func() { results <- c.CloseWrite() }()
				synctest.Wait()
				select {
				case got := <-results:
					t.Fatalf("half-close returned before original EOF classification: %v", got)
				default:
				}
				if c.Err() != nil {
					t.Fatal("half-close replaced pending original EOF classification")
				}
				c.fail(final.err)
				synctest.Wait()
				want := error(ErrIO)
				if final.err == io.EOF {
					want = nil
				}
				for range 2 {
					if got := <-results; !errors.Is(got, want) {
						t.Fatalf("half-close=%v want %v", got, want)
					}
				}
				if !errors.Is(c.Err(), final.err) {
					t.Fatalf("original classification changed: %v", c.Err())
				}
			})
		})
	}
}

func TestClientCloseWritePublishesFailureAfterOriginalFullClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		stream := &failedHalfCloseStream{closeEntered: make(chan struct{}), closeRelease: release}
		c := &Client{stream: stream, done: make(chan struct{})}
		results := make(chan error, 2)
		go func() { results <- c.CloseWrite() }()
		synctest.Wait()
		select {
		case <-stream.closeEntered:
		default:
			t.Fatal("failed half-close did not enter original full close")
		}
		select {
		case <-c.Done():
		default:
			t.Fatal("failed half-close did not publish terminal transport state")
		}
		go func() { results <- c.CloseWrite() }()
		synctest.Wait()
		select {
		case got := <-results:
			t.Fatalf("shared result escaped unfinished original full close: %v", got)
		default:
		}
		close(release)
		released = true
		synctest.Wait()
		for range 2 {
			if got := <-results; !errors.Is(got, ErrIO) {
				t.Fatalf("shared half-close=%v want I/O failure", got)
			}
		}
		if !errors.Is(c.Err(), ErrIO) || c.ReadEOF() {
			t.Fatalf("terminal classification=%v EOF=%v", c.Err(), c.ReadEOF())
		}
	})
}
