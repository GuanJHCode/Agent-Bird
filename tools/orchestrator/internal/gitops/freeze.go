package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// CandidateBinding is supplied by the Source Host's coordinator grant, not by
// the provider output. Freezing is called only after ConfirmTreeExited.
type CandidateBinding struct {
	RunID          string `json:"run_id"`
	TaskID         string `json:"task_id"`
	AttemptID      string `json:"attempt_id"`
	SegmentID      string `json:"segment_id"`
	WorkRevision   int    `json:"work_revision"`
	PlanRevision   int    `json:"plan_revision"`
	RevisionSHA256 string `json:"revision_sha256,omitempty"`
}

type CandidateChange struct {
	Path    string `json:"path"`
	Mode    string `json:"mode,omitempty"`
	BlobOID string `json:"blob_oid,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

// PreparedWorkspace is Host-local. It is deliberately not an operation
// accepted by gitops-worker: a caller-supplied "exited" flag is not proof.
type PreparedWorkspace struct {
	Receipt    MaterializeReceipt
	Binding    CandidateBinding
	Paths      []string
	PrivateRef string
}

func PrepareWorkspace(ctx context.Context, req MaterializeRequest, binding CandidateBinding, paths []string, ref string, journal Journal) (PreparedWorkspace, error) {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	if err := candidateRepositoryAllowed(ctx, req.RepoRoot); err != nil {
		return PreparedWorkspace{}, err
	}
	if binding.RunID == "" || binding.TaskID == "" || binding.AttemptID == "" || binding.SegmentID == "" || binding.WorkRevision < 1 || binding.PlanRevision < 1 || req.AttemptID != binding.AttemptID || req.PlanRevision != int64(binding.PlanRevision) || req.BaseOID != req.CandidateOID || len(paths) == 0 || len(paths) > 256 {
		return PreparedWorkspace{}, fail(CodeInvalidInput, "prepare workspace", fmt.Errorf("candidate binding missing"))
	}
	if err := validatePrivateRef(ref); err != nil {
		return PreparedWorkspace{}, err
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || len(path) > 512 || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == "." || path == ".." || strings.HasPrefix(path, "../") || strings.ContainsAny(path, "\x00\r\n") || seen[path] {
			return PreparedWorkspace{}, fail(CodeInvalidInput, "prepare workspace", fmt.Errorf("invalid owned file path"))
		}
		for _, part := range strings.Split(path, "/") {
			if strings.EqualFold(part, ".git") {
				return PreparedWorkspace{}, fail(CodeInvalidInput, "prepare workspace", fmt.Errorf("git metadata is not a source file"))
			}
		}
		seen[path] = true
	}
	inProgress, err := candidateOperationInProgress(ctx, req.RepoRoot)
	if err != nil {
		return PreparedWorkspace{}, err
	}
	if inProgress {
		return PreparedWorkspace{}, fail(CodeTargetDirty, "prepare workspace", fmt.Errorf("git operation in progress"))
	}
	receipt, err := Materialize(ctx, req, journal)
	if err != nil {
		return PreparedWorkspace{}, err
	}
	expectedTree, err := rev(ctx, req.RepoRoot, req.BaseOID+"^{tree}")
	if err != nil {
		return PreparedWorkspace{}, err
	}
	if receipt.CommitOID != req.BaseOID || receipt.TreeOID != expectedTree {
		return PreparedWorkspace{}, fail(CodeTargetDrift, "prepare", fmt.Errorf("materialized base mismatch"))
	}
	return PreparedWorkspace{Receipt: receipt, Binding: binding, Paths: append([]string(nil), paths...), PrivateRef: ref}, nil
}

// FreezeWorkspace commits only a fresh prepared workspace's declared changes.
// Repositories requiring external hooks/signing are rejected before execution.
// A failed or interrupted commit is not
// retried here; the journal and worktree remain for explicit reconciliation.
func FreezeWorkspace(ctx context.Context, prepared PreparedWorkspace, journal CandidateJournal) (CandidateReceipt, error) {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	if err := candidateRepositoryAllowed(ctx, prepared.Receipt.Worktree); err != nil {
		return CandidateReceipt{}, err
	}
	r := prepared.Receipt
	if journal == nil || r.AttemptID == "" || r.AttemptID != prepared.Binding.AttemptID || r.PlanRevision != int64(prepared.Binding.PlanRevision) {
		return CandidateReceipt{}, fail(CodeInvalidInput, "freeze", fmt.Errorf("unbound prepared workspace"))
	}
	lock, err := acquireIntegrationLock(r.CommonDir)
	if err != nil {
		return CandidateReceipt{}, err
	}
	defer releaseIntegrationLock(lock)
	identity, err := directoryIdentity(r.Worktree)
	if err != nil || identity != r.RootIdentity {
		return CandidateReceipt{}, fail(CodeIdentityChanged, "freeze", fmt.Errorf("workspace identity changed"))
	}
	common, err := commonDir(ctx, r.Worktree)
	if err != nil {
		return CandidateReceipt{}, err
	}
	if common != r.CommonDir {
		return CandidateReceipt{}, fail(CodeIdentityChanged, "freeze", fmt.Errorf("repository changed"))
	}
	head, err := rev(ctx, r.Worktree, "HEAD")
	if err != nil {
		return CandidateReceipt{}, err
	}
	inProgress, err := candidateOperationInProgress(ctx, r.Worktree)
	if err != nil {
		return CandidateReceipt{}, err
	}
	if head != r.CommitOID || inProgress {
		return CandidateReceipt{}, fail(CodeTargetDrift, "freeze", fmt.Errorf("workspace base changed"))
	}
	if _, err = freezeGit(ctx, r.Worktree, nil, "diff", "--cached", "--quiet", "--no-ext-diff", r.CommitOID, "--"); err != nil {
		return CandidateReceipt{}, fail(CodeTargetDirty, "freeze", err)
	}
	files, err := snapshotWithContext(ctx, r.Worktree)
	if err != nil {
		return CandidateReceipt{}, err
	}
	if files[".git"] != r.Files[".git"] {
		return CandidateReceipt{}, fail(CodeIdentityChanged, "freeze", fmt.Errorf("git link changed"))
	}
	changes, err := workspaceChanges(ctx, r.Worktree, prepared.Paths)
	if err != nil {
		return CandidateReceipt{}, err
	}
	if len(changes) == 0 {
		return CandidateReceipt{}, fail(CodeInvalidInput, "freeze", fmt.Errorf("no candidate changes"))
	}
	intent := CandidateIntent{AttemptID: r.AttemptID, RepoRoot: r.RepoRoot, Worktree: r.Worktree, BaseOID: r.CommitOID, PrivateRef: prepared.PrivateRef, OrderedInputOIDs: []string{r.CommitOID}, PlanRevision: r.PlanRevision, Binding: &prepared.Binding, Changes: changes}
	if err = journal.PersistCandidateIntent(ctx, intent); err != nil {
		return CandidateReceipt{}, err
	}
	args := []string{"--literal-pathspecs", "add", "--all", "--"}
	for _, change := range changes {
		args = append(args, change.Path)
	}
	if _, err = freezeGit(ctx, r.Worktree, nil, args...); err != nil {
		return CandidateReceipt{}, err
	}
	// Compare the actual staged tree with the pre-add byte inventory. Filters
	// that silently transform content fail closed instead of changing the version.
	tree, err := freezeGit(ctx, r.Worktree, nil, "write-tree")
	if err != nil {
		return CandidateReceipt{}, err
	}
	tree = strings.TrimSpace(tree)
	if err = verifyFrozenChanges(ctx, r.Worktree, r.CommitOID, tree, changes); err != nil {
		return CandidateReceipt{}, err
	}
	if _, err = freezeGit(ctx, r.Worktree, nil, "commit", "-m", "orchestrator: freeze candidate "+DigestDetail(prepared.Binding.RunID+"\x00"+prepared.Binding.TaskID+"\x00"+prepared.Binding.SegmentID)); err != nil {
		return CandidateReceipt{}, err
	}
	candidate, err := rev(ctx, r.Worktree, "HEAD")
	if err != nil {
		return CandidateReceipt{}, err
	}
	parent, err := rev(ctx, r.Worktree, "HEAD^")
	if err != nil {
		return CandidateReceipt{}, err
	}
	if parent != r.CommitOID {
		return CandidateReceipt{}, fail(CodeTargetDrift, "freeze", fmt.Errorf("candidate parent changed"))
	}
	actualTree, err := rev(ctx, r.Worktree, "HEAD^{tree}")
	if err != nil {
		return CandidateReceipt{}, err
	}
	if actualTree != tree {
		return CandidateReceipt{}, fail(CodeTargetDrift, "freeze", fmt.Errorf("commit changed staged candidate"))
	}
	postFiles, err := snapshotWithContext(ctx, r.Worktree)
	if err != nil || !reflect.DeepEqual(files, postFiles) {
		return CandidateReceipt{}, fail(CodeIdentityChanged, "freeze", fmt.Errorf("workspace changed during commit"))
	}
	status, err := freezeGit(ctx, r.Worktree, nil, "status", "--porcelain=v1", "--untracked-files=all", "--ignored")
	if err != nil {
		return CandidateReceipt{}, err
	}
	if status != "" {
		return CandidateReceipt{}, fail(CodeTargetDirty, "freeze", fmt.Errorf("workspace not clean after commit"))
	}
	refIntent := CandidateRefIntent{AttemptID: r.AttemptID, RepoRoot: r.RepoRoot, PrivateRef: prepared.PrivateRef, CandidateOID: candidate}
	if err = journal.PersistCandidateRefIntent(ctx, refIntent); err != nil {
		return CandidateReceipt{}, err
	}
	if err = PrivateRef(ctx, r.RepoRoot, prepared.PrivateRef, "", candidate); err != nil {
		return CandidateReceipt{}, err
	}
	receipt := CandidateReceipt{AttemptID: r.AttemptID, RepoRoot: r.RepoRoot, Worktree: r.Worktree, CommonDir: r.CommonDir, BaseOID: r.CommitOID, CandidateOID: candidate, TreeOID: tree, OrderedInputOIDs: []string{r.CommitOID}, RootIdentity: r.RootIdentity, WorktreeIdentity: r.WorktreeIdentity, Files: postFiles, PlanRevision: r.PlanRevision, PrivateRef: prepared.PrivateRef, PrivateRefExpectedOID: candidate, Binding: &prepared.Binding, Changes: changes}
	if err = journal.RecordCandidateFreeze(ctx, receipt); err != nil {
		return CandidateReceipt{}, err
	}
	return receipt, nil
}

func workspaceChanges(ctx context.Context, worktree string, owned []string) ([]CandidateChange, error) {
	status, err := freezeGit(ctx, worktree, nil, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all", "--ignored")
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, path := range owned {
		allowed[path] = true
	}
	root, err := os.Open(worktree)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	changes := []CandidateChange{}
	for _, entry := range strings.Split(status, "\x00") {
		if entry == "" {
			continue
		}
		if len(entry) < 4 {
			return nil, fail(CodeInvalidInput, "freeze inventory", fmt.Errorf("invalid status"))
		}
		path := entry[3:]
		if entry[:2] == "!!" || !allowed[path] {
			return nil, fail(CodeUnknownObject, "freeze inventory", fmt.Errorf("undeclared or ignored file"))
		}
		change := CandidateChange{Path: path}
		if entry[:2] == " D" {
			change.Deleted = true
			changes = append(changes, change)
			continue
		}
		if entry[:2] != " M" && entry[:2] != "??" {
			return nil, fail(CodeTargetDirty, "freeze inventory", fmt.Errorf("unsupported source change"))
		}
		parent, base, closeParent, err := openRelativeParent(root, path)
		if err != nil {
			return nil, err
		}
		// NONBLOCK prevents a concurrent FIFO substitution from blocking the Host.
		fd, err := unix.Openat(int(parent.Fd()), base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		closeParent()
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(fd), path)
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			file.Close()
			return nil, fail(CodeUnknownObject, "freeze inventory", fmt.Errorf("source is not a regular file"))
		}
		blob, hashErr := freezeGit(ctx, worktree, file, "hash-object", "--stdin")
		after, afterErr := file.Stat()
		file.Close()
		beforeID, _ := identityFromInfo(info)
		afterID := FileIdentity{}
		if afterErr == nil {
			afterID, _ = identityFromInfo(after)
		}
		if hashErr != nil {
			return nil, hashErr
		}
		if afterErr != nil || beforeID != afterID {
			return nil, fail(CodeIdentityChanged, "freeze inventory", fmt.Errorf("source changed while hashing"))
		}
		change.BlobOID = strings.TrimSpace(blob)
		change.Mode = "100644"
		if info.Mode().Perm()&0111 != 0 {
			change.Mode = "100755"
		}
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func verifyFrozenChanges(ctx context.Context, worktree, base, tree string, changes []CandidateChange) error {
	diff, err := freezeGit(ctx, worktree, nil, "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", base, tree)
	if err != nil {
		return err
	}
	paths := strings.Split(strings.TrimSuffix(diff, "\x00"), "\x00")
	want := make([]string, 0, len(changes))
	for _, c := range changes {
		want = append(want, c.Path)
	}
	sort.Strings(paths)
	if !reflect.DeepEqual(paths, want) {
		return fail(CodeTargetDrift, "freeze tree", fmt.Errorf("staged paths changed"))
	}
	for _, change := range changes {
		entry, err := freezeGit(ctx, worktree, nil, "--literal-pathspecs", "ls-tree", "-z", tree, "--", change.Path)
		if err != nil {
			return err
		}
		want := ""
		if !change.Deleted {
			want = change.Mode + " blob " + change.BlobOID + "\t" + change.Path + "\x00"
		}
		if entry != want {
			return fail(CodeTargetDrift, "freeze tree", fmt.Errorf("staged bytes changed"))
		}
	}
	return nil
}

func CandidateArtifact(providerResult string, receipt CandidateReceipt) ([]byte, error) {
	// The complete inventory stays in the durable journal for owned cleanup.
	full, _ := json.Marshal(receipt)
	digest := DigestDetail(string(full))
	receipt.Files = nil
	body, err := json.Marshal(struct {
		ReceiptSHA256  string           `json:"candidate_receipt_sha256"`
		Version        int              `json:"version"`
		ProviderResult string           `json:"provider_result"`
		Candidate      CandidateReceipt `json:"candidate"`
	}{digest, 1, providerResult, receipt})
	if err != nil {
		return nil, err
	}
	if len(body) > 1024*1024 {
		return nil, fail(CodeInvalidInput, "candidate artifact", fmt.Errorf("encoded candidate artifact too large"))
	}
	return body, nil
}

// Check the encoded upper bound before commit/pin. The actual inventory cannot
// contain paths outside prepared.Paths; all variable-length identities are known.
func CheckCandidateArtifactBudget(prepared PreparedWorkspace, providerResult string) error {
	r := prepared.Receipt
	bound := CandidateReceipt{Binding: &prepared.Binding, AttemptID: r.AttemptID, RepoRoot: r.RepoRoot, Worktree: r.Worktree, CommonDir: r.CommonDir, BaseOID: r.CommitOID, CandidateOID: strings.Repeat("f", 64), TreeOID: strings.Repeat("f", 64), OrderedInputOIDs: []string{r.CommitOID}, RootIdentity: r.RootIdentity, WorktreeIdentity: r.WorktreeIdentity, PlanRevision: r.PlanRevision, PrivateRef: prepared.PrivateRef, PrivateRefExpectedOID: strings.Repeat("f", 64)}
	for _, path := range prepared.Paths {
		bound.Changes = append(bound.Changes, CandidateChange{Path: path, Mode: "100755", BlobOID: strings.Repeat("f", 64), Deleted: true})
	}
	_, err := CandidateArtifact(providerResult, bound)
	return err
}

// Candidate inventory is bounded independently of the provider result. ReadDir
// batches also bound allocation for a single directory containing many entries.
func snapshotWithContext(ctx context.Context, root string) (map[string]FileIdentity, error) {
	if managed, _ := ctx.Value(candidateRunnerKey{}).(bool); !managed {
		return snapshot(root)
	}
	root = CanonicalPath(root)
	out := map[string]FileIdentity{}
	pending := []string{root}
	pathBytes := 0
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		f := os.NewFile(uintptr(fd), dir)
		for {
			if err := ctx.Err(); err != nil {
				f.Close()
				return nil, err
			}
			entries, readErr := f.ReadDir(128)
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					f.Close()
					return nil, err
				}
				path := filepath.Join(dir, entry.Name())
				rel, err := filepath.Rel(root, path)
				if err != nil {
					f.Close()
					return nil, err
				}
				pathBytes += len(rel)
				if len(out) >= 4096 || pathBytes > 256*1024 {
					f.Close()
					return nil, fail(CodeInvalidInput, "candidate inventory", fmt.Errorf("inventory limit exceeded"))
				}
				id, err := fileIdentity(path)
				if err != nil {
					f.Close()
					return nil, err
				}
				out[filepath.ToSlash(rel)] = id
				if os.FileMode(id.Mode).IsDir() {
					pending = append(pending, path)
				}
			}
			if readErr != nil {
				f.Close()
				if readErr != io.EOF {
					return nil, readErr
				}
				break
			}
		}
	}
	return out, nil
}
