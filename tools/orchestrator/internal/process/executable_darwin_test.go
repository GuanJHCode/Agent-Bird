//go:build darwin

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExecutablePathUsesKernelPath(t *testing.T) {
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err = filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ExecutablePath(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	got, err = filepath.EvalSymlinks(got)
	if err != nil || got != want {
		t.Fatalf("self executable mismatch: %v", err)
	}
	cmd := exec.Command("/bin/sleep", "60")
	cmd.Args[0] = "not-the-executable-private-sentinel"
	cmd.Env = []string{"PRIVATE_SENTINEL=must-not-be-read"}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	got, err = ExecutablePath(cmd.Process.Pid)
	if err != nil || got != "/bin/sleep" {
		t.Fatalf("kernel executable mismatch: %v", err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	for _, pid := range []int{0, -1, cmd.Process.Pid} {
		if _, err = ExecutablePath(pid); err == nil {
			t.Fatalf("invalid or exited PID %d accepted", pid)
		}
	}
}
