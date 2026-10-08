//go:build darwin

package process

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Persisted birth identities use ps's local-time, second-resolution C format.
// Other locales retain their existing formatter rather than changing identity.
func darwinBirthLocation() (*time.Location, bool) {
	locale := os.Getenv("LC_ALL")
	if locale == "" {
		locale = os.Getenv("LC_TIME")
	}
	if locale == "" {
		locale = os.Getenv("LANG")
	}
	if locale != "" && locale != "C" && locale != "POSIX" && locale != "C.UTF-8" && locale != "C.utf8" {
		return nil, false
	}
	zone, set := os.LookupEnv("TZ")
	if set && (zone == "" || strings.HasPrefix(zone, ":") || strings.HasPrefix(zone, "/") || (zone != "UTC" && zone != "GMT" && !strings.Contains(zone, "/"))) {
		return nil, false
	}
	if !set {
		zone = "/etc/localtime"
	} else {
		// IANA names only; POSIX TZ rules retain the old formatter.
	}
	var location *time.Location
	var err error
	if strings.HasPrefix(zone, "/") {
		var data []byte
		data, err = os.ReadFile(zone)
		if err == nil {
			location, err = time.LoadLocationFromTZData(zone, data)
		}
	} else {
		location, err = time.LoadLocation(zone)
	}
	return location, err == nil && location != nil
}

func nativeProcessIdentity(ctx context.Context, fields string, pid int) ([]byte, bool, error) {
	var location *time.Location
	if fields == "lstart=" || fields == "pgid=,lstart=" {
		var supported bool
		location, supported = darwinBirthLocation()
		if !supported {
			return nil, false, nil
		}
	}
	k, err := darwinProcessRecord(ctx, pid)
	if err != nil {
		return nil, true, err
	}
	out, err := formatDarwinIdentity(k, fields, location)
	return out, true, err
}

func darwinProcessRecord(ctx context.Context, pid int) (*unix.KinfoProc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pid <= 0 || int64(pid) > 1<<31-1 {
		return nil, CodeError("process_birth_unknown")
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || k == nil || int(k.Proc.P_pid) != pid || k.Proc.P_starttime.Sec <= 0 || k.Proc.P_starttime.Usec < 0 || k.Proc.P_starttime.Usec >= 1000000 {
		return nil, CodeError("process_birth_unknown")
	}
	return k, nil
}

func formatDarwinIdentity(k *unix.KinfoProc, fields string, location *time.Location) ([]byte, error) {
	switch fields {
	case "lstart=":
		return []byte(time.Unix(k.Proc.P_starttime.Sec, 0).In(location).Format("Mon Jan _2 15:04:05 2006") + "\n"), nil
	case "ppid=,uid=":
		return []byte(fmt.Sprintf("%d %d\n", k.Eproc.Ppid, k.Eproc.Ucred.Uid)), nil
	case "pgid=,lstart=":
		return []byte(fmt.Sprintf("%d %s\n", k.Eproc.Pgid, time.Unix(k.Proc.P_starttime.Sec, 0).In(location).Format("Mon Jan _2 15:04:05 2006"))), nil
	default:
		return nil, CodeError("process_ancestry_unknown")
	}
}

// Keep the bounded ancestry reducer; additionally compare full kernel start,
// parent and effective UID across before/metadata/after observations.
func nativeAncestryQuery() func(context.Context, string, int) ([]byte, error) {
	if _, supported := darwinBirthLocation(); !supported {
		return nil
	}
	type identity struct {
		seconds int64
		micros  int32
		parent  int32
		uid     uint32
	}
	observed := map[int]identity{}
	return func(ctx context.Context, fields string, pid int) ([]byte, error) {
		location, supported := darwinBirthLocation()
		if !supported {
			return nil, CodeError("process_ancestry_unknown")
		}
		k, err := darwinProcessRecord(ctx, pid)
		if err != nil {
			return nil, err
		}
		current := identity{k.Proc.P_starttime.Sec, k.Proc.P_starttime.Usec, k.Eproc.Ppid, k.Eproc.Ucred.Uid}
		if prior, ok := observed[pid]; ok && prior != current {
			return nil, CodeError("process_identity_changed")
		}
		observed[pid] = current
		return formatDarwinIdentity(k, fields, location)
	}
}

// kern.proc.pgrp must not inherit SysctlKinfoProcSlice's unbounded ENOMEM retry.
// Query a bounded snapshot, retry growth once, and never treat partial data as
// absence. Kernel records are read as bytes, without following kernel pointers.
func darwinProcessTable(ctx context.Context, pgid int) ([]byte, error) {
	const maxBytes = 8 * 1024 * 1024
	if pgid <= 0 || int64(pgid) > 1<<31-1 {
		return nil, ErrProcessTreeUnknown
	}
	mib := [4]int32{1, 14, 2, int32(pgid)} // CTL_KERN, KERN_PROC, KERN_PROC_PGRP, pgid
	query := func(buf []byte) (uintptr, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n := uintptr(len(buf))
		var address uintptr
		if len(buf) > 0 {
			address = uintptr(unsafe.Pointer(&buf[0]))
		}
		_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL, uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)), address, uintptr(unsafe.Pointer(&n)), 0, 0)
		runtime.KeepAlive(buf)
		runtime.KeepAlive(mib)
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if errno != 0 {
			return n, errno
		}
		return n, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		n, err := query(nil)
		if err != nil || n > maxBytes || n%unix.SizeofKinfoProc != 0 {
			return nil, ErrProcessTreeUnknown
		}
		capacity := n + 16*unix.SizeofKinfoProc
		if capacity > maxBytes {
			capacity = n
		}
		buf := make([]byte, int(capacity))
		n, err = query(buf)
		if errors.Is(err, syscall.ENOMEM) {
			continue
		}
		if err != nil || n >= uintptr(len(buf)) || n%unix.SizeofKinfoProc != 0 {
			return nil, ErrProcessTreeUnknown
		}
		return buf[:int(n)], nil
	}
	return nil, ErrProcessTreeUnknown
}

func nativeProcessGroups(ctx context.Context, fields string, pgid int) ([]byte, bool, error) {
	if fields != "pgid=" && fields != "pid=,pgid=,stat=" {
		return nil, true, ErrProcessTreeUnknown
	}
	raw, err := darwinProcessTable(ctx, pgid)
	if err != nil {
		return nil, true, err
	}
	var k unix.KinfoProc
	pidOffset := int(unsafe.Offsetof(k.Proc) + unsafe.Offsetof(k.Proc.P_pid))
	statOffset := int(unsafe.Offsetof(k.Proc) + unsafe.Offsetof(k.Proc.P_stat))
	groupOffset := int(unsafe.Offsetof(k.Eproc) + unsafe.Offsetof(k.Eproc.Pgid))
	var out strings.Builder
	for offset := 0; offset < len(raw); offset += unix.SizeofKinfoProc {
		record := raw[offset : offset+unix.SizeofKinfoProc]
		pid := int32(binary.NativeEndian.Uint32(record[pidOffset : pidOffset+4]))
		group := int32(binary.NativeEndian.Uint32(record[groupOffset : groupOffset+4]))
		if pid <= 0 || group != int32(pgid) {
			return nil, true, ErrProcessTreeUnknown
		}
		if fields == "pgid=" {
			fmt.Fprintf(&out, "%d\n", group)
		} else {
			state := "R"
			if record[statOffset] == 5 {
				state = "Z"
			} // Darwin SZOMB.
			fmt.Fprintf(&out, "%d %d %s\n", pid, group, state)
		}
		if out.Len() > 1024*1024 {
			return nil, true, ErrProcessTreeUnknown
		}
	}
	if ctx.Err() != nil {
		return nil, true, ctx.Err()
	}
	return []byte(out.String()), true, nil
}
