//go:build darwin

package process

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDarwinNativeBirthFormatAndFallback(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	for _, zone := range []string{"Asia/Shanghai", "UTC"} {
		t.Setenv("TZ", zone)
		got, err := Birth(os.Getpid())
		raw, e := exec.Command("/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(os.Getpid())).Output()
		if err != nil || e != nil || got != strings.TrimSpace(string(raw)) {
			t.Fatalf("zone=%s native=%q legacy=%q errors=%v/%v", zone, got, raw, err, e)
		}
	}
	for _, zone := range []string{"", "UTC0", ":/etc/localtime"} {
		t.Setenv("TZ", zone)
		if _, native := darwinBirthLocation(); native {
			t.Fatalf("unsupported TZ accepted: %q", zone)
		}
	}
	t.Setenv("TZ", "UTC")
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	if _, native := darwinBirthLocation(); native {
		t.Fatal("non-C identity format silently changed")
	}
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_TIME", "C")
	t.Setenv("LANG", "fr_FR.UTF-8")
	if _, native := darwinBirthLocation(); !native {
		t.Fatal("LC_TIME precedence lost")
	}
}

func TestDarwinNativeWaitableZombie(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	pid := cmd.Process.Pid
	for {
		k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err == nil && k.Proc.P_stat == 5 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("original child did not become waitable zombie", err)
		}
		time.Sleep(time.Millisecond)
	}
	birth, err := BirthContext(ctx, pid)
	if err != nil || MatchWaitableGroupLeader(ctx, pid, birth) != nil {
		t.Fatalf("zombie identity unavailable: %q %v", birth, err)
	}
	quiet, err := GroupQuiescent(ctx, pid)
	if err != nil || !quiet {
		t.Fatalf("zombie group not quiescent: %v %v", quiet, err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	waited = true
	t.Logf("child_pid=%d original_wait=0", pid)
	if err = ConfirmGroupExitedContext(ctx, pid); err != nil {
		t.Fatal(err)
	}
}
