//go:build linux

package ipc

import (
	"syscall"
	"unsafe"
)

func ownerControlUnnamed(fd int) bool {
	// Go's SockaddrUnix conversion loses socklen and renders both an unnamed
	// socket and a zero-byte abstract name as "@". Only family-with-no-path is
	// an inherited anonymous pair; a named abstract endpoint is not accepted.
	for _, operation := range []uintptr{syscall.SYS_GETSOCKNAME, syscall.SYS_GETPEERNAME} {
		var address syscall.RawSockaddrUnix
		size := uint32(unsafe.Sizeof(address))
		_, _, errno := syscall.Syscall(operation, uintptr(fd), uintptr(unsafe.Pointer(&address)), uintptr(unsafe.Pointer(&size)))
		if errno != 0 || address.Family != syscall.AF_UNIX || size != uint32(unsafe.Offsetof(address.Path)) {
			return false
		}
	}
	return true
}
