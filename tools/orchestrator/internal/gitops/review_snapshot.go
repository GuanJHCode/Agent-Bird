package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const (
	reviewSnapshotRawLimit  = 128 * 1024
	reviewSnapshotFileLimit = 64
	reviewSnapshotJSONLimit = 256 * 1024
)

// The indexed path is intentionally opt-in. Its manifest is compact metadata;
// chunk bytes are reconstructed only from the receipt's immutable Git blobs.
const (
	indexedReviewPathLimit        = 512
	indexedReviewPathBytesLimit   = 64 * 1024
	indexedReviewTextLimit        = 8 * 1024 * 1024
	indexedReviewBlobReadLimit    = 8 * 1024 * 1024
	indexedReviewChunkLimit       = 32 * 1024
	indexedReviewChunkCountLimit  = 2048
	indexedReviewPageEntriesLimit = 64
	indexedReviewPageJSONLimit    = 64 * 1024
	indexedReviewManifestLimit    = 1024 * 1024
)

type ReviewIndexChunk = contract.IndexedReviewChunk
type ReviewIndexPage = contract.IndexedReviewPage
type ReviewIndex = contract.IndexedReviewManifest

func reviewChunkDigest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ReviewIndexManifestSHA256 binds persisted metadata without serializing source
// content into an accepted artifact.
func ReviewIndexManifestSHA256(index ReviewIndex) string {
	raw, err := json.Marshal(index)
	if err != nil {
		return ""
	}
	return reviewChunkDigest(raw)
}

// HydrateReviewChunks reads only the immutable blob ranges assigned by an
// accepted manifest. It never rebuilds an index or discovers paths.
func HydrateReviewChunks(ctx context.Context, receipt CandidateReceipt, chunks []ReviewIndexChunk) ([]ReviewIndexChunk, error) {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	if err := validateReviewSnapshotReceipt(ctx, receipt); err != nil {
		return nil, err
	}
	out := append([]ReviewIndexChunk(nil), chunks...)
	byBlob := map[string][]int{}
	retained := 0
	for i := range out {
		if out[i].Length < 0 || out[i].Length > indexedReviewChunkLimit || out[i].Offset < 0 || out[i].Offset > indexedReviewTextLimit || out[i].Length > indexedReviewTextLimit-out[i].Offset || out[i].BlobOID == "" {
			return nil, fail(CodeInvalidInput, "review hydrate", fmt.Errorf("invalid chunk"))
		}
		retained += out[i].Length
		if retained > 384*1024 {
			return nil, fail(CodeInvalidInput, "review hydrate", fmt.Errorf("assigned text exceeds consumer limit"))
		}
		byBlob[out[i].BlobOID] = append(byBlob[out[i].BlobOID], i)
	}
	read := 0
	for oid, positions := range byBlob {
		sizeText, err := freezeGit(ctx, receipt.Worktree, nil, "cat-file", "-s", oid)
		if err != nil {
			return nil, err
		}
		var size int
		if _, err = fmt.Sscanf(strings.TrimSpace(sizeText), "%d", &size); err != nil || size < 0 || size > indexedReviewBlobReadLimit || size > indexedReviewTextLimit-read {
			return nil, fail(CodeInvalidInput, "review hydrate", fmt.Errorf("blob read exceeds limit"))
		}
		read += size
		c := &hydrateCollector{chunks: out, positions: positions, expected: size, whole: sha256.New()}
		if err = candidateGitStream(ctx, receipt.Worktree, c, "cat-file", "blob", oid); err != nil {
			return nil, err
		}
		if c.seen != size {
			return nil, fail(CodeUnknownObject, "review hydrate", fmt.Errorf("blob size changed"))
		}
		whole := hex.EncodeToString(c.whole.Sum(nil))
		for _, pos := range positions {
			if out[pos].WholeSHA256 != whole || len(out[pos].Content) != out[pos].Length || reviewChunkDigest([]byte(out[pos].Content)) != out[pos].SHA256 {
				return nil, fail(CodeUnknownObject, "review hydrate", fmt.Errorf("chunk mismatch"))
			}
		}
	}
	return out, nil
}

type hydrateCollector struct {
	chunks         []ReviewIndexChunk
	positions      []int
	expected, seen int
	whole          hash.Hash
}

func (c *hydrateCollector) Write(p []byte) (int, error) {
	start := c.seen
	c.seen += len(p)
	_, _ = c.whole.Write(p)
	for _, pos := range c.positions {
		q := &c.chunks[pos]
		a, b := q.Offset, q.Offset+q.Length
		lo, hi := a, b
		if lo < start {
			lo = start
		}
		if hi > c.seen {
			hi = c.seen
		}
		if lo < hi {
			q.Content += string(p[lo-start : hi-start])
		}
	}
	return len(p), nil
}

// CandidateReviewIndex enumerates both immutable trees, preserving a distinct
// logical side even when their blob OIDs match. It rejects unsupported entries
// rather than presenting partial coverage as complete.
func CandidateReviewIndex(ctx context.Context, receipt CandidateReceipt, receiptSHA256 string) (ReviewIndex, error) {
	ctx = context.WithValue(ctx, candidateRunnerKey{}, true)
	if len(receiptSHA256) != 64 {
		return ReviewIndex{}, fail(CodeInvalidInput, "review index", fmt.Errorf("invalid candidate receipt digest"))
	}
	if err := validateReviewSnapshotReceipt(ctx, receipt); err != nil {
		return ReviewIndex{}, err
	}
	baseTree, err := rev(ctx, receipt.Worktree, receipt.BaseOID+"^{tree}")
	if err != nil {
		return ReviewIndex{}, err
	}
	candidateTree, err := rev(ctx, receipt.Worktree, receipt.CandidateOID+"^{tree}")
	if err != nil {
		return ReviewIndex{}, err
	}
	if candidateTree != receipt.TreeOID {
		return ReviewIndex{}, fail(CodeTargetDrift, "review index", fmt.Errorf("candidate tree differs from receipt"))
	}
	if err := verifyFrozenChanges(ctx, receipt.Worktree, receipt.BaseOID, candidateTree, receipt.Changes); err != nil {
		return ReviewIndex{}, err
	}
	base, err := reviewTree(ctx, receipt.Worktree, baseTree)
	if err != nil {
		return ReviewIndex{}, err
	}
	candidate, err := reviewTree(ctx, receipt.Worktree, candidateTree)
	if err != nil {
		return ReviewIndex{}, err
	}
	paths, seen := make([]string, 0, len(base)+len(candidate)), map[string]bool{}
	for p := range base {
		seen[p] = true
	}
	for p := range candidate {
		seen[p] = true
	}
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) > indexedReviewPathLimit {
		return ReviewIndex{}, fail(CodeInvalidInput, "review index", fmt.Errorf("too many tracked paths"))
	}
	pathBytes := 0
	index := ReviewIndex{Version: 1, Scope: "complete-tracked-text-base-and-candidate", CandidateReceiptSHA256: receiptSHA256, BaseOID: receipt.BaseOID, CandidateOID: receipt.CandidateOID, TreeOID: receipt.TreeOID}
	for _, path := range paths {
		pathBytes += len(path)
		if pathBytes > indexedReviewPathBytesLimit {
			return ReviewIndex{}, fail(CodeInvalidInput, "review index", fmt.Errorf("path bytes exceed limit"))
		}
		for _, side := range []struct {
			name  string
			entry reviewTreeEntry
			ok    bool
		}{{"base", base[path], base[path].oid != ""}, {"candidate", candidate[path], candidate[path].oid != ""}} {
			if !side.ok {
				continue
			}
			if err := appendIndexedReviewBlob(ctx, receipt.Worktree, path, side.name, side.entry, &index); err != nil {
				return ReviewIndex{}, err
			}
		}
	}
	for start := 0; start < len(index.Chunks); start += indexedReviewPageEntriesLimit {
		end := start + indexedReviewPageEntriesLimit
		if end > len(index.Chunks) {
			end = len(index.Chunks)
		}
		ordinals := make([]int, 0, end-start)
		for _, chunk := range index.Chunks[start:end] {
			ordinals = append(ordinals, chunk.Ordinal)
		}
		page := ReviewIndexPage{Ordinal: len(index.Pages), ChunkOrdinals: ordinals}
		if raw, _ := json.Marshal(page); len(raw) > indexedReviewPageJSONLimit {
			return ReviewIndex{}, fail(CodeInvalidInput, "review index", fmt.Errorf("page exceeds limit"))
		}
		index.Pages = append(index.Pages, page)
	}
	// Content is deliberately excluded by the json tag, so this marshal is the
	// durable metadata manifest rather than a second content store.
	if raw, _ := json.Marshal(index); len(raw) > indexedReviewManifestLimit {
		return ReviewIndex{}, fail(CodeInvalidInput, "review index", fmt.Errorf("manifest exceeds limit"))
	}
	if err := contract.ValidateIndexedReviewManifest(index); err != nil {
		return ReviewIndex{}, fail(CodeInvalidInput, "review index", err)
	}
	return index, nil
}

func appendIndexedReviewBlob(ctx context.Context, repo, path, side string, entry reviewTreeEntry, index *ReviewIndex) error {
	sizeText, err := freezeGit(ctx, repo, nil, "cat-file", "-s", entry.oid)
	if err != nil {
		return err
	}
	var size int
	if _, err = fmt.Sscanf(strings.TrimSpace(sizeText), "%d", &size); err != nil || size < 0 || size > indexedReviewBlobReadLimit {
		return fail(CodeInvalidInput, "review index", fmt.Errorf("blob exceeds read limit"))
	}
	if size > indexedReviewTextLimit-indexedTextBytes(index.Chunks) {
		return fail(CodeInvalidInput, "review index", fmt.Errorf("logical text exceeds limit"))
	}
	c := indexedChunkCollector{path: path, side: side, entry: entry, index: index, expected: size, whole: sha256.New()}
	if err := candidateGitStream(ctx, repo, &c, "cat-file", "blob", entry.oid); err != nil {
		return err
	}
	return c.finish()
}

func indexedTextBytes(chunks []ReviewIndexChunk) int {
	n := 0
	for _, c := range chunks {
		n += c.Length
	}
	return n
}

type indexedChunkCollector struct {
	path, side             string
	entry                  reviewTreeEntry
	index                  *ReviewIndex
	expected, seen, offset int
	pending, prefix        []byte
	whole                  hash.Hash
	invalid                bool
}

func (c *indexedChunkCollector) Write(p []byte) (int, error) {
	c.seen += len(p)
	if c.seen > c.expected || strings.IndexByte(string(p), 0) >= 0 {
		c.invalid = true
		return len(p), nil
	}
	_, _ = c.whole.Write(p)
	if len(c.prefix) < 64 {
		n := 64 - len(c.prefix)
		if n > len(p) {
			n = len(p)
		}
		c.prefix = append(c.prefix, p[:n]...)
	}
	c.pending = append(c.pending, p...)
	for len(c.pending) >= indexedReviewChunkLimit {
		end := indexedReviewChunkLimit
		for end > 0 && !utf8.Valid(c.pending[:end]) {
			end--
		}
		if end == 0 {
			c.invalid = true
			return len(p), nil
		}
		if err := c.append(c.pending[:end]); err != nil {
			c.invalid = true
			return len(p), nil
		}
		c.pending = append(c.pending[:0], c.pending[end:]...)
	}
	return len(p), nil
}
func (c *indexedChunkCollector) append(part []byte) error {
	if len(c.index.Chunks) >= indexedReviewChunkCountLimit {
		return fmt.Errorf("too many chunks")
	}
	c.index.Chunks = append(c.index.Chunks, ReviewIndexChunk{Ordinal: len(c.index.Chunks), Path: c.path, Side: c.side, Mode: c.entry.mode, BlobOID: c.entry.oid, Offset: c.offset, Length: len(part), SHA256: reviewChunkDigest(part)})
	c.offset += len(part)
	return nil
}
func (c *indexedChunkCollector) finish() error {
	if c.invalid || c.seen != c.expected || !utf8.Valid(c.pending) || strings.HasPrefix(string(c.prefix), "version https://git-lfs.github.com/spec/v1\n") {
		return fail(CodeUnknownObject, "review index", fmt.Errorf("unsupported tracked content"))
	}
	if len(c.pending) > 0 {
		if err := c.append(c.pending); err != nil {
			return fail(CodeInvalidInput, "review index", err)
		}
	} else if c.expected == 0 {
		if err := c.append(nil); err != nil {
			return fail(CodeInvalidInput, "review index", err)
		}
	}
	whole := hex.EncodeToString(c.whole.Sum(nil))
	start := len(c.index.Chunks)
	for start > 0 && c.index.Chunks[start-1].BlobOID == c.entry.oid && c.index.Chunks[start-1].Path == c.path && c.index.Chunks[start-1].Side == c.side {
		start--
	}
	for i := start; i < len(c.index.Chunks); i++ {
		c.index.Chunks[i].WholeSHA256 = whole
	}
	return nil
}

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
