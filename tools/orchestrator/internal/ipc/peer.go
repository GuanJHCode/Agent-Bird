package ipc

import (
	"errors"
	"net"
	"runtime"
	"syscall"
)

func PeerPID(conn net.Conn) (int, error) {
	if runtime.GOOS != "darwin" {
		return 0, errors.New("owner_peer_platform_unsupported")
	}
	socket, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, ErrInvalidMessage
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var socketErr error
	err = raw.Control(func(fd uintptr) { pid, socketErr = syscall.GetsockoptInt(int(fd), 0, 0x002) })
	if err != nil {
		return 0, err
	}
	if socketErr != nil || pid < 1 {
		return 0, ErrInvalidMessage
	}
	return pid, nil
}
