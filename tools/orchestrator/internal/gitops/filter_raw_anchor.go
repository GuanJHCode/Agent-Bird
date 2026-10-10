package gitops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type filterOwnedEntry struct {
	identity FileIdentity
	sha256   string
}
type filterRawWorkspace struct {
	parent, root                 *os.File
	parentPath, path             string
	parentIdentity, rootIdentity DirectoryIdentity
	anchor                       filterRepositoryAnchor
	backlink                     filterPinnedFile
	initialIndex                 *filterPinnedFile
	owned                        map[string]filterOwnedEntry
	total                        int64
}

func filterDirectoryInfo(st os.FileInfo) (DirectoryIdentity, error) {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || int(s.Uid) != os.Geteuid() {
		return DirectoryIdentity{}, fail(CodeIdentityChanged, "filter owned directory", nil)
	}
	return DirectoryIdentity{int64(s.Dev), int64(s.Ino), int64(st.Mode()), int64(s.Uid)}, nil
}
func filterOpenAt(parent *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func filterOpenParent(target string) (*filterRawWorkspace, error) {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target || CanonicalPath(target) != target || target == "/" {
		return nil, fail(CodeInvalidInput, "filter target path", nil)
	}
	parent := filepath.Dir(target)
	if err := noSymlinkComponents(parent); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(parent, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	w := &filterRawWorkspace{parent: f, parentPath: parent, path: target, owned: map[string]filterOwnedEntry{}}
	st, err := f.Stat()
	if err == nil {
		w.parentIdentity, err = filterDirectoryInfo(st)
	}
	if err == nil {
		err = w.verifyParent()
	}
	if err == nil {
		err = w.absent()
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}
func (w *filterRawWorkspace) Close() error {
	var a, b error
	if w.root != nil {
		a = w.root.Close()
		w.root = nil
	}
	if w.parent != nil {
		b = w.parent.Close()
		w.parent = nil
	}
	return errors.Join(a, b)
}
func (w *filterRawWorkspace) verifyParent() error {
	if w.parent == nil {
		return fail(CodeIdentityChanged, "filter parent closed", nil)
	}
	st, err := w.parent.Stat()
	if err != nil {
		return err
	}
	held, err := filterDirectoryInfo(st)
	named, e := directoryIdentity(w.parentPath)
	if err != nil || e != nil || held != w.parentIdentity || named != held || CanonicalPath(w.parentPath) != w.parentPath {
		return fail(CodeIdentityChanged, "filter parent", errors.Join(err, e))
	}
	return nil
}
func (w *filterRawWorkspace) absent() error {
	var st unix.Stat_t
	err := unix.Fstatat(int(w.parent.Fd()), filepath.Base(w.path), &st, unix.AT_SYMLINK_NOFOLLOW)
	if !errors.Is(err, unix.ENOENT) {
		return fail(CodeCollision, "filter target must be absent", err)
	}
	return nil
}
func (w *filterRawWorkspace) capture(source *FilterGitContext) error {
	if err := w.verifyParent(); err != nil {
		return err
	}
	f, err := filterOpenAt(w.parent, filepath.Base(w.path), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	w.root = f
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if _, err = filterDirectoryInfo(st); err != nil {
		return err
	}
	if err = f.Chmod(0700); err != nil {
		return err
	}
	st, err = f.Stat()
	if err != nil {
		return err
	}
	w.rootIdentity, err = filterDirectoryInfo(st)
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(2)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != ".git" {
		return fail(CodeUnknownObject, "filter initial workspace", nil)
	}
	link, raw, err := readFilterGitLink(filepath.Join(w.path, ".git"))
	if err != nil {
		return err
	}
	text, ok := strings.CutPrefix(raw, "gitdir: ")
	gitDir, err := filterGitLinkTarget(w.path, text)
	if !ok || err != nil || filepath.Dir(gitDir) != filepath.Join(source.commonDir, "worktrees") || gitDir == source.repository.gitDir {
		return fail(CodeIdentityChanged, "filter created git directory", err)
	}
	if err = noSymlinkComponents(gitDir); err != nil {
		return err
	}
	gid, err := directoryIdentity(gitDir)
	if err != nil {
		return err
	}
	common, raw, err := readFilterGitLink(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return err
	}
	target, err := filterGitLinkTarget(gitDir, raw)
	if err != nil || target != source.commonDir {
		return fail(CodeIdentityChanged, "filter created common", err)
	}
	backlink, raw, err := readFilterGitLink(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return err
	}
	target, err = filterGitLinkTarget(gitDir, raw)
	if err != nil || target != filepath.Join(w.path, ".git") {
		return fail(CodeIdentityChanged, "filter created backlink", err)
	}
	w.anchor = filterRepositoryAnchor{gitDir: gitDir, gitIdentity: gid, gitLink: &link, commonLink: &common}
	w.backlink = backlink
	if _, err = os.Lstat(filepath.Join(gitDir, "index")); err == nil {
		pin, e := pinFilterFileKind(filepath.Join(gitDir, "index"), "", 16<<20, false, nil)
		if e != nil {
			return e
		}
		w.initialIndex = &pin
	} else if !os.IsNotExist(err) {
		return err
	}
	w.owned[".git"] = filterOwnedEntry{link.identity, link.sha256}
	return w.verify()
}
func (w *filterRawWorkspace) verify() error {
	if err := w.verifyParent(); err != nil {
		return err
	}
	if w.root == nil {
		return fail(CodeIdentityChanged, "filter root closed", nil)
	}
	st, err := w.root.Stat()
	if err != nil {
		return err
	}
	held, err := filterDirectoryInfo(st)
	named, e := directoryIdentity(w.path)
	if err != nil || e != nil || held != w.rootIdentity || named != held {
		return fail(CodeIdentityChanged, "filter root", errors.Join(err, e))
	}
	if w.anchor.gitLink == nil || w.anchor.commonLink == nil || !verifyFilterPin(*w.anchor.gitLink) || !verifyFilterPin(*w.anchor.commonLink) || !verifyFilterPin(w.backlink) {
		return fail(CodeIdentityChanged, "filter created links", nil)
	}
	gid, err := directoryIdentity(w.anchor.gitDir)
	if err != nil || gid != w.anchor.gitIdentity {
		return fail(CodeIdentityChanged, "filter created git directory", err)
	}
	return nil
}
func (w *filterRawWorkspace) verifyInitialIndex() error {
	if w.initialIndex != nil {
		if verifyFilterPin(*w.initialIndex) {
			return nil
		}
		return fail(CodeIdentityChanged, "filter initial index", nil)
	}
	if _, err := os.Lstat(filepath.Join(w.anchor.gitDir, "index")); !os.IsNotExist(err) {
		return fail(CodeIdentityChanged, "filter unexpected index", err)
	}
	return nil
}
func (w *filterRawWorkspace) create(path string) (*os.File, error) {
	if err := w.verify(); err != nil {
		return nil, err
	}
	parts := strings.Split(path, "/")
	current := w.root
	opened := []*os.File{}
	defer func() {
		for _, f := range opened {
			f.Close()
		}
	}()
	for i, part := range parts[:len(parts)-1] {
		rel := strings.Join(parts[:i+1], "/")
		expected, exists := w.owned[rel]
		if !exists {
			if err := unix.Mkdirat(int(current.Fd()), part, 0700); err != nil {
				return nil, err
			}
		}
		f, err := filterOpenAt(current, part, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return nil, err
		}
		opened = append(opened, f)
		st, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if _, err = filterDirectoryInfo(st); err != nil {
			return nil, err
		}
		id, err := identityFromInfo(st)
		if err != nil {
			return nil, err
		}
		if exists && !sameRemovalIdentity(id, expected.identity) {
			return nil, fail(CodeIdentityChanged, "filter parent component", nil)
		}
		if !exists {
			w.owned[rel] = filterOwnedEntry{identity: id}
		}
		current = f
	}
	return filterOpenAt(current, parts[len(parts)-1], unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
}
func filterRegularIdentity(f *os.File) (FileIdentity, error) {
	st, err := f.Stat()
	if err != nil {
		return FileIdentity{}, err
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || int(s.Uid) != os.Geteuid() || s.Nlink != 1 {
		return FileIdentity{}, fail(CodeIdentityChanged, "filter owned file", nil)
	}
	return identityFromInfo(st)
}
func (w *filterRawWorkspace) write(ctx context.Context, e filterTreeEntry, produce func(io.Writer) (string, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := w.create(e.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = filterRegularIdentity(f); err != nil {
		return err
	}
	hash := sha256.New()
	limit := filterWorkspaceMaximumBytes - w.total
	if limit > 16<<20 {
		limit = 16 << 20
	}
	out := &filterBoundedWriter{dst: io.MultiWriter(f, hash), limit: limit}
	expected, err := produce(out)
	if err = errors.Join(err, out.err); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fail(CodeIdentityChanged, "filter rendered bytes", nil)
	}
	mode := os.FileMode(0600)
	if e.mode == "100755" {
		mode = 0700
	}
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	id, err := filterRegularIdentity(f)
	if err != nil {
		return err
	}
	if os.FileMode(id.Mode).Perm() != mode || id.Size != out.count {
		return fail(CodeIdentityChanged, "filter rendered mode/size", nil)
	}
	w.owned[e.path] = filterOwnedEntry{id, actual}
	w.total += out.count
	return w.verify()
}

// Enumerate from original directory descriptors. Every entry must be one this
// invocation created; a fresh pathname snapshot cannot confer ownership.
func (w *filterRawWorkspace) inventory(ctx context.Context) (map[string]FileIdentity, error) {
	if err := w.verify(); err != nil {
		return nil, err
	}
	out := map[string]FileIdentity{}
	pathBytes := 0
	var total int64
	var walk func(*os.File, string) error
	walk = func(parent *os.File, prefix string) error {
		if _, err := parent.Seek(0, io.SeekStart); err != nil {
			return err
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, err := parent.ReadDir(128)
			for _, entry := range entries {
				rel := entry.Name()
				if prefix != "" {
					rel = prefix + "/" + rel
				}
				pathBytes += len(rel)
				want, exists := w.owned[rel]
				if !exists || len(out) >= 4096 || pathBytes > 256<<10 {
					return fail(CodeUnknownObject, "filter inventory", nil)
				}
				flags := unix.O_RDONLY
				if os.FileMode(want.identity.Mode).IsDir() {
					flags |= unix.O_DIRECTORY
				}
				f, e := filterOpenAt(parent, entry.Name(), flags, 0)
				if e != nil {
					return e
				}
				e = func() error {
					defer f.Close()
					st, e := f.Stat()
					if e != nil {
						return e
					}
					id, e := identityFromInfo(st)
					if e != nil {
						return e
					}
					if !sameRemovalIdentity(id, want.identity) {
						return fail(CodeIdentityChanged, "filter inventory entry", nil)
					}
					if st.IsDir() {
						if _, e = filterDirectoryInfo(st); e != nil {
							return e
						}
						out[rel] = id
						return walk(f, rel)
					}
					if _, e = filterRegularIdentity(f); e != nil {
						return e
					}
					hash := sha256.New()
					n, e := io.Copy(hash, io.LimitReader(f, id.Size+1))
					if e != nil {
						return e
					}
					after, e := filterRegularIdentity(f)
					if e != nil || after != id || n != id.Size || hex.EncodeToString(hash.Sum(nil)) != want.sha256 {
						return fail(CodeIdentityChanged, "filter inventory content", e)
					}
					if rel != ".git" {
						total += n
						if total > filterWorkspaceMaximumBytes {
							return fail(CodeInvalidInput, "filter workspace bytes", nil)
						}
					}
					out[rel] = id
					return nil
				}()
				if e != nil {
					return e
				}
			}
			if err != nil {
				if err == io.EOF {
					break
				}
				return err
			}
		}
		return nil
	}
	if err := walk(w.root, ""); err != nil {
		return nil, err
	}
	if len(out) != len(w.owned) || total != w.total {
		return nil, fail(CodeIdentityChanged, "filter inventory incomplete", nil)
	}
	return out, w.verify()
}
