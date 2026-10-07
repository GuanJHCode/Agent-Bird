//go:build darwin || linux

package artifacts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type chain struct {
	path        string
	rootPath    string
	privateRoot bool
	dirs        []*os.File
	ids         []unix.Stat_t
	names       []string
}

func openAt(dir *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func private(st unix.Stat_t, dir bool) bool {
	if st.Uid != uint32(os.Geteuid()) {
		return false
	}
	if dir {
		return st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Mode&07777 == 0700
	}
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Mode&07777 == 0600 && st.Nlink == 1 && st.Size >= 0
}
func identity(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Uid == b.Uid && a.Mode == b.Mode
}
func state(a, b unix.Stat_t) bool {
	return identity(a, b) && a.Size == b.Size && a.Nlink == b.Nlink && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
func directory(st unix.Stat_t, ownerOnly bool) bool {
	return st.Uid == uint32(os.Geteuid()) && st.Mode&unix.S_IFMT == unix.S_IFDIR &&
		st.Mode&07022 == 0 && (!ownerOnly || st.Mode&07777 == 0700)
}

func walk(path string, ownerOnly bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrUnsafe
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "/")
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, e := openAt(f, part, unix.O_RDONLY|unix.O_DIRECTORY)
		closeErr := f.Close()
		if e != nil {
			return nil, errors.Join(ErrUnsafe, e, closeErr)
		}
		if closeErr != nil {
			_ = next.Close()
			return nil, closeErr
		}
		f = next
	}
	var st unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &st); err != nil || !directory(st, ownerOnly) {
		_ = f.Close()
		return nil, errors.Join(ErrUnsafe, err)
	}
	return f, nil
}
func openChain(path string) (*chain, error) {
	return openAnchor(path, true)
}

func openAnchor(path string, ownerOnly bool) (*chain, error) {
	f, err := walk(path, ownerOnly)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &st); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &chain{path: path, rootPath: path, privateRoot: ownerOnly, dirs: []*os.File{f}, ids: []unix.Stat_t{st}}, nil
}
func (c *chain) last() *os.File { return c.dirs[len(c.dirs)-1] }
func (c *chain) close() error {
	var err error
	for i := len(c.dirs) - 1; i >= 0; i-- {
		err = errors.Join(err, c.dirs[i].Close())
	}
	c.dirs = nil
	return err
}
func (c *chain) check() error {
	root, err := walk(c.rootPath, c.privateRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	var named unix.Stat_t
	if err = unix.Fstat(int(root.Fd()), &named); err != nil || !identity(named, c.ids[0]) {
		return errors.Join(ErrUnsafe, err)
	}
	for i, f := range c.dirs {
		var held unix.Stat_t
		if err = unix.Fstat(int(f.Fd()), &held); err != nil || !directory(held, i > 0 || c.privateRoot) || !identity(held, c.ids[i]) {
			return errors.Join(ErrUnsafe, err)
		}
		if i > 0 {
			if err = unix.Fstatat(int(c.dirs[i-1].Fd()), c.names[i-1], &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || !identity(held, named) {
				return errors.Join(ErrUnsafe, err)
			}
		}
	}
	return nil
}
func (c *chain) child(name string, create bool) error {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return ErrUnsafe
	}
	if err := c.check(); err != nil {
		return err
	}
	parent := c.last()
	if create {
		if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
	}
	f, err := openAt(parent, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return errors.Join(ErrUnsafe, err)
	}
	var st, named unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &st); err == nil {
		err = unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW)
	}
	if err != nil || !private(st, true) || !identity(st, named) {
		_ = f.Close()
		return errors.Join(ErrUnsafe, err)
	}
	c.dirs = append(c.dirs, f)
	c.ids = append(c.ids, st)
	c.names = append(c.names, name)
	c.path = filepath.Join(c.path, name)
	if err = c.check(); err != nil {
		return err
	}
	if create {
		if err = f.Sync(); err != nil {
			return err
		}
		if err = parent.Sync(); err != nil {
			return err
		}
	}
	return nil
}
func (c *chain) stat(name string) (unix.Stat_t, error) {
	var st unix.Stat_t
	if err := c.check(); err != nil {
		return st, err
	}
	err := unix.Fstatat(int(c.last().Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	return st, err
}
func (c *chain) regular(name string, flags int, limit int64, create bool) (*os.File, unix.Stat_t, error) {
	var before unix.Stat_t
	if err := c.check(); err != nil {
		return nil, before, err
	}
	if create {
		flags |= unix.O_CREAT
	}
	f, err := openAt(c.last(), name, flags)
	if err != nil {
		return nil, before, errors.Join(ErrUnsafe, err)
	}
	if err = unix.Fstat(int(f.Fd()), &before); err == nil {
		err = c.verify(f, name, before, true, limit)
	}
	if err != nil {
		_ = f.Close()
		return nil, before, err
	}
	return f, before, nil
}
func (c *chain) verify(f *os.File, name string, before unix.Stat_t, exact bool, limit int64) error {
	var held, named unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &held); err != nil {
		return err
	}
	if err := unix.Fstatat(int(c.last().Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return errors.Join(ErrUnsafe, err)
	}
	if !private(held, false) || held.Size > limit || !state(held, named) || !identity(held, before) || (exact && !state(held, before)) {
		return ErrUnsafe
	}
	return c.check()
}

type object struct {
	*chain
	lockFile *os.File
	lockID   unix.Stat_t
	step     func(string) error
}

func openObject(root, id string) (n *object, err error) {
	return openObjectMode(root, id, true)
}
func openObjectMode(root, id string, create bool) (n *object, err error) {
	return openObjectModeRoot(root, id, create, false)
}

// Host spool roots are runtime-owned capability namespaces, unlike the report
// intake anchor whose parent may be readable. Keep that stricter policy local
// to Host slots so existing report behavior remains unchanged.
func openHostObjectMode(root, id string, create bool) (n *object, err error) {
	return openObjectModeRoot(root, id, create, true)
}
func openObjectModeRoot(root, id string, create, ownerOnlyRoot bool) (n *object, err error) {
	// The DB parent may be readable; all newly created artifact namespaces
	// remain owner-only. A parent writable by other users is never accepted.
	c, err := openAnchor(root, ownerOnlyRoot)
	if err != nil {
		return nil, err
	}
	n = &object{chain: c}
	opened := n
	defer func() {
		if err != nil {
			_ = opened.close()
			n = nil
		}
	}()
	if err = c.child("managed-artifacts", create); err != nil {
		return nil, err
	}
	if err = c.child(id, create); err != nil {
		return nil, err
	}
	n.lockFile, n.lockID, err = c.regular("writer.lock", unix.O_RDWR, 0, create)
	return n, err
}
func (n *object) close() error {
	var err error
	if n.lockFile != nil {
		err = n.lockFile.Close()
	}
	return errors.Join(err, n.chain.close())
}
func (n *object) lock(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := n.verify(n.lockFile, "writer.lock", n.lockID, true, 0); err != nil {
			return err
		}
		err := unix.Flock(int(n.lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return errors.Join(ctx.Err(), n.verify(n.lockFile, "writer.lock", n.lockID, true, 0))
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return err
		}
		timer := time.NewTimer(2 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (n *object) point(name string) error {
	if n.step != nil {
		if err := n.step(name); err != nil {
			return err
		}
	}
	return n.verify(n.lockFile, "writer.lock", n.lockID, true, 0)
}
func (n *object) sync() error {
	if err := n.point("namespace:before-sync"); err != nil {
		return err
	}
	if err := n.last().Sync(); err != nil {
		return err
	}
	return n.point("namespace:after-sync")
}
func (n *object) layout(size int64) error {
	f, err := openAt(n.last(), ".", unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer f.Close()
	names, err := f.Readdirnames(6)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 5 {
		return ErrUnsafe
	}
	for _, name := range names {
		limit := size
		switch name {
		case "writer.lock":
			limit = 0
		case "intent.json", "intent.stage":
			limit = manifestBytes
		case "data.bin", "data.stage":
		default:
			return ErrUnsafe
		}
		st, err := n.stat(name)
		if err != nil || !private(st, false) || st.Size > limit {
			return errors.Join(ErrUnsafe, err)
		}
	}
	return n.check()
}
func (n *object) read(name string, limit int64) (body []byte, err error) {
	f, before, err := n.regular(name, unix.O_RDONLY, limit, false)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	body = make([]byte, before.Size)
	if _, err = io.ReadFull(f, body); err != nil {
		return nil, err
	}
	if err = n.verify(f, name, before, true, limit); err != nil {
		return nil, err
	}
	return body, nil
}
func (n *object) ensureIntent(ctx context.Context, body []byte) error {
	got, err := n.read("intent.json", manifestBytes)
	if err == nil {
		if !bytes.Equal(got, body) {
			return ErrConflict
		}
		if _, e := n.stat("intent.stage"); !errors.Is(e, unix.ENOENT) {
			return ErrUnsafe
		}
		return nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return err
	}
	for _, name := range []string{"data.bin", "data.stage"} {
		if _, e := n.stat(name); !errors.Is(e, unix.ENOENT) {
			return ErrUnsafe
		}
	}
	f, before, err := n.regular("intent.stage", unix.O_RDWR, manifestBytes, true)
	if err != nil {
		return err
	}
	writeErr := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := n.point("intent.stage:before-truncate"); err != nil {
			return err
		}
		if err := n.verify(f, "intent.stage", before, false, manifestBytes); err != nil {
			return err
		}
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := f.Write(body); err != nil {
			return err
		}
		if err := n.point("intent.stage:after-write"); err != nil {
			return err
		}
		if err := n.verify(f, "intent.stage", before, false, manifestBytes); err != nil {
			return err
		}
		return f.Sync()
	}()
	if err = errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return n.publish("intent.stage", "intent.json")
}
func (n *object) publish(from, to string) error {
	limit := MaxBytes
	if from == "intent.stage" {
		limit = manifestBytes
	}
	f, before, err := n.regular(from, unix.O_RDONLY, limit, false)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = n.point(from + ":before-rename"); err != nil {
		return err
	}
	if err = n.verify(f, from, before, true, limit); err != nil {
		return err
	}
	if err = renameAbsent(int(n.last().Fd()), from, to); err != nil {
		return err
	}
	// Rename changes ctime on supported filesystems. Keep inode, byte length
	// and mtime fixed while comparing the current held/named file exactly.
	if err = n.verify(f, to, before, false, limit); err != nil {
		return err
	}
	var renamed unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &renamed); err != nil || renamed.Size != before.Size || renamed.Mtim != before.Mtim {
		return errors.Join(ErrUnsafe, err)
	}
	if err = n.point(from + ":after-rename"); err != nil {
		return err
	}
	if err = n.point(from + ":before-dir-sync"); err != nil {
		return err
	}
	if err = n.sync(); err != nil {
		return err
	}
	return n.point(from + ":after-dir-sync")
}
