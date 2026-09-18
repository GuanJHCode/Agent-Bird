//go:build darwin

package process

import (
	"bytes"
	"path/filepath"
	"syscall"
	"unsafe"
)

// ExecutablePath reads only the kernel executable path, never argv or environment.
func ExecutablePath(pid int) (string, error) {
	if pid <= 0 {
		return "", CodeError("process_executable_unknown")
	}
	// XNU proc_pidpath: PROC_INFO_CALL_PIDINFO=2, PROC_PIDPATHINFO=11,
	// PROC_PIDPATHINFO_MAXSIZE=4096. Success returns zero and fills a
	// NUL-terminated path; the syscall return value is not a byte count.
	var path [4096]byte
	_, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, 2, uintptr(pid), 11, 0, uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)))
	if errno != 0 {
		return "", CodeError("process_executable_unknown")
	}
	end := bytes.IndexByte(path[:], 0)
	if end <= 0 || end >= len(path)-1 || !filepath.IsAbs(string(path[:end])) {
		return "", CodeError("process_executable_unknown")
	}
	return string(path[:end]), nil
}
