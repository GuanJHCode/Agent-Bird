package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type candidateReadOnlyKey struct{}

// CandidateProjectionDigest identifies the bounded public receipt projection.
// The legacy candidate_receipt_sha256 continues to name the full Host inventory,
// which is deliberately not embedded in the artifact.
func CandidateProjectionDigest(receipt CandidateReceipt) string {
	receipt.Files = nil
	raw, _ := json.Marshal(receipt)
	return DigestDetail("candidate-receipt-projection-v1\x00" + string(raw))
}

// VerifyFrozenCandidate is a read-only, bounded object check. It never checks out
// files, refreshes an index, executes a diff driver, or asks a remote for objects.
// Object/type and exact diff inspection use batches independent of path count.
func VerifyFrozenCandidate(parent context.Context, c CandidateReceipt, paths []string) error {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	invalid := func() error {
		return fail(CodeTargetDrift, "frozen candidate", fmt.Errorf("candidate object or receipt changed"))
	}
	if c.RepoIdentity == nil || c.CommonIdentity == nil || len(paths) < 1 || len(paths) > 256 || len(c.Changes) < 1 || len(c.Changes) > len(paths) || c.RepoRoot != CanonicalPath(c.RepoRoot) || c.CommonDir != CanonicalPath(c.CommonDir) || c.Worktree != CanonicalPath(c.Worktree) || c.WorktreeIdentity != c.Worktree {
		return invalid()
	}
	for _, oid := range []string{c.BaseOID, c.CandidateOID, c.TreeOID, c.PrivateRefExpectedOID} {
		if requireOID(oid) != nil {
			return invalid()
		}
	}
	if c.PrivateRefExpectedOID != c.CandidateOID || validatePrivateRef(c.PrivateRef) != nil {
		return invalid()
	}
	if err := candidateRepositoryAllowed(ctx, c.RepoRoot); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, candidateReadOnlyKey{}, true)
	git := func(dir string, args ...string) (string, error) {
		prefix := []string{"--no-optional-locks", "--no-pager", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never", "-c", "diff.external=", "-c", "fetch.recurseSubmodules=false"}
		return freezeGit(ctx, dir, nil, append(prefix, args...)...)
	}
	checkDirectories := func() error {
		for _, item := range []struct {
			path string
			want DirectoryIdentity
		}{{c.RepoRoot, *c.RepoIdentity}, {c.CommonDir, *c.CommonIdentity}} {
			got, err := directoryIdentity(item.path)
			if err != nil || got != item.want {
				return invalid()
			}
		}
		return nil
	}
	if err := checkDirectories(); err != nil {
		return err
	}
	out, err := git(c.RepoRoot, "rev-parse", "--show-toplevel", "--path-format=absolute", "--git-common-dir")
	repositoryRecords := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if err != nil || len(repositoryRecords) != 2 || repositoryRecords[0] != c.RepoRoot || CanonicalPath(repositoryRecords[1]) != c.CommonDir {
		return invalid()
	}

	// Disable replacement objects via the controlled runner. Full OIDs and exact
	// types prevent a tag, missing object or promisor from standing in for a commit.
	objects := []string{c.BaseOID, c.CandidateOID, c.TreeOID}
	types := []string{"commit", "commit", "tree"}
	owned := map[string]bool{}
	for _, p := range paths {
		if p == "" || len(p) > 512 || p == "." || p == ".." || filepath.IsAbs(p) || filepath.ToSlash(filepath.Clean(p)) != p || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\x00\r\n") || owned[p] {
			return invalid()
		}
		for _, part := range strings.Split(p, "/") {
			if strings.EqualFold(part, ".git") {
				return invalid()
			}
		}
		owned[p] = true
	}
	changes := map[string]CandidateChange{}
	for _, change := range c.Changes {
		if !owned[change.Path] || changes[change.Path].Path != "" {
			return invalid()
		}
		changes[change.Path] = change
		if !change.Deleted {
			if requireOID(change.BlobOID) != nil || (change.Mode != "100644" && change.Mode != "100755" && change.Mode != "120000") {
				return invalid()
			}
			objects = append(objects, change.BlobOID)
			types = append(types, "blob")
		} else if change.Mode != "" || change.BlobOID != "" {
			return invalid()
		}
	}
	result, err := candidateGit(ctx, c.RepoRoot, strings.NewReader(strings.Join(objects, "\n")+"\n"), "--no-optional-locks", "-c", "protocol.allow=never", "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return err
	}
	records := strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n")
	if len(records) != len(objects) {
		return invalid()
	}
	for i, line := range records {
		if line != objects[i]+" "+types[i] {
			return invalid()
		}
	}
	commit, err := git(c.RepoRoot, "cat-file", "commit", c.CandidateOID)
	if err != nil {
		return err
	}
	headers, _, ok := strings.Cut(commit, "\n\n")
	if !ok {
		return invalid()
	}
	var tree string
	var parents []string
	for _, line := range strings.Split(headers, "\n") {
		if strings.HasPrefix(line, "tree ") {
			if tree != "" {
				return invalid()
			}
			tree = strings.TrimPrefix(line, "tree ")
		}
		if strings.HasPrefix(line, "parent ") {
			parents = append(parents, strings.TrimPrefix(line, "parent "))
		}
	}
	if tree != c.TreeOID || len(parents) != 1 || parents[0] != c.BaseOID {
		return invalid()
	}
	diff, err := git(c.RepoRoot, "diff-tree", "--no-commit-id", "--raw", "-r", "-z", "--no-abbrev", "--no-renames", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", c.BaseOID, c.CandidateOID, "--")
	if err != nil {
		return err
	}
	fields := strings.Split(diff, "\x00")
	if len(fields) < 3 || fields[len(fields)-1] != "" || (len(fields)-1)%2 != 0 {
		return invalid()
	}
	seen := map[string]bool{}
	var priorBlobs []string
	for i := 0; i < len(fields)-1; i += 2 {
		meta := strings.Fields(fields[i])
		path := fields[i+1]
		change, ok := changes[path]
		if !ok || seen[path] || len(meta) != 5 || !strings.HasPrefix(meta[0], ":") {
			return invalid()
		}
		seen[path] = true

		oldMode := strings.TrimPrefix(meta[0], ":")
		if oldMode != "000000" {
			if (oldMode != "100644" && oldMode != "100755" && oldMode != "120000") || requireOID(meta[2]) != nil {
				return invalid()
			}
			priorBlobs = append(priorBlobs, meta[2])
		}
		if change.Deleted {
			if meta[1] != "000000" || strings.Trim(meta[3], "0") != "" || meta[4] != "D" {
				return invalid()
			}
		} else if meta[1] != change.Mode || meta[3] != change.BlobOID || (meta[4] != "A" && meta[4] != "M" && meta[4] != "T") {
			return invalid()
		}
	}
	if len(seen) != len(changes) {
		return invalid()
	}
	if len(priorBlobs) > 0 {
		prior, err := candidateGit(ctx, c.RepoRoot, strings.NewReader(strings.Join(priorBlobs, "\n")+"\n"), "--no-optional-locks", "-c", "protocol.allow=never", "cat-file", "--batch-check=%(objectname) %(objecttype)")
		if err != nil {
			return err
		}
		lines := strings.Split(strings.TrimSuffix(prior.Stdout, "\n"), "\n")
		if len(lines) != len(priorBlobs) {
			return invalid()
		}
		for i, line := range lines {
			if line != priorBlobs[i]+" blob" {
				return invalid()
			}
		}
	}
	// Recheck the mutable ref and kernel directory identities at the end; immutable
	// OID inspection above never relies on the user's HEAD or index.
	ref, err := git(c.RepoRoot, "rev-parse", "--verify", c.PrivateRef)
	if err != nil || strings.TrimSpace(ref) != c.CandidateOID {
		return invalid()
	}
	return checkDirectories()
}
