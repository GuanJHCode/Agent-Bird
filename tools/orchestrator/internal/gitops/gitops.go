// Package gitops contains the narrow Git/worktree boundary used by the G1
// coordinator. It deliberately knows nothing about the coordinator database:
// durable intent and outcome records are supplied through Journal.
package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	IntegrationIntegrated = "integrated"
	IntegrationRejected   = "rejected"
	IntegrationUncertain  = "integration_uncertain"
	CleanupRemoved        = "removed"
	CleanupPending        = "cleanup_pending"
)

type Code string

const (
	CodeReviewMismatch  Code = "review_mismatch"
	CodeTargetDirty     Code = "target_dirty"
	CodeCollision       Code = "path_collision"
	CodeTargetDrift     Code = "target_drift"
	CodeUnknownObject   Code = "unknown_object"
	CodeIdentityChanged Code = "identity_changed"
	CodeGitFailure      Code = "git_failure"
	CodeInvalidInput    Code = "invalid_input"
)

type Error struct {
	Code Code
	Op   string
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}
func (e *Error) Unwrap() error                   { return e.Err }
func IsCode(err error, code Code) bool           { var ge *Error; return errors.As(err, &ge) && ge.Code == code }
func fail(code Code, op string, err error) error { return &Error{Code: code, Op: op, Err: err} }

// Journal is the persistence seam for G1's Store. Implementations must commit
// intent before a side effect and outcome after postcondition inspection.
type Journal interface {
	PersistIntegrationIntent(context.Context, IntegrationIntent) error
	RecordIntegration(context.Context, IntegrationOutcome) error
	PersistCleanupIntent(context.Context, CleanupIntent) error
	RecordCleanup(context.Context, CleanupOutcome) error
}

// CandidateJournal is kept separate from Journal so the G1 Store can add the
// candidate-freeze records without changing its existing integration ABI.
type CandidateJournal interface {
	PersistCandidateIntent(context.Context, CandidateIntent) error
	RecordCandidateFreeze(context.Context, CandidateReceipt) error
}

type CandidateIntent struct {
	AttemptID, RepoRoot, Worktree, BaseOID string
	OrderedInputOIDs                       []string
	PlanRevision                           int64
	CreatedAt                              time.Time
}

type CandidateCompositionRequest struct {
	RepoRoot, Worktree, AttemptID, BaseOID string
	OrderedInputOIDs                       []string
	PlanRevision                           int64
}

type CandidateReceipt struct {
	AttemptID, RepoRoot, Worktree, CommonDir string
	BaseOID, CandidateOID, TreeOID           string
	OrderedInputOIDs                         []string
	RootIdentity                             DirectoryIdentity
	Files                                    map[string]FileIdentity
	PlanRevision                             int64
}

type ReviewBinding struct {
	TargetWorktreeIdentity  string
	TargetDirectoryIdentity DirectoryIdentity
	TargetRef               string
	TargetBaseOID           string
	FinalCandidateOID       string
	OrderedInputOIDs        []string
	ValidationDigest        string
	PlanRevision            int64
	ReviewRevision          int64
}

type MaterializeRequest struct {
	RepoRoot, Worktree, AttemptID, BaseOID, CandidateOID string
	OrderedInputOIDs                                     []string
	PlanRevision                                         int64
}

type MaterializeReceipt struct {
	AttemptID, RepoRoot, Worktree, CommonDir, CommitOID, TreeOID string
	BaseOID                                                      string
	WorktreeIdentity                                             string
	RootIdentity                                                 DirectoryIdentity
	Files                                                        map[string]FileIdentity
	OrderedInputOIDs                                             []string
	PlanRevision                                                 int64
}

type IntegrationRequest struct {
	RepoRoot, TargetWorktree, TargetRef, TargetBaseOID, CandidateOID string
	Review                                                           ReviewBinding
	Journal                                                          Journal
	// BeforeMerge is a deterministic test/fault injection seam. Production
	// callers leave it nil; no production code relies on it.
	BeforeMerge func() error
}

// DirectoryIdentity is the kernel identity of a managed directory.  A
// canonical pathname is retained for Git's CLI, but never used as the sole
// ownership proof.
type DirectoryIdentity struct {
	Device, Inode, Mode, UID int64
}

type IntegrationIntent struct {
	AttemptID, TargetWorktree, TargetRef, TargetBaseOID, CandidateOID string
	TargetIdentity, InitialHEAD, InitialIndexTree, ValidationDigest   string
	PlanRevision, ReviewRevision                                      int64
	UntrackedIgnored                                                  []string
	OrderedInputOIDs                                                  []string
	CreatedAt                                                         time.Time
}

type IntegrationOutcome struct {
	State, Code, TargetHEAD, CandidateOID, TargetWorktree string
	GitExitCode                                           int
	ProcessPID                                            int
	DetailDigest                                          string
	RecordedAt                                            time.Time
}

type CleanupIntent struct {
	Root, AttemptID, CandidateOID, RootIdentity string
	CreatedAt                                   time.Time
}
type CleanupOutcome struct {
	State, Code, Root string
	RemovedFiles      int
	DetailDigest      string
	RecordedAt        time.Time
}

type FileIdentity struct {
	Device, Inode, Mode, Size int64
	ModUnixNano               int64
}

type CommandResult struct {
	Stdout, Stderr string
	ExitCode, PID  int
}

type ExecRunner struct{}

var oidRE = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

func CanonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(real)
	}
	return abs
}

func noSymlinkComponents(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Resolve existing benign aliases (for example macOS /var -> /private/var)
	// before checking each component.  The final object is checked first so a
	// caller cannot smuggle a symlinked managed root through EvalSymlinks.
	if info, e := os.Lstat(abs); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return fail(CodeUnknownObject, "path", fmt.Errorf("managed path is a symlink"))
	}
	if real, e := filepath.EvalSymlinks(abs); e == nil {
		abs = filepath.Clean(real)
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(filepath.Clean(abs), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fail(CodeUnknownObject, "path", fmt.Errorf("symlink component"))
		}
	}
	return nil
}

func run(ctx context.Context, dir string, args ...string) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Start()
	if err != nil {
		return CommandResult{}, err
	}
	pid := cmd.Process.Pid
	waitErr := cmd.Wait()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	result := CommandResult{Stdout: strings.TrimSpace(out.String()), Stderr: strings.TrimSpace(errOut.String()), ExitCode: code, PID: pid}
	if waitErr != nil {
		return result, waitErr
	}
	return result, nil
}

func gitText(ctx context.Context, dir string, args ...string) (string, CommandResult, error) {
	r, err := run(ctx, dir, args...)
	if err != nil {
		return "", r, err
	}
	if r.ExitCode != 0 {
		return r.Stdout, r, fail(CodeGitFailure, strings.Join(args, " "), fmt.Errorf("exit=%d: %s", r.ExitCode, r.Stderr))
	}
	return r.Stdout, r, nil
}

func requireOID(oid string) error {
	if !oidRE.MatchString(oid) {
		return fail(CodeInvalidInput, "oid", fmt.Errorf("invalid object id"))
	}
	return nil
}
func rev(ctx context.Context, repo, arg string) (string, error) {
	v, _, err := gitText(ctx, repo, "rev-parse", "--verify", arg)
	return v, err
}
func commonDir(ctx context.Context, repo string) (string, error) {
	v, _, err := gitText(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	return CanonicalPath(v), err
}

func fileIdentity(path string) (FileIdentity, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return FileIdentity{}, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return FileIdentity{}, fail(CodeUnknownObject, "lstat", fmt.Errorf("symlink"))
	}
	fi := st.Sys()
	var dev, ino int64
	if s, ok := fi.(*syscall.Stat_t); ok {
		dev, ino = int64(s.Dev), int64(s.Ino)
	}
	return FileIdentity{Device: dev, Inode: ino, Mode: int64(st.Mode()), Size: st.Size(), ModUnixNano: st.ModTime().UnixNano()}, nil
}
func directoryIdentity(path string) (DirectoryIdentity, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return DirectoryIdentity{}, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return DirectoryIdentity{}, fail(CodeUnknownObject, "directory identity", fmt.Errorf("not a real directory"))
	}
	var dev, ino, uid int64
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		dev, ino = int64(s.Dev), int64(s.Ino)
		uid = int64(s.Uid)
	}
	return DirectoryIdentity{Device: dev, Inode: ino, Mode: int64(st.Mode()), UID: uid}, nil
}
func sameDirectoryIdentity(a, b DirectoryIdentity) bool { return a == b }
func sameIdentity(a, b FileIdentity) bool               { return a == b }
func snapshot(root string) (map[string]FileIdentity, error) {
	root = CanonicalPath(root)
	out := map[string]FileIdentity{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fail(CodeUnknownObject, "snapshot", fmt.Errorf("symlink: %s", path))
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		id, err := fileIdentity(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = id
		return nil
	})
	return out, err
}

func validateRepo(ctx context.Context, repo string) (string, string, error) {
	root, _, err := gitText(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	common, err := commonDir(ctx, repo)
	if err != nil {
		return "", "", err
	}
	return CanonicalPath(root), common, nil
}
func validateOIDInRepo(ctx context.Context, repo, oid string) error {
	if err := requireOID(oid); err != nil {
		return err
	}
	_, err := rev(ctx, repo, oid+"^{commit}")
	return err
}
func candidatePaths(ctx context.Context, repo, oid string) ([]string, error) {
	if err := validateOIDInRepo(ctx, repo, oid); err != nil {
		return nil, err
	}
	text, _, err := gitText(ctx, repo, "diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "--no-renames", oid)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(text, "\n") {
		if line != "" {
			paths = append(paths, filepath.ToSlash(filepath.Clean(line)))
		}
	}
	return paths, nil
}
func targetStatus(ctx context.Context, target string) (trackedDirty bool, untrackedIgnored []string, err error) {
	text, _, e := gitText(ctx, target, "status", "--porcelain=v2", "--untracked-files=all", "--ignored=matching")
	if e != nil {
		return false, nil, e
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		prefix := line[:1]
		switch prefix {
		case "1", "2", "u":
			trackedDirty = true
		case "?", "!":
			fields := strings.Fields(line)
			if len(fields) > 1 {
				untrackedIgnored = append(untrackedIgnored, filepath.ToSlash(fields[len(fields)-1]))
			}
		}
	}
	sort.Strings(untrackedIgnored)
	return trackedDirty, untrackedIgnored, nil
}
func pathCollision(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
}
func operationInProgress(ctx context.Context, repo string) bool {
	for _, n := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		p, _, err := gitText(ctx, repo, "rev-parse", "--git-path", n)
		if err == nil {
			if _, e := os.Lstat(p); e == nil {
				return true
			}
		}
	}
	return false
}

func acquireIntegrationLock(common string) (*os.File, error) {
	if common == "" {
		return nil, fail(CodeInvalidInput, "lock", fmt.Errorf("missing common dir"))
	}
	lockPath := filepath.Join(common, "g3-integration.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fail(CodeTargetDrift, "lock", err)
	}
	return f, nil
}

func releaseIntegrationLock(f *os.File) {
	if f == nil {
		return
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

func validateReview(req IntegrationRequest) error {
	if req.Journal == nil || req.TargetWorktree == "" || req.TargetRef == "" || req.CandidateOID == "" {
		return fail(CodeInvalidInput, "review", fmt.Errorf("missing integration input"))
	}
	r := req.Review
	if r.TargetWorktreeIdentity != CanonicalPath(req.TargetWorktree) || r.TargetRef != req.TargetRef || r.TargetBaseOID != req.TargetBaseOID || r.FinalCandidateOID != req.CandidateOID || r.ValidationDigest == "" || r.PlanRevision <= 0 || r.ReviewRevision <= 0 || r.TargetDirectoryIdentity == (DirectoryIdentity{}) {
		return fail(CodeReviewMismatch, "review", fmt.Errorf("review binding mismatch"))
	}
	if len(r.OrderedInputOIDs) == 0 {
		return fail(CodeReviewMismatch, "review", fmt.Errorf("missing ordered inputs"))
	}
	return nil
}

func Materialize(ctx context.Context, req MaterializeRequest, journal Journal) (MaterializeReceipt, error) {
	if journal == nil || req.AttemptID == "" || req.Worktree == "" || req.RepoRoot == "" {
		return MaterializeReceipt{}, fail(CodeInvalidInput, "materialize", fmt.Errorf("missing input"))
	}
	root, common, err := validateRepo(ctx, req.RepoRoot)
	if err != nil {
		return MaterializeReceipt{}, err
	}
	if root != CanonicalPath(req.RepoRoot) {
		return MaterializeReceipt{}, fail(CodeInvalidInput, "materialize", fmt.Errorf("repo root mismatch"))
	}
	if err := validateOIDInRepo(ctx, req.RepoRoot, req.CandidateOID); err != nil {
		return MaterializeReceipt{}, err
	}
	if req.BaseOID != "" {
		if err := validateOIDInRepo(ctx, req.RepoRoot, req.BaseOID); err != nil {
			return MaterializeReceipt{}, err
		}
	}
	for _, input := range req.OrderedInputOIDs {
		if err := validateOIDInRepo(ctx, req.RepoRoot, input); err != nil {
			return MaterializeReceipt{}, err
		}
		if _, e := run(ctx, req.RepoRoot, "merge-base", "--is-ancestor", input, req.CandidateOID); e != nil {
			return MaterializeReceipt{}, fail(CodeReviewMismatch, "dependency", e)
		}
	}
	parent := filepath.Dir(req.Worktree)
	if !filepath.IsAbs(parent) {
		return MaterializeReceipt{}, fail(CodeInvalidInput, "materialize", fmt.Errorf("parent path"))
	}
	if err := noSymlinkComponents(parent); err != nil {
		return MaterializeReceipt{}, err
	}
	if _, e := os.Lstat(req.Worktree); e == nil {
		return MaterializeReceipt{}, fail(CodeCollision, "materialize", fmt.Errorf("worktree exists"))
	} else if !os.IsNotExist(e) {
		return MaterializeReceipt{}, e
	}
	intent := IntegrationIntent{AttemptID: req.AttemptID, TargetWorktree: req.Worktree, TargetBaseOID: req.BaseOID, CandidateOID: req.CandidateOID, PlanRevision: req.PlanRevision, OrderedInputOIDs: append([]string(nil), req.OrderedInputOIDs...), CreatedAt: time.Now().UTC()}
	if err := journal.PersistIntegrationIntent(ctx, intent); err != nil {
		return MaterializeReceipt{}, err
	}
	result, err := run(ctx, req.RepoRoot, "worktree", "add", "--detach", req.Worktree, req.CandidateOID)
	if err != nil || result.ExitCode != 0 {
		return MaterializeReceipt{}, fail(CodeGitFailure, "worktree add", err)
	}
	commit, err := rev(ctx, req.Worktree, "HEAD")
	if err != nil {
		return MaterializeReceipt{}, err
	}
	tree, err := rev(ctx, req.Worktree, "HEAD^{tree}")
	if err != nil {
		return MaterializeReceipt{}, err
	}
	files, err := snapshot(req.Worktree)
	if err != nil {
		return MaterializeReceipt{}, err
	}
	rootIdentity, err := directoryIdentity(req.Worktree)
	if err != nil {
		return MaterializeReceipt{}, err
	}
	return MaterializeReceipt{AttemptID: req.AttemptID, RepoRoot: root, Worktree: CanonicalPath(req.Worktree), CommonDir: common, CommitOID: commit, TreeOID: tree, BaseOID: req.BaseOID, WorktreeIdentity: CanonicalPath(req.Worktree), RootIdentity: rootIdentity, Files: files, OrderedInputOIDs: append([]string(nil), req.OrderedInputOIDs...), PlanRevision: req.PlanRevision}, nil
}

// ComposeCandidate materializes the declared base in a dedicated worktree,
// applies each dependency in the declared order with protected fast-forward
// merges, and freezes the resulting commit/tree as the candidate receipt.  It
// never changes the caller's target worktree or any branch ref.
func ComposeCandidate(ctx context.Context, req CandidateCompositionRequest, journal CandidateJournal) (CandidateReceipt, error) {
	if journal == nil || req.AttemptID == "" || req.Worktree == "" || req.RepoRoot == "" || req.BaseOID == "" || len(req.OrderedInputOIDs) == 0 {
		return CandidateReceipt{}, fail(CodeInvalidInput, "compose", fmt.Errorf("missing input"))
	}
	root, common, err := validateRepo(ctx, req.RepoRoot)
	if err != nil {
		return CandidateReceipt{}, err
	}
	if root != CanonicalPath(req.RepoRoot) || !filepath.IsAbs(req.Worktree) {
		return CandidateReceipt{}, fail(CodeInvalidInput, "compose", fmt.Errorf("repository/worktree identity"))
	}
	if err := validateOIDInRepo(ctx, req.RepoRoot, req.BaseOID); err != nil {
		return CandidateReceipt{}, err
	}
	if err := noSymlinkComponents(filepath.Dir(req.Worktree)); err != nil {
		return CandidateReceipt{}, err
	}
	if _, err := os.Lstat(req.Worktree); err == nil {
		return CandidateReceipt{}, fail(CodeCollision, "compose", fmt.Errorf("worktree exists"))
	} else if !os.IsNotExist(err) {
		return CandidateReceipt{}, err
	}
	inputs := append([]string(nil), req.OrderedInputOIDs...)
	for _, input := range inputs {
		if err := validateOIDInRepo(ctx, req.RepoRoot, input); err != nil {
			return CandidateReceipt{}, err
		}
	}
	if err := journal.PersistCandidateIntent(ctx, CandidateIntent{AttemptID: req.AttemptID, RepoRoot: root, Worktree: req.Worktree, BaseOID: req.BaseOID, OrderedInputOIDs: inputs, PlanRevision: req.PlanRevision, CreatedAt: time.Now().UTC()}); err != nil {
		return CandidateReceipt{}, err
	}
	if result, e := run(ctx, req.RepoRoot, "worktree", "add", "--detach", req.Worktree, req.BaseOID); e != nil || result.ExitCode != 0 {
		return CandidateReceipt{}, fail(CodeGitFailure, "compose worktree add", e)
	}
	for _, input := range inputs {
		result, e := run(ctx, req.Worktree, "merge", "--ff-only", "--no-autostash", "--no-overwrite-ignore", input)
		if e != nil || result.ExitCode != 0 {
			return CandidateReceipt{}, fail(CodeGitFailure, "compose dependency merge", e)
		}
	}
	candidateOID, err := rev(ctx, req.Worktree, "HEAD")
	if err != nil {
		return CandidateReceipt{}, err
	}
	treeOID, err := rev(ctx, req.Worktree, "HEAD^{tree}")
	if err != nil {
		return CandidateReceipt{}, err
	}
	for _, input := range inputs {
		if _, e := run(ctx, req.RepoRoot, "merge-base", "--is-ancestor", input, candidateOID); e != nil {
			return CandidateReceipt{}, fail(CodeReviewMismatch, "compose freeze", e)
		}
	}
	files, err := snapshot(req.Worktree)
	if err != nil {
		return CandidateReceipt{}, err
	}
	rootIdentity, err := directoryIdentity(req.Worktree)
	if err != nil {
		return CandidateReceipt{}, err
	}
	receipt := CandidateReceipt{AttemptID: req.AttemptID, RepoRoot: root, Worktree: CanonicalPath(req.Worktree), CommonDir: common, BaseOID: req.BaseOID, CandidateOID: candidateOID, TreeOID: treeOID, OrderedInputOIDs: inputs, RootIdentity: rootIdentity, Files: files, PlanRevision: req.PlanRevision}
	if err := journal.RecordCandidateFreeze(ctx, receipt); err != nil {
		return CandidateReceipt{}, err
	}
	return receipt, nil
}

func Integrate(ctx context.Context, req IntegrationRequest) (IntegrationOutcome, error) {
	out := IntegrationOutcome{State: IntegrationRejected, CandidateOID: req.CandidateOID, TargetWorktree: req.TargetWorktree, RecordedAt: time.Now().UTC()}
	if err := validateReview(req); err != nil {
		return out, err
	}
	root, targetCommon, err := validateRepo(ctx, req.TargetWorktree)
	if err != nil {
		return out, err
	}
	_, repoCommon, err := validateRepo(ctx, req.RepoRoot)
	if err != nil {
		return out, err
	}
	if root != CanonicalPath(req.TargetWorktree) || targetCommon != repoCommon {
		return out, fail(CodeInvalidInput, "target", fmt.Errorf("repository identity mismatch"))
	}
	targetIdentity, err := directoryIdentity(req.TargetWorktree)
	if err != nil || !sameDirectoryIdentity(targetIdentity, req.Review.TargetDirectoryIdentity) {
		return out, fail(CodeTargetDrift, "target identity", fmt.Errorf("directory identity changed"))
	}
	lock, err := acquireIntegrationLock(targetCommon)
	if err != nil {
		return out, err
	}
	defer releaseIntegrationLock(lock)
	ref, _, err := gitText(ctx, req.TargetWorktree, "symbolic-ref", "-q", "HEAD")
	if err != nil || ref != req.TargetRef {
		return out, fail(CodeTargetDrift, "target ref", err)
	}
	base, err := rev(ctx, req.TargetWorktree, "HEAD")
	if err != nil {
		return out, err
	}
	if base != req.TargetBaseOID {
		return out, fail(CodeTargetDrift, "target base", fmt.Errorf("head drift"))
	}
	if operationInProgress(ctx, req.TargetWorktree) {
		return out, fail(CodeTargetDirty, "operation", fmt.Errorf("git operation in progress"))
	}
	dirty, entries, err := targetStatus(ctx, req.TargetWorktree)
	if err != nil {
		return out, err
	}
	if dirty {
		return out, fail(CodeTargetDirty, "status", fmt.Errorf("tracked/index changes"))
	}
	paths, err := candidatePaths(ctx, req.RepoRoot, req.CandidateOID)
	if err != nil {
		return out, err
	}
	for _, input := range req.Review.OrderedInputOIDs {
		if _, e := run(ctx, req.RepoRoot, "merge-base", "--is-ancestor", input, req.CandidateOID); e != nil {
			return out, fail(CodeReviewMismatch, "dependency", e)
		}
	}
	for _, entry := range entries {
		for _, path := range paths {
			if pathCollision(entry, path) {
				return out, fail(CodeCollision, "collision", fmt.Errorf("candidate path collides"))
			}
		}
	}
	if _, e := run(ctx, req.TargetWorktree, "merge-base", "--is-ancestor", req.TargetBaseOID, req.CandidateOID); e != nil {
		return out, fail(CodeReviewMismatch, "fast-forward", e)
	}
	initialTree, err := rev(ctx, req.TargetWorktree, "HEAD^{tree}")
	if err != nil {
		return out, err
	}
	initialIndex, _, indexErr := gitText(ctx, req.TargetWorktree, "write-tree")
	if indexErr != nil {
		return out, fail(CodeTargetDirty, "index", indexErr)
	}
	candidateTree, err := rev(ctx, req.RepoRoot, req.CandidateOID+"^{tree}")
	if err != nil {
		return out, err
	}
	intent := IntegrationIntent{TargetWorktree: req.TargetWorktree, TargetRef: req.TargetRef, TargetBaseOID: req.TargetBaseOID, CandidateOID: req.CandidateOID, TargetIdentity: CanonicalPath(req.TargetWorktree), InitialHEAD: base, InitialIndexTree: initialTree, UntrackedIgnored: entries, ValidationDigest: req.Review.ValidationDigest, PlanRevision: req.Review.PlanRevision, ReviewRevision: req.Review.ReviewRevision, CreatedAt: time.Now().UTC()}
	intent.InitialIndexTree = initialIndex
	if err := req.Journal.PersistIntegrationIntent(ctx, intent); err != nil {
		return out, err
	}
	if req.BeforeMerge != nil {
		if err := req.BeforeMerge(); err != nil {
			return out, recordIntegration(ctx, req.Journal, out, err)
		}
	}
	ref2, _, e := gitText(ctx, req.TargetWorktree, "symbolic-ref", "-q", "HEAD")
	head2, e2 := rev(ctx, req.TargetWorktree, "HEAD")
	if e != nil || e2 != nil || ref2 != req.TargetRef || head2 != base {
		out.State, out.Code, out.TargetHEAD = IntegrationUncertain, string(CodeTargetDrift), head2
		return out, recordIntegration(ctx, req.Journal, out, fail(CodeTargetDrift, "pre-merge", fmt.Errorf("target changed")))
	}
	result, mergeErr := run(ctx, req.TargetWorktree, "merge", "--ff-only", "--no-autostash", "--no-overwrite-ignore", req.CandidateOID)
	out.ProcessPID, out.GitExitCode = result.PID, result.ExitCode
	postHead, postErr := rev(ctx, req.TargetWorktree, "HEAD")
	postDirty, _, postStatusErr := targetStatus(ctx, req.TargetWorktree)
	postRef, _, postRefErr := gitText(ctx, req.TargetWorktree, "symbolic-ref", "-q", "HEAD")
	postTree, postTreeErr := rev(ctx, req.TargetWorktree, "HEAD^{tree}")
	postIndex, _, postIndexErr := gitText(ctx, req.TargetWorktree, "write-tree")
	if mergeErr == nil && postErr == nil && postStatusErr == nil && postRefErr == nil && postTreeErr == nil && postIndexErr == nil && result.ExitCode == 0 && postRef == req.TargetRef && postHead == req.CandidateOID && postTree == candidateTree && postIndex == postTree && !postDirty {
		out.State, out.Code, out.TargetHEAD = IntegrationIntegrated, "", postHead
		if journalErr := req.Journal.RecordIntegration(ctx, out); journalErr != nil {
			out.State = IntegrationUncertain
			return out, journalErr
		}
		return out, nil
	}
	out.State, out.Code, out.TargetHEAD = IntegrationUncertain, string(CodeGitFailure), postHead
	if postErr != nil || postStatusErr != nil {
		out.Code = string(CodeTargetDrift)
	}
	return out, recordIntegration(ctx, req.Journal, out, fail(CodeTargetDrift, "post-merge", fmt.Errorf("merge outcome not proven")))
}

func recordIntegration(ctx context.Context, journal Journal, out IntegrationOutcome, cause error) error {
	if err := journal.RecordIntegration(ctx, out); err != nil {
		if cause != nil {
			return errors.Join(cause, err)
		}
		return err
	}
	return cause
}

func recordCleanup(ctx context.Context, journal Journal, out CleanupOutcome, cause error) error {
	if err := journal.RecordCleanup(ctx, out); err != nil {
		if cause != nil {
			return errors.Join(cause, err)
		}
		return err
	}
	return cause
}

func Cleanup(ctx context.Context, req CleanupRequest) (CleanupOutcome, error) {
	out := CleanupOutcome{State: CleanupPending, Root: req.Root, RecordedAt: time.Now().UTC()}
	if req.Journal == nil || req.Root == "" || req.Receipt.WorktreeIdentity == "" {
		return out, fail(CodeInvalidInput, "cleanup", fmt.Errorf("missing cleanup input"))
	}
	if err := noSymlinkComponents(req.Root); err != nil {
		return out, err
	}
	if CanonicalPath(req.Root) != req.Receipt.WorktreeIdentity {
		return out, fail(CodeIdentityChanged, "cleanup", fmt.Errorf("root identity"))
	}
	if _, err := os.Stat(req.Root); os.IsNotExist(err) {
		out.State = CleanupRemoved
		return out, recordCleanup(ctx, req.Journal, out, nil)
	} else if err != nil {
		return out, err
	}
	rootIdentity, err := directoryIdentity(req.Root)
	if err != nil {
		return out, err
	}
	if req.Receipt.RootIdentity != (DirectoryIdentity{}) && !sameDirectoryIdentity(rootIdentity, req.Receipt.RootIdentity) {
		return out, fail(CodeIdentityChanged, "cleanup", fmt.Errorf("root inode changed"))
	}
	// Keep an anchored directory handle open across inventory, the final
	// identity check and git's removal operation.  Git itself has no dirfd
	// worktree-remove API, so inability to revalidate the handle makes cleanup
	// pending rather than treating a pathname check as ownership proof.
	rootFD, err := os.Open(req.Root)
	if err != nil {
		return out, err
	}
	defer rootFD.Close()
	fdInfo, err := rootFD.Stat()
	if err != nil {
		return out, err
	}
	pathInfo, err := os.Stat(req.Root)
	if err != nil || pathInfo == nil || !os.SameFile(fdInfo, pathInfo) {
		return out, fail(CodeIdentityChanged, "cleanup", fmt.Errorf("root handle changed"))
	}
	for _, pid := range req.ActivePIDs {
		if pid > 0 {
			err := syscall.Kill(pid, 0)
			if err == nil || err != syscall.ESRCH {
				return out, fail(CodeUnknownObject, "cleanup", fmt.Errorf("writer alive or unknown"))
			}
		}
	}
	intent := CleanupIntent{Root: req.Root, AttemptID: req.Receipt.AttemptID, CandidateOID: req.Receipt.CommitOID, RootIdentity: req.Receipt.WorktreeIdentity, CreatedAt: time.Now().UTC()}
	if err := req.Journal.PersistCleanupIntent(ctx, intent); err != nil {
		return out, err
	}
	current, err := snapshot(req.Root)
	if err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	if latest, e := directoryIdentity(req.Root); e != nil || !sameDirectoryIdentity(latest, rootIdentity) {
		out.Code = string(CodeIdentityChanged)
		return out, recordCleanup(ctx, req.Journal, out, fail(CodeIdentityChanged, "cleanup", fmt.Errorf("root changed after inventory")))
	}
	for path, id := range current {
		expected, ok := req.Receipt.Files[path]
		if !ok {
			out.Code = string(CodeUnknownObject)
			return out, recordCleanup(ctx, req.Journal, out, fail(CodeUnknownObject, "cleanup", fmt.Errorf("unowned entry")))
		}
		if !sameIdentity(id, expected) {
			out.Code = string(CodeIdentityChanged)
			return out, recordCleanup(ctx, req.Journal, out, fail(CodeIdentityChanged, "cleanup", fmt.Errorf("entry changed")))
		}
	}
	if req.BeforeRemove != nil {
		if err := req.BeforeRemove(); err != nil {
			out.Code = string(CodeIdentityChanged)
			return out, recordCleanup(ctx, req.Journal, out, err)
		}
	}
	if latest, e := directoryIdentity(req.Root); e != nil || !sameDirectoryIdentity(latest, rootIdentity) {
		out.Code = string(CodeIdentityChanged)
		return out, recordCleanup(ctx, req.Journal, out, fail(CodeIdentityChanged, "cleanup", fmt.Errorf("root changed before remove")))
	}
	// Quarantine the already classified tree before asking Git to forget the
	// worktree. Git accepts a missing worktree path and removes only its own
	// administrative record; the held descriptor keeps the moved directory
	// anchored while we verify it again. Any replacement or rename failure is
	// retained as UNKNOWN and never passed to RemoveAll.
	parent := filepath.Dir(req.Root)
	if err := noSymlinkComponents(parent); err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	quarantine, err := os.MkdirTemp(parent, ".g3-quarantine-")
	if err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	if err := os.Remove(quarantine); err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	if err := os.Rename(req.Root, quarantine); err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	qInfo, err := os.Stat(quarantine)
	if err != nil || !os.SameFile(fdInfo, qInfo) {
		out.Code = string(CodeIdentityChanged)
		return out, recordCleanup(ctx, req.Journal, out, fail(CodeIdentityChanged, "cleanup", fmt.Errorf("quarantine identity changed")))
	}
	qCurrent, err := snapshot(quarantine)
	if err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	if len(qCurrent) != len(current) {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, fail(CodeUnknownObject, "cleanup", fmt.Errorf("quarantine inventory changed")))
	}
	for path, id := range qCurrent {
		if !sameIdentity(id, current[path]) {
			out.Code = string(CodeIdentityChanged)
			return out, recordCleanup(ctx, req.Journal, out, fail(CodeIdentityChanged, "cleanup", fmt.Errorf("quarantine entry changed")))
		}
	}
	if _, err := run(ctx, req.Receipt.RepoRoot, "worktree", "remove", req.Root); err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, fail(CodeUnknownObject, "worktree remove", err))
	}
	if _, err := os.Lstat(req.Root); err == nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, fail(CodeUnknownObject, "cleanup", fmt.Errorf("root remains")))
	} else if !os.IsNotExist(err) {
		return out, err
	}
	if err := os.RemoveAll(quarantine); err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	if _, err := os.Lstat(quarantine); err == nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, fail(CodeUnknownObject, "cleanup", fmt.Errorf("quarantine remains")))
	} else if !os.IsNotExist(err) {
		return out, err
	}
	out.State, out.Code, out.RemovedFiles = CleanupRemoved, "", len(current)
	if journalErr := req.Journal.RecordCleanup(ctx, out); journalErr != nil {
		out.State = CleanupPending
		return out, journalErr
	}
	return out, nil
}

type CleanupRequest struct {
	Root       string
	Receipt    MaterializeReceipt
	Journal    Journal
	ActivePIDs []int
	// BeforeRemove is a test/fault seam. Production leaves it nil.
	BeforeRemove func() error
}

func validatePrivateRef(ref string) error {
	if !strings.HasPrefix(ref, "refs/orchestrator/g3/") || strings.Contains(ref, "..") || strings.ContainsAny(ref, " \t\n") {
		return fail(CodeInvalidInput, "private-ref", fmt.Errorf("ref outside managed namespace"))
	}
	return nil
}

// PrivateRef performs a compare-and-swap creation/update.  The expected old
// OID is mandatory; callers use the empty string only when the ref must be
// absent.  No arbitrary user ref can be modified by this package.
func PrivateRef(ctx context.Context, repo, ref, expectedOld, oid string) error {
	if err := validatePrivateRef(ref); err != nil {
		return err
	}
	if expectedOld != "" {
		if err := requireOID(expectedOld); err != nil {
			return err
		}
	} else if current, _, err := gitText(ctx, repo, "rev-parse", "--verify", ref); err == nil && current != "" {
		return fail(CodeTargetDrift, "create-ref", fmt.Errorf("ref already exists"))
	}
	if err := requireOID(oid); err != nil {
		return err
	}
	args := []string{"update-ref", ref, oid}
	if expectedOld != "" {
		args = append(args, expectedOld)
	} else {
		args = append(args, "")
	}
	result, err := run(ctx, repo, args...)
	if err != nil || result.ExitCode != 0 {
		return fail(CodeTargetDrift, "update-ref", err)
	}
	return nil
}
func DeletePrivateRef(ctx context.Context, repo, ref, expected string) error {
	if err := validatePrivateRef(ref); err != nil {
		return err
	}
	if err := requireOID(expected); err != nil {
		return err
	}
	current, _, err := gitText(ctx, repo, "rev-parse", "--verify", ref)
	if err != nil {
		return err
	}
	if current != expected {
		return fail(CodeTargetDrift, "delete-ref", fmt.Errorf("ref changed"))
	}
	_, err = run(ctx, repo, "update-ref", "-d", ref, expected)
	return err
}
func DigestDetail(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
