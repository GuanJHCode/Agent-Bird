package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type candidateRunnerKey struct{}
type trackedIntegrationRunnerKey struct{}
type candidateJournalKey struct{}

type GitProcessRecord struct {
	Phase         string            `json:"phase"`
	CommandDigest string            `json:"command_digest"`
	Process       *process.Identity `json:"process,omitempty"`
	ExitCode      int               `json:"exit_code"`
}

type GitProcessJournal interface {
	RecordGitProcess(context.Context, GitProcessRecord) error
}

func WithCandidateJournal(ctx context.Context, journal GitProcessJournal) context.Context {
	return context.WithValue(ctx, candidateJournalKey{}, journal)
}

type candidateOutput struct {
	mu sync.Mutex
	bytes.Buffer
	overflow bool
}

func (out *candidateOutput) Write(p []byte) (int, error) {
	out.mu.Lock()
	defer out.mu.Unlock()
	if out.Len()+len(p) > 16*1024*1024 {
		out.overflow = true
		return len(p), nil
	}
	return out.Buffer.Write(p)
}

func candidateGit(ctx context.Context, dir string, stdin io.Reader, args ...string) (CommandResult, error) {
	filter := filterGitFromContext(ctx)
	if filter != nil {
		if err := filter.verifyDirectory(dir); err != nil {
			return CommandResult{}, err
		}
		if err := filter.Verify(ctx); err != nil {
			return CommandResult{}, err
		}
		result, err := candidateGitWithStart(ctx, dir, stdin, process.Start, args...)
		if verifyErr := filter.Verify(context.Background()); verifyErr != nil {
			return result, verifyErr
		}
		return result, err
	}
	return candidateGitWithStart(ctx, dir, stdin, process.Start, args...)
}

func candidateGitWithStart(ctx context.Context, dir string, stdin io.Reader, start func(context.Context, process.Command) (*process.Handle, error), args ...string) (CommandResult, error) {
	var input []byte
	var err error
	if stdin != nil {
		input, err = io.ReadAll(io.LimitReader(stdin, 16*1024*1024+1))
		if err != nil || len(input) > 16*1024*1024 {
			return CommandResult{}, fail(CodeInvalidInput, "candidate git", fmt.Errorf("source file exceeds freeze limit"))
		}
	}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "GIT_") {
			env = append(env, entry)
		}
	}
	env = append(env, "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0")
	gitPath := "/usr/bin/git"
	pinnedPath, pinnedSHA := "", ""
	argv := append([]string{"--no-replace-objects", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", dir}, args...)
	if filter := filterGitFromContext(ctx); filter != nil {
		if tracked, _ := ctx.Value(trackedIntegrationRunnerKey{}).(bool); tracked {
			return CommandResult{}, fail(CodeInvalidInput, "filter git", fmt.Errorf("tracked integration unsupported"))
		}
		gitPath, pinnedPath, pinnedSHA, env, argv = filter.git.path, filter.git.path, filter.git.sha256, filterGitEnv(), append(filterGitConfigArgs(filter.manifest.DriverID), append([]string{"-C", dir}, args...)...)
	}
	if tracked, _ := ctx.Value(trackedIntegrationRunnerKey{}).(bool); tracked {
		// Final integration retains the normal Git configuration/environment,
		// including environment-provided hooks and signing. Share only process
		// ownership, bounded output and journal handling with candidate Git.
		env = os.Environ()
		argv = append([]string{"-C", dir}, args...)
	}
	commandDigest := DigestDetail(dir + "\x00" + strings.Join(argv, "\x00"))
	journal, _ := ctx.Value(candidateJournalKey{}).(GitProcessJournal)
	record := func(phase string, identity *process.Identity, exit int) error {
		if journal == nil {
			return nil
		}
		return journal.RecordGitProcess(context.Background(), GitProcessRecord{Phase: phase, CommandDigest: commandDigest, Process: identity, ExitCode: exit})
	}
	if err = record("intent", nil, -1); err != nil {
		return CommandResult{}, err
	}
	out := &candidateOutput{}
	p, err := start(ctx, process.Command{Path: gitPath, PinnedPath: pinnedPath, PinnedSHA256: pinnedSHA, Args: argv, Dir: dir, Env: env, ExactEnv: true, Stdin: input, Stdout: out})
	if err != nil {
		if p != nil {
			identity := p.Identity()
			_ = record("unknown", &identity, p.ExitCode())
			cleanupErr := process.CleanupFailedStart(ctx, p, 10*time.Second)
			return CommandResult{PID: p.PID(), ExitCode: p.ExitCode()}, errors.Join(process.ErrProcessTreeUnknown, cleanupErr)
		}
		return CommandResult{}, candidateStartFailure(err)
	}
	identity := p.Identity()
	stop := func() error {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return p.Stop(stopCtx)
	}
	if err = record("spawned", &identity, -1); err != nil {
		if stopErr := stop(); stopErr != nil {
			return CommandResult{}, process.ErrProcessTreeUnknown
		}
		return CommandResult{}, err
	}
	if err = p.Wait(ctx); err != nil {
		if stopErr := stop(); stopErr != nil {
			_ = record("unknown", &identity, p.ExitCode())
			return CommandResult{}, process.ErrProcessTreeUnknown
		}
	}
	if treeErr := p.ConfirmTreeExited(); treeErr != nil {
		_ = record("unknown", &identity, p.ExitCode())
		return CommandResult{}, process.ErrProcessTreeUnknown
	}
	if recordErr := record("exited", &identity, p.ExitCode()); recordErr != nil {
		return CommandResult{}, recordErr
	}
	result := CommandResult{ExitCode: p.ExitCode(), PID: p.PID()}
	out.mu.Lock()
	result.Stdout = out.String()
	overflow := out.overflow
	out.mu.Unlock()
	if err != nil {
		return result, err
	}
	if overflow {
		return result, fail(CodeInvalidInput, "candidate git", fmt.Errorf("git output exceeds freeze limit"))
	}
	if result.ExitCode != 0 {
		return result, fail(CodeGitFailure, "candidate git", fmt.Errorf("git command failed"))
	}
	return result, nil
}

func freezeGit(ctx context.Context, dir string, stdin io.Reader, args ...string) (string, error) {
	result, err := candidateGit(ctx, dir, stdin, args...)
	return result.Stdout, err
}

// candidateGitStream keeps Git object bytes out of the command-result cache.
// process retains its existing bounded diagnostic capture while the supplied
// writer consumes data incrementally under the same process/journal contract.
func candidateGitStream(ctx context.Context, dir string, out io.Writer, args ...string) error {
	filter := filterGitFromContext(ctx)
	if filter != nil {
		if err := filter.verifyDirectory(dir); err != nil {
			return err
		}
		if err := filter.Verify(ctx); err != nil {
			return err
		}
		err := candidateGitStreamWithStart(ctx, dir, out, process.Start, args...)
		if verifyErr := filter.Verify(context.Background()); verifyErr != nil {
			return verifyErr
		}
		return err
	}
	return candidateGitStreamWithStart(ctx, dir, out, process.Start, args...)
}

func candidateGitStreamWithStart(ctx context.Context, dir string, out io.Writer, start func(context.Context, process.Command) (*process.Handle, error), args ...string) error {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "GIT_") {
			env = append(env, entry)
		}
	}
	env = append(env, "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0")
	gitPath := "/usr/bin/git"
	pinnedPath, pinnedSHA := "", ""
	argv := append([]string{"--no-replace-objects", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "-C", dir}, args...)
	if filter := filterGitFromContext(ctx); filter != nil {
		if tracked, _ := ctx.Value(trackedIntegrationRunnerKey{}).(bool); tracked {
			return fail(CodeInvalidInput, "filter git", fmt.Errorf("tracked integration unsupported"))
		}
		gitPath, pinnedPath, pinnedSHA, env, argv = filter.git.path, filter.git.path, filter.git.sha256, filterGitEnv(), append(filterGitConfigArgs(filter.manifest.DriverID), append([]string{"-C", dir}, args...)...)
	}
	digest := DigestDetail(dir + "\x00" + strings.Join(argv, "\x00"))
	journal, _ := ctx.Value(candidateJournalKey{}).(GitProcessJournal)
	record := func(phase string, identity *process.Identity, exit int) error {
		if journal == nil {
			return nil
		}
		return journal.RecordGitProcess(context.Background(), GitProcessRecord{Phase: phase, CommandDigest: digest, Process: identity, ExitCode: exit})
	}
	if err := record("intent", nil, -1); err != nil {
		return err
	}
	p, err := start(ctx, process.Command{Path: gitPath, PinnedPath: pinnedPath, PinnedSHA256: pinnedSHA, Args: argv, Dir: dir, Env: env, ExactEnv: true, Stdout: out})
	if err != nil {
		if p != nil {
			identity := p.Identity()
			_ = record("unknown", &identity, p.ExitCode())
			cleanupErr := process.CleanupFailedStart(ctx, p, 10*time.Second)
			return errors.Join(process.ErrProcessTreeUnknown, cleanupErr)
		}
		return candidateStartFailure(err)
	}
	return finishCandidateGitStream(ctx, p, record)
}

// The production caller always supplies the original process.Handle. The
// narrow interface lets fault tests exercise uncertain Stop and Wait outcomes
// without manufacturing process identities or signaling unrelated processes.
type candidateStreamProcess interface {
	Identity() process.Identity
	ExitCode() int
	Wait(context.Context) error
	Stop(context.Context) error
	ConfirmTreeExited() error
}

func finishCandidateGitStream(ctx context.Context, p candidateStreamProcess, record func(string, *process.Identity, int) error) error {
	identity := p.Identity()
	if err := record("spawned", &identity, -1); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := p.Stop(stopCtx); stopErr != nil {
			_ = record("unknown", &identity, p.ExitCode())
			return process.ErrProcessTreeUnknown
		}
		return err
	}
	waitErr := p.Wait(ctx)
	if waitErr != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := p.Stop(stopCtx); stopErr != nil {
			_ = record("unknown", &identity, p.ExitCode())
			return process.ErrProcessTreeUnknown
		}
	}
	if err := p.ConfirmTreeExited(); err != nil {
		_ = record("unknown", &identity, p.ExitCode())
		return process.ErrProcessTreeUnknown
	}
	if err := record("exited", &identity, p.ExitCode()); err != nil {
		return err
	}
	if waitErr != nil {
		return waitErr
	}
	if p.ExitCode() != 0 {
		return fail(CodeGitFailure, "candidate git", fmt.Errorf("git command failed"))
	}
	return nil
}

// Git extensions can execute code supplied by the worker with Host permissions.
// Until their execution has a separate sandbox, reject them; never skip hooks.
func candidateRepositoryAllowed(ctx context.Context, repo string) error {
	names, err := freezeGit(ctx, repo, nil, "config", "--name-only", "--list")
	if err != nil {
		return err
	}
	for _, name := range strings.Split(strings.ToLower(names), "\n") {
		blocked := name == "core.sparsecheckout" || name == "core.hookspath" || name == "core.fsmonitor" || name == "commit.gpgsign" || name == "diff.external" || strings.HasPrefix(name, "gpg.") || strings.HasPrefix(name, "filter.") || strings.HasPrefix(name, "include.") || strings.HasPrefix(name, "includeif.") || name == "extensions.partialclone" || strings.HasSuffix(name, ".promisor") || strings.HasPrefix(name, "diff.") && (strings.HasSuffix(name, ".command") || strings.HasSuffix(name, ".textconv")) || strings.HasSuffix(name, ".recentobjectshook")
		if blocked {
			return fail(CodeInvalidInput, "candidate repository", fmt.Errorf("git execution extension requires isolated Host support"))
		}
	}
	common, err := commonDir(ctx, repo)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(filepath.Join(common, "info", "grafts")); err == nil {
		return fail(CodeInvalidInput, "candidate repository", fmt.Errorf("git grafts unsupported"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	hooks := filepath.Join(common, "hooks")
	info, err := os.Lstat(hooks)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail(CodeUnknownObject, "candidate hooks", fmt.Errorf("untrusted hooks directory"))
	}
	entries, err := os.ReadDir(hooks)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sample") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0111 != 0 {
			return fail(CodeInvalidInput, "candidate hooks", fmt.Errorf("git hooks require isolated Host support"))
		}
	}
	return nil
}

func candidateOperationInProgress(ctx context.Context, repo string) (bool, error) {
	dir, err := freezeGit(ctx, repo, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false, err
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-apply", "rebase-merge", "sequencer"} {
		if _, err := os.Lstat(filepath.Join(strings.TrimSpace(dir), name)); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func candidateStartFailure(err error) error {
	// Start returns context errors only before spawning. A later cancellation
	// cannot prove a child with unknown birth has exited.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: candidate start failed", process.ErrProcessTreeUnknown)
}
