//go:build !linux

package ipc

import "syscall"

func ownerControlUnnamed(fd int) bool {
	local, err := syscall.Getsockname(fd)
	localUnix, ok := local.(*syscall.SockaddrUnix)
	if err != nil || !ok || localUnix.Name != "" {
		return false
	}
	peer, err := syscall.Getpeername(fd)
	peerUnix, ok := peer.(*syscall.SockaddrUnix)
	return err == nil && ok && peerUnix.Name == ""
}
