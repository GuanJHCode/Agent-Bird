package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"codex-cli-orchestration-design/tools/orchestrator/internal/gitops"
	"golang.org/x/sys/unix"
)

func managedPolicyError() error { return errors.New("managed_write_policy_untrusted") }

// Preserve the original spelling for exact write grants. The comparison form
// uses the target macOS filesystem's decomposition, never a rewritten path.
func managedComparisonNames(ctx context.Context, names []string) ([]string, error) {
	text := strings.Join(names, "\x00")
	if len(text) > 256*1024 || !utf8.ValidString(text) {
		return nil, managedPolicyError()
	}
	if strings.IndexFunc(text, func(r rune) bool { return r > unicode.MaxASCII }) >= 0 {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/usr/bin/iconv", "-f", "UTF-8", "-t", "UTF-8-MAC")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		cmd.Stdin = strings.NewReader(text)
		var out limitedManagedOutput
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		if cmd.Run() != nil {
			return nil, managedPolicyError()
		}
		text = out.String()
	}
	return strings.Split(text, "\x00"), nil
}

type limitedManagedOutput struct{ bytes.Buffer }

func (b *limitedManagedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1024*1024 {
		return 0, managedPolicyError()
	}
	return b.Buffer.Write(p)
}

func managedFold(s string) string {
	return strings.Map(func(r rune) rune {
		low := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < low {
				low = next
			}
		}
		return low
	}, s)
}

// Check the entire declaration before any workspace or ancestor is created.
func validateManagedDeclarations(ctx context.Context, paths []string) error {
	if len(paths) == 0 || len(paths) > 256 {
		return managedPolicyError()
	}
	for _, path := range paths {
		if path == "" || len(path) > 512 || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." || strings.HasPrefix(path, "../") || strings.ContainsAny(path, "\x00\r\n") {
			return managedPolicyError()
		}
		for _, part := range strings.Split(path, "/") {
			for _, forbidden := range []string{".git", ".codex", ".claude", ".claude.json", ".mcp.json"} {
				if strings.EqualFold(part, forbidden) {
					return managedPolicyError()
				}
			}
		}
	}
	normalized, err := managedComparisonNames(ctx, paths)
	if err != nil {
		return err
	}
	type declaration struct {
		spelling string
		file     bool
	}
	nodes := map[string]declaration{}
	for i, path := range normalized {
		parts, raw := strings.Split(path, "/"), strings.Split(paths[i], "/")
		for j := range parts {
			key := managedFold(strings.Join(parts[:j+1], "/"))
			spelling := strings.Join(raw[:j+1], "/")
			file := j == len(parts)-1
			if old, ok := nodes[key]; ok && (old.spelling != spelling || old.file || file) {
				return managedPolicyError()
			}
			nodes[key] = declaration{spelling, file}
		}
	}
	return nil
}

func openManagedDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// Reject aliases on disk too, even on a case-insensitive/normalizing volume.
func checkManagedDiskSpelling(ctx context.Context, parent *os.File, name string) error {
	listing, err := openManagedDirectory(parent, ".")
	if err != nil {
		return err
	}
	defer listing.Close()
	entries, err := listing.ReadDir(8193)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) > 8192 {
		return managedPolicyError()
	}
	names := []string{name}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	normalized, err := managedComparisonNames(ctx, names)
	if err != nil {
		return err
	}
	for i, entry := range entries {
		if managedFold(normalized[i+1]) == managedFold(normalized[0]) && entry.Name() != name {
			return managedPolicyError()
		}
	}
	return nil
}

// Validate every existing component before the first mkdir, using the same
// no-follow directory walk as preparation. Missing components are not created.
func validateManagedTopology(ctx context.Context, root *os.File, paths []string) error {
	for _, path := range paths {
		current := root
		opened := []*os.File{}
		err := func() error {
			parts := strings.Split(path, "/")
			for i, part := range parts {
				if err := checkManagedDiskSpelling(ctx, current, part); err != nil {
					return err
				}
				var st unix.Stat_t
				err := unix.Fstatat(int(current.Fd()), part, &st, unix.AT_SYMLINK_NOFOLLOW)
				if errors.Is(err, unix.ENOENT) {
					return nil
				}
				if err != nil {
					return err
				}
				if i == len(parts)-1 {
					if st.Mode&unix.S_IFMT != unix.S_IFREG {
						return managedPolicyError()
					}
					return nil
				}
				next, err := openManagedDirectory(current, part)
				if err != nil {
					return err
				}
				opened = append(opened, next)
				current = next
			}
			return nil
		}()
		for _, file := range opened {
			file.Close()
		}
		if err != nil {
			return managedPolicyError()
		}
	}
	return nil
}

type managedDirectoryGuard struct {
	cwd         string
	root        *os.File
	rootInfo    os.FileInfo
	identities  map[string]os.FileInfo
	directories map[string]*os.File
}

func (g *managedDirectoryGuard) Close() {
	for _, f := range g.directories {
		f.Close()
	}
	g.root.Close()
}
func sameManagedDirectory(a, b os.FileInfo) bool {
	if a == nil || b == nil || !os.SameFile(a, b) || a.Mode() != b.Mode() {
		return false
	}
	before, ok := a.Sys().(*syscall.Stat_t)
	after, other := b.Sys().(*syscall.Stat_t)
	return ok && other && before.Uid == after.Uid
}

func (g *managedDirectoryGuard) Validate() error {
	if resolved, err := filepath.EvalSymlinks(g.cwd); err != nil || resolved != g.cwd {
		return managedPolicyError()
	}
	root, err := os.OpenFile(g.cwd, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return managedPolicyError()
	}
	defer root.Close()
	before := g.rootInfo
	after, e := root.Stat()
	if e != nil || !sameManagedDirectory(before, after) {
		return managedPolicyError()
	}
	for path := range g.directories {
		parent := g.root
		if dir := filepath.Dir(path); dir != "." {
			parent = g.directories[dir]
		}
		current, err := openManagedDirectory(parent, filepath.Base(path))
		if err != nil {
			return managedPolicyError()
		}
		a := g.identities[path]
		b, e2 := current.Stat()
		current.Close()
		if e2 != nil || !sameManagedDirectory(a, b) {
			return managedPolicyError()
		}
	}
	return nil
}

// A durable workspace receipt and directory intent precede mutation. On error
// all evidence is retained; no recursive deletion or inferred ownership occurs.
func prepareManagedDirectories(ctx context.Context, cwd string, paths []string, record func(string, any) error, expected ...gitops.DirectoryIdentity) (*managedDirectoryGuard, error) {
	if err := validateManagedDeclarations(ctx, paths); err != nil {
		return nil, err
	}
	fd, err := unix.Open(cwd, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, managedPolicyError()
	}
	g := &managedDirectoryGuard{cwd: cwd, root: os.NewFile(uintptr(fd), cwd), directories: map[string]*os.File{}, identities: map[string]os.FileInfo{}}
	fail := func(err error) (*managedDirectoryGuard, error) { g.Close(); return nil, err }
	if err := validateManagedTopology(ctx, g.root, paths); err != nil {
		return fail(err)
	}
	rootInfo, err := g.root.Stat()
	if err != nil {
		return fail(err)
	}
	g.rootInfo = rootInfo
	if len(expected) > 0 {
		st, ok := rootInfo.Sys().(*syscall.Stat_t)
		want := expected[0]
		if !ok || int64(st.Dev) != want.Device || int64(st.Ino) != want.Inode || int64(rootInfo.Mode()) != want.Mode || int64(st.Uid) != want.UID {
			return fail(managedPolicyError())
		}
	}
	if err := record("directory_prepare_intent", map[string]any{"worktree": cwd, "root_identity": rootInfo.Sys(), "paths": paths}); err != nil {
		return fail(err)
	}
	for _, path := range paths {
		parent := g.root
		parts := strings.Split(path, "/")
		for i, part := range parts[:len(parts)-1] {
			relative := strings.Join(parts[:i+1], "/")
			if existing, ok := g.directories[relative]; ok {
				parent = existing
				continue
			}
			if err := g.Validate(); err != nil {
				return fail(err)
			}
			if err := checkManagedDiskSpelling(ctx, parent, part); err != nil {
				return fail(err)
			}
			child, err := openManagedDirectory(parent, part)
			created := false
			if errors.Is(err, unix.ENOENT) {
				if err = unix.Mkdirat(int(parent.Fd()), part, 0700); err != nil {
					return fail(managedPolicyError())
				}
				created = true
				child, err = openManagedDirectory(parent, part)
			}
			if err != nil {
				return fail(managedPolicyError())
			}
			g.directories[relative] = child
			parent = child
			info, err := child.Stat()
			if err != nil {
				return fail(err)
			}
			g.identities[relative] = info
			if created {
				if err = record("directory_created", map[string]any{"worktree": cwd, "path": relative, "identity": info.Sys()}); err != nil {
					return fail(err)
				}
			}
		}
	}
	if err := g.Validate(); err != nil {
		return fail(err)
	}
	if err := validateManagedTopology(ctx, g.root, paths); err != nil {
		return fail(err)
	}
	return g, nil
}
