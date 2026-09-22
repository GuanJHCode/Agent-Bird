//go:build darwin

package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func runningSleep(t *testing.T) (*exec.Cmd, Identity, string, string) {
	t.Helper()
	path := "/bin/sleep"
	digest, err := ExecutableDigest(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	birth, err := Birth(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, Identity{PID: cmd.Process.Pid, Birth: birth, BirthKnown: true}, path, digest
}

func TestVerifyRunningExecutableAcceptsPinnedLiveImage(t *testing.T) {
	_, identity, path, digest := runningSleep(t)
	if err := VerifyRunningExecutable(context.Background(), identity, path, digest); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRunningExecutableRejectsWrongBirth(t *testing.T) {
	_, identity, path, digest := runningSleep(t)
	identity.Birth = "wrong-birth"
	if err := VerifyRunningExecutable(context.Background(), identity, path, digest); err == nil {
		t.Fatal("accepted wrong process birth")
	}
}

func TestVerifyRunningExecutableRejectsWrongDigest(t *testing.T) {
	_, identity, path, _ := runningSleep(t)
	if err := VerifyRunningExecutable(context.Background(), identity, path, "0000"); err == nil {
		t.Fatal("accepted wrong digest")
	}
}

func TestVerifyRunningExecutableRejectsNonExecutableClaim(t *testing.T) {
	_, identity, _, digest := runningSleep(t)
	f, err := os.CreateTemp(t.TempDir(), "not-executable")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not an executable"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRunningExecutable(context.Background(), identity, f.Name(), digest); err == nil {
		t.Fatal("accepted non-executable file as image")
	}
}

func TestVerifyRunningExecutableRejectsPathReplacedAfterExec(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "live-image")
	if err := os.Link(self, path); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-test.run=TestRunningImageHelper", "--", "running-image-helper")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	time.Sleep(20 * time.Millisecond)
	birth, err := Birth(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := ExecutableDigest(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement")
	if err := copyExecutable("/bin/sleep", replacement); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	identity := Identity{PID: cmd.Process.Pid, Birth: birth, BirthKnown: true}
	if err := VerifyRunningExecutable(context.Background(), identity, path, digest); err == nil {
		t.Fatal("accepted replacement inode at the process executable path")
	}
}

func TestRunningImageHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "running-image-helper" {
		return
	}
	time.Sleep(30 * time.Second)
}

func copyExecutable(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	_, copyErr := out.ReadFrom(in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
