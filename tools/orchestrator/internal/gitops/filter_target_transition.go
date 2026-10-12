package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"golang.org/x/sys/unix"
)

// FilterTargetTransition is the original-owner authority for the one allowed
// target transition.  It deliberately is separate from the readonly pair: a
// normal fast-forward replaces the target index and branch ref, neither of
// which a readonly observer may adopt.
type FilterTargetTransition struct {
	self           *FilterTargetTransition
	mu             sync.Mutex
	closed         bool
	finalInventory map[string]filterTargetEntry
	directories    []transitionDirectoryPin
	pair           *FilterTargetCandidatePair
	expected       map[string]filterTargetExpected
	attempted      bool
	running        bool
	final          bool

	finalIndex    *os.File
	lock          *os.File
	finalIndexID  FileIdentity
	finalIndexSHA string
	finalBranch   filterPinnedFile
}

// FilterTargetTransitionHooks receives the original process object only at
// the two lifecycle boundaries where a carrier may bind and drain transport.
// It cannot replace the frozen Git command or its process ownership.
type FilterTargetTransitionHooks struct {
	configArgs   []string
	bindCommand  func(context.Context, string) (string, error)
	Activate     func(*process.Handle, string) error
	AfterWait    func(*process.Handle, string) error
	BeforeExited func(*process.Handle, string) error
}

// OpenFilterTargetTransition captures the accepted candidate's rendered R
// inventory while the pair is still readonly.  The returned value owns only
// its final-index descriptor; callers retain and close the pair and owners.
func OpenFilterTargetTransition(ctx context.Context, pair *FilterTargetCandidatePair) (*FilterTargetTransition, error) {
	if ctx == nil {
		return nil, fail(CodeInvalidInput, "filter target transition deadline", nil)
	}
	if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
		return nil, fail(CodeInvalidInput, "filter target transition deadline", nil)
	}
	if pair == nil || pair.target == nil || pair.candidate == nil {
		return nil, fail(CodeReviewMismatch, "filter target transition pair", nil)
	}
	lock, err := acquireIntegrationLock(pair.target.source.commonDir)
	if err != nil {
		return nil, err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			releaseIntegrationLock(lock)
		}
	}()
	if err := pair.Verify(ctx); err != nil {
		return nil, err
	}
	t := &FilterTargetTransition{pair: pair, expected: map[string]filterTargetExpected{}}
	t.self = t
	if err := t.captureDirectories(); err != nil {
		_ = t.Close()
		return nil, err
	}
	for name, entry := range pair.candidate.workspace.owned {
		if name == ".git" {
			continue
		}
		mode := "directory"
		if !os.FileMode(entry.identity.Mode).IsDir() {
			mode = "100644"
			if entry.identity.Mode&0111 != 0 {
				mode = "100755"
			}
		}
		t.expected[name] = filterTargetExpected{Mode: mode, SHA256: entry.sha256, Size: entry.identity.Size}
	}
	if len(t.expected) == 0 {
		_ = t.Close()
		return nil, fail(CodeReviewMismatch, "filter target transition candidate inventory", nil)
	}
	t.lock = lock
	keepLock = true
	return t, nil
}

// VerifyInitial preserves the existing complete readonly proof before intent,
// before the merge callback, and before spawning Git.
func (t *FilterTargetTransition) VerifyInitial(ctx context.Context) error {
	if !t.enter() {
		return fail(CodeIdentityChanged, "filter transition original owner", nil)
	}
	defer t.mu.Unlock()
	return t.verifyInitial(ctx)
}
func (t *FilterTargetTransition) verifyInitial(ctx context.Context) error {
	if t.closed || t.attempted || t.running || t.final || t.pair == nil || !transitionContext(ctx) {
		return fail(CodeIdentityChanged, "filter target transition state", nil)
	}
	if err := t.verifyDirectories(); err != nil {
		return err
	}
	return t.pair.Verify(ctx)
}

// Merge executes precisely the normal Git fast-forward envelope.  It never
// uses update-ref/read-tree and retains the original process Wait/tree proof.
func (t *FilterTargetTransition) Merge(ctx context.Context) (CommandResult, error) {
	return t.MergeWithHooks(ctx, FilterTargetTransitionHooks{})
}

// MergeWithHooks leaves the carrier join point explicit: after the
// original Start and durable spawned record have both completed, and after the
// running pure guard has succeeded, activation runs before Wait.  A carrier
// can therefore bind its owned transport to the exact live Git process without
// moving the merge or weakening its lifecycle proof.
func (t *FilterTargetTransition) MergeWithHooks(ctx context.Context, hooks FilterTargetTransitionHooks) (CommandResult, error) {
	if !t.enter() {
		return CommandResult{}, fail(CodeIdentityChanged, "filter transition original owner", nil)
	}
	defer t.mu.Unlock()
	if err := t.verifyInitial(ctx); err != nil {
		return CommandResult{}, err
	}
	t.attempted, t.running = true, true

	target := t.pair.target
	dir := target.request.Worktree
	args := append(append([]string(nil), hooks.configArgs...), "-C", dir, "merge", "--ff-only", "--no-autostash", "--no-overwrite-ignore", target.candidate.CandidateOID)
	env := append([]string(nil), os.Environ()...)
	digest := DigestDetail(target.source.git.path + "\x00" + target.source.git.sha256 + "\x00" + dir + "\x00" + strings.Join(env, "\x00") + "\x00" + strings.Join(args, "\x00"))
	if hooks.bindCommand != nil {
		var err error
		digest, err = hooks.bindCommand(ctx, digest)
		if err != nil {
			return CommandResult{}, err
		}
	}
	if err := t.verifyPreStart(ctx); err != nil {
		return CommandResult{}, err
	}
	record := func(phase string, id *process.Identity, exit int) error {
		if err := target.journal.RecordGitProcess(context.Background(), GitProcessRecord{Phase: phase, CommandDigest: digest, Process: id, ExitCode: exit}); err != nil {
			return err
		}
		return t.verifyRunning(ctx)
	}
	if err := record("intent", nil, -1); err != nil {
		return CommandResult{}, err
	}
	if err := t.verifyPreStart(ctx); err != nil {
		return CommandResult{}, err
	}
	p, err := process.Start(ctx, process.Command{Path: target.source.git.path, PinnedPath: target.source.git.path, PinnedSHA256: target.source.git.sha256, Args: args, Dir: dir, Env: env, ExactEnv: true})
	if err != nil {
		if p == nil {
			return CommandResult{}, candidateStartFailure(err)
		}
		id := p.Identity()
		_ = record("unknown", &id, p.ExitCode())
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return CommandResult{PID: p.PID(), ExitCode: p.ExitCode()}, errors.Join(process.ErrProcessTreeUnknown, process.CleanupFailedStart(stopCtx, p, 10*time.Second))
	}
	id := p.Identity()
	drained := false
	defer func() {
		if !drained && hooks.AfterWait != nil {
			_ = hooks.AfterWait(p, digest)
		}
	}()
	stop := func() error {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return p.Stop(stopCtx)
	}
	if err := record("spawned", &id, -1); err != nil {
		if stop() != nil {
			return CommandResult{}, process.ErrProcessTreeUnknown
		}
		return CommandResult{}, err
	}
	if hooks.Activate != nil {
		if err := hooks.Activate(p, digest); err != nil {
			if stop() != nil {
				_ = record("unknown", &id, p.ExitCode())
				return CommandResult{}, process.ErrProcessTreeUnknown
			}
			return CommandResult{}, err
		}
	}
	waitErr := p.Wait(ctx)
	stopErr := error(nil)
	if waitErr != nil {
		stopErr = stop()
	}
	if hooks.AfterWait != nil {
		drained = true
		if err := hooks.AfterWait(p, digest); err != nil {
			_ = record("unknown", &id, p.ExitCode())
			return CommandResult{PID: p.PID(), ExitCode: p.ExitCode()}, err
		}
	}
	if stopErr != nil {
		_ = record("unknown", &id, p.ExitCode())
		return CommandResult{}, process.ErrProcessTreeUnknown
	}
	if err := p.ConfirmTreeExited(); err != nil {
		_ = record("unknown", &id, p.ExitCode())
		return CommandResult{}, process.ErrProcessTreeUnknown
	}
	if hooks.BeforeExited != nil {
		if err := hooks.BeforeExited(p, digest); err != nil {
			_ = record("unknown", &id, p.ExitCode())
			return CommandResult{PID: p.PID(), ExitCode: p.ExitCode()}, err
		}
	}
	result := CommandResult{PID: p.PID(), ExitCode: p.ExitCode()}
	if err := record("exited", &id, result.ExitCode); err != nil {
		return result, err
	}
	if waitErr != nil {
		return result, waitErr
	}
	if result.ExitCode != 0 {
		return result, fail(CodeGitFailure, "filter target transition merge", fmt.Errorf("git command failed"))
	}
	if err := t.captureFinal(ctx); err != nil {
		return result, err
	}
	return result, nil
}

// verifyPreStart restores the complete readonly proof after callback windows
// and before the one authorized merge process starts.  Its state check differs
// from verifyInitial only because MergeWithHooks has already recorded this
// attempt as running; target branch, index, and inventory remain immutable
// until process.Start succeeds.
func (t *FilterTargetTransition) verifyPreStart(ctx context.Context) error {
	if t == nil || t.self != t || t.closed || !t.attempted || !t.running || t.final || t.pair == nil || !transitionContext(ctx) {
		return fail(CodeIdentityChanged, "filter target transition pre-start", nil)
	}
	if err := t.pair.Verify(ctx); err != nil {
		return err
	}
	if err := t.verifyDirectories(); err != nil {
		return err
	}
	return t.verifyLock()
}

func (t *FilterTargetTransition) verifyLock() error {
	if t.lock == nil || t.pair == nil || t.pair.target == nil {
		return fail(CodeIdentityChanged, "filter target transition lock", nil)
	}
	held, err := t.lock.Stat()
	if err != nil {
		return fail(CodeIdentityChanged, "filter target transition lock", err)
	}
	path := filepath.Join(t.pair.target.source.commonDir, "g3-integration.lock")
	named, err := os.Lstat(path)
	if err != nil || !named.Mode().IsRegular() || !os.SameFile(named, held) {
		return fail(CodeIdentityChanged, "filter target transition lock", err)
	}
	stat, ok := named.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || int64(stat.Uid) != int64(os.Getuid()) || named.Mode().Perm() != 0600 {
		return fail(CodeIdentityChanged, "filter target transition lock", nil)
	}
	return nil
}

// verifyRunning intentionally excludes the readonly target index, inventory,
// and mutable branch ref.  All other original-owner and child evidence stays
// pinned while the sole authorized Git process is alive.
func (t *FilterTargetTransition) verifyRunning(ctx context.Context) error {
	if t == nil || t.self != t || t.closed || !t.running || t.final || t.pair == nil || !transitionContext(ctx) {
		return fail(CodeIdentityChanged, "filter target transition running", nil)
	}
	m, p := t.pair.target, t.pair
	if err := m.verifyAnchors(); err != nil {
		return err
	}
	if err := m.verifySourcePins(); err != nil {
		return err
	}
	if err := t.verifyDirectories(); err != nil {
		return err
	}
	if err := t.verifyImmutableRefs(); err != nil {
		return err
	}
	if err := p.candidate.workspace.verify(); err != nil {
		return err
	}
	if _, err := p.candidate.workspace.inventory(ctx); err != nil {
		return err
	}
	if err := pairVerifyChildContext(p.candidate.binding.worktree, m.source); err != nil {
		return err
	}
	if err := p.verifyChildGitDir(); err != nil {
		return err
	}
	if err := pairVerifyHead(p.childHead, p.candidate.receipt.CommitOID); err != nil {
		return err
	}
	raw, id, err := pairReadIndex(p.childIndex, p.childGitDir)
	if err != nil || id != p.childIndexIdentity || filterTargetHash(raw) != p.childIndexSHA256 {
		return fail(CodeIdentityChanged, "filter target transition child index", err)
	}
	return nil
}

func (t *FilterTargetTransition) captureFinal(ctx context.Context) error {
	if t == nil || t.self != t || t.closed || !t.running || t.final {
		return fail(CodeIdentityChanged, "filter target transition final state", nil)
	}
	m := t.pair.target
	deadline, ok := ctx.Deadline()
	if !ok {
		return fail(CodeInvalidInput, "filter target transition final deadline", nil)
	}
	pureCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := t.verifyRunning(pureCtx); err != nil {
		return err
	}
	if err := m.checkMetadata(); err != nil {
		return err
	}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"symbolic-ref", "-q", "HEAD"}, m.request.Ref},
		{[]string{"rev-parse", "--verify", "HEAD"}, m.candidate.CandidateOID},
		{[]string{"rev-parse", "--verify", "HEAD^{tree}"}, m.candidate.TreeOID},
		{[]string{"write-tree"}, m.candidate.TreeOID},
	} {
		r, err := t.query(pureCtx, check.args...)
		if err != nil || r.Stdout != check.want {
			return fail(CodeTargetDrift, "filter target transition final git", err)
		}
	}
	if err := t.snapshotFinalInventory(pureCtx, true); err != nil {
		return err
	}
	branchPath := filepath.Join(m.source.commonDir, filepath.FromSlash(m.request.Ref))
	pin, err := pinFilterFileKind(branchPath, "", 1<<20, false, nil)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(branchPath, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, 8193))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(raw, []byte(m.candidate.CandidateOID+"\n")) || !verifyFilterPin(pin) {
		return fail(CodeTargetDrift, "filter target transition final ref", errors.Join(readErr, closeErr))
	}
	index, err := filterOpenAt(m.gitDir, "index", unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	raw, id, err := pairReadIndex(index, m.gitDir)
	if err != nil {
		_ = index.Close()
		return err
	}
	t.finalIndex, t.finalIndexID, t.finalIndexSHA, t.finalBranch, t.final = index, id, filterTargetHash(raw), pin, true
	t.running = false
	return t.revalidateFinal(pureCtx)
}

func (t *FilterTargetTransition) snapshotFinalInventory(ctx context.Context, capture bool) error {
	m := t.pair.target
	original := m.expected
	m.expected = t.expected
	got, err := m.snapshot(ctx)
	m.expected = original
	if err != nil {
		return err
	}
	if len(got) != len(t.expected) || !capture && !reflect.DeepEqual(got, t.finalInventory) {
		return fail(CodeTargetDrift, "filter target transition final inventory", nil)
	}
	if capture {
		t.finalInventory = got
	}
	return nil
}

// RevalidateFinal is deliberately pure: it issues no Git command and closes
// the RecordIntegration callback window after the final snapshot is captured.
func (t *FilterTargetTransition) RevalidateFinal(ctx context.Context) error {
	if !t.enter() {
		return fail(CodeIdentityChanged, "filter transition original owner", nil)
	}
	defer t.mu.Unlock()
	return t.revalidateFinal(ctx)
}
func (t *FilterTargetTransition) revalidateFinal(ctx context.Context) error {
	if t.closed || !t.final || t.running || t.finalIndex == nil || !transitionContext(ctx) {
		return fail(CodeIdentityChanged, "filter target transition final", nil)
	}
	m := t.pair.target
	if err := m.verifyAnchors(); err != nil {
		return err
	}
	if err := m.checkMetadata(); err != nil {
		return err
	}
	if err := m.verifySourcePins(); err != nil {
		return err
	}
	if !verifyFilterPin(t.finalBranch) {
		return fail(CodeIdentityChanged, "filter target transition final ref", nil)
	}
	raw, id, err := pairReadIndex(t.finalIndex, m.gitDir)
	if err != nil || id != t.finalIndexID || filterTargetHash(raw) != t.finalIndexSHA {
		return fail(CodeIdentityChanged, "filter target transition final index", err)
	}
	if err := t.verifyDirectories(); err != nil {
		return err
	}
	if err := t.verifyImmutableRefs(); err != nil {
		return err
	}
	// Candidate side remains immutable after the final target state too.
	t.running, t.final = true, false
	err = t.verifyRunning(ctx)
	t.running, t.final = false, true
	if err != nil {
		return err
	}
	return t.snapshotFinalInventory(ctx, false)
}

func (t *FilterTargetTransition) Close() error {
	if t == nil {
		return nil
	}
	if !t.enter() {
		return fail(CodeIdentityChanged, "filter transition original owner", nil)
	}
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	var err error
	for _, pin := range t.directories {
		err = errors.Join(err, pin.file.Close())
	}
	t.directories = nil
	if t.finalIndex != nil {
		err = t.finalIndex.Close()
		t.finalIndex = nil
	}
	if t.lock != nil {
		releaseIntegrationLock(t.lock)
		t.lock = nil
	}
	return err
}
