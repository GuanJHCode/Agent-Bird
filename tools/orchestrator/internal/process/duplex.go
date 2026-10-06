package process

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// StartDuplex launches through the same original-process authority as Start.
// Its stream exclusively writes child stdin and reads child stdout; Output
// contains bounded stderr only. EOF and Close are never process exit proof.
// A failure after Start retains the Handle and closes all stream descriptors;
// the caller must still account for an unproven process tree.
func StartDuplex(ctx context.Context, spec Command) (*Handle, io.ReadWriteCloser, error) {
	h, stream, err := startCommand(ctx, spec, true, processBirth)
	if stream == nil {
		return h, nil, err
	}
	return h, stream, err
}

type duplexStream struct {
	reader   *os.File
	writer   *os.File
	once     sync.Once
	closeErr error
}

func (s *duplexStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *duplexStream) Write(p []byte) (int, error) { return s.writer.Write(p) }

// os.File.Close interrupts the original pollable pipe operations. Never take
// an I/O lock or wait for the child here: Read and Write may both be blocked.
func (s *duplexStream) Close() error {
	s.once.Do(func() { s.closeErr = errors.Join(s.writer.Close(), s.reader.Close()) })
	return s.closeErr
}

type duplexPipes struct {
	stream     *duplexStream
	childRead  *os.File
	childWrite *os.File
}

func newDuplexPipes() (*duplexPipes, error) {
	childRead, parentWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	parentRead, childWrite, err := os.Pipe()
	if err != nil {
		_ = childRead.Close()
		_ = parentWrite.Close()
		return nil, err
	}
	return &duplexPipes{stream: &duplexStream{reader: parentRead, writer: parentWrite}, childRead: childRead, childWrite: childWrite}, nil
}

func (p *duplexPipes) closeChildren() error {
	return errors.Join(p.childRead.Close(), p.childWrite.Close())
}

// Go-only original-launch observations, never a wire authority or cancel API.
type DuplexStartupCleanup struct {
	Context     context.Context `json:"-"`
	Deadline    time.Time       `json:"-"`
	Result      error           `json:"-"`
	CompletedAt time.Time       `json:"-"`
}
type DuplexLaunchState struct {
	SpawnedAt      time.Time             `json:"-"`
	StartupCleanup *DuplexStartupCleanup `json:"-"`
}

func (h *Handle) DuplexLaunchState() (DuplexLaunchState, bool) {
	if h == nil {
		return DuplexLaunchState{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.duplexLaunch == nil {
		return DuplexLaunchState{}, false
	}
	view := *h.duplexLaunch
	if view.StartupCleanup != nil {
		cleanup := *view.StartupCleanup
		view.StartupCleanup = &cleanup
	}
	return view, true
}
