//go:build linux

package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestLinuxGroupNativeFixture(t *testing.T) {
	if os.Getenv("LINUX_GROUP_NATIVE_FIXTURE") == "" {
		return
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(io.LimitReader(os.Stdin, 32)).ReadString('\n')
	if err != nil || line != "release\n" {
		t.Fatalf("release handshake=%q err=%v", line, err)
	}
}

type nativeGroupProcess struct {
	cmd      *exec.Cmd
	in       io.WriteCloser
	birth    string
	drain    <-chan error
	waitOnce sync.Once
	waitDone chan struct{}
	waitErr  error
}

// Wait starts only on demand: the zombie must be observed before it is reaped.
// Every normal and cleanup path joins the same original Cmd.Wait exactly once.
func (p *nativeGroupProcess) wait(ctx context.Context) error {
	p.waitOnce.Do(func() { go func() { p.waitErr = p.cmd.Wait(); close(p.waitDone) }() })
	select {
	case <-p.waitDone:
		return p.waitErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *nativeGroupProcess) release(t *testing.T) {
	t.Helper()
	if _, err := fmt.Fprintln(p.in, "release"); err != nil {
		t.Fatal(err)
	}
	if err := p.in.Close(); err != nil {
		t.Fatal(err)
	}
}
func nativeGroupCmd(t *testing.T, pgid int) *nativeGroupProcess {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestLinuxGroupNativeFixture$")
	c.Env = append(os.Environ(), "LINUX_GROUP_NATIVE_FIXTURE=1")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	in, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		in.Close()
		t.Fatal(err)
	}
	// Cmd.Wait does not own or forcibly close this reader. EOF proves that both
	// child stdout and stderr writers closed, independently of the Wait receipt.
	c.Stdout, c.Stderr = writer, writer
	if err = c.Start(); err != nil {
		in.Close()
		reader.Close()
		writer.Close()
		t.Fatal(err)
	}
	p := &nativeGroupProcess{cmd: c, in: in, waitDone: make(chan struct{})}
	t.Cleanup(func() {
		p.in.Close()
		select {
		case <-p.waitDone:
		default:
			// The original unreaped child cannot have its PID reused.
			t.Errorf("fixture needed forced cleanup pid=%d", c.Process.Pid)
			_ = c.Process.Kill()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if e := p.wait(ctx); errors.Is(e, context.DeadlineExceeded) {
				t.Errorf("original Wait did not join: %v", e)
			}
		}
		reader.Close()
	})
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan error, 1)
	drained := make(chan error, 1)
	p.drain = drained
	go func() {
		defer reader.Close()
		r := bufio.NewReader(io.LimitReader(reader, 4097))
		line, e := r.ReadString('\n')
		if e == nil && line != "ready\n" {
			e = fmt.Errorf("unexpected ready %q", line)
		}
		ready <- e
		rest, readErr := io.ReadAll(r)
		if readErr == nil && len(line)+len(rest) >= 4097 {
			readErr = errors.New("fixture output exceeded bound before EOF")
		}
		drained <- readErr
	}()
	select {
	case e := <-ready:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fixture ready timeout")
	}
	p.birth, err = Birth(c.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := syscall.Getpgid(c.Process.Pid)
	if pgid == 0 {
		pgid = c.Process.Pid
	}
	if err != nil || actual != pgid {
		t.Fatalf("fixture group=%d want=%d err=%v", actual, pgid, err)
	}
	t.Logf("live pid=%d pgid=%d birth=%s", c.Process.Pid, actual, p.birth)
	return p
}
func nativeWaitAndDrain(t *testing.T, p *nativeGroupProcess) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.wait(ctx); err != nil {
		t.Fatal(err)
	}
	if p.cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("original Wait exit=%d", p.cmd.ProcessState.ExitCode())
	}
	select {
	case err := <-p.drain:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("original stdout/stderr did not reach natural EOF")
	}
	t.Logf("original Wait pid=%d exit=0; stdout/stderr natural EOF; no forced cleanup", p.cmd.Process.Pid)
}
func TestLinuxNativeGroupLeaderGoneMemberLive(t *testing.T) {
	leader := nativeGroupCmd(t, 0)
	member := nativeGroupCmd(t, leader.cmd.Process.Pid)
	leader.release(t)
	nativeWaitAndDrain(t, leader)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if quiet, err := GroupQuiescent(ctx, leader.cmd.Process.Pid); err != nil || quiet {
		t.Fatalf("leader gone/live member quiet=%v err=%v", quiet, err)
	}
	if err := ConfirmGroupExitedContext(ctx, leader.cmd.Process.Pid); !errors.Is(err, ErrProcessTreeUnknown) {
		t.Fatalf("live member exit confirmation=%v", err)
	}
	if birth, err := Birth(member.cmd.Process.Pid); err != nil || birth != member.birth {
		t.Fatalf("original live member changed birth=%s err=%v", birth, err)
	}
	t.Log("leader original Wait complete; same-birth member keeps group nonquiescent/UNKNOWN")
	member.release(t)
	nativeWaitAndDrain(t, member)
	if err := ConfirmGroupExitedContext(ctx, leader.cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	t.Log("both original Wait complete; final group absent")
}
func TestLinuxNativeGroupZombieNeedsOriginalWait(t *testing.T) {
	leader := nativeGroupCmd(t, 0)
	leader.release(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		st, err := linuxStatFor(ctx, leader.cmd.Process.Pid)
		if err == nil && st.state == 'Z' {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("leader did not become zombie")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := MatchWaitableGroupLeader(ctx, leader.cmd.Process.Pid, leader.birth); err != nil {
		t.Fatal(err)
	}
	if quiet, err := GroupQuiescent(ctx, leader.cmd.Process.Pid); err != nil || !quiet {
		t.Fatalf("zombie quiescent=%v err=%v", quiet, err)
	}
	if err := ConfirmGroupExitedContext(ctx, leader.cmd.Process.Pid); !errors.Is(err, ErrProcessTreeUnknown) {
		t.Fatalf("unreaped zombie exit confirmation=%v", err)
	}
	t.Log("same-birth unreaped Z leader is quiescent, group exit remains UNKNOWN")
	nativeWaitAndDrain(t, leader)
	if err := ConfirmGroupExitedContext(ctx, leader.cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	t.Log("original Wait reaped zombie; final group absent")
}
