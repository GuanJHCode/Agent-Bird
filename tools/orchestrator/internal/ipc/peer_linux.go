//go:build linux

package ipc

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func PeerPID(conn net.Conn) (int, error) {
	socket, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, ErrInvalidMessage
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var socketErr error
	err = raw.Control(func(fd uintptr) { cred, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil {
		return 0, err
	}
	if socketErr != nil || cred == nil || cred.Pid < 1 || int(cred.Uid) != os.Geteuid() {
		return 0, ErrInvalidMessage
	}
	return int(cred.Pid), nil
}
