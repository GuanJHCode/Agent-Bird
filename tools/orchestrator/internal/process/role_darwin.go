//go:build darwin

package process

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"syscall"
	"unsafe"
)

const maxRoleArguments = 1024 * 1024

// IsCodexAppServerDaemon observes the two established non-session command shapes. It never
// exports argv or parses the environment trailing the kernel's argument vector.
// The caller must independently verify the executable digest and PID birth.
func IsCodexAppServerDaemon(pid int, expectedExecutable string) (bool, error) {
	if pid <= 0 || !filepath.IsAbs(expectedExecutable) || filepath.Clean(expectedExecutable) != expectedExecutable {
		return false, CodeError("process_role_unknown")
	}
	mib := [3]int32{1, 49, int32(pid)} // CTL_KERN, KERN_PROCARGS2, pid.
	size := uintptr(0)
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 3, 0, uintptr(unsafe.Pointer(&size)), 0, 0)
	if errno != 0 || size < 6 || size > maxRoleArguments {
		return false, CodeError("process_role_unknown")
	}
	data := make([]byte, int(size))
	defer clear(data)
	_, _, errno = syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), 3, uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&size)), 0, 0)
	if errno != 0 || size < 6 || size > uintptr(len(data)) {
		return false, CodeError("process_role_unknown")
	}
	return codexDaemonArguments(data[:int(size)], expectedExecutable)
}

func codexDaemonArguments(data []byte, executable string) (bool, error) {
	unknown := CodeError("process_role_unknown")
	if len(data) < 6 || len(data) > maxRoleArguments {
		return false, unknown
	}
	nargs := int(binary.LittleEndian.Uint32(data[:4]))
	if nargs < 1 || nargs > 4096 {
		return false, unknown
	}
	remaining := data[4:]
	end := bytes.IndexByte(remaining, 0)
	if end <= 0 || string(remaining[:end]) != executable {
		return false, unknown
	}
	if nargs != 3 && nargs != 4 {
		return false, nil
	}
	remaining = remaining[end+1:]
	for len(remaining) > 0 && remaining[0] == 0 {
		remaining = remaining[1:]
	}
	// Store only the established non-secret role tokens. Parse the exact number of argv
	// entries, then stop before any environment bytes; never log raw contents.
	role := [4]bool{}
	for i := 0; i < nargs; i++ {
		end = bytes.IndexByte(remaining, 0)
		if end < 0 {
			return false, unknown
		}
		if i == 0 {
			role[0] = bytes.Equal(remaining[:end], []byte(executable)) || bytes.Equal(remaining[:end], []byte(filepath.Base(executable)))
			if !role[0] {
				return false, nil
			}
		}
		if i == 1 {
			role[1] = bytes.Equal(remaining[:end], []byte("app-server"))
		}
		if i == 2 {
			role[2] = bytes.Equal(remaining[:end], []byte("daemon"))
		}
		if i == 3 {
			role[3] = bytes.Equal(remaining[:end], []byte("pid-update-loop"))
		}
		remaining = remaining[end+1:]
	}
	return role[0] && role[1] && role[2] && (nargs == 3 || role[3]), nil
}
