package gitops

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
)

const filterWorkspaceMaximumBytes int64 = 256 << 20

type filterTreeEntry struct {
	path, mode, oid string
	size            int64
}

// Git's diagnostic capture deliberately does not propagate stdout errors.
// Every consumer must remember its first error and inspect it after Wait.
type filterBoundedWriter struct {
	dst          io.Writer
	limit, count int64
	err          error
}

func (w *filterBoundedWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if int64(len(p)) > w.limit-w.count {
		w.err = fail(CodeInvalidInput, "filter byte limit", nil)
		return 0, w.err
	}
	n, err := w.dst.Write(p)
	w.count += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}
func filterBoundedQuery(ctx context.Context, g *FilterGitContext, input []byte, args ...string) ([]byte, error) {
	var out bytes.Buffer
	w := &filterBoundedWriter{dst: &out, limit: 1 << 20}
	err := candidateGitInputStream(g.Context(ctx), g.repoRoot, input, w, args...)
	return out.Bytes(), errors.Join(err, w.err)
}
func filterSafePath(p string) bool {
	if p == "" || len(p) > 512 || !utf8.ValidString(p) || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}
func filterReadTree(ctx context.Context, g *FilterGitContext, oid string) ([]filterTreeEntry, error) {
	raw, err := filterBoundedQuery(ctx, g, nil, "ls-tree", "-r", "-l", "-z", "--full-tree", oid)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return nil, fail(CodeInvalidInput, "filter tree framing", nil)
	}
	out := []filterTreeEntry{}
	inventory := map[string]string{".git": ".git"}
	dirs := map[string]bool{}
	pathBytes := len(".git")
	add := func(p string, dir bool) error {
		key := strings.ToLower(p)
		if prior, exists := inventory[key]; exists {
			if prior == p && dirs[p] && dir {
				return nil
			}
			return fail(CodeInvalidInput, "filter tree collision", nil)
		}
		if !filterSafePath(p) || len(inventory) >= 4096 || pathBytes+len(p) > 256<<10 {
			return fail(CodeInvalidInput, "filter tree inventory", nil)
		}
		inventory[key] = p
		dirs[p] = dir
		pathBytes += len(p)
		return nil
	}
	for _, record := range bytes.Split(raw[:len(raw)-1], []byte{0}) {
		meta, p, ok := strings.Cut(string(record), "\t")
		parts := strings.Fields(meta)
		if !ok || len(parts) != 4 || (parts[0] != "100644" && parts[0] != "100755") || parts[1] != "blob" || requireOID(parts[2]) != nil || parts[2] != strings.ToLower(parts[2]) {
			return nil, fail(CodeInvalidInput, "filter tree entry", nil)
		}
		size, e := strconv.ParseInt(parts[3], 10, 64)
		if e != nil || size < 0 || size > gitfilter.MaximumBytes {
			return nil, fail(CodeInvalidInput, "filter blob size", e)
		}
		if err = add(p, false); err != nil {
			return nil, err
		}
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			if err = add(d, true); err != nil {
				return nil, err
			}
		}
		out = append(out, filterTreeEntry{p, parts[0], parts[2], size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}
func filterReadBlob(ctx context.Context, g *FilterGitContext, e filterTreeEntry, dst io.Writer) (string, error) {
	var object hash.Hash = sha1.New()
	if len(e.oid) == 64 {
		object = sha256.New()
	}
	_, _ = fmt.Fprintf(object, "blob %d\x00", e.size)
	content := sha256.New()
	w := &filterBoundedWriter{dst: io.MultiWriter(dst, object, content), limit: e.size}
	err := candidateGitStream(g.Context(ctx), g.repoRoot, w, "cat-file", "blob", e.oid)
	if err = errors.Join(err, w.err); err != nil {
		return "", err
	}
	if w.count != e.size || hex.EncodeToString(object.Sum(nil)) != e.oid {
		return "", fail(CodeIdentityChanged, "filter raw object", nil)
	}
	return hex.EncodeToString(content.Sum(nil)), nil
}
func filterCheckTree(ctx context.Context, g *FilterGitContext, entries []filterTreeEntry) error {
	byPath := map[string]filterTreeEntry{}
	filtered := map[string]bool{}
	attrs := 0
	paths := make([]string, 0, len(entries))
	for _, p := range g.manifest.Paths {
		filtered[p] = true
	}
	for _, e := range entries {
		byPath[e.path] = e
		paths = append(paths, e.path)
		if strings.EqualFold(path.Base(e.path), ".gitattributes") {
			pin, ok := g.attributes[e.path]
			if !ok || e.size > filterAttributeMaximumBytes {
				return fail(CodeInvalidInput, "filter tree attributes", nil)
			}
			sha, err := filterReadBlob(ctx, g, e, io.Discard)
			if err != nil {
				return err
			}
			if sha != pin.sha256 {
				return fail(CodeIdentityChanged, "filter tree attributes", nil)
			}
			attrs++
		}
	}
	if attrs != len(g.attributes) {
		return fail(CodeInvalidInput, "filter tree attribute set", nil)
	}
	for p := range filtered {
		if _, ok := byPath[p]; !ok {
			return fail(CodeInvalidInput, "filter path missing from tree", nil)
		}
	}
	input := []byte(strings.Join(paths, "\x00") + "\x00")
	raw, err := filterBoundedQuery(ctx, g, input, "check-attr", "--all", "-z", "--stdin")
	if err != nil {
		return err
	}
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return fail(CodeInvalidInput, "filter attribute vector", nil)
	}
	parts := strings.Split(string(raw[:len(raw)-1]), "\x00")
	seen := map[string]bool{}
	if len(parts)%3 != 0 {
		return fail(CodeInvalidInput, "filter attribute vector", nil)
	}
	for i := 0; i < len(parts); i += 3 {
		p := parts[i]
		if !filtered[p] || seen[p] || parts[i+1] != "filter" || parts[i+2] != g.manifest.DriverID {
			return fail(CodeInvalidInput, "filter undeclared tree attribute", nil)
		}
		seen[p] = true
	}
	if len(seen) != len(filtered) {
		return fail(CodeInvalidInput, "filter attribute coverage", nil)
	}
	return g.Verify(ctx)
}
