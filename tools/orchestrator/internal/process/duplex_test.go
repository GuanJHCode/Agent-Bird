package process

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const duplexFixtureLifetime = 10 * time.Second

// Only an owned test executable is launched. Helper modes have an independent
// finite lifetime even if the parent assertion fails. No provider is invoked.
func TestDuplexOSHelper(t *testing.T) {
	mode := os.Getenv("C06_DUPLEX_HELPER")
	if mode == "" {
		return
	}
	time.AfterFunc(duplexFixtureLifetime, func() { os.Exit(3) })
	switch mode {
	case "echo":
		_, _ = os.Stderr.WriteString("owned-stderr-marker")
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "quick":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("q"), 4096))
	case "held":
		_, _ = os.Stdout.WriteString("ready\n")
		select {}
	case "exact-env":
		value, present := os.LookupEnv("GIT_DIR")
		if !present {
			value = "unset"
		}
		_, _ = os.Stdout.WriteString(value)
	case "drain-leader":
		terminated := make(chan os.Signal, 1)
		signal.Notify(terminated, syscall.SIGTERM)
		child := exec.Command(os.Args[0], "-test.run=^TestDuplexOSHelper$")
		child.Env = []string{"C06_DUPLEX_HELPER=drain-child", "C03_RELEASE=" + os.Getenv("C03_RELEASE"), "C03_CHILD_READY=" + os.Getenv("C03_CHILD_READY"), "C03_CHILD_TERM=" + os.Getenv("C03_CHILD_TERM")}
		if err := child.Start(); err != nil {
			os.Exit(13)
		}
		go func() { _ = child.Wait() }()
		<-terminated
	case "drain-child":
		terminated := make(chan os.Signal, 1)
		signal.Notify(terminated, syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("C03_CHILD_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(14)
		}
		for {
			select {
			case <-terminated:
				if err := os.WriteFile(os.Getenv("C03_CHILD_TERM"), []byte("received"), 0600); err != nil {
					os.Exit(15)
				}
			default:
			}
			if _, err := os.Stat(os.Getenv("C03_RELEASE")); err == nil {
				os.Exit(0)
			}
			time.Sleep(5 * time.Millisecond)
		}
	case "stderr":
		_, _ = os.Stdout.WriteString("stdout-exclusive-marker")
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("e"), 2<<20))
	case "descendant":
		for {
			if _, err := os.Stat(os.Getenv("C06_DUPLEX_RELEASE")); err == nil {
				os.Exit(0)
			}
			time.Sleep(5 * time.Millisecond)
		}
	case "peer-anchor-leader":
		child := exec.Command(os.Args[0], "-test.run=^TestDuplexOSHelper$")
		child.Env = []string{"C06_DUPLEX_HELPER=peer-anchor-child", "C06_DUPLEX_RELEASE=" + os.Getenv("C06_DUPLEX_RELEASE")}
		if err := child.Start(); err != nil {
			os.Exit(16)
		}
		if err := os.WriteFile(os.Getenv("C06_DUPLEX_CHILD_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(17)
		}
		if err := child.Wait(); err != nil {
			os.Exit(18)
		}
	case "peer-anchor-child":
		for {
			if _, err := os.Stat(os.Getenv("C06_DUPLEX_RELEASE")); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	case "group":
		child := exec.Command(os.Args[0], "-test.run=^TestDuplexOSHelper$")
		child.Env = []string{"C06_DUPLEX_HELPER=descendant", "C06_DUPLEX_RELEASE=" + os.Getenv("C06_DUPLEX_RELEASE")}
		// Inherit the leader's original group, but no pipe descriptor. EOF and
		// real leader Wait can finish while this child is demonstrably alive.
		if err := child.Start(); err != nil {
			os.Exit(4)
		}
		_ = os.WriteFile(os.Getenv("C06_DUPLEX_CHILD_PID"), []byte(fmt.Sprint(child.Process.Pid)), 0600)
	case "term-ignore":
		signal.Ignore(syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("G1_ESC_MARKER"), []byte("ready"), 0600); err != nil {
			os.Exit(7)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "term-tree":
		signal.Ignore(syscall.SIGTERM)
		child := exec.Command(os.Args[0], "-test.run=^TestDuplexOSHelper$")
		child.Env = []string{"C06_DUPLEX_HELPER=term-ignore", "G1_ESC_MARKER=" + os.Getenv("G1_TREE_CHILD_MARKER")}
		if err := child.Start(); err != nil {
			os.Exit(10)
		}
		go func() { _ = child.Wait() }()
		ready := false
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if body, err := os.ReadFile(os.Getenv("G1_TREE_CHILD_MARKER")); err == nil && string(body) == "ready" {
				ready = true
				break
			}
			time.Sleep(time.Millisecond)
		}
		if !ready {
			os.Exit(11)
		}
		if err := os.WriteFile(os.Getenv("G1_ESC_MARKER"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(12)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "term-clean":
		terminated := make(chan os.Signal, 1)
		signal.Notify(terminated, syscall.SIGTERM)
		defer signal.Stop(terminated)
		if err := os.WriteFile(os.Getenv("G1_ESC_MARKER"), []byte("started"), 0600); err != nil {
			os.Exit(8)
		}
		<-terminated
		time.Sleep(300 * time.Millisecond)
		if err := os.WriteFile(os.Getenv("G1_ESC_MARKER"), []byte("started\ndone"), 0600); err != nil {
			os.Exit(9)
		}
	case "rpc":
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(scanner.Bytes(), &request) != nil {
				os.Exit(5)
			}
			if request.Method == "fixture/block" {
				_ = os.WriteFile(os.Getenv("C06_DUPLEX_DISPATCHED"), []byte("received"), 0600)
				select {}
			}
			body, _ := json.Marshal(struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
			}{request.ID, request.Params})
			_, _ = os.Stdout.Write(append(body, '\n'))
		}
	default:
		os.Exit(6)
	}
	os.Exit(0)
}

func duplexCommand(t *testing.T, mode string) Command {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("original process authority is unsupported on this platform")
	}
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return Command{Path: binary, Args: []string{"-test.run=^TestDuplexOSHelper$"}, ExactEnv: true, Env: []string{"C06_DUPLEX_HELPER=" + mode}}
}

func ownDuplex(t *testing.T, spec Command) (*Handle, io.ReadWriteCloser, context.Context) {
	t.Helper()
	warmDuplexPoller(t)
	beforeFD := duplexFDCount(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	t.Cleanup(cancel)
	h, stream, err := StartDuplex(ctx, spec)
	if err != nil {
		if stream != nil {
			_ = stream.Close()
		}
		if h != nil {
			stopCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			_ = h.Stop(stopCtx)
			stop()
		}
		t.Fatalf("supported duplex must start: %v", err)
	}
	if h == nil || stream == nil {
		t.Fatal("missing original Handle or duplex stream")
	}
	t.Cleanup(func() {
		_ = stream.Close()
		stopCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := h.Stop(stopCtx); err != nil {
			t.Errorf("owned helper cleanup lacks original proof: %v", err)
		}
		if err := h.Wait(stopCtx); err != nil {
			t.Errorf("owned helper original Wait: %v", err)
		}
		proofErr := h.ConfirmTreeExited()
		if proofErr != nil {
			t.Errorf("owned helper group remains: %v", proofErr)
		}
		h.mu.Lock()
		waited := h.waited
		h.mu.Unlock()
		identity := h.Identity()
		afterFD := duplexFDCount(t)
		t.Logf("original_process_cleanup pid=%d pgid=%d birth_known=%t original_wait=%t group_absent=%t fd_before=%d fd_after=%d", identity.PID, identity.PGID, identity.BirthKnown, waited, proofErr == nil, beforeFD, afterFD)
		if afterFD != beforeFD {
			t.Errorf("successful duplex lifecycle leaked descriptors: before=%d after=%d", beforeFD, afterFD)
		}
	})
	return h, stream, ctx
}

func TestStartDuplexEchoOwnsOriginalHandleAndStdout(t *testing.T) {
	h, stream, ctx := ownDuplex(t, duplexCommand(t, "echo"))
	input := []byte("owned bytes\x00\nexact UTF-8 数据\n")
	if n, err := stream.Write(input); err != nil || n != len(input) {
		t.Fatalf("stdin write=%d err=%v", n, err)
	}
	output := make([]byte, len(input))
	if _, err := io.ReadFull(stream, output); err != nil {
		t.Fatalf("stdout read: %v", err)
	}
	if !bytes.Equal(input, output) {
		t.Fatal("duplex changed exact bytes")
	}
	identity := h.Identity()
	if identity.PID != h.PID() || identity.PGID != h.PGID() || !identity.BirthKnown || identity.Birth == "" || identity.ExecutableSHA256 == "" {
		t.Fatal("duplex omitted the original process authority")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if h.ExitCode() != 0 || h.ConfirmTreeExited() != nil {
		t.Fatal("missing original successful Wait/group proof")
	}
	if h.Output() != "owned-stderr-marker" {
		t.Fatal("raw stdout entered legacy Output or stderr was lost")
	}
}

func TestStartDuplexDrainsAfterOriginalQuickWait(t *testing.T) {
	h, stream, ctx := ownDuplex(t, duplexCommand(t, "quick"))
	if err := h.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	// Original Wait has finished before the consumer first drains stdout.
	output, err := io.ReadAll(stream)
	if err != nil || !bytes.Equal(output, bytes.Repeat([]byte("q"), 4096)) {
		t.Fatalf("Wait closed/consumed unread child stdout: length=%d err=%v", len(output), err)
	}
	if h.Output() != "" || h.ConfirmTreeExited() != nil {
		t.Fatal("stdout copied or EOF substituted for original proof")
	}
}

type duplexIOOutcome struct {
	n   int
	err error
}

func duplexBlockedRead(stream io.Reader, done chan<- duplexIOOutcome) {
	var data [1]byte
	n, err := stream.Read(data[:])
	done <- duplexIOOutcome{n, err}
}

func duplexBlockedWrite(stream io.Writer, done chan<- duplexIOOutcome) {
	n, err := stream.Write(bytes.Repeat([]byte("w"), 4<<20))
	done <- duplexIOOutcome{n, err}
}

func waitDuplexRealIOBarrier(t *testing.T, symbol string, done <-chan duplexIOOutcome) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		stack := make([]byte, 256<<10)
		stack = stack[:runtime.Stack(stack, true)]
		for _, goroutine := range strings.Split(string(stack), "\n\n") {
			if strings.Contains(goroutine, symbol) && strings.Contains(goroutine, "[IO wait]") && strings.Contains(goroutine, "internal/poll.runtime_pollWait") {
				return // Real OS-pipe poll wait, not a pre-call signal or sleep.
			}
		}
		select {
		case result := <-done:
			t.Fatalf("IO finished before an actual blocking barrier: n=%d err=%v", result.n, result.err)
		case <-deadline.C:
			t.Fatal("original OS pipe did not reach the required blocking phase")
		case <-ticker.C:
		}
	}
}

func TestDuplexCloseInterruptsBlockedRealReadAndWrite(t *testing.T) {
	h, stream, _ := ownDuplex(t, duplexCommand(t, "held"))
	ready := make([]byte, 6)
	if _, err := io.ReadFull(stream, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("actual helper ready barrier: %v", err)
	}
	readDone, writeDone := make(chan duplexIOOutcome, 1), make(chan duplexIOOutcome, 1)
	go duplexBlockedRead(stream, readDone)
	go duplexBlockedWrite(stream, writeDone)
	waitDuplexRealIOBarrier(t, "duplexBlockedRead", readDone)
	waitDuplexRealIOBarrier(t, "duplexBlockedWrite", writeDone)
	closeDone := make(chan error, 1)
	started := time.Now()
	go func() { closeDone <- stream.Close() }()
	for _, done := range []<-chan duplexIOOutcome{readDone, writeDone} {
		select {
		case result := <-done:
			if result.err == nil {
				t.Fatal("closed blocked pipe returned success")
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not promptly interrupt original blocked IO")
		}
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close waited for the still-living child")
	}
	if time.Since(started) > time.Second {
		t.Fatal("Close consumed process-exit grace")
	}
	if h.ConfirmTreeExited() == nil {
		t.Fatal("Close falsely became a process-tree receipt")
	}
}

func TestDuplexConcurrentRepeatedCloseAndOriginalStop(t *testing.T) {
	h, stream, _ := ownDuplex(t, duplexCommand(t, "held"))
	ready := make([]byte, 6)
	if _, err := io.ReadFull(stream, ready); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 17)
	for range 16 {
		wg.Go(func() { errorsSeen <- stream.Close() })
	}
	wg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		errorsSeen <- h.Stop(ctx)
	})
	joined := make(chan struct{})
	go func() { wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(3 * time.Second):
		t.Fatal("Close/Stop overlap did not join")
	}
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Errorf("Close/Stop error: %v", err)
		}
	}
	if h.ConfirmTreeExited() != nil {
		t.Fatal("Stop lost original Wait/group proof")
	}
}

func TestStartDuplexStderrBoundIsSeparateFromStdout(t *testing.T) {
	h, stream, ctx := ownDuplex(t, duplexCommand(t, "stderr"))
	stdout, err := io.ReadAll(stream)
	if err != nil || string(stdout) != "stdout-exclusive-marker" {
		t.Fatalf("exclusive stdout length=%d err=%v", len(stdout), err)
	}
	if err := h.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	stderr := h.Output()
	if len(stderr) != 1<<20 || strings.Trim(stderr, "e") != "" {
		t.Fatal("stderr was unbounded or contained stdout")
	}
}

func duplexFDCount(t *testing.T) int {
	t.Helper()
	directory, err := os.Open("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	files, err := directory.Readdirnames(-1)
	if err != nil {
		t.Fatal(err)
	}
	return len(files)
}

func warmDuplexPoller(t *testing.T) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = writer.Close()
}

func TestStartDuplexRejectsBeforeSpawnAndClosesLaunchFailureFDs(t *testing.T) {
	for _, mode := range []string{"canceled", "pin", "stdin", "stdout", "start_failure"} {
		t.Run(mode, func(t *testing.T) {
			spec := duplexCommand(t, "held")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var want error
			switch mode {
			case "canceled":
				cancel()
				want = context.Canceled
			case "pin":
				spec.PinnedPath = spec.Path
				spec.PinnedSHA256 = strings.Repeat("0", 64)
				want = CodeError("binary_pin_mismatch")
			case "stdin":
				spec.Stdin = []byte{}
				want = CodeError("duplex_stdio_conflict")
			case "stdout":
				spec.Stdout = new(bytes.Buffer)
				want = CodeError("duplex_stdio_conflict")
			case "start_failure":
				spec.Dir = filepath.Join(t.TempDir(), "absent")
				want = os.ErrNotExist
			}
			warmDuplexPoller(t) // Separate permanent runtime poller FDs from leaks.
			before := duplexFDCount(t)
			for range 16 {
				h, stream, err := StartDuplex(ctx, spec)
				if h != nil || stream != nil || !errors.Is(err, want) {
					t.Fatalf("failure admitted/spawned or lost reason: handle=%v stream=%v err=%v", h != nil, stream != nil, err)
				}
			}
			if after := duplexFDCount(t); after != before {
				t.Fatalf("launch/validation failure leaked descriptors: before=%d after=%d", before, after)
			}
		})
	}
}

func TestDuplexEOFAndCloseCannotProveHeldDescendantExited(t *testing.T) {
	root := t.TempDir()
	release, childFile := filepath.Join(root, "release"), filepath.Join(root, "child-pid")
	spec := duplexCommand(t, "group")
	spec.Env = append(spec.Env, "C06_DUPLEX_RELEASE="+release, "C06_DUPLEX_CHILD_PID="+childFile)
	h, stream, ctx := ownDuplex(t, spec)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	if err := h.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	stdout, err := io.ReadAll(stream)
	if err != nil || len(stdout) != 0 {
		t.Fatalf("expected actual leader EOF: length=%d err=%v", len(stdout), err)
	}
	childBytes, err := os.ReadFile(childFile)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(string(childBytes))
	if err != nil || childPID <= 0 {
		t.Fatal("missing owned descendant identity")
	}
	if birth, err := processBirth(childPID); err != nil || birth == "" {
		t.Fatal("descendant was not actually alive at EOF")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.ConfirmTreeExited(); !errors.Is(err, ErrProcessTreeUnknown) {
		t.Fatalf("EOF/Close erased original live group: %v", err)
	}
	bounded, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = h.Stop(bounded)
	cancel()
	if !errors.Is(err, ErrProcessTreeUnknown) {
		t.Fatalf("held descendant received a false tree receipt: %v", err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	proofCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := h.Stop(proofCtx); err != nil {
		t.Fatalf("released original group did not retain its original Wait proof: %v", err)
	}
}

func TestDuplexBirthFailureClosesFDsAndRetainsOriginalHandle(t *testing.T) {
	spec := duplexCommand(t, "quick")
	warmDuplexPoller(t)
	beforeFD := duplexFDCount(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observedPID := 0
	var observationErr error
	// Drive the production launcher and an actual birth observation, then
	// inject only the private per-launch observation error. No global hook,
	// fabricated Handle, second launcher or false OS-failure claim is used.
	h, stream, err := startCommand(ctx, spec, true, func(pid int) (string, error) {
		observedPID = pid
		birth, err := processBirth(pid)
		if err != nil || birth == "" {
			observationErr = errors.New("original birth could not be observed")
		}
		return "", errors.New("owned birth observation failure")
	})
	if !errors.Is(err, CodeError("process_birth_unknown")) || stream != nil || h == nil {
		t.Fatalf("post-launch error erased identity or left stream open: handle=%t stream=%t err=%v", h != nil, stream != nil, err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if h.Stop(stopCtx) != nil || h.Wait(stopCtx) != nil || h.ConfirmTreeExited() != nil {
			t.Error("birth-failure fixture lacks original cleanup proof")
		}
	})
	if observationErr != nil {
		t.Fatal(observationErr)
	}
	identity := h.Identity()
	if identity.PID != observedPID || identity.PGID != observedPID || identity.ExecutableSHA256 == "" || identity.BirthKnown {
		t.Fatal("post-launch error lost original identity or forged birth")
	}
	if h.Wait(ctx) != nil || h.ConfirmTreeExited() != nil {
		t.Fatal("birth-failure helper was not reaped by its original Wait")
	}
	afterFD := duplexFDCount(t)
	t.Logf("birth_failure_cleanup pid=%d pgid=%d birth_known=%t original_wait=true group_absent=true fd_before=%d fd_after=%d", identity.PID, identity.PGID, identity.BirthKnown, beforeFD, afterFD)
	if afterFD != beforeFD {
		t.Fatalf("birth failure leaked descriptors: before=%d after=%d", beforeFD, afterFD)
	}
}
