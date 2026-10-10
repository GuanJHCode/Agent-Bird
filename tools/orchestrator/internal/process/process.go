package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type CodeError string

func (e CodeError) Error() string { return string(e) }

const ErrProcessTreeUnknown CodeError = "process_tree_unknown"

// OutputStream identifies the child stream supplied to OutputObserver.
type OutputStream uint8

const (
	OutputStdout OutputStream = 1
	OutputStderr OutputStream = 2
)

// OutputObserver receives a borrowed, read-only slice only for the duration of
// the callback. It must be non-blocking and safe for concurrent stdout/stderr
// calls. Its result cannot affect child I/O.
type OutputObserver func(OutputStream, []byte)

type Command struct {
	SealedPath   string
	SealedSHA256 string
	PinnedPath   string
	PinnedSHA256 string
	Path         string
	Args         []string
	Dir          string
	Env          []string
	ExactEnv     bool
	Stdin        []byte
	// OwnedStdin transfers exclusive read ownership through the original Wait.
	// The caller must never mutate/reuse its backing array after Start, including
	// uncertain returns. Unlike Stdin, this Go-only input is not copied.
	OwnedStdin    []byte `json:"-"`
	Stdout        io.Writer
	ObserveOutput OutputObserver `json:"-"`
}
type Identity struct {
	PID              int
	PGID             int
	Birth            string
	BirthKnown       bool
	Executable       string
	ExecutableSHA256 string
	StartedAt        string
}
type Handle struct {
	originalSpawnedAt time.Time
	duplexLaunch      *DuplexLaunchState
	cmd               *exec.Cmd
	done              chan struct{}
	mu                sync.Mutex
	exit              int
	waited            bool
	output            []byte
	outputLimit       int
	outputTruncated   bool
	pgid              int
	identity          Identity
	stdout            io.Writer
	outputObserver    OutputObserver
	peerAnchor        *peerAnchor
	peerRevoked       bool
}
type captureWriter struct {
	h      *Handle
	stdout bool
}

func (w captureWriter) Write(p []byte) (int, error) {
	w.h.mu.Lock()
	remaining := w.h.outputLimit - len(w.h.output)
	if len(p) > remaining {
		w.h.outputTruncated = true
	}
	if remaining > 0 {
		if len(p) > remaining {
			w.h.output = append(w.h.output, p[:remaining]...)
		} else {
			w.h.output = append(w.h.output, p...)
		}
	}
	downstream := io.Writer(nil)
	if w.stdout {
		downstream = w.h.stdout
	}
	observer := w.h.outputObserver
	w.h.mu.Unlock()
	if observer != nil {
		stream := OutputStderr
		if w.stdout {
			stream = OutputStdout
		}
		observer(stream, p)
	}
	if downstream != nil {
		_, _ = downstream.Write(p)
	}
	return len(p), nil
}

// Start returns the original Handle whenever cmd.Start succeeded, including
// observation errors afterwards. Callers must retain and clean up a non-nil
// Handle even when err is non-nil; an error alone is not no-start evidence.
func Start(ctx context.Context, spec Command) (*Handle, error) {
	return startWithBirth(ctx, spec, processBirth)
}

func startWithBirth(ctx context.Context, spec Command, birthLookup func(int) (string, error)) (*Handle, error) {
	h, _, err := startCommand(ctx, spec, false, birthLookup)
	return h, err
}

// Both modes retain the same launcher and original Wait. birthLookup is a
// per-launch observation seam; production always uses processBirth.
func startCommand(ctx context.Context, spec Command, duplex bool, birthLookup func(int) (string, error)) (*Handle, *duplexStream, error) {
	return startCommandUsing(ctx, spec, duplex, birthLookup, func(cmd *exec.Cmd) error { return cmd.Start() })
}
func startCommandUsing(ctx context.Context, spec Command, duplex bool, birthLookup func(int) (string, error), starter func(*exec.Cmd) error) (*Handle, *duplexStream, error) {
	return startCommandUsingWithPeerAnchor(ctx, spec, duplex, birthLookup, starter, capturePeerAnchor)
}

func startCommandUsingWithPeerAnchor(ctx context.Context, spec Command, duplex bool, birthLookup func(int) (string, error), starter func(*exec.Cmd) error, anchorCapture func(context.Context, int) (peerAnchor, error)) (*Handle, *duplexStream, error) {
	if spec.Stdin != nil && spec.OwnedStdin != nil {
		return nil, nil, CodeError("stdin_ownership_conflict")
	}
	if duplex && (spec.Stdin != nil || spec.OwnedStdin != nil || spec.Stdout != nil) {
		return nil, nil, CodeError("duplex_stdio_conflict")
	}
	if spec.Path == "" {
		return nil, nil, errors.New("command_path_required")
	}
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	default:
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, nil, CodeError("unsupported_platform")
	}
	resolved, err := filepath.EvalSymlinks(spec.Path)
	if err != nil || !filepath.IsAbs(spec.Path) || resolved != spec.Path {
		return nil, nil, CodeError("command_path_untrusted")
	}
	digest, err := ExecutableDigest(ctx, spec.Path)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if spec.ExactEnv {
		cmd.Env = append([]string{}, spec.Env...)
	} else if spec.Env != nil {
		cmd.Env = mergeEnv(os.Environ(), spec.Env)
	}
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(append([]byte(nil), spec.Stdin...))
	} else if spec.OwnedStdin != nil {
		cmd.Stdin = bytes.NewReader(spec.OwnedStdin)
	}
	h := &Handle{cmd: cmd, done: make(chan struct{}), exit: -1, outputLimit: 1024 * 1024, stdout: spec.Stdout, outputObserver: spec.ObserveOutput}
	var pipes *duplexPipes
	keepStream := false
	if duplex {
		pipes, err = newDuplexPipes()
		if err != nil {
			return nil, nil, err
		}
		pipes.stream.outputObserver = h.outputObserver
		defer func() {
			_ = pipes.closeChildren()
			if !keepStream {
				_ = pipes.stream.Close()
			}
		}()
		cmd.Stdin, cmd.Stdout = pipes.childRead, pipes.childWrite
	} else {
		cmd.Stdout = captureWriter{h: h, stdout: true}
	}
	cmd.Stderr = captureWriter{h: h}
	// Recheck the provider after all wrappers/probes have been assembled. The
	// wrapper executable's own identity is separate from the provider lock.
	if spec.PinnedPath != "" || spec.PinnedSHA256 != "" {
		canonical, pinErr := filepath.EvalSymlinks(spec.PinnedPath)
		actual, hashErr := ExecutableDigest(ctx, spec.PinnedPath)
		if pinErr != nil || canonical != spec.PinnedPath || hashErr != nil || !strings.EqualFold(actual, spec.PinnedSHA256) {
			return nil, nil, CodeError("binary_pin_mismatch")
		}
	}
	if spec.SealedPath != "" || spec.SealedSHA256 != "" {
		resolved, e := filepath.EvalSymlinks(spec.SealedPath)
		info, statErr := os.Lstat(spec.SealedPath)
		parent, parentErr := os.Lstat(filepath.Dir(spec.SealedPath))
		safe := statErr == nil && parentErr == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0500 && parent.IsDir() && parent.Mode().Perm() == 0500
		if safe {
			st, ok := info.Sys().(*syscall.Stat_t)
			pst, pok := parent.Sys().(*syscall.Stat_t)
			safe = ok && pok && st.Uid == uint32(os.Geteuid()) && pst.Uid == uint32(os.Geteuid()) && st.Nlink == 1
		}
		actual, hashErr := ExecutableDigest(ctx, spec.SealedPath)
		if !safe || e != nil || resolved != spec.SealedPath || hashErr != nil || actual != spec.SealedSHA256 {
			return nil, nil, CodeError("validation_tool_pin_changed")
		}
	}
	startErr := starter(cmd)
	if startErr != nil && cmd.Process == nil {
		return nil, nil, neverStartedError(ctx, spec, cmd, startErr)
	}
	if cmd.Process == nil {
		return nil, nil, CodeError("process_start_without_process")
	}
	// Keep the original monotonic boundary before child-descriptor/birth
	// observation or post-Start cleanup, for both ordinary and duplex owners.
	spawnedAt := time.Now()
	h.mu.Lock()
	h.originalSpawnedAt = spawnedAt
	if duplex {
		h.duplexLaunch = &DuplexLaunchState{SpawnedAt: spawnedAt}
	}
	h.mu.Unlock()
	var pipeCloseErr error
	if pipes != nil {
		pipeCloseErr = pipes.closeChildren()
	}
	h.pgid = cmd.Process.Pid
	// This is deliberately best-effort. Existing Start authority remains usable
	// if the private carrier anchor cannot be obtained at the original boundary.
	if anchorCapture != nil {
		if anchor, anchorErr := anchorCapture(ctx, cmd.Process.Pid); anchorErr == nil && anchor.pid == cmd.Process.Pid && anchor.pgid == h.pgid {
			h.mu.Lock()
			h.peerAnchor = &anchor
			h.mu.Unlock()
		}
	}
	startedAt := ""
	if duplex {
		startedAt = h.duplexLaunch.SpawnedAt.UTC().Format(time.RFC3339Nano)
	} else {
		startedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	h.identity = Identity{PID: cmd.Process.Pid, PGID: h.pgid, Executable: resolved, ExecutableSHA256: digest, StartedAt: startedAt}
	// Keep the child waitable until its birth is captured: even a child that
	// has already exited retains its PID and start time until Wait reaps it.
	birth, birthErr := birthLookup(cmd.Process.Pid)
	if birthErr == nil {
		h.identity.Birth, h.identity.BirthKnown = birth, true
	}
	go func() {
		err := cmd.Wait()
		h.mu.Lock()
		h.waited = true
		if cmd.ProcessState != nil {
			h.exit = cmd.ProcessState.ExitCode()
			if h.exit < 0 {
				if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
					h.exit = -int(ws.Signal())
				}
			}
		} else if err != nil {
			h.exit = -1
		}
		h.mu.Unlock()
		close(h.done)
	}()
	if birthErr != nil || pipeCloseErr != nil || startErr != nil {
		if pipes != nil {
			_ = pipes.stream.Close()
		}
		if duplex {
			stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			stopResult := h.Stop(stopCtx)
			completedAt := time.Now()
			deadline, _ := stopCtx.Deadline()
			h.mu.Lock()
			h.duplexLaunch.StartupCleanup = &DuplexStartupCleanup{Context: stopCtx, Deadline: deadline, Result: stopResult, CompletedAt: completedAt}
			h.mu.Unlock()
			cancel()
		}
		if birthErr != nil {
			return h, nil, CodeError("process_birth_unknown")
		}
		if startErr != nil {
			return h, nil, errors.Join(startErr, pipeCloseErr)
		}
		return h, nil, errors.Join(CodeError("duplex_child_pipe_close_failed"), pipeCloseErr)
	}
	if pipes != nil {
		keepStream = true
		return h, pipes.stream, nil
	}
	return h, nil, nil
}
func (h *Handle) PID() int           { return h.cmd.Process.Pid }
func (h *Handle) PGID() int          { return h.pgid }
func (h *Handle) Identity() Identity { h.mu.Lock(); defer h.mu.Unlock(); return h.identity }
func (h *Handle) ExitCode() int      { h.mu.Lock(); defer h.mu.Unlock(); return h.exit }
func (h *Handle) Output() string {
	output, _ := h.OutputSnapshot()
	return output
}

// OutputSnapshot returns one consistent bounded capture and whether any bytes
// have actually been discarded. Reaching the cap alone is not truncation.
func (h *Handle) OutputSnapshot() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return string(append([]byte(nil), h.output...)), h.outputTruncated
}
func (h *Handle) Wait(ctx context.Context) error {
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ConfirmTreeExited proves that the reaped leader's process group has no
// surviving members. A leader exit alone is not a process-tree receipt.
func (h *Handle) ConfirmTreeExited() error {
	h.mu.Lock()
	waited := h.waited
	h.mu.Unlock()
	if !waited {
		return ErrProcessTreeUnknown
	}
	alive, err := processGroupAlive(h.pgid)
	if err != nil || alive {
		return ErrProcessTreeUnknown
	}
	return nil
}

// stopOperations is a per-call seam for the birth/signal race. Production
// always binds the original observations; tests do not mutate global hooks.
type stopOperations struct {
	birth         func(int) (string, error)
	signal        func(int, syscall.Signal) error
	groupAlive    func(int) (bool, error)
	waitGroupGone func(context.Context, int) bool
}

func nativeStopOperations() stopOperations {
	return stopOperations{birth: processBirth, signal: syscall.Kill, groupAlive: processGroupAlive, waitGroupGone: waitProcessGroupGone}
}

func (h *Handle) Stop(ctx context.Context) error {
	return h.stopWith(ctx, nativeStopOperations())
}

func (h *Handle) stopWith(ctx context.Context, ops stopOperations) error {
	h.mu.Lock()
	// A Stop attempt ends carrier authority even when the subsequent signal or
	// receipt proof fails. This avoids authorizing a peer during an in-flight
	// ownership transition.
	h.peerRevoked = true
	waited := h.waited
	identity := h.identity
	h.mu.Unlock()
	if waited {
		return h.confirmStopped(ctx, ops)
	}
	if h.pgid <= 0 {
		return ErrProcessTreeUnknown
	}
	birth, err := ops.birth(identity.PID)
	if err != nil || !identity.BirthKnown || birth != identity.Birth {
		return h.confirmStopped(ctx, ops)
	}
	if err := ops.signal(-h.pgid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			// Another stop can end the owned group after our birth check.
			// ESRCH is only a reason to seek proof, never proof by itself.
			return h.confirmStopped(ctx, ops)
		}
		return ErrProcessTreeUnknown
	}
	if ops.waitGroupGone(ctx, h.pgid) {
		return h.confirmStopped(ctx, ops)
	}
	birth, err = ops.birth(identity.PID)
	if err != nil || birth != identity.Birth {
		return ErrProcessTreeUnknown
	}
	_ = ops.signal(-h.pgid, syscall.SIGKILL)
	grace, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if ops.waitGroupGone(grace, h.pgid) {
		return h.confirmStopped(grace, ops)
	}
	return ErrProcessTreeUnknown
}

// A numeric process-group observation cannot replace the original cmd.Wait.
// Recheck absence after Wait, keeping all observations in the same bounded
// stage. No Handle mutex is held across waits or OS observations. As before,
// this proves the owned group only; macOS setsid escapes require containment.
func (h *Handle) confirmStopped(ctx context.Context, ops stopOperations) error {
	if ctx.Err() != nil || h.pgid <= 0 {
		return ErrProcessTreeUnknown
	}
	alive, err := ops.groupAlive(h.pgid)
	if ctx.Err() != nil || err != nil {
		return ErrProcessTreeUnknown
	}
	// Another original stop may already have removed the leader while its
	// group is still draining. Seek proof passively in this stage's remaining
	// context; never signal a drifted identity or create another grace window.
	if alive {
		if _, bounded := ctx.Deadline(); !bounded || !ops.waitGroupGone(ctx, h.pgid) {
			return ErrProcessTreeUnknown
		}
	}
	if _, bounded := ctx.Deadline(); !bounded {
		// Absence does not prove original Wait completion. Without a caller
		// deadline, preserve UNKNOWN unless that proof is already ready.
		select {
		case <-h.done:
		default:
			return ErrProcessTreeUnknown
		}
	}
	if h.Wait(ctx) != nil || ctx.Err() != nil {
		return ErrProcessTreeUnknown
	}
	alive, err = ops.groupAlive(h.pgid)
	if ctx.Err() != nil || err != nil || alive {
		return ErrProcessTreeUnknown
	}
	return nil
}

// ConfirmGroupExited checks the OS group without sending a signal that can
// mutate it. An exited leader alone does not establish descendant termination.
func ConfirmGroupExited(pgid int) error {
	alive, err := processGroupAlive(pgid)
	if err != nil || alive {
		return ErrProcessTreeUnknown
	}
	return nil
}

func processGroupAlive(pgid int) (bool, error) {
	if runtime.GOOS == "linux" {
		return linuxGroupAlive(context.Background(), pgid)
	}
	if pgid <= 0 {
		return false, ErrProcessTreeUnknown
	}
	err := syscall.Kill(-pgid, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return processGroupListed(pgid)
	}
	return false, err
}

func processGroupListed(pgid int) (bool, error) {
	out, err := queryProcessGroups(context.Background(), "pgid=", pgid)
	if err != nil || len(out) > 1024*1024 {
		return false, ErrProcessTreeUnknown
	}
	want := strconv.Itoa(pgid)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == want {
			return true, nil
		}
	}
	return false, nil
}

func waitProcessGroupGone(ctx context.Context, pgid int) bool {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		alive, err := processGroupAlive(pgid)
		if err != nil {
			return false
		}
		if !alive {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

func mergeEnv(base, overrides []string) []string {
	m := map[string]string{}
	order := []string{}
	for _, v := range base {
		if i := indexByte(v, '='); i > 0 {
			key := v[:i]
			if _, ok := m[key]; !ok {
				order = append(order, key)
			}
			m[key] = v[i+1:]
		}
	}
	for _, v := range overrides {
		if i := indexByte(v, '='); i > 0 {
			key := v[:i]
			if _, ok := m[key]; !ok {
				order = append(order, key)
			}
			m[key] = v[i+1:]
		}
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+m[k])
	}
	return out
}
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// ExecutableDigest rejects devices/FIFOs before reading and hashes with bounded
// memory. Used before wrappers are applied and again at the actual exec boundary.
func ExecutableDigest(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Size() < 1 || info.Size() > 512*1024*1024 {
		return "", CodeError("command_executable_invalid")
	}
	h := sha256.New()
	buf := make([]byte, 64*1024)
	var size int64
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		size += int64(n)
		if size > 512*1024*1024 {
			return "", CodeError("command_executable_invalid")
		}
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	after, err := f.Stat()
	if err != nil {
		return "", err
	}
	if size != info.Size() || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		return "", CodeError("command_executable_changed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func processBirth(pid int) (string, error) {
	return BirthContext(context.Background(), pid)
}

// Birth returns the kernel-reported macOS process start time for pid.
func Birth(pid int) (string, error) { return processBirth(pid) }
