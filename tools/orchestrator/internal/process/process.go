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

type Command struct {
	Path  string
	Args  []string
	Dir   string
	Env   []string
	Stdin []byte
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
	cmd         *exec.Cmd
	done        chan struct{}
	mu          sync.Mutex
	exit        int
	waited      bool
	output      []byte
	outputLimit int
	pgid        int
	identity    Identity
}
type captureWriter struct{ h *Handle }

func (w captureWriter) Write(p []byte) (int, error) {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	remaining := w.h.outputLimit - len(w.h.output)
	if remaining > 0 {
		if len(p) > remaining {
			w.h.output = append(w.h.output, p[:remaining]...)
		} else {
			w.h.output = append(w.h.output, p...)
		}
	}
	return len(p), nil
}
func Start(ctx context.Context, spec Command) (*Handle, error) {
	if spec.Path == "" {
		return nil, errors.New("command_path_required")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if runtime.GOOS != "darwin" {
		return nil, CodeError("unsupported_platform")
	}
	resolved, err := filepath.EvalSymlinks(spec.Path)
	if err != nil || !filepath.IsAbs(spec.Path) || resolved != spec.Path {
		return nil, CodeError("command_path_untrusted")
	}
	digest, err := executableDigest(spec.Path)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if spec.Env != nil {
		cmd.Env = mergeEnv(os.Environ(), spec.Env)
	}
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(append([]byte(nil), spec.Stdin...))
	}
	h := &Handle{cmd: cmd, done: make(chan struct{}), exit: -1, outputLimit: 1024 * 1024}
	cmd.Stdout = captureWriter{h}
	cmd.Stderr = captureWriter{h}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	h.pgid = cmd.Process.Pid
	h.identity = Identity{PID: cmd.Process.Pid, PGID: h.pgid, Executable: resolved, ExecutableSHA256: digest, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
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
	birth, err := processBirth(cmd.Process.Pid)
	if err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = h.Stop(stopCtx)
		cancel()
		return nil, CodeError("process_birth_unknown")
	}
	h.mu.Lock()
	h.identity.Birth = birth
	h.identity.BirthKnown = true
	h.mu.Unlock()
	return h, nil
}
func (h *Handle) PID() int           { return h.cmd.Process.Pid }
func (h *Handle) PGID() int          { return h.pgid }
func (h *Handle) Identity() Identity { h.mu.Lock(); defer h.mu.Unlock(); return h.identity }
func (h *Handle) ExitCode() int      { h.mu.Lock(); defer h.mu.Unlock(); return h.exit }
func (h *Handle) Output() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return string(append([]byte(nil), h.output...))
}
func (h *Handle) Wait(ctx context.Context) error {
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (h *Handle) Stop(ctx context.Context) error {
	h.mu.Lock()
	waited := h.waited
	h.mu.Unlock()
	if waited {
		return nil
	}
	if h.pgid <= 0 {
		return ErrProcessTreeUnknown
	}
	if err := syscall.Kill(-h.pgid, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return ErrProcessTreeUnknown
	}
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
	}
	_ = syscall.Kill(-h.pgid, syscall.SIGKILL)
	grace, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-h.done:
		return nil
	case <-grace.Done():
		return ErrProcessTreeUnknown
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

func executableDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func processBirth(pid int) (string, error) {
	if pid <= 0 {
		return "", CodeError("process_birth_unknown")
	}
	out, err := exec.Command("/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", CodeError("process_birth_unknown")
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		return "", CodeError("process_birth_unknown")
	}
	return strings.TrimSpace(lines[0]), nil
}

// Birth returns the kernel-reported macOS process start time for pid.
func Birth(pid int) (string, error) { return processBirth(pid) }
