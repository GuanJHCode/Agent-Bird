//go:build darwin

package process

import (
	"context"
	"os"
	"reflect"
	"syscall"
	"unsafe"
)

func darwinArguments(ctx context.Context, pid int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pid <= 0 || int64(pid) > 1<<31-1 {
		return nil, CodeError("process_arguments_unknown")
	}
	// KERN_PROCARGS2 includes trailing environment bytes. Decode only argc
	// arguments, never expose/log that suffix, and erase the scratch buffer.
	body := make([]byte, 1<<20)
	defer clear(body)
	size := uintptr(len(body))
	mib := [3]int32{1, 49, int32(pid)}
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)), uintptr(unsafe.Pointer(&body[0])), uintptr(unsafe.Pointer(&size)), 0, 0)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if errno != 0 || size == 0 || size > uintptr(len(body)) {
		return nil, CodeError("process_arguments_unknown")
	}
	return decodeProcessArguments(body[:size])
}

func ObserveArguments(ctx context.Context, pid int) (ArgumentSnapshot, error) {
	var result ArgumentSnapshot
	if err := ctx.Err(); err != nil {
		return result, err
	}
	before, err := darwinPeerRecord(ctx, pid)
	if err != nil || before.euid != os.Geteuid() {
		return result, CodeError("process_arguments_unknown")
	}
	args, err := darwinArguments(ctx, pid)
	if err != nil {
		return result, err
	}
	executable, err := ExecutablePath(pid)
	if err != nil {
		return result, err
	}
	birth, err := BirthContext(ctx, pid)
	if err != nil {
		return result, err
	}
	again, err := darwinArguments(ctx, pid)
	if err != nil || !reflect.DeepEqual(args, again) {
		return result, CodeError("process_identity_changed")
	}
	if path, err := ExecutablePath(pid); err != nil || path != executable {
		return result, CodeError("process_identity_changed")
	}
	after, err := darwinPeerRecord(ctx, pid)
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err != nil || before != after {
		return result, CodeError("process_identity_changed")
	}
	return ArgumentSnapshot{PID: pid, ParentPID: before.ppid, UID: before.euid, Birth: birth, Executable: executable, Args: args}, nil
}
