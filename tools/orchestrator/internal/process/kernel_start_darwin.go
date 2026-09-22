//go:build darwin

package process

import (
	"fmt"
	"syscall"
	"unsafe"
)

// procBSDInfo is Darwin's struct proc_bsdinfo from <sys/proc_info.h>. Its
// start fields are at offsets 120 and 128 and the complete ABI is 136 bytes.
// The explicit leading fields prevent a partial or guessed layout from being
// passed to PROC_PIDTBSDINFO.
type procBSDInfo struct {
	Flags     uint32
	Status    uint32
	XStatus   uint32
	PID       uint32
	PPID      uint32
	UID       uint32
	GID       uint32
	RUID      uint32
	RGID      uint32
	SVUID     uint32
	SVGID     uint32
	Reserved  uint32
	Comm      [16]byte
	Name      [32]byte
	NFiles    uint32
	PGID      uint32
	PJobc     uint32
	TDev      uint32
	TPGID     uint32
	Nice      int32
	StartSec  uint64
	StartUSec uint64
}

const (
	procInfoCallPIDInfo = 2
	procPIDTBSDInfo     = 3
	procBSDInfoSize     = unsafe.Sizeof(procBSDInfo{})
	procBSDInfoStartOff = unsafe.Offsetof(procBSDInfo{}.StartSec)
)

// KernelStartID returns the kernel's process start timestamp as a fixed
// seconds.microseconds decimal identity. It uses the raw Darwin proc_info
// syscall directly and never executes a command.
func KernelStartID(pid int) (string, error) {
	if pid <= 0 {
		return "", CodeError("kernel_start_unknown")
	}
	var info procBSDInfo
	if procBSDInfoSize != 136 || procBSDInfoStartOff != 120 {
		return "", CodeError("kernel_start_unknown")
	}
	returned, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, procInfoCallPIDInfo, uintptr(pid), procPIDTBSDInfo, 0, uintptr(unsafe.Pointer(&info)), procBSDInfoSize)
	if errno != 0 || returned != procBSDInfoSize || info.PID != uint32(pid) || info.StartSec == 0 || info.StartUSec >= 1_000_000 {
		return "", CodeError("kernel_start_unknown")
	}
	return fmt.Sprintf("%d.%06d", info.StartSec, info.StartUSec), nil
}
