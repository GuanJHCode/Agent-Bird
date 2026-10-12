//go:build linux

package process

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestLinuxGroupFixture(t *testing.T) {
	if os.Getenv("LINUX_GROUP_FIXTURE") == "" {
		return
	}
	fmt.Fprintln(os.Stdout, "ready")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func TestLinuxGroupQuiescentAndExitedForWaitedLeader(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestLinuxGroupFixture$")
	cmd.Env = append(os.Environ(), "LINUX_GROUP_FIXTURE=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if _, err = bufio.NewReader(out).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if quiet, err := GroupQuiescent(ctx, cmd.Process.Pid); err != nil || quiet {
		t.Fatalf("live group quiet=%v err=%v", quiet, err)
	}
	if _, err = fmt.Fprintln(in, "exit"); err != nil {
		t.Fatal(err)
	}
	_ = in.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	waited = true
	if err = ConfirmGroupExitedContext(ctx, cmd.Process.Pid); err != nil {
		t.Fatalf("waited group did not exit: %v", err)
	}
}
