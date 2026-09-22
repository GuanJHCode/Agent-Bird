//go:build darwin

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"unsafe"
)

func TestKernelStartIDUsesDocumentedProcBSDInfoABI(t *testing.T) {
	if got := unsafe.Sizeof(procBSDInfo{}); got != 136 {
		t.Fatalf("proc_bsdinfo size = %d, want 136", got)
	}
	if got := unsafe.Offsetof(procBSDInfo{}.StartSec); got != 120 {
		t.Fatalf("pbi_start_tvsec offset = %d, want 120", got)
	}
}

func TestKernelStartIDRawProcInfoFillsFullABI(t *testing.T) {
	var info procBSDInfo
	returned, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, procInfoCallPIDInfo, uintptr(os.Getpid()), procPIDTBSDInfo, 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if errno != 0 || returned != unsafe.Sizeof(info) || info.PID != uint32(os.Getpid()) || info.StartSec == 0 || info.StartUSec >= 1_000_000 {
		t.Fatalf("proc_info returned=%d errno=%v pid=%d sec=%d usec=%d", returned, errno, info.PID, info.StartSec, info.StartUSec)
	}
}

func TestKernelStartIDIsStableForCurrentProcess(t *testing.T) {
	first, err := KernelStartID(os.Getpid())
	if err != nil || first == "" {
		t.Fatalf("first KernelStartID = %q, %v", first, err)
	}
	second, err := KernelStartID(os.Getpid())
	if err != nil || second != first {
		t.Fatalf("second KernelStartID = %q, %v; want %q", second, err, first)
	}
}

func TestKernelStartIDDiffersForNewChild(t *testing.T) {
	parent, err := KernelStartID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	got, err := KernelStartID(child.Process.Pid)
	if err != nil || got == "" {
		t.Fatalf("child KernelStartID = %q, %v", got, err)
	}
	if got == parent {
		t.Fatalf("child reused parent start identity %q", got)
	}
}

func TestKernelStartIDRejectsInvalidPID(t *testing.T) {
	for _, pid := range []int{-1, 0, 999999999} {
		if got, err := KernelStartID(pid); err == nil || got != "" {
			t.Fatalf("KernelStartID(%d) = %q, %v; want error", pid, got, err)
		}
	}
}

func TestKernelStartIDWorksWhenSandboxDeniesProcessExec(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	profile := "(version 1)\n" +
		"(deny default)\n" +
		"(import \"dyld-support.sb\")\n" +
		"(allow process-fork)\n" +
		"(allow signal (target self))\n" +
		"(allow sysctl-read)\n" +
		"(allow file-read* (subpath \"/System\"))\n" +
		"(allow file-read* (subpath \"/usr/lib\"))\n" +
		"(allow file-read* (subpath \"/private\"))\n" +
		"(allow file-read* (subpath \"/private/etc\"))\n" +
		"(allow file-map-executable (subpath \"/System/Library\"))\n" +
		"(allow file-map-executable (subpath \"/usr/lib\"))\n" +
		"(allow file-read* (literal \"/dev/null\"))\n" +
		"(allow file-read* (literal \"/dev/urandom\"))\n" +
		"(allow file-read* (literal " + strconv.Quote(executable) + "))\n" +
		"(allow file-map-executable (literal " + strconv.Quote(executable) + "))\n" +
		"(allow process-exec (literal " + strconv.Quote(executable) + "))\n" +
		"(deny process-exec (literal \"/bin/ps\"))\n"
	cmd := exec.Command("/usr/bin/sandbox-exec", "-p", profile, executable, "-test.run=^TestKernelStartIDSandboxHelper$", "--", "kernel-start-sandbox-helper")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sandboxed helper: %v\n%s", err, output)
	}
}

func TestKernelStartIDSandboxHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "kernel-start-sandbox-helper" {
		return
	}
	if _, err := KernelStartID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
}
