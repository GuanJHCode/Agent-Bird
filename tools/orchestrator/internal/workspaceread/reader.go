// Package workspaceread supplies bounded repository context without executing
// shell commands or granting access outside one bound workspace.
package workspaceread

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

type CodeError string

func (e CodeError) Error() string { return string(e) }

const (
	maxFileBytes   = 1024 * 1024
	maxOutputBytes = 32 * 1024
)

type Reader struct {
	mu       sync.RWMutex
	path     string
	root     *os.Root
	identity os.FileInfo
	closed   bool
}

func Open(path string) (*Reader, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, CodeError("workspace_untrusted")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || !owned(info) || info.Mode().Perm()&0022 != 0 {
		return nil, CodeError("workspace_untrusted")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, CodeError("workspace_unavailable")
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, CodeError("workspace_changed")
	}
	return &Reader{path: path, root: root, identity: info}, nil
}

func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.root.Close()
}

func (r *Reader) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return CodeError("workspace_read_cancelled")
	}
	if r.closed {
		return CodeError("workspace_closed")
	}
	info, err := os.Lstat(r.path)
	if err != nil || !info.IsDir() || !os.SameFile(info, r.identity) || !owned(info) || info.Mode().Perm()&0022 != 0 {
		return CodeError("workspace_changed")
	}
	canonical, err := filepath.EvalSymlinks(r.path)
	if err != nil || canonical != r.path {
		return CodeError("workspace_changed")
	}
	return nil
}

func validPath(path string, dot bool) bool {
	if path == "." {
		return dot
	}
	if path == "" || len(path) > 1024 || filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == "." || part == "" || strings.EqualFold(part, ".git") || strings.EqualFold(part, ".codex") || strings.EqualFold(part, ".agent-bird") {
			return false
		}
	}
	return true
}

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func ordinaryFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && owned(info) && stat.Nlink == 1
}

// Each component is opened relative to an already-held directory. Checking the
// opened identity against Lstat rejects symlink substitution as well as aliases.
func childRoot(parent *os.Root, name string, info os.FileInfo) (*os.Root, error) {
	if !info.IsDir() || !owned(info) {
		return nil, CodeError("workspace_path_untrusted")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, CodeError("workspace_path_unavailable")
	}
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		child.Close()
		return nil, CodeError("workspace_path_changed")
	}
	return child, nil
}

func (r *Reader) readFile(path string) ([]byte, error) {
	if !validPath(path, false) {
		return nil, CodeError("workspace_path_rejected")
	}
	parts := strings.Split(path, "/")
	parent := r.root
	var held []*os.Root
	defer func() {
		for _, dir := range held {
			_ = dir.Close()
		}
	}()
	for _, part := range parts[:len(parts)-1] {
		info, err := parent.Lstat(part)
		if err != nil {
			return nil, CodeError("workspace_path_unavailable")
		}
		child, err := childRoot(parent, part, info)
		if err != nil {
			return nil, err
		}
		held = append(held, child)
		parent = child
	}
	name := parts[len(parts)-1]
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, CodeError("workspace_path_unavailable")
	}
	if !ordinaryFile(info) {
		return nil, CodeError("workspace_file_untrusted")
	}
	if info.Size() > maxFileBytes {
		return nil, CodeError("workspace_file_too_large")
	}
	f, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, CodeError("workspace_file_unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !ordinaryFile(opened) || !os.SameFile(info, opened) || info.Size() != opened.Size() || !info.ModTime().Equal(opened.ModTime()) {
		return nil, CodeError("workspace_file_changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || len(raw) > maxFileBytes || int64(len(raw)) != info.Size() || !ordinaryFile(after) || !os.SameFile(info, after) || after.Size() != info.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, CodeError("workspace_file_changed")
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, CodeError("workspace_file_not_text")
	}
	return raw, nil
}

type File struct {
	Path      string   `json:"path"`
	StartLine int      `json:"start_line"`
	Lines     []string `json:"lines"`
	NextLine  int      `json:"next_line,omitempty"`
	Truncated bool     `json:"truncated"`
}

func (r *Reader) Read(ctx context.Context, path string, start, limit int) (File, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := File{Path: path, StartLine: start, Lines: []string{}}
	if start < 1 || start > 1_000_000 || limit < 1 || limit > 200 {
		return result, CodeError("workspace_read_range_invalid")
	}
	if err := r.check(ctx); err != nil {
		return result, err
	}
	raw, err := r.readFile(path)
	if err != nil {
		return result, err
	}
	lines := strings.Split(string(raw), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	size := 0
	for index := start - 1; index < len(lines); index++ {
		line := strings.TrimSuffix(lines[index], "\r")
		// A sixfold margin bounds JSON escaping of control characters too.
		if len(result.Lines) >= limit || size+len(line)*6+4 > maxOutputBytes {
			if len(result.Lines) == 0 {
				return result, CodeError("workspace_line_too_large")
			}
			result.Truncated = true
			result.NextLine = index + 1
			break
		}
		result.Lines = append(result.Lines, line)
		size += len(line)*6 + 4
	}
	return result, r.check(ctx)
}
