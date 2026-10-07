//go:build linux

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const linuxProcLimit = 64 * 1024

type linuxStat struct {
	ppid, pgid int
	state      byte
	start      string
}

func linuxRead(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(name)
	if err != nil || len(b) == 0 || len(b) > linuxProcLimit {
		return nil, CodeError("process_birth_unknown")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b, nil
}
func linuxParseStat(b []byte) (linuxStat, error) {
	s := strings.TrimSpace(string(b))
	close := strings.LastIndex(s, ")")
	if close < 2 || close+3 > len(s) || s[close+1] != ' ' {
		return linuxStat{}, CodeError("process_birth_unknown")
	}
	f := strings.Fields(s[close+2:])
	if len(f) < 20 || len(f) > 64 {
		return linuxStat{}, CodeError("process_birth_unknown")
	}
	ppid, e1 := strconv.Atoi(f[1])
	pgid, e2 := strconv.Atoi(f[2])
	if e1 != nil || e2 != nil || ppid < 0 || pgid < 0 || f[19] == "" {
		return linuxStat{}, CodeError("process_birth_unknown")
	}
	if _, err := strconv.ParseUint(f[19], 10, 64); err != nil {
		return linuxStat{}, CodeError("process_birth_unknown")
	}
	return linuxStat{ppid, pgid, f[0][0], f[19]}, nil
}
func linuxBoot(ctx context.Context) (string, error) {
	b, e := linuxRead(ctx, "/proc/sys/kernel/random/boot_id")
	if e != nil {
		return "", e
	}
	v := strings.TrimSpace(string(b))
	if len(v) < 8 || strings.ContainsAny(v, " \t\n") {
		return "", CodeError("process_birth_unknown")
	}
	return v, nil
}
func linuxBirth(ctx context.Context, pid int) (string, error) {
	if pid <= 0 {
		return "", CodeError("process_birth_unknown")
	}
	boot, e := linuxBoot(ctx)
	if e != nil {
		return "", e
	}
	b, e := linuxRead(ctx, fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return "", e
	}
	st, e := linuxParseStat(b)
	if e != nil {
		return "", e
	}
	return "linux:" + boot + ":" + st.start, nil
}
func linuxStatFor(ctx context.Context, pid int) (linuxStat, error) {
	b, e := linuxRead(ctx, fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return linuxStat{}, e
	}
	return linuxParseStat(b)
}
func linuxUID(ctx context.Context, pid int) (int, error) {
	b, e := linuxRead(ctx, fmt.Sprintf("/proc/%d/status", pid))
	if e != nil {
		return 0, e
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 5 && f[0] == "Uid:" {
			u, e := strconv.Atoi(f[2])
			if e == nil && u >= 0 {
				return u, nil
			}
		}
	}
	return 0, CodeError("process_birth_unknown")
}
func linuxExecutablePath(ctx context.Context, pid int) (string, error) {
	return linuxExecutablePathWith(ctx, pid, linuxBirth, os.Readlink, filepath.EvalSymlinks, os.Stat)
}
func linuxExecutablePathWith(ctx context.Context, pid int, birth func(context.Context, int) (string, error), readlink func(string) (string, error), eval func(string) (string, error), stat func(string) (os.FileInfo, error)) (string, error) {
	b1, e := birth(ctx, pid)
	if e != nil {
		return "", e
	}
	kernelPath := fmt.Sprintf("/proc/%d/exe", pid)
	raw, e := readlink(kernelPath)
	if e != nil || strings.HasSuffix(raw, " (deleted)") || !filepath.IsAbs(raw) {
		return "", CodeError("process_executable_platform_unsupported")
	}
	p, e := eval(raw)
	if e != nil {
		return "", CodeError("process_executable_platform_unsupported")
	}
	kernelInfo, e := stat(kernelPath)
	if e != nil || !kernelInfo.Mode().IsRegular() {
		return "", CodeError("process_executable_platform_unsupported")
	}
	info, e := stat(p)
	if e != nil || !info.Mode().IsRegular() || !os.SameFile(kernelInfo, info) {
		return "", CodeError("process_executable_platform_unsupported")
	}
	raw2, e := readlink(kernelPath)
	if e != nil || raw2 != raw {
		return "", CodeError("process_executable_platform_unsupported")
	}
	b2, e := birth(ctx, pid)
	if e != nil || b1 != b2 {
		return "", CodeError("process_executable_platform_unsupported")
	}
	return p, nil
}

func linuxAncestryContext(ctx context.Context, pid int) (map[int]string, error) {
	return linuxAncestryContextWith(ctx, pid, linuxObserveAncestor, linuxBoot)
}
func linuxObserveAncestor(ctx context.Context, pid int) (linuxStat, int, error) {
	before, err := linuxStatFor(ctx, pid)
	if err != nil {
		return linuxStat{}, 0, err
	}
	uid, err := linuxUID(ctx, pid)
	if err != nil {
		return linuxStat{}, 0, err
	}
	after, err := linuxStatFor(ctx, pid)
	if err != nil {
		return linuxStat{}, 0, err
	}
	if before.start != after.start || before.ppid != after.ppid || before.pgid != after.pgid {
		return linuxStat{}, 0, errors.New("process_ancestry_unknown")
	}
	return before, uid, nil
}
func linuxAncestryContextWith(ctx context.Context, pid int, observe func(context.Context, int) (linuxStat, int, error), boot func(context.Context) (string, error)) (map[int]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 64*time.Second)
	defer cancel()
	if pid <= 0 {
		return nil, errors.New("process_ancestry_invalid")
	}
	bootID, err := boot(ctx)
	if err != nil {
		return nil, err
	}
	result := map[int]string{}
	for pid > 1 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(result) >= 64 {
			return nil, errors.New("process_ancestry_invalid")
		}
		if _, ok := result[pid]; ok {
			return nil, errors.New("process_ancestry_invalid")
		}
		nctx, ncancel := context.WithTimeout(ctx, time.Second)
		before, uid, err := observe(nctx, pid)
		ncancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, errors.New("process_ancestry_unknown")
		}
		if uid != os.Geteuid() {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			break
		}
		result[pid] = "linux:" + bootID + ":" + before.start
		pid = before.ppid
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return result, nil
}
func linuxGroupScan(ctx context.Context, pgid int) (bool, bool, error) {
	if pgid <= 0 {
		return false, false, ErrProcessTreeUnknown
	}
	es, e := os.ReadDir("/proc")
	if e != nil || len(es) > 131072 {
		return false, false, ErrProcessTreeUnknown
	}
	found, running := false, false
	for _, x := range es {
		if ctx.Err() != nil {
			return false, false, ctx.Err()
		}
		pid, e := strconv.Atoi(x.Name())
		if e != nil || pid <= 0 {
			continue
		}
		st, e := linuxStatFor(ctx, pid)
		if e != nil {
			return false, false, ErrProcessTreeUnknown
		}
		if st.pgid == pgid {
			found = true
			if st.state != 'Z' {
				running = true
			}
		}
	}
	return found, running, nil
}
func linuxGroupAlive(ctx context.Context, pgid int) (bool, error) {
	return linuxGroupAliveWith(ctx, pgid, syscall.Kill)
}
func linuxGroupAliveWith(ctx context.Context, pgid int, probe func(int, syscall.Signal) error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if pgid <= 0 {
		return false, ErrProcessTreeUnknown
	}
	// The kernel includes unreaped zombies in group membership. Unlike a
	// /proc listing, this observation cannot miss a member forked while an
	// unrelated listed PID exits. Quiescence still needs the conservative
	// /proc scan; absence and original Wait remain separate proofs.
	err := probe(-pgid, 0)
	if cancelled := ctx.Err(); cancelled != nil {
		return false, cancelled
	}
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, ErrProcessTreeUnknown
}
func linuxGroupQuiescent(ctx context.Context, pgid int) (bool, error) {
	f, r, e := linuxGroupScan(ctx, pgid)
	if e != nil {
		return false, ErrProcessTreeUnknown
	}
	return !f || !r, nil
}
func linuxMatchGroupLeader(ctx context.Context, pid int, birth string) error {
	st, e := linuxStatFor(ctx, pid)
	if e != nil || st.pgid != pid {
		return ErrProcessTreeUnknown
	}
	b, e := linuxBirth(ctx, pid)
	if e != nil || b != birth {
		return ErrProcessTreeUnknown
	}
	return nil
}
