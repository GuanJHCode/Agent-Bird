package hookcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// os.File inherited from a launcher is commonly not registered with Go's
// poller. We duplicate only a FIFO/socket descriptor, make its shared file
// description nonblocking for this short Hook process, and restore flags when
// the owned duplicate closes. No background read or write survives Run.
type boundedFD struct {
	fd            int
	originalFlags int
	ctx           context.Context
	read          bool
}

func openBoundedFD(ctx context.Context, f *os.File, read bool) (*boundedFD, error) {
	fd := int(f.Fd())
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return nil, errors.New("bridge_io_unbounded")
	}
	kind := info.Mode & unix.S_IFMT
	if kind != unix.S_IFIFO && kind != unix.S_IFSOCK {
		return nil, errors.New("bridge_io_unbounded")
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, errors.New("bridge_io_unbounded")
	}
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return nil, errors.New("bridge_io_unbounded")
	}
	if _, err := unix.FcntlInt(uintptr(duplicate), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
		_ = unix.Close(duplicate)
		return nil, errors.New("bridge_io_unbounded")
	}
	return &boundedFD{fd: duplicate, originalFlags: flags, ctx: ctx, read: read}, nil
}

func (b *boundedFD) Close() error {
	_, restoreErr := unix.FcntlInt(uintptr(b.fd), unix.F_SETFL, b.originalFlags)
	closeErr := unix.Close(b.fd)
	if restoreErr != nil {
		return restoreErr
	}
	return closeErr
}

func (b *boundedFD) wait(events int16) error {
	for {
		if err := b.ctx.Err(); err != nil {
			return err
		}
		remaining := time.Until(deadline(b.ctx))
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		if remaining > 50*time.Millisecond {
			remaining = 50 * time.Millisecond
		}
		ms := int(remaining / time.Millisecond)
		if ms < 1 {
			ms = 1
		}
		fds := []unix.PollFd{{Fd: int32(b.fd), Events: events}}
		n, err := unix.Poll(fds, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return errors.New("bridge_io_unavailable")
		}
		if n == 0 {
			continue
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return errors.New("bridge_io_unavailable")
		}
		return nil
	}
}

func (b *boundedFD) Read(p []byte) (int, error) {
	if !b.read {
		return 0, errors.New("bridge_io_unavailable")
	}
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if err := b.wait(unix.POLLIN); err != nil {
			return 0, err
		}
		n, err := unix.Read(b.fd, p)
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err == unix.EINTR {
			continue
		}
		if err != nil {
			return n, errors.New("bridge_io_unavailable")
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
}

func (b *boundedFD) Write(p []byte) (int, error) {
	if b.read {
		return 0, errors.New("bridge_io_unavailable")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := b.wait(unix.POLLOUT); err != nil {
		return 0, err
	}
	n, err := unix.Write(b.fd, p)
	if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err == unix.EINTR {
		return 0, errors.New("bridge_output_incomplete")
	}
	if err != nil {
		return n, errors.New("bridge_output_incomplete")
	}
	return n, nil
}

func boundedInput(ctx context.Context, in io.Reader) (io.Reader, func(), error) {
	switch in.(type) {
	case *bytes.Buffer, *bytes.Reader, *strings.Reader:
		return in, func() {}, nil
	}
	f, ok := in.(*os.File)
	if !ok {
		return nil, nil, errors.New("bridge_io_unbounded")
	}
	b, err := openBoundedFD(ctx, f, true)
	if err != nil {
		return nil, nil, err
	}
	return b, func() { _ = b.Close() }, nil
}

func boundedOutput(ctx context.Context, out io.Writer) (io.Writer, func(), error) {
	if b, ok := out.(*boundedFD); ok && b.ctx == ctx {
		return b, func() {}, nil
	}
	if _, ok := out.(*bytes.Buffer); ok {
		return out, func() {}, nil
	}
	f, ok := out.(*os.File)
	if !ok {
		return nil, nil, errors.New("bridge_io_unbounded")
	}
	b, err := openBoundedFD(ctx, f, false)
	if err != nil {
		return nil, nil, err
	}
	return b, func() { _ = b.Close() }, nil
}

// Diagnostics are best effort after the application deadline. One nonblocking
// syscall prevents a saturated stderr pipe from extending Hook lifetime.
func writeBestEffortDiagnostic(out io.Writer, message string) {
	if buffer, ok := out.(*bytes.Buffer); ok {
		_, _ = buffer.WriteString(message)
		return
	}
	f, ok := out.(*os.File)
	if !ok {
		return
	}
	b, err := openBoundedFD(context.Background(), f, false)
	if err != nil {
		return
	}
	defer b.Close()
	_, _ = unix.Write(b.fd, []byte(message))
}
