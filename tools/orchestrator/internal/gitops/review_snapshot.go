package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	reviewSnapshotRawLimit  = 128 * 1024
	reviewSnapshotFileLimit = 64
	reviewSnapshotJSONLimit = 256 * 1024
)

type reviewSnapshotBlob struct {
	Mode    string `json:"mode"`
	BlobOID string `json:"blob_oid"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

type reviewSnapshotFile struct {
	Path      string              `json:"path"`
	Base      *reviewSnapshotBlob `json:"base"`
	Candidate *reviewSnapshotBlob `json:"candidate"`
}

type reviewSnapshot struct {
	Version      int                  `json:"version"`
	Scope        string               `json:"scope"`
	BaseOID      string               `json:"base_oid"`
	CandidateOID string               `json:"candidate_oid"`
	TreeOID      string               `json:"tree_oid"`
	Files        []reviewSnapshotFile `json:"files"`
}

type reviewTreeEntry struct {
	mode string
	oid  string
}

// CandidateReviewSnapshot returns a bounded, deterministic review payload made
// only from the receipt's immutable base and candidate Git objects.
func CandidateReviewSnapshot(ctx context.Context, receipt CandidateReceipt) ([]byte, error) {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	if err := validateReviewSnapshotReceipt(ctx, receipt); err != nil {
		return nil, err
	}
	baseTree, err := rev(ctx, receipt.Worktree, receipt.BaseOID+"^{tree}")
	if err != nil {
		return nil, err
	}
	candidateTree, err := rev(ctx, receipt.Worktree, receipt.CandidateOID+"^{tree}")
	if err != nil {
		return nil, err
	}
	if candidateTree != receipt.TreeOID {
		return nil, fail(CodeTargetDrift, "review snapshot", fmt.Errorf("candidate tree differs from receipt"))
	}
	if err := verifyFrozenChanges(ctx, receipt.Worktree, receipt.BaseOID, candidateTree, receipt.Changes); err != nil {
		return nil, err
	}
	base, err := reviewTree(ctx, receipt.Worktree, baseTree)
	if err != nil {
		return nil, err
	}
	candidate, err := reviewTree(ctx, receipt.Worktree, candidateTree)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(base)+len(candidate))
	seen := make(map[string]bool, len(base)+len(candidate))
	for path := range base {
		seen[path] = true
	}
	for path := range candidate {
		seen[path] = true
	}
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) > reviewSnapshotFileLimit {
		return nil, fail(CodeInvalidInput, "review snapshot", fmt.Errorf("too many tracked files"))
	}

	remaining := reviewSnapshotRawLimit
	files := make([]reviewSnapshotFile, 0, len(paths))
	for _, path := range paths {
		file := reviewSnapshotFile{Path: path}
		if entry, ok := base[path]; ok {
			file.Base, err = reviewBlob(ctx, receipt.Worktree, entry, &remaining)
			if err != nil {
				return nil, err
			}
		}
		if entry, ok := candidate[path]; ok {
			file.Candidate, err = reviewBlob(ctx, receipt.Worktree, entry, &remaining)
			if err != nil {
				return nil, err
			}
		}
		files = append(files, file)
	}
	body, err := json.Marshal(reviewSnapshot{Version: 1, Scope: "complete-tracked-text-base-and-candidate", BaseOID: receipt.BaseOID, CandidateOID: receipt.CandidateOID, TreeOID: receipt.TreeOID, Files: files})
	if err != nil {
		return nil, err
	}
	if len(body) > reviewSnapshotJSONLimit {
		return nil, fail(CodeInvalidInput, "review snapshot", fmt.Errorf("encoded snapshot exceeds limit"))
	}
	return body, nil
}

func validateReviewSnapshotReceipt(ctx context.Context, receipt CandidateReceipt) error {
	if receipt.RepoRoot == "" || receipt.Worktree == "" || receipt.CommonDir == "" || len(receipt.Changes) == 0 {
		return fail(CodeInvalidInput, "review snapshot", fmt.Errorf("incomplete candidate receipt"))
	}
	for _, oid := range []string{receipt.BaseOID, receipt.CandidateOID, receipt.TreeOID} {
		if err := requireOID(oid); err != nil {
			return err
		}
	}
	if err := candidateRepositoryAllowed(ctx, receipt.RepoRoot); err != nil {
		return err
	}
	if err := candidateRepositoryAllowed(ctx, receipt.Worktree); err != nil {
		return err
	}
	root, common, err := validateRepo(ctx, receipt.RepoRoot)
	if err != nil {
		return err
	}
	worktreeRoot, worktreeCommon, err := validateRepo(ctx, receipt.Worktree)
	if err != nil {
		return err
	}
	if root != CanonicalPath(receipt.RepoRoot) || worktreeRoot != CanonicalPath(receipt.Worktree) || common != worktreeCommon || common != CanonicalPath(receipt.CommonDir) {
		return fail(CodeIdentityChanged, "review snapshot", fmt.Errorf("receipt repository changed"))
	}
	if err = validateOIDInRepo(ctx, receipt.Worktree, receipt.BaseOID); err != nil {
		return err
	}
	if err = validateOIDInRepo(ctx, receipt.Worktree, receipt.CandidateOID); err != nil {
		return err
	}
	parent, err := rev(ctx, receipt.Worktree, receipt.CandidateOID+"^")
	if err != nil {
		return err
	}
	if parent != receipt.BaseOID {
		return fail(CodeTargetDrift, "review snapshot", fmt.Errorf("candidate parent differs from receipt base"))
	}
	previous := ""
	for _, change := range receipt.Changes {
		if err := validateReviewPath(change.Path); err != nil {
			return err
		}
		if previous >= change.Path {
			return fail(CodeInvalidInput, "review snapshot", fmt.Errorf("changes are unordered or duplicated"))
		}
		previous = change.Path
		if change.Deleted {
			continue
		}
		if (change.Mode != "100644" && change.Mode != "100755") || requireOID(change.BlobOID) != nil {
			return fail(CodeInvalidInput, "review snapshot", fmt.Errorf("invalid candidate change"))
		}
	}
	return nil
}

func reviewTree(ctx context.Context, repo, tree string) (map[string]reviewTreeEntry, error) {
	text, err := freezeGit(ctx, repo, nil, "ls-tree", "-r", "-z", tree)
	if err != nil {
		return nil, err
	}
	out := make(map[string]reviewTreeEntry)
	for _, record := range strings.Split(strings.TrimSuffix(text, "\x00"), "\x00") {
		if record == "" {
			continue
		}
		meta, path, ok := strings.Cut(record, "\t")
		parts := strings.Split(meta, " ")
		if !ok || len(parts) != 3 || parts[1] != "blob" || (parts[0] != "100644" && parts[0] != "100755") {
			return nil, fail(CodeUnknownObject, "review snapshot", fmt.Errorf("unsupported tree entry"))
		}
		if err := validateReviewPath(path); err != nil {
			return nil, err
		}
		if err := requireOID(parts[2]); err != nil {
			return nil, err
		}
		if _, exists := out[path]; exists {
			return nil, fail(CodeUnknownObject, "review snapshot", fmt.Errorf("duplicate tree path"))
		}
		out[path] = reviewTreeEntry{mode: parts[0], oid: parts[2]}
	}
	return out, nil
}

func validateReviewPath(path string) error {
	if path == "" || len(path) > 512 || !utf8.ValidString(path) || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\r\n") || path == "." || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "//") {
		return fail(CodeInvalidInput, "review snapshot path", fmt.Errorf("invalid tracked path"))
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return fail(CodeInvalidInput, "review snapshot path", fmt.Errorf("invalid tracked path"))
		}
	}
	return nil
}

func reviewBlob(ctx context.Context, repo string, entry reviewTreeEntry, remaining *int) (*reviewSnapshotBlob, error) {
	sizeText, err := freezeGit(ctx, repo, nil, "cat-file", "-s", entry.oid)
	if err != nil {
		return nil, err
	}
	var size int
	if _, err = fmt.Sscanf(strings.TrimSpace(sizeText), "%d", &size); err != nil || size < 0 || size > *remaining {
		return nil, fail(CodeInvalidInput, "review snapshot", fmt.Errorf("tracked content exceeds limit"))
	}
	content, err := freezeGit(ctx, repo, nil, "cat-file", "blob", entry.oid)
	if err != nil {
		return nil, err
	}
	if len(content) != size || strings.Contains(content, "\x00") || !utf8.ValidString(content) || strings.HasPrefix(content, "version https://git-lfs.github.com/spec/v1\n") {
		return nil, fail(CodeUnknownObject, "review snapshot", fmt.Errorf("unsupported tracked content"))
	}
	actualOID, err := freezeGit(ctx, repo, strings.NewReader(content), "hash-object", "--stdin")
	if err != nil || strings.TrimSpace(actualOID) != entry.oid {
		return nil, fail(CodeUnknownObject, "review snapshot", fmt.Errorf("blob identity changed"))
	}
	*remaining -= size
	hash := sha256.Sum256([]byte(content))
	return &reviewSnapshotBlob{Mode: entry.mode, BlobOID: entry.oid, SHA256: hex.EncodeToString(hash[:]), Content: content}, nil
}
