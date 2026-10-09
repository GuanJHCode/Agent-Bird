package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const ownerLaunchFDMarker byte = 0x4c

// OwnerLaunchFDs owns only the empty child endpoints. No method reads their
// payload. The launcher must Close before signing its handoff-closed receipt;
// only then may the source write its environment/bootstrap to the other ends.
// The authenticated session is serial: no concurrent frame reader may run
// during this one-byte SCM_RIGHTS exchange.
type OwnerLaunchFDs struct{ files []*os.File }

func (f *OwnerLaunchFDs) Files() []*os.File {
	if f == nil || len(f.files) != 2 {
		return nil
	}
	return append([]*os.File(nil), f.files...)
}

func (f *OwnerLaunchFDs) Close() error {
	if f == nil {
		return nil
	}
	var result error
	for _, file := range f.files {
		result = errors.Join(result, file.Close())
	}
	f.files = nil
	return result
}

func (f *OwnerLaunchFDs) ValidateEmpty() error {
	if f == nil || len(f.files) != 2 {
		return ErrInvalidMessage
	}
	pipe, control := int(f.files[0].Fd()), int(f.files[1].Fd())
	var info syscall.Stat_t
	if err := syscall.Fstat(pipe, &info); err != nil || info.Mode&syscall.S_IFMT != syscall.S_IFIFO || info.Uid != uint32(os.Geteuid()) {
		return ErrInvalidMessage
	}
	flags, err := unix.FcntlInt(uintptr(pipe), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return ErrInvalidMessage
	}
	kind, err := syscall.GetsockoptInt(control, syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil || kind != syscall.SOCK_STREAM || !ownerControlUnnamed(control) {
		return ErrInvalidMessage
	}
	// Poll readiness, never peek/read even one byte of authentication material.
	// A closed parent, pending payload or invalid descriptor cannot be launched.
	// Darwin poll can be interrupted even with a zero timeout. A signal is
	// not descriptor readiness; retry a bounded burst without reading payload.
	for interruptions := 0; interruptions < 16; interruptions++ {
		fds := []unix.PollFd{{Fd: int32(pipe), Events: unix.POLLIN}, {Fd: int32(control), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 0)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil || n != 0 || fds[0].Revents != 0 || fds[1].Revents != 0 {
			return ErrInvalidMessage
		}
		return nil
	}
	return ErrInvalidMessage
}

func ownerLaunchFDDeadline(ctx context.Context, c *net.UnixConn) (func(), error) {
	if c == nil {
		return nil, ErrInvalidMessage
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.SetDeadline(deadline); err != nil {
		return nil, err
	}
	// Cancellation aborts this session: a partial descriptor exchange is not
	// a reusable framed connection.
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	return func() { stop(); _ = c.SetDeadline(time.Time{}) }, nil
}

// SendOwnerLaunchFDs borrows the two empty endpoints. The caller closes its
// copies after this exchange, before awaiting the launch receipt.
func SendOwnerLaunchFDs(ctx context.Context, c *net.UnixConn, bootstrap, control *os.File) error {
	if bootstrap == nil || control == nil {
		return ErrInvalidMessage
	}
	fds := &OwnerLaunchFDs{files: []*os.File{bootstrap, control}}
	if err := fds.ValidateEmpty(); err != nil {
		return err
	}
	finish, err := ownerLaunchFDDeadline(ctx, c)
	if err != nil {
		return err
	}
	defer finish()
	rights := syscall.UnixRights(int(bootstrap.Fd()), int(control.Fd()))
	n, oobn, err := c.WriteMsgUnix([]byte{ownerLaunchFDMarker}, rights, nil)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if n != 1 || oobn != len(rights) {
		return ErrInvalidMessage
	}
	return nil
}

func ReceiveOwnerLaunchFDs(ctx context.Context, c *net.UnixConn) (*OwnerLaunchFDs, error) {
	return receiveOwnerLaunchFDs(ctx, c, syscall.Recvmsg)
}

type ownerLaunchRecvmsg func(fd int, payload, oob []byte, flags int) (n, oobn, recvflags int, from syscall.Sockaddr, err error)

func receiveOwnerLaunchFDs(ctx context.Context, c *net.UnixConn, receive ownerLaunchRecvmsg) (result *OwnerLaunchFDs, resultErr error) {
	finish, err := ownerLaunchFDDeadline(ctx, c)
	if err != nil {
		return nil, err
	}
	defer finish()
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	// Parse/close every received right even on excess/truncation. The fixed
	// buffer exceeds the supported platform's one-message descriptor limit.
	oob := make([]byte, syscall.CmsgSpace(4*256))
	var payload [1]byte
	var received []int
	var n, oobn, flags int
	var receiveErr error
	defer func() {
		if result == nil {
			for _, fd := range received {
				_ = syscall.Close(fd)
			}
		}
	}()
	err = raw.Read(func(fd uintptr) bool {
		// Darwin lacks MSG_CMSG_CLOEXEC. Hold the fork read lock only for a
		// nonblocking recv and CLOEXEC setup, never while waiting for a peer.
		syscall.ForkLock.RLock()
		defer syscall.ForkLock.RUnlock()
		// EINTR did not consume the readiness edge. Retry here; only EAGAIN
		// may ask RawConn to wait for a new edge. Bound interrupted bursts.
		for interruptions := 0; interruptions < 16; interruptions++ {
			if receiveErr = ctx.Err(); receiveErr != nil {
				break
			}
			n, oobn, flags, _, receiveErr = receive(int(fd), payload[:], oob, syscall.MSG_DONTWAIT)
			if !errors.Is(receiveErr, syscall.EINTR) {
				break
			}
		}
		if receiveErr == syscall.EAGAIN || receiveErr == syscall.EWOULDBLOCK {
			return false
		}
		if receiveErr != nil {
			return true
		}
		messages, parseErr := syscall.ParseSocketControlMessage(oob[:oobn])
		if parseErr != nil {
			receiveErr = ErrInvalidMessage
			return true
		}
		for _, message := range messages {
			if message.Header.Level != syscall.SOL_SOCKET || message.Header.Type != syscall.SCM_RIGHTS {
				receiveErr = ErrInvalidMessage
				continue
			}
			rights, rightsErr := syscall.ParseUnixRights(&message)
			if rightsErr != nil {
				receiveErr = ErrInvalidMessage
				continue
			}
			for _, childFD := range rights {
				syscall.CloseOnExec(childFD)
				received = append(received, childFD)
			}
		}
		return true
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if receiveErr != nil {
		return nil, receiveErr
	}
	if n != 1 || payload[0] != ownerLaunchFDMarker || flags&(syscall.MSG_CTRUNC|syscall.MSG_TRUNC) != 0 || len(received) != 2 {
		return nil, ErrInvalidMessage
	}
	handoff := &OwnerLaunchFDs{files: []*os.File{os.NewFile(uintptr(received[0]), "owner-launch-bootstrap"), os.NewFile(uintptr(received[1]), "owner-launch-control")}}
	// From here the os.File objects exclusively own the received descriptors.
	received = nil
	if err = handoff.ValidateEmpty(); err != nil {
		_ = handoff.Close()
		return nil, err
	}
	return handoff, nil
}
