package gitops

import (
	"context"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This is a live original-owner join, never a wire-controlled launch runner.
type FilterTargetCarrier interface {
	GitConfigArgs() []string
	BindCommand(context.Context, string) (string, error)
	Activate(context.Context, *process.Handle, string) error
	Drain(context.Context) error
	Remove(context.Context, *process.Handle) error
}

func (t *FilterTargetTransition) MergeWithCarrier(ctx context.Context, c FilterTargetCarrier) (CommandResult, error) {
	if c == nil {
		return CommandResult{}, fail(CodeInvalidInput, "filter transition carrier", nil)
	}
	args := append([]string(nil), c.GitConfigArgs()...)
	if len(args) != 8 {
		return CommandResult{}, fail(CodeInvalidInput, "filter transition config", nil)
	}
	if t == nil || t.pair == nil {
		return CommandResult{}, fail(CodeIdentityChanged, "filter transition owner", nil)
	}
	prefix := "filter." + t.pair.target.source.manifest.DriverID + "."
	for i := 0; i < len(args); i += 2 {
		if args[i] != "-c" {
			return CommandResult{}, fail(CodeInvalidInput, "filter transition config", nil)
		}
	}
	if !strings.HasPrefix(args[1], prefix+"process=") || len(args[1]) <= len(prefix+"process=") || args[3] != prefix+"required=true" || args[5] != prefix+"clean=" || args[7] != prefix+"smudge=" {
		return CommandResult{}, fail(CodeInvalidInput, "filter transition config", nil)
	}
	cleanup := func(fn func(context.Context) error) error {
		bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return fn(bounded)
	}
	return t.MergeWithHooks(ctx, FilterTargetTransitionHooks{configArgs: args, bindCommand: c.BindCommand,
		Activate:  func(h *process.Handle, d string) error { return c.Activate(ctx, h, d) },
		AfterWait: func(_ *process.Handle, _ string) error { return cleanup(c.Drain) },
		BeforeExited: func(h *process.Handle, _ string) error {
			return cleanup(func(ctx context.Context) error { return c.Remove(ctx, h) })
		},
	})
}

type transitionDirectoryPin struct {
	path string
	file *os.File
	id   DirectoryIdentity
}

func transitionContext(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}
func (t *FilterTargetTransition) enter() bool { return t != nil && t.self == t && t.mu.TryLock() }
func (t *FilterTargetTransition) captureDirectories() error {
	m := t.pair.target
	paths := []string{m.source.commonDir, filepath.Dir(filepath.Join(m.source.commonDir, filepath.FromSlash(m.request.Ref)))}
	for _, path := range paths {
		if err := noSymlinkComponents(path); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		id, err := filterTargetDirectory(f)
		if err != nil {
			f.Close()
			return err
		}
		t.directories = append(t.directories, transitionDirectoryPin{path, f, id})
	}
	return t.verifyDirectories()
}
func (t *FilterTargetTransition) verifyDirectories() error {
	for _, pin := range t.directories {
		held, err := filterTargetDirectory(pin.file)
		named, ne := directoryIdentity(pin.path)
		if err != nil || ne != nil || held != pin.id || named != pin.id || CanonicalPath(pin.path) != pin.path {
			return fail(CodeIdentityChanged, "filter transition directory", errors.Join(err, ne))
		}
	}
	return nil
}
func (t *FilterTargetTransition) verifyImmutableRefs() error {
	m := t.pair.target
	branch := filepath.Join(m.source.commonDir, filepath.FromSlash(m.request.Ref))
	for name, pin := range m.refs {
		if name == branch {
			continue
		}
		if pin == nil {
			if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return fail(CodeIdentityChanged, "filter transition absent ref", err)
			}
		} else if !verifyFilterPin(*pin) {
			return fail(CodeIdentityChanged, "filter transition immutable ref", nil)
		}
	}
	return nil
}

// Post-merge queries retain the original pinned Git/Wait/tree ownership without
// inheriting the readonly target guard or invoking status/clean filters.
func (t *FilterTargetTransition) query(ctx context.Context, args ...string) (CommandResult, error) {
	m := t.pair.target
	argv := append([]string{"-C", m.request.Worktree}, args...)
	env := append([]string(nil), os.Environ()...)
	digest := DigestDetail(m.source.git.path + "\x00" + m.source.git.sha256 + "\x00" + m.request.Worktree + "\x00" + strings.Join(env, "\x00") + "\x00" + strings.Join(argv, "\x00"))
	record := func(phase string, id *process.Identity, exit int) error {
		if err := m.journal.RecordGitProcess(ctx, GitProcessRecord{Phase: phase, CommandDigest: digest, Process: id, ExitCode: exit}); err != nil {
			return err
		}
		return t.verifyRunning(ctx)
	}
	if err := record("intent", nil, -1); err != nil {
		return CommandResult{}, err
	}
	out := &candidateOutput{}
	p, err := process.Start(ctx, process.Command{Path: m.source.git.path, PinnedPath: m.source.git.path, PinnedSHA256: m.source.git.sha256, Dir: m.request.Worktree, Args: argv, Env: env, ExactEnv: true, Stdout: out})
	if err != nil {
		if p != nil {
			bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return CommandResult{}, errors.Join(process.ErrProcessTreeUnknown, process.CleanupFailedStart(bounded, p, 10*time.Second))
		}
		return CommandResult{}, candidateStartFailure(err)
	}
	err = finishCandidateGitStream(ctx, p, record)
	out.mu.Lock()
	text, overflow := out.String(), out.overflow
	out.mu.Unlock()
	if overflow {
		return CommandResult{}, fail(CodeInvalidInput, "filter transition query output", nil)
	}
	return CommandResult{PID: p.PID(), ExitCode: p.ExitCode(), Stdout: strings.TrimSpace(text)}, err
}
