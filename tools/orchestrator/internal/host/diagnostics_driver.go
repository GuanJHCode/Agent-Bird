//go:build darwin || linux

package host

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"golang.org/x/sys/unix"
)

const diagnosticOperationTimeout = 2 * time.Second

var errDiagnosticAuthority = errors.New("diagnostics_host_authority_invalid")

type diagnosticWriter interface {
	Append(context.Context, diagnostics.Record) error
	Flush(context.Context) error
	Close(context.Context) error
	Abort(context.Context, error) error
}

// One execution owner chooses Finish (only after original Wait/tree/drain proof)
// or Abort once. Later calls only join that choice; a deadline stops waiting,
// not ownership. A Writer operation already inside the kernel may outlive it.
type diagnosticDriver struct {
	capture   *diagnostics.Capture
	writer    diagnosticWriter
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	onFailure func(error) // Must only wake/cancel the execution, never wait for it.
	terminal  sync.Once
	mu        sync.Mutex // Never held across Writer, filesystem or owner callback work.
	accepting bool
	err       error
}

func diagnosticLaunchScope(producer string, g contract.LaunchCommand) (diagnostics.Scope, string, error) {
	if g.Diagnostics == nil || g.Diagnostics.Validate() != nil || producer == "" || g.HostID != producer || !diagnosticPathToken(g.AttemptID) {
		return diagnostics.Scope{}, "", errDiagnosticAuthority
	}
	scope := diagnostics.Scope{Version: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, ProducerID: producer, PolicySHA256: g.Diagnostics.PolicySHA256}
	if _, err := scope.Hash(); err != nil {
		return diagnostics.Scope{}, "", err
	}
	segment := fmt.Sprintf("%020d:%s", g.Diagnostics.SegmentOrdinal, g.SegmentID)
	if g.SegmentID == "" {
		return diagnostics.Scope{}, "", diagnostics.ErrInvalidSegment
	}
	if _, err := diagnostics.NewCapture(segment); err != nil {
		return diagnostics.Scope{}, "", err
	}
	return scope, segment, nil
}

func diagnosticPathToken(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (h *Host) openDetailedDiagnostics(ctx context.Context, g contract.LaunchCommand, onFailure func(error)) (*diagnosticDriver, error) {
	if g.Diagnostics == nil {
		return nil, nil
	}
	scope, segment, err := diagnosticLaunchScope(h.producerID, g)
	if err != nil {
		return nil, err
	}
	if onFailure == nil {
		return nil, errDiagnosticAuthority
	}
	// Reuse the existing descriptor-relative directory primitives, with no
	// provider state/auth access. Keep every parent edge pinned through open.
	chain, err := codexOpenStateRoot(ctx, h.spoolRoot)
	if err != nil {
		return nil, errors.Join(diagnostics.ErrUnsafeLayout, err)
	}
	defer chain.close()
	for _, name := range []string{g.AttemptID, ".diagnostics"} {
		parent := chain[len(chain)-1].fd
		created := unix.Mkdirat(parent, name, 0700)
		if created != nil && !errors.Is(created, unix.EEXIST) {
			return nil, created
		}
		if err = codexAppendStateDirectory(ctx, &chain, name, true); err != nil {
			return nil, errors.Join(diagnostics.ErrUnsafeLayout, err)
		}
		if chain[len(chain)-1].identity.Mode != 0700 || chain.verify(ctx) != nil {
			return nil, diagnostics.ErrUnsafeLayout
		}
		if created == nil {
			if err = unix.Fsync(parent); err != nil {
				return nil, err
			}
		}
	}
	if err = chain.verify(ctx); err != nil {
		return nil, errors.Join(diagnostics.ErrUnsafeLayout, err)
	}
	w, err := diagnostics.OpenWriter(ctx, filepath.Join(h.spoolRoot, g.AttemptID, ".diagnostics"), scope)
	if err != nil {
		return nil, err
	}
	if err = chain.verify(ctx); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), diagnosticOperationTimeout)
		defer cancel()
		return nil, errors.Join(err, w.Abort(cleanup, err))
	}
	c, _ := diagnostics.NewCapture(segment) // Validated before filesystem mutation.
	return startDiagnosticDriver(c, w, onFailure), nil
}

func startDiagnosticDriver(c *diagnostics.Capture, w diagnosticWriter, onFailure func(error)) *diagnosticDriver {
	ctx, cancel := context.WithCancel(context.Background())
	d := &diagnosticDriver{capture: c, writer: w, ctx: ctx, cancel: cancel, done: make(chan struct{}), onFailure: onFailure, accepting: true}
	go d.run()
	return d
}

// Observe holds the same small lock as stopObservation throughout Offer. Finish
// cannot close Capture between the enabled check and the last in-flight copy.
func (d *diagnosticDriver) Observe(stream process.OutputStream, data []byte) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.accepting {
		return
	}
	var kind diagnostics.Stream
	switch stream {
	case process.OutputStdout:
		kind = diagnostics.Stdout
	case process.OutputStderr:
		kind = diagnostics.Stderr
	}
	if _, err := d.capture.Offer(kind, data); err != nil {
		if d.err == nil {
			d.err = err
		}
		d.accepting = false
		d.cancel() // No disk/RPC/Stop in the pipe callback; consumer owns cleanup.
	}
}

func (d *diagnosticDriver) stopObservation(cause error) {
	d.mu.Lock()
	d.accepting = false
	if cause != nil && d.err == nil {
		d.err = cause
	}
	d.capture.Close()
	if cause != nil {
		d.cancel()
	}
	d.mu.Unlock()
}

func (d *diagnosticDriver) failure() error { d.mu.Lock(); defer d.mu.Unlock(); return d.err }

func (d *diagnosticDriver) operation(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(d.ctx, diagnosticOperationTimeout)
	defer cancel()
	return fn(ctx)
}

func (d *diagnosticDriver) run() {
	defer close(d.done)
	defer d.cancel()
	nextFlush := time.Now().Add(time.Second)
	var err error
	for {
		ctx, cancel := context.WithDeadline(d.ctx, nextFlush)
		lease, nextErr := d.capture.Next(ctx)
		cancel()
		if errors.Is(nextErr, context.DeadlineExceeded) && d.ctx.Err() == nil {
			err = d.operation(d.writer.Flush)
			if err != nil {
				break
			}
			nextFlush = time.Now().Add(time.Second)
			continue
		}
		if errors.Is(nextErr, diagnostics.ErrCaptureClosed) {
			if err = d.failure(); err == nil {
				err = d.operation(d.writer.Close)
			}
			if err == nil {
				return
			}
			break
		}
		if nextErr != nil {
			err = nextErr
			break
		}
		appendErr := d.operation(func(ctx context.Context) error { return d.writer.Append(ctx, lease.Record()) })
		err = d.capture.Done(lease, appendErr)
		if err != nil {
			break
		}
	}
	d.stopObservation(err)
	cause := d.failure()
	// This notification is outside both Capture and driver locks. The original
	// Host owns every process signal, original Wait, tree proof and pipe drain.
	d.onFailure(cause)
	cleanup, cancel := context.WithTimeout(context.Background(), diagnosticOperationTimeout)
	abortErr := d.writer.Abort(cleanup, cause)
	cancel()
	d.mu.Lock()
	d.err = errors.Join(cause, abortErr)
	d.mu.Unlock()
}

func (d *diagnosticDriver) join(ctx context.Context) error {
	select {
	case <-d.done:
		return d.failure()
	default:
	}
	select {
	case <-d.done:
		return d.failure()
	case <-ctx.Done():
		return errors.Join(ctx.Err(), d.failure())
	}
}

func (d *diagnosticDriver) Finish(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.terminal.Do(func() { d.stopObservation(nil) })
	return d.join(ctx)
}

func (d *diagnosticDriver) Abort(ctx context.Context, cause error) error {
	if d == nil {
		return nil
	}
	d.terminal.Do(func() {
		if cause == nil {
			cause = diagnostics.ErrAborted
		}
		d.stopObservation(cause)
	})
	return d.join(ctx)
}
