// Package gitops contains the narrow Git/worktree boundary used by the G1
// coordinator. It deliberately knows nothing about the coordinator database:
// durable intent and outcome records are supplied through Journal.
package gitops

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
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
	PersistCandidateRefIntent(context.Context, CandidateRefIntent) error
	RecordCandidateFreeze(context.Context, CandidateReceipt) error
}

type PrivateRefJournal interface {
	PersistPrivateRefIntent(context.Context, PrivateRefIntent) error
	RecordPrivateRef(context.Context, PrivateRefOutcome) error
}

type CandidateIntent struct {
	Binding          *CandidateBinding `json:"binding,omitempty"`
	Changes          []CandidateChange `json:"changes,omitempty"`
	AttemptID        string            `json:"attempt_id"`
	RepoRoot         string            `json:"repo_root"`
	Worktree         string            `json:"worktree"`
	BaseOID          string            `json:"base_oid"`
	PrivateRef       string            `json:"private_ref"`
	OrderedInputOIDs []string          `json:"ordered_input_oids"`
	PlanRevision     int64             `json:"plan_revision"`
	CreatedAt        time.Time         `json:"created_at"`
}

type CandidateRefIntent struct {
	AttemptID      string    `json:"attempt_id"`
	RepoRoot       string    `json:"repo_root"`
	PrivateRef     string    `json:"private_ref"`
	ExpectedOldOID string    `json:"expected_old_oid"`
	CandidateOID   string    `json:"candidate_oid"`
	CreatedAt      time.Time `json:"created_at"`
}

type CandidateCompositionRequest struct {
	RepoRoot         string   `json:"repo_root"`
	Worktree         string   `json:"worktree"`
	AttemptID        string   `json:"attempt_id"`
	BaseOID          string   `json:"base_oid"`
	OrderedInputOIDs []string `json:"ordered_input_oids"`
	PlanRevision     int64    `json:"plan_revision"`
	PrivateRef       string   `json:"private_ref"`
}

type CandidateReceipt struct {
	Binding               *CandidateBinding       `json:"binding,omitempty"`
	Changes               []CandidateChange       `json:"changes,omitempty"`
	AttemptID             string                  `json:"attempt_id"`
	RepoRoot              string                  `json:"repo_root"`
	Worktree              string                  `json:"worktree"`
	CommonDir             string                  `json:"common_dir"`
	BaseOID               string                  `json:"base_oid"`
	CandidateOID          string                  `json:"candidate_oid"`
	TreeOID               string                  `json:"tree_oid"`
	OrderedInputOIDs      []string                `json:"ordered_input_oids"`
	RootIdentity          DirectoryIdentity       `json:"root_identity"`
	WorktreeIdentity      string                  `json:"worktree_identity"`
	Files                 map[string]FileIdentity `json:"files"`
	PlanRevision          int64                   `json:"plan_revision"`
	PrivateRef            string                  `json:"private_ref"`
	PrivateRefExpectedOID string                  `json:"private_ref_expected_oid"`
}

type PrivateRefRequest struct {
	RepoRoot       string `json:"repo_root"`
	PrivateRef     string `json:"private_ref"`
	ExpectedOldOID string `json:"expected_old_oid"`
	OID            string `json:"oid"`
}

type DeletePrivateRefRequest struct {
	RepoRoot    string `json:"repo_root"`
	PrivateRef  string `json:"private_ref"`
	ExpectedOID string `json:"expected_oid"`
}

type PrivateRefReceipt struct {
	RepoRoot    string `json:"repo_root"`
	PrivateRef  string `json:"private_ref"`
	ExpectedOID string `json:"expected_oid"`
	State       string `json:"state"`
}

type PrivateRefIntent struct {
	Operation      string    `json:"operation"`
	RepoRoot       string    `json:"repo_root"`
	PrivateRef     string    `json:"private_ref"`
	ExpectedOldOID string    `json:"expected_old_oid,omitempty"`
	OID            string    `json:"oid"`
	CreatedAt      time.Time `json:"created_at"`
}

type PrivateRefOutcome struct {
	Operation  string    `json:"operation"`
	RepoRoot   string    `json:"repo_root"`
	PrivateRef string    `json:"private_ref"`
	OID        string    `json:"oid"`
	State      string    `json:"state"`
	Code       string    `json:"code,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

type ReviewBinding struct {
	TargetWorktreeIdentity  string            `json:"target_worktree_identity"`
	TargetDirectoryIdentity DirectoryIdentity `json:"target_directory_identity"`
	TargetRef               string            `json:"target_ref"`
	TargetBaseOID           string            `json:"target_base_oid"`
	FinalCandidateOID       string            `json:"final_candidate_oid"`
	OrderedInputOIDs        []string          `json:"ordered_input_oids"`
	ValidationDigest        string            `json:"validation_digest"`
	PlanRevision            int64             `json:"plan_revision"`
	ReviewRevision          int64             `json:"review_revision"`
}

type MaterializeRequest struct {
	RepoRoot         string   `json:"repo_root"`
	Worktree         string   `json:"worktree"`
	AttemptID        string   `json:"attempt_id"`
	BaseOID          string   `json:"base_oid"`
	CandidateOID     string   `json:"candidate_oid"`
	OrderedInputOIDs []string `json:"ordered_input_oids"`
	PlanRevision     int64    `json:"plan_revision"`
}

type MaterializeReceipt struct {
	AttemptID        string                  `json:"attempt_id"`
	RepoRoot         string                  `json:"repo_root"`
	Worktree         string                  `json:"worktree"`
	CommonDir        string                  `json:"common_dir"`
	CommitOID        string                  `json:"commit_oid"`
	TreeOID          string                  `json:"tree_oid"`
	BaseOID          string                  `json:"base_oid"`
	WorktreeIdentity string                  `json:"worktree_identity"`
	RootIdentity     DirectoryIdentity       `json:"root_identity"`
	Files            map[string]FileIdentity `json:"files"`
	OrderedInputOIDs []string                `json:"ordered_input_oids"`
	PlanRevision     int64                   `json:"plan_revision"`
}

type IntegrationRequest struct {
	AttemptID      string        `json:"attempt_id,omitempty"`
	RepoRoot       string        `json:"repo_root"`
	TargetWorktree string        `json:"target_worktree"`
	TargetRef      string        `json:"target_ref"`
	TargetBaseOID  string        `json:"target_base_oid"`
	CandidateOID   string        `json:"candidate_oid"`
	Review         ReviewBinding `json:"review"`
	Journal        Journal       `json:"-"`
	// BeforeMerge is a deterministic test/fault injection seam. Production
	// callers leave it nil; no production code relies on it.
	BeforeMerge func() error `json:"-"`
}

// DirectoryIdentity is the kernel identity of a managed directory.  A
// canonical pathname is retained for Git's CLI, but never used as the sole
// ownership proof.
type DirectoryIdentity struct {
	Device int64 `json:"device"`
	Inode  int64 `json:"inode"`
	Mode   int64 `json:"mode"`
	UID    int64 `json:"uid"`
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
	State          string    `json:"state"`
	Code           string    `json:"code,omitempty"`
	TargetHEAD     string    `json:"target_head,omitempty"`
	CandidateOID   string    `json:"candidate_oid"`
	TargetWorktree string    `json:"target_worktree"`
	GitExitCode    int       `json:"git_exit_code"`
	ProcessPID     int       `json:"process_pid"`
	DetailDigest   string    `json:"detail_digest,omitempty"`
	RecordedAt     time.Time `json:"recorded_at"`
}

type CleanupIntent struct {
	Root                  string    `json:"root"`
	AttemptID             string    `json:"attempt_id"`
	CandidateOID          string    `json:"candidate_oid"`
	RootIdentity          string    `json:"root_identity"`
	PrivateRef            string    `json:"private_ref,omitempty"`
	PrivateRefExpectedOID string    `json:"private_ref_expected_oid,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}
type CleanupOutcome struct {
	State           string    `json:"state"`
	Code            string    `json:"code,omitempty"`
	Root            string    `json:"root"`
	RemovedFiles    int       `json:"removed_files"`
	PrivateRef      string    `json:"private_ref,omitempty"`
	PrivateRefState string    `json:"private_ref_state,omitempty"`
	DetailDigest    string    `json:"detail_digest,omitempty"`
	RecordedAt      time.Time `json:"recorded_at"`
}

type FileIdentity struct {
	Device      int64 `json:"device"`
	Inode       int64 `json:"inode"`
	Mode        int64 `json:"mode"`
	Size        int64 `json:"size"`
	ModUnixNano int64 `json:"mod_unix_nano"`
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
	if managed, _ := ctx.Value(candidateRunnerKey{}).(bool); managed {
		result, err := candidateGit(ctx, dir, nil, args...)
		result.Stdout = strings.TrimSpace(result.Stdout)
		return result, err
	}
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
	return identityFromInfo(st)
}
func identityFromInfo(st os.FileInfo) (FileIdentity, error) {
	if st.Mode()&os.ModeSymlink != 0 || (!st.Mode().IsRegular() && !st.IsDir()) {
		return FileIdentity{}, fail(CodeUnknownObject, "identity", fmt.Errorf("unsupported file type"))
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

// NUL-delimited paths must retain leading/trailing whitespace and newlines.
func gitRecords(ctx context.Context, repo string, args ...string) (string, error) {
	if managed, _ := ctx.Value(candidateRunnerKey{}).(bool); managed {
		result, err := candidateGit(ctx, repo, nil, args...)
		return result.Stdout, err
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	body, err := cmd.Output()
	return string(body), err
}
func candidatePaths(ctx context.Context, repo, base, oid string) ([]string, error) {
	if err := validateOIDInRepo(ctx, repo, oid); err != nil {
		return nil, err
	}
	text, err := gitRecords(ctx, repo, "diff-tree", "--no-commit-id", "--name-only", "-r", "--no-renames", "-z", base, oid, "--")
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(text, "\x00"), "\x00"), nil
}
func targetStatus(ctx context.Context, target string) (trackedDirty bool, untrackedIgnored []string, err error) {
	text, err := gitRecords(ctx, target, "status", "--porcelain=v2", "-z", "--no-renames", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return false, nil, err
	}
	for _, record := range strings.Split(text, "\x00") {
		if record == "" {
			continue
		}
		if strings.HasPrefix(record, "? ") || strings.HasPrefix(record, "! ") {
			untrackedIgnored = append(untrackedIgnored, record[2:])
		} else if strings.HasPrefix(record, "1 ") || strings.HasPrefix(record, "2 ") || strings.HasPrefix(record, "u ") {
			trackedDirty = true
		} else {
			return false, nil, fail(CodeInvalidInput, "status", fmt.Errorf("unexpected git status record"))
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
	if err := noSymlinkComponents(common); err != nil {
		return nil, err
	}
	commonIdentity, err := directoryIdentity(common)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(common, "g3-integration.lock")
	if st, e := os.Lstat(lockPath); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil, fail(CodeUnknownObject, "lock", fmt.Errorf("lock path is symlink"))
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	pathInfo, statErr := os.Stat(lockPath)
	fileInfo, fileErr := f.Stat()
	if statErr != nil || fileErr != nil || pathInfo == nil || fileInfo == nil || !os.SameFile(pathInfo, fileInfo) {
		_ = f.Close()
		return nil, fail(CodeIdentityChanged, "lock", fmt.Errorf("lock inode changed"))
	}
	if lockStat, ok := fileInfo.Sys().(*syscall.Stat_t); !ok || lockStat.Nlink != 1 || int64(lockStat.Uid) != int64(os.Getuid()) || fileInfo.Mode().Perm() != 0600 {
		_ = f.Close()
		return nil, fail(CodeUnknownObject, "lock", fmt.Errorf("lock ownership or mode"))
	}
	if latest, e := directoryIdentity(common); e != nil || !sameDirectoryIdentity(latest, commonIdentity) {
		_ = f.Close()
		return nil, fail(CodeIdentityChanged, "lock", fmt.Errorf("common directory changed"))
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
	files, err := snapshotWithContext(ctx, req.Worktree)
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
// merges, and freezes the resulting commit/tree behind a managed private ref.
// It never changes the caller's target worktree or any user branch ref.
func ComposeCandidate(ctx context.Context, req CandidateCompositionRequest, journal CandidateJournal) (CandidateReceipt, error) {
	if journal == nil || req.AttemptID == "" || req.Worktree == "" || req.RepoRoot == "" || req.BaseOID == "" || req.PrivateRef == "" || len(req.OrderedInputOIDs) == 0 {
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
	if err := validatePrivateRef(req.PrivateRef); err != nil {
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
	if err := journal.PersistCandidateIntent(ctx, CandidateIntent{AttemptID: req.AttemptID, RepoRoot: root, Worktree: req.Worktree, BaseOID: req.BaseOID, PrivateRef: req.PrivateRef, OrderedInputOIDs: inputs, PlanRevision: req.PlanRevision, CreatedAt: time.Now().UTC()}); err != nil {
		return CandidateReceipt{}, err
	}
	if result, e := run(ctx, req.RepoRoot, "worktree", "add", "--detach", req.Worktree, req.BaseOID); e != nil || result.ExitCode != 0 {
		return CandidateReceipt{}, fail(CodeGitFailure, "compose worktree add", e)
	}
	for _, input := range inputs {
		// A dependency may be a descendant of the current composition (the
		// cheap fast-forward case), or it may be an independent branch from
		// the same base.  The latter must be composed with a normal merge;
		// ff-only is reserved for the final consumer integration.
		if _, e := run(ctx, req.Worktree, "merge-base", "--is-ancestor", input, "HEAD"); e == nil {
			continue
		}
		if _, e := run(ctx, req.Worktree, "merge-base", "--is-ancestor", "HEAD", input); e == nil {
			result, mergeErr := run(ctx, req.Worktree, "merge", "--ff-only", "--no-autostash", "--no-overwrite-ignore", input)
			if mergeErr != nil || result.ExitCode != 0 {
				return CandidateReceipt{}, fail(CodeGitFailure, "compose fast-forward", mergeErr)
			}
			continue
		}
		result, mergeErr := run(ctx, req.Worktree, "merge", "--no-ff", "--no-commit", "--no-autostash", "--no-overwrite-ignore", input)
		if mergeErr != nil || result.ExitCode != 0 {
			return CandidateReceipt{}, fail(CodeGitFailure, "compose dependency merge", mergeErr)
		}
		result, mergeErr = run(ctx, req.Worktree, "commit", "--no-edit", "-m", "G3 candidate composition")
		if mergeErr != nil || result.ExitCode != 0 {
			return CandidateReceipt{}, fail(CodeGitFailure, "compose merge commit", mergeErr)
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
	refIntent := CandidateRefIntent{AttemptID: req.AttemptID, RepoRoot: root, PrivateRef: req.PrivateRef, ExpectedOldOID: "", CandidateOID: candidateOID, CreatedAt: time.Now().UTC()}
	if err := journal.PersistCandidateRefIntent(ctx, refIntent); err != nil {
		return CandidateReceipt{}, err
	}
	if err := PrivateRef(ctx, req.RepoRoot, req.PrivateRef, "", candidateOID); err != nil {
		return CandidateReceipt{}, err
	}
	worktree := CanonicalPath(req.Worktree)
	receipt := CandidateReceipt{AttemptID: req.AttemptID, RepoRoot: root, Worktree: worktree, CommonDir: common, BaseOID: req.BaseOID, CandidateOID: candidateOID, TreeOID: treeOID, OrderedInputOIDs: inputs, RootIdentity: rootIdentity, WorktreeIdentity: worktree, Files: files, PlanRevision: req.PlanRevision, PrivateRef: req.PrivateRef, PrivateRefExpectedOID: candidateOID}
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
	inProgress := false
	if managed, _ := ctx.Value(candidateRunnerKey{}).(bool); managed {
		inProgress, err = candidateOperationInProgress(ctx, req.TargetWorktree)
		if err != nil {
			return out, err
		}
	} else {
		inProgress = operationInProgress(ctx, req.TargetWorktree)
	}
	if inProgress {
		return out, fail(CodeTargetDirty, "operation", fmt.Errorf("git operation in progress"))
	}
	dirty, entries, err := targetStatus(ctx, req.TargetWorktree)
	if err != nil {
		return out, err
	}
	if dirty {
		return out, fail(CodeTargetDirty, "status", fmt.Errorf("tracked/index changes"))
	}
	paths, err := candidatePaths(ctx, req.RepoRoot, req.TargetBaseOID, req.CandidateOID)
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
	intent := IntegrationIntent{AttemptID: req.AttemptID, OrderedInputOIDs: append([]string(nil), req.Review.OrderedInputOIDs...), TargetWorktree: req.TargetWorktree, TargetRef: req.TargetRef, TargetBaseOID: req.TargetBaseOID, CandidateOID: req.CandidateOID, TargetIdentity: CanonicalPath(req.TargetWorktree), InitialHEAD: base, InitialIndexTree: initialTree, UntrackedIgnored: entries, ValidationDigest: req.Review.ValidationDigest, PlanRevision: req.Review.PlanRevision, ReviewRevision: req.Review.ReviewRevision, CreatedAt: time.Now().UTC()}
	intent.InitialIndexTree = initialIndex
	if err := req.Journal.PersistIntegrationIntent(ctx, intent); err != nil {
		return out, err
	}
	if req.BeforeMerge != nil {
		if err := req.BeforeMerge(); err != nil {
			return out, recordIntegration(ctx, req.Journal, out, err)
		}
	}
	uncertain := func(cause error) (IntegrationOutcome, error) {
		out.State = IntegrationUncertain
		out.Code = string(CodeGitFailure)
		return out, recordIntegration(ctx, req.Journal, out, cause)
	}
	ref2, _, e := gitText(ctx, req.TargetWorktree, "symbolic-ref", "-q", "HEAD")
	if e != nil {
		return uncertain(e)
	}
	head2, e := rev(ctx, req.TargetWorktree, "HEAD")
	if e != nil {
		return uncertain(e)
	}
	if ref2 != req.TargetRef || head2 != base {
		out.TargetHEAD = head2
		out.Code = string(CodeTargetDrift)
		out.State = IntegrationUncertain
		return out, recordIntegration(ctx, req.Journal, out, fail(CodeTargetDrift, "pre-merge", fmt.Errorf("target changed")))
	}
	result, mergeErr := run(ctx, req.TargetWorktree, "merge", "--ff-only", "--no-autostash", "--no-overwrite-ignore", req.CandidateOID)
	out.ProcessPID, out.GitExitCode = result.PID, result.ExitCode
	if errors.Is(mergeErr, process.ErrProcessTreeUnknown) {
		return uncertain(mergeErr)
	}
	postHead, postErr := rev(ctx, req.TargetWorktree, "HEAD")
	if postErr != nil {
		return uncertain(postErr)
	}
	out.TargetHEAD = postHead
	postDirty, _, postStatusErr := targetStatus(ctx, req.TargetWorktree)
	if postStatusErr != nil {
		return uncertain(postStatusErr)
	}
	postRef, _, postRefErr := gitText(ctx, req.TargetWorktree, "symbolic-ref", "-q", "HEAD")
	if postRefErr != nil {
		return uncertain(postRefErr)
	}
	postTree, postTreeErr := rev(ctx, req.TargetWorktree, "HEAD^{tree}")
	if postTreeErr != nil {
		return uncertain(postTreeErr)
	}
	postIndex, _, postIndexErr := gitText(ctx, req.TargetWorktree, "write-tree")
	if postIndexErr != nil {
		return uncertain(postIndexErr)
	}
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

type cleanupAdmin struct {
	path, name string
	rootFD     *os.File
	parentFD   *os.File
	rootInfo   os.FileInfo
	files      map[string]FileIdentity
}

func (a *cleanupAdmin) close() {
	if a == nil {
		return
	}
	_ = a.rootFD.Close()
	_ = a.parentFD.Close()
}

func prepareCleanupAdmin(ctx context.Context, root, expectedCommon string) (*cleanupAdmin, error) {
	gitDirText, _, err := gitText(ctx, root, "rev-parse", "--path-format=absolute", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	adminPath := CanonicalPath(gitDirText)
	common := CanonicalPath(expectedCommon)
	adminParent := filepath.Join(common, "worktrees")
	if adminPath == "" || common == "" || filepath.Dir(adminPath) != adminParent {
		return nil, fail(CodeUnknownObject, "cleanup admin", fmt.Errorf("git dir outside managed worktrees"))
	}
	worktreeGitFile, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil || strings.TrimSpace(string(worktreeGitFile)) != "gitdir: "+adminPath {
		return nil, fail(CodeIdentityChanged, "cleanup admin", fmt.Errorf("worktree gitdir binding changed"))
	}
	adminGitFile, err := os.ReadFile(filepath.Join(adminPath, "gitdir"))
	if err != nil || CanonicalPath(strings.TrimSpace(string(adminGitFile))) != filepath.Join(CanonicalPath(root), ".git") {
		return nil, fail(CodeIdentityChanged, "cleanup admin", fmt.Errorf("admin worktree binding changed"))
	}
	parentFD, err := os.Open(adminParent)
	if err != nil {
		return nil, err
	}
	rootFD, err := os.Open(adminPath)
	if err != nil {
		_ = parentFD.Close()
		return nil, err
	}
	rootInfo, err := rootFD.Stat()
	if err != nil {
		_ = rootFD.Close()
		_ = parentFD.Close()
		return nil, err
	}
	pathInfo, err := os.Stat(adminPath)
	if err != nil || !os.SameFile(rootInfo, pathInfo) {
		_ = rootFD.Close()
		_ = parentFD.Close()
		return nil, fail(CodeIdentityChanged, "cleanup admin", fmt.Errorf("admin handle changed"))
	}
	files, err := snapshot(adminPath)
	if err != nil {
		_ = rootFD.Close()
		_ = parentFD.Close()
		return nil, err
	}
	return &cleanupAdmin{path: adminPath, name: filepath.Base(adminPath), rootFD: rootFD, parentFD: parentFD, rootInfo: rootInfo, files: files}, nil
}

func Cleanup(ctx context.Context, req CleanupRequest) (CleanupOutcome, error) {
	receipt, receiptErr := cleanupReceipt(req)
	out := CleanupOutcome{State: CleanupPending, Root: req.Root, PrivateRef: receipt.PrivateRef, RecordedAt: time.Now().UTC()}
	if receiptErr != nil {
		return out, receiptErr
	}
	if req.Journal == nil || req.Root == "" || receipt.WorktreeIdentity == "" {
		return out, fail(CodeInvalidInput, "cleanup", fmt.Errorf("missing cleanup input"))
	}
	if (receipt.PrivateRef == "") != (receipt.PrivateRefExpectedOID == "") {
		return out, fail(CodeInvalidInput, "cleanup", fmt.Errorf("incomplete private ref binding"))
	}
	if receipt.PrivateRef != "" {
		if err := validatePrivateRef(receipt.PrivateRef); err != nil {
			return out, err
		}
		if err := requireOID(receipt.PrivateRefExpectedOID); err != nil {
			return out, err
		}
		root, common, err := validateRepo(ctx, receipt.RepoRoot)
		if err != nil || root != CanonicalPath(receipt.RepoRoot) || common != receipt.CommonDir {
			return out, fail(CodeIdentityChanged, "cleanup private-ref repository", err)
		}
		out.PrivateRefState = "retained"
	}
	if err := noSymlinkComponents(req.Root); err != nil {
		return out, err
	}
	if CanonicalPath(req.Root) != receipt.WorktreeIdentity {
		return out, fail(CodeIdentityChanged, "cleanup", fmt.Errorf("root identity"))
	}
	if _, err := os.Stat(req.Root); os.IsNotExist(err) {
		if receipt.PrivateRef != "" {
			intent := CleanupIntent{Root: req.Root, AttemptID: receipt.AttemptID, CandidateOID: receipt.CommitOID, RootIdentity: receipt.WorktreeIdentity, PrivateRef: receipt.PrivateRef, PrivateRefExpectedOID: receipt.PrivateRefExpectedOID, CreatedAt: time.Now().UTC()}
			if err := req.Journal.PersistCleanupIntent(ctx, intent); err != nil {
				return out, err
			}
			if err := DeletePrivateRef(ctx, receipt.RepoRoot, receipt.PrivateRef, receipt.PrivateRefExpectedOID); err != nil {
				out.Code = string(CodeTargetDrift)
				return out, recordCleanup(ctx, req.Journal, out, err)
			}
			out.PrivateRefState = "deleted"
		}
		out.State = CleanupRemoved
		return out, recordCleanup(ctx, req.Journal, out, nil)
	} else if err != nil {
		return out, err
	}
	rootIdentity, err := directoryIdentity(req.Root)
	if err != nil {
		return out, err
	}
	if receipt.RootIdentity != (DirectoryIdentity{}) && !sameDirectoryIdentity(rootIdentity, receipt.RootIdentity) {
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
	rootParentFD, err := os.Open(filepath.Dir(req.Root))
	if err != nil {
		return out, err
	}
	defer rootParentFD.Close()
	admin, err := prepareCleanupAdmin(ctx, req.Root, receipt.CommonDir)
	if err != nil {
		return out, err
	}
	defer admin.close()
	commonLock, err := acquireIntegrationLock(receipt.CommonDir)
	if err != nil {
		return out, err
	}
	defer releaseIntegrationLock(commonLock)
	for _, pid := range req.ActivePIDs {
		if pid > 0 {
			err := syscall.Kill(pid, 0)
			if err == nil || err != syscall.ESRCH {
				return out, fail(CodeUnknownObject, "cleanup", fmt.Errorf("writer alive or unknown"))
			}
		}
	}
	intent := CleanupIntent{Root: req.Root, AttemptID: receipt.AttemptID, CandidateOID: receipt.CommitOID, RootIdentity: receipt.WorktreeIdentity, PrivateRef: receipt.PrivateRef, PrivateRefExpectedOID: receipt.PrivateRefExpectedOID, CreatedAt: time.Now().UTC()}
	if err := req.Journal.PersistCleanupIntent(ctx, intent); err != nil {
		return out, err
	}
	if receipt.PrivateRef != "" {
		current, exists, refErr := privateRefOID(ctx, receipt.RepoRoot, receipt.PrivateRef)
		if refErr != nil || !exists || current != receipt.PrivateRefExpectedOID {
			out.Code = string(CodeTargetDrift)
			if refErr == nil {
				refErr = fail(CodeTargetDrift, "cleanup private-ref", fmt.Errorf("ref changed"))
			}
			return out, recordCleanup(ctx, req.Journal, out, refErr)
		}
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
		expected, ok := receipt.Files[path]
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
	// Quarantine the classified root through its held parent. All later
	// removals are relative to held descriptors.
	parent := filepath.Dir(req.Root)
	if err := noSymlinkComponents(parent); err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	quarantineName, err := quarantineAt(rootParentFD, filepath.Base(req.Root), ".g3-quarantine-")
	if err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	quarantine := filepath.Join(parent, quarantineName)
	qInfo, err := os.Stat(quarantine)
	if err != nil || !os.SameFile(fdInfo, qInfo) {
		out.Code = string(CodeIdentityChanged)
		return out, recordCleanup(ctx, req.Journal, out, fail(CodeIdentityChanged, "cleanup", fmt.Errorf("quarantine identity changed")))
	}
	if req.AfterQuarantine != nil {
		if err := req.AfterQuarantine(quarantine); err != nil {
			out.Code = string(CodeUnknownObject)
			return out, recordCleanup(ctx, req.Journal, out, err)
		}
	}
	if req.BeforeAnchoredRemove != nil {
		if err := req.BeforeAnchoredRemove(quarantine); err != nil {
			out.Code = string(CodeUnknownObject)
			return out, recordCleanup(ctx, req.Journal, out, err)
		}
	}
	if err := removeAnchoredTree(rootFD, rootParentFD, quarantineName, current, fdInfo); err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}

	// Remove only this worktree's verified administrative directory. This
	// avoids giving Git a reusable pathname and does not touch refs, objects,
	// other worktrees, prune, or GC.
	adminQuarantine, err := quarantineAt(admin.parentFD, admin.name, ".g3-admin-quarantine-")
	if err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	if err := removeAnchoredTree(admin.rootFD, admin.parentFD, adminQuarantine, admin.files, admin.rootInfo); err != nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, err)
	}
	if _, err := os.Lstat(req.Root); err == nil {
		out.Code = string(CodeUnknownObject)
		return out, recordCleanup(ctx, req.Journal, out, fail(CodeUnknownObject, "cleanup", fmt.Errorf("worktree path recreated and retained")))
	} else if !os.IsNotExist(err) {
		return out, err
	}
	if receipt.PrivateRef != "" {
		if err := DeletePrivateRef(ctx, receipt.RepoRoot, receipt.PrivateRef, receipt.PrivateRefExpectedOID); err != nil {
			out.Code = string(CodeTargetDrift)
			return out, recordCleanup(ctx, req.Journal, out, err)
		}
		out.PrivateRefState = "deleted"
	}
	out.State, out.Code, out.RemovedFiles = CleanupRemoved, "", len(current)
	if journalErr := req.Journal.RecordCleanup(ctx, out); journalErr != nil {
		out.State = CleanupPending
		return out, journalErr
	}
	return out, nil
}

type CleanupRequest struct {
	Root             string             `json:"root"`
	Receipt          MaterializeReceipt `json:"receipt,omitempty"`
	CandidateReceipt *CandidateReceipt  `json:"candidate_receipt,omitempty"`
	Journal          Journal            `json:"-"`
	ActivePIDs       []int              `json:"active_pids,omitempty"`
	// BeforeRemove is a test/fault seam. Production leaves it nil.
	BeforeRemove func() error `json:"-"`
	// AfterQuarantine is a test/fault seam used to prove late additions are
	// retained. Production leaves it nil.
	AfterQuarantine func(string) error `json:"-"`
	// BeforeAnchoredRemove runs after the final pathname-based checks. It exists
	// only to prove that descriptor-relative removal retains late replacements.
	BeforeAnchoredRemove func(string) error `json:"-"`
}

type cleanupReceiptBinding struct {
	AttemptID             string
	RepoRoot              string
	CommonDir             string
	CommitOID             string
	WorktreeIdentity      string
	RootIdentity          DirectoryIdentity
	Files                 map[string]FileIdentity
	PrivateRef            string
	PrivateRefExpectedOID string
}

func cleanupReceipt(req CleanupRequest) (cleanupReceiptBinding, error) {
	hasMaterialized := req.Receipt.WorktreeIdentity != ""
	hasCandidate := req.CandidateReceipt != nil
	if hasMaterialized == hasCandidate {
		return cleanupReceiptBinding{}, fail(CodeInvalidInput, "cleanup", fmt.Errorf("exactly one receipt is required"))
	}
	if hasMaterialized {
		return cleanupReceiptBinding{
			AttemptID: req.Receipt.AttemptID, RepoRoot: req.Receipt.RepoRoot, CommonDir: req.Receipt.CommonDir,
			CommitOID: req.Receipt.CommitOID, WorktreeIdentity: req.Receipt.WorktreeIdentity,
			RootIdentity: req.Receipt.RootIdentity, Files: req.Receipt.Files,
		}, nil
	}
	candidate := req.CandidateReceipt
	return cleanupReceiptBinding{
		AttemptID: candidate.AttemptID, RepoRoot: candidate.RepoRoot, CommonDir: candidate.CommonDir,
		CommitOID: candidate.CandidateOID, WorktreeIdentity: candidate.WorktreeIdentity, RootIdentity: candidate.RootIdentity,
		Files:      candidate.Files,
		PrivateRef: candidate.PrivateRef, PrivateRefExpectedOID: candidate.PrivateRefExpectedOID,
	}, nil
}

var privateRefRE = regexp.MustCompile(`^refs/orchestrator/g3/[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func validatePrivateRef(ref string) error {
	if !privateRefRE.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") {
		return fail(CodeInvalidInput, "private-ref", fmt.Errorf("ref outside managed namespace"))
	}
	return nil
}

func privateRefOID(ctx context.Context, repo, ref string) (string, bool, error) {
	if _, _, err := validateRepo(ctx, repo); err != nil {
		return "", false, err
	}
	result, err := run(ctx, repo, "rev-parse", "--verify", "--quiet", ref)
	if err != nil || result.ExitCode != 0 {
		if result.ExitCode == 1 {
			return "", false, nil
		}
		return "", false, fail(CodeGitFailure, "rev-parse private-ref", err)
	}
	if err := requireOID(result.Stdout); err != nil {
		return "", false, err
	}
	return result.Stdout, true, nil
}

// PrivateRef performs a compare-and-swap creation/update.  The expected old
// OID is mandatory; callers use the empty string only when the ref must be
// absent.  No arbitrary user ref can be modified by this package.
func PrivateRef(ctx context.Context, repo, ref, expectedOld, oid string) error {
	if err := validatePrivateRef(ref); err != nil {
		return err
	}
	if _, _, err := validateRepo(ctx, repo); err != nil {
		return err
	}
	if expectedOld != "" {
		if err := requireOID(expectedOld); err != nil {
			return err
		}
	}
	if err := requireOID(oid); err != nil {
		return err
	}
	if err := validateOIDInRepo(ctx, repo, oid); err != nil {
		return err
	}
	current, exists, err := privateRefOID(ctx, repo, ref)
	if err != nil {
		return err
	}
	if expectedOld == "" {
		if exists && current == oid {
			return nil
		}
		if exists {
			return fail(CodeTargetDrift, "create-ref", fmt.Errorf("ref already exists"))
		}
	} else if !exists || current != expectedOld {
		return fail(CodeTargetDrift, "update-ref", fmt.Errorf("ref changed"))
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
	current, exists, err = privateRefOID(ctx, repo, ref)
	if err != nil || !exists || current != oid {
		return fail(CodeTargetDrift, "verify-ref", err)
	}
	return nil
}

func CreatePrivateRef(ctx context.Context, req PrivateRefRequest, journal PrivateRefJournal) (PrivateRefReceipt, error) {
	receipt := PrivateRefReceipt{RepoRoot: CanonicalPath(req.RepoRoot), PrivateRef: req.PrivateRef, ExpectedOID: req.OID, State: "create_pending"}
	if journal == nil || req.RepoRoot == "" || req.PrivateRef == "" || req.OID == "" {
		return receipt, fail(CodeInvalidInput, "create-private-ref", fmt.Errorf("missing input"))
	}
	intent := PrivateRefIntent{Operation: "create_private_ref", RepoRoot: receipt.RepoRoot, PrivateRef: req.PrivateRef, ExpectedOldOID: req.ExpectedOldOID, OID: req.OID, CreatedAt: time.Now().UTC()}
	if err := journal.PersistPrivateRefIntent(ctx, intent); err != nil {
		return receipt, err
	}
	operationErr := PrivateRef(ctx, req.RepoRoot, req.PrivateRef, req.ExpectedOldOID, req.OID)
	outcome := PrivateRefOutcome{Operation: intent.Operation, RepoRoot: receipt.RepoRoot, PrivateRef: req.PrivateRef, OID: req.OID, State: "created", RecordedAt: time.Now().UTC()}
	if operationErr != nil {
		outcome.State, outcome.Code = "rejected", errorCode(operationErr)
	}
	recordErr := journal.RecordPrivateRef(ctx, outcome)
	if operationErr != nil || recordErr != nil {
		return receipt, errors.Join(operationErr, recordErr)
	}
	receipt.State = outcome.State
	return receipt, nil
}

func DeletePrivateRef(ctx context.Context, repo, ref, expected string) error {
	if err := validatePrivateRef(ref); err != nil {
		return err
	}
	if _, _, err := validateRepo(ctx, repo); err != nil {
		return err
	}
	if err := requireOID(expected); err != nil {
		return err
	}
	current, exists, err := privateRefOID(ctx, repo, ref)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if current != expected {
		return fail(CodeTargetDrift, "delete-ref", fmt.Errorf("ref changed"))
	}
	result, err := run(ctx, repo, "update-ref", "-d", ref, expected)
	if err != nil || result.ExitCode != 0 {
		return fail(CodeTargetDrift, "delete-ref", err)
	}
	_, exists, err = privateRefOID(ctx, repo, ref)
	if err != nil || exists {
		return fail(CodeTargetDrift, "verify-delete-ref", err)
	}
	return nil
}

func DeletePrivateRefWithReceipt(ctx context.Context, req DeletePrivateRefRequest, journal PrivateRefJournal) (PrivateRefReceipt, error) {
	receipt := PrivateRefReceipt{RepoRoot: CanonicalPath(req.RepoRoot), PrivateRef: req.PrivateRef, ExpectedOID: req.ExpectedOID, State: "delete_pending"}
	if journal == nil || req.RepoRoot == "" || req.PrivateRef == "" || req.ExpectedOID == "" {
		return receipt, fail(CodeInvalidInput, "delete-private-ref", fmt.Errorf("missing input"))
	}
	intent := PrivateRefIntent{Operation: "delete_private_ref", RepoRoot: receipt.RepoRoot, PrivateRef: req.PrivateRef, OID: req.ExpectedOID, CreatedAt: time.Now().UTC()}
	if err := journal.PersistPrivateRefIntent(ctx, intent); err != nil {
		return receipt, err
	}
	operationErr := DeletePrivateRef(ctx, req.RepoRoot, req.PrivateRef, req.ExpectedOID)
	outcome := PrivateRefOutcome{Operation: intent.Operation, RepoRoot: receipt.RepoRoot, PrivateRef: req.PrivateRef, OID: req.ExpectedOID, State: "deleted", RecordedAt: time.Now().UTC()}
	if operationErr != nil {
		outcome.State, outcome.Code = "retained", errorCode(operationErr)
	}
	recordErr := journal.RecordPrivateRef(ctx, outcome)
	if operationErr != nil || recordErr != nil {
		return receipt, errors.Join(operationErr, recordErr)
	}
	receipt.State = outcome.State
	return receipt, nil
}

func errorCode(err error) string {
	var gitErr *Error
	if errors.As(err, &gitErr) {
		return string(gitErr.Code)
	}
	if err != nil {
		return string(CodeGitFailure)
	}
	return ""
}
func DigestDetail(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
