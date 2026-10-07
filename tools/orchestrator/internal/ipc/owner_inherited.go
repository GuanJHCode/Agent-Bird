package ipc

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// ReadOwnerBootstrapPipe consumes an inherited one-shot pipe before execution.
// The caller decodes its role-specific contract and validates live enrollment.
func ReadOwnerBootstrapPipe(ctx context.Context, fd int) ([]byte, error) {
	file, err := ownerInheritedFile(fd, "owner-bootstrap-pipe")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return nil, ErrInvalidMessage
	}
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = op.Err(); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(op, func() { _ = file.Close() })
	defer stop()
	deadline, _ := op.Deadline()
	if err = file.SetReadDeadline(deadline); err != nil {
		return nil, ErrInvalidMessage
	}
	raw, err := io.ReadAll(io.LimitReader(file, contract.OriginalOwnerEnvelopeLimit+1))
	if err != nil {
		if op.Err() != nil {
			return nil, op.Err()
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			return nil, context.DeadlineExceeded
		}
		return nil, ErrInvalidMessage
	}
	if len(raw) == 0 || len(raw) > contract.OriginalOwnerEnvelopeLimit {
		return nil, ErrInvalidMessage
	}
	return raw, nil
}

// NewOwnerControlPair creates a private control stream and the child endpoint
// for ExtraFiles. Socketpair peer credentials do not prove child identity; each
// owner/transport still independently authenticates to Coordinator.
func NewOwnerControlPair() (*net.UnixConn, *os.File, error) {
	// Darwin has no atomic SOCK_CLOEXEC creation flag. Match Go's fork lock
	// protocol so another goroutine cannot exec between creation and CLOEXEC.
	syscall.ForkLock.RLock()
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(pair[0])
		syscall.CloseOnExec(pair[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "owner-control-parent")
	child := os.NewFile(uintptr(pair[1]), "owner-control-child")
	conn, err := ownerControlConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	return conn, child, nil
}

// TakeOwnerControlFD owns and closes the original inherited descriptor, leaving
// only a close-on-exec Unix connection. It does not grant command authority.
func TakeOwnerControlFD(fd int) (*net.UnixConn, error) {
	file, err := ownerInheritedFile(fd, "owner-control-inherited")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return ownerControlConn(file)
}

func ownerInheritedFile(fd int, name string) (*os.File, error) {
	if fd < 3 {
		return nil, ErrInvalidMessage // never take ownership of caller stdio
	}
	syscall.CloseOnExec(fd)
	// Set nonblocking before NewFile so the Go poller can enforce deadlines.
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, ErrInvalidMessage
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, ErrInvalidMessage
	}
	return file, nil
}

func ownerControlConn(file *os.File) (*net.UnixConn, error) {
	fd := int(file.Fd())
	kind, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil || kind != syscall.SOCK_STREAM {
		return nil, ErrInvalidMessage
	}
	if !ownerControlUnnamed(fd) {
		return nil, ErrInvalidMessage
	}
	conn, err := net.FileConn(file) // duplicates with CLOEXEC; caller closes file
	if err != nil {
		return nil, err
	}
	stream, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, ErrInvalidMessage
	}
	return stream, nil
}
