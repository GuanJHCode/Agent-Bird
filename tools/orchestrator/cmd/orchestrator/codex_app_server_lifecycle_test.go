package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// The descendant itself reports its kernel identity on a pipe independent of
// RPC stdout. An initialize response alone is not a descendant-ready receipt.
type appServerChildReady struct {
	PID   int
	PGID  int
	Birth string
}

func appServerFixture(t *testing.T, mode string) (*appServerRPC, func() error, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := appServerCommand
	appServerCommand = func(context.Context) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAppServerFixtureProcess$")
		cmd.Env = append(os.Environ(), "APP_SERVER_FIXTURE="+mode, "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
		cmd.ExtraFiles = []*os.File{writer}
		return cmd
	}
	t.Cleanup(func() { appServerCommand = old; _ = reader.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rpc, closeRPC, err := startCodexAppServer(ctx)
	_ = writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeRPC() })
	return rpc, closeRPC, reader
}

func waitAppServerChildReady(reader *os.File, pgid int, budget time.Duration) (appServerChildReady, error) {
	var ready appServerChildReady
	if err := reader.SetReadDeadline(time.Now().Add(budget)); err != nil {
		return ready, err
	}
	if err := json.NewDecoder(reader).Decode(&ready); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return ready, errors.New("child_ready_timeout")
		}
		if errors.Is(err, io.EOF) {
			return ready, errors.New("child_exited_before_ready")
		}
		return ready, fmt.Errorf("child_ready_invalid: %w", err)
	}
	birth, birthErr := process.Birth(ready.PID)
	actualPGID, groupErr := syscall.Getpgid(ready.PID)
	if ready.PID <= 0 || ready.PID == pgid || ready.PGID != pgid || groupErr != nil || actualPGID != pgid || birthErr != nil || birth != ready.Birth || ready.Birth == "" {
		return ready, errors.New("child_ready_identity_invalid")
	}
	return ready, nil
}

func cleanupAppServerChild(t *testing.T, ready appServerChildReady) {
	t.Helper()
	t.Cleanup(func() {
		// Only signal the actual recorded child while its birth still matches.
		// Never use a file's old PID or an unanchored group as cleanup authority.
		if birth, err := process.Birth(ready.PID); err == nil && birth == ready.Birth {
			_ = syscall.Kill(ready.PID, syscall.SIGKILL)
		}
	})
}

func testAppServerCloseWithChild(t *testing.T, mode string) {
	t.Helper()
	rpc, closeRPC, pipe := appServerFixture(t, mode)
	ready, err := waitAppServerChildReady(pipe, rpc.pid, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAppServerChild(t, ready)
	started := time.Now()
	if err := closeRPC(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("close exceeded existing stop windows: %v", elapsed)
	}
	if birth, err := process.Birth(ready.PID); err == nil && birth == ready.Birth {
		t.Fatal("recorded descendant survived close")
	}
	if err := process.ConfirmGroupExited(ready.PGID); err != nil {
		t.Fatalf("close did not prove group exit: %v", err)
	}
}

func TestAppServerCloseEscalatesForChildIgnoringTERM(t *testing.T) {
	testAppServerCloseWithChild(t, "ignore-term")
}

func TestAppServerChildReadinessDistinguishesExitAndTimeout(t *testing.T) {
	for _, mode := range []string{"early-exit", "never-ready"} {
		t.Run(mode, func(t *testing.T) {
			rpc, _, pipe := appServerFixture(t, mode)
			_, err := waitAppServerChildReady(pipe, rpc.pid, 100*time.Millisecond)
			want := "child_exited_before_ready"
			if mode == "never-ready" {
				want = "child_ready_timeout"
			}
			if err == nil || err.Error() != want {
				t.Fatalf("readiness error = %v, want %s", err, want)
			}
		})
	}
}

func TestAppServerInitializeDoesNotEstablishChildReadiness(t *testing.T) {
	rpc, closeRPC, pipe := appServerFixture(t, "gated")
	if _, err := waitAppServerChildReady(pipe, rpc.pid, 100*time.Millisecond); err == nil || err.Error() != "child_ready_timeout" {
		t.Fatalf("initialize unexpectedly proved child readiness: %v", err)
	}
	// The gate releases child creation explicitly, without a scheduling sleep.
	if err := rpc.notify("fixture/release-child", map[string]any{}); err != nil {
		t.Fatalf("fixture_release_child_failed_before_ready: %v", err)
	}
	ready, err := waitAppServerChildReady(pipe, rpc.pid, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAppServerChild(t, ready)
	if err := closeRPC(); err != nil {
		t.Fatal(err)
	}
	if err := process.ConfirmGroupExited(ready.PGID); err != nil {
		t.Fatal(err)
	}
}

func TestAppServerCloseRejectsChangedLeaderIdentity(t *testing.T) {
	rpc, closeRPC, pipe := appServerFixture(t, "exit")
	ready, err := waitAppServerChildReady(pipe, rpc.pid, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAppServerChild(t, ready)
	rpc.birth = "not-the-recorded-birth"
	if err := closeRPC(); errorCode(err) != "app_server_process_tree_unknown" {
		t.Fatalf("changed identity close = %v", err)
	}
	if birth, err := process.Birth(ready.PID); err != nil || birth != ready.Birth {
		t.Fatal("close signaled descendants without a matching leader identity")
	}
}

func TestAppServerInitializeFailureReapsProcess(t *testing.T) {
	old := appServerCommand
	defer func() { appServerCommand = old }()
	var cmd *exec.Cmd
	appServerCommand = func(context.Context) *exec.Cmd {
		cmd = exec.Command(os.Args[0], "-test.run=^TestAppServerFixtureProcess$")
		cmd.Env = append(os.Environ(), "APP_SERVER_FIXTURE=bad-init", "GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
		return cmd
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := startCodexAppServer(ctx)
	if errorCode(err) != "app_server_protocol_failed" {
		t.Fatalf("initialize failure = %v", err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatal("failed initialize left its leader unreaped")
	}
	if err := process.ConfirmGroupExited(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
}

func TestAppServerCloseRetainsExitedLeaderAsIdentityAnchor(t *testing.T) {
	rpc, closeRPC, pipe := appServerFixture(t, "exit")
	ready, err := waitAppServerChildReady(pipe, rpc.pid, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAppServerChild(t, ready)
	deadline := time.Now().Add(time.Second)
	for {
		out, err := exec.Command("/bin/ps", "-o", "stat=", "-p", fmt.Sprint(rpc.pid)).Output()
		if err != nil {
			t.Fatal("leader was reaped before descendant cleanup; its PGID is no longer anchored")
		}
		if strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture leader did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if birth, err := process.Birth(rpc.pid); err != nil || birth != rpc.birth {
		t.Fatalf("waitable leader identity unavailable: %q %v", birth, err)
	}
	if runtime.GOOS == "darwin" {
		if _, err := syscall.Getpgid(rpc.pid); err != syscall.ESRCH {
			t.Fatalf("Darwin zombie getpgid = %v, want ESRCH", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := process.MatchWaitableGroupLeader(ctx, rpc.pid, rpc.birth); err != nil {
		t.Fatalf("waitable zombie group identity unavailable: %v", err)
	}
	if err := closeRPC(); err != nil {
		t.Fatal(err)
	}
	if _, err := process.Birth(rpc.pid); err == nil {
		t.Fatal("leader was not reaped after cleanup")
	}
}

func TestAppServerFixtureProcess(t *testing.T) {
	mode := os.Getenv("APP_SERVER_FIXTURE")
	if mode == "" {
		return
	}
	ready := os.NewFile(3, "child-ready")
	if strings.HasPrefix(mode, "child-") {
		if mode == "child-ignore-term" {
			signal.Ignore(syscall.SIGTERM)
		}
		birth, err := process.Birth(os.Getpid())
		if err != nil {
			os.Exit(71)
		}
		pgid, err := syscall.Getpgid(os.Getpid())
		if err != nil || json.NewEncoder(ready).Encode(appServerChildReady{PID: os.Getpid(), PGID: pgid, Birth: birth}) != nil {
			os.Exit(72)
		}
		_ = ready.Close()
		for {
			time.Sleep(time.Hour)
		}
	}
	input := bufio.NewReader(os.Stdin)
	if _, err := input.ReadString('\n'); err != nil {
		os.Exit(73)
	}
	if mode == "bad-init" {
		_, _ = fmt.Fprintln(os.Stdout, `{"id":1,"result":{"codexHome":"relative"}}`)
		_, _ = io.Copy(io.Discard, input)
		os.Exit(0)
	}
	_, _ = fmt.Fprintln(os.Stdout, `{"id":1,"result":{"codexHome":"/private/test"}}`)
	if _, err := input.ReadString('\n'); err != nil {
		os.Exit(74)
	}
	if mode == "early-exit" {
		os.Exit(0)
	}
	if mode == "never-ready" {
		_, _ = io.Copy(io.Discard, input)
		os.Exit(0)
	}
	if mode == "gated" {
		if _, err := input.ReadString('\n'); err != nil {
			os.Exit(76)
		}
	}
	child := exec.Command(os.Args[0], "-test.run=^TestAppServerFixtureProcess$")
	child.Env = append(os.Environ(), "APP_SERVER_FIXTURE=child-"+mode)
	child.Stdout = os.Stdout // Deliberately inherited from the RPC leader.
	child.ExtraFiles = []*os.File{ready}
	if err := child.Start(); err != nil {
		os.Exit(75)
	}
	_ = ready.Close()
	os.Exit(0)
}
