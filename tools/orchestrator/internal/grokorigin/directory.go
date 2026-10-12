//go:build !windows

package grokorigin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

var errPrivateDirectory = errors.New("grokorigin_private_directory")

type privateLayout struct {
	state, root, home, grok, tmp string
	parent, leaf                 *os.File
	original                     os.FileInfo
	removed                      bool
	deleting                     bool
	anchor                       *os.Root
	children                     map[string]privateChild
}

type privateChild struct {
	file *os.File
	info os.FileInfo
}

func privateDirectory(info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode() != os.ModeDir|0700 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
func openPrivateDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errPrivateDirectory
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil || !privateDirectory(info) {
		f.Close()
		return nil, errPrivateDirectory
	}
	return f, nil
}
func newPrivateLayout(state string) (layout *privateLayout, err error) {
	if !validPath(state) {
		return nil, errPrivateDirectory
	}
	canonical, e := filepath.EvalSymlinks(state)
	before, e2 := os.Lstat(state)
	if e != nil || canonical != state || e2 != nil || !privateDirectory(before) {
		return nil, errPrivateDirectory
	}
	root, err := os.OpenFile(state, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errPrivateDirectory
	}
	defer root.Close()
	held, e := root.Stat()
	if e != nil || !os.SameFile(before, held) || !privateDirectory(held) {
		return nil, errPrivateDirectory
	}
	if e = unix.Mkdirat(int(root.Fd()), "grok-origin", 0700); e != nil && !errors.Is(e, unix.EEXIST) {
		return nil, errPrivateDirectory
	}
	parent, err := openPrivateDirectory(root, "grok-origin")
	if err != nil {
		return nil, err
	}
	layout = &privateLayout{state: state, parent: parent, children: make(map[string]privateChild)}
	defer func() {
		if err != nil {
			if layout.leaf != nil {
				_ = layout.remove()
			}
			layout.closeFiles()
			layout = nil
		}
	}()
	var random [16]byte
	if _, e = rand.Read(random[:]); e != nil {
		return layout, errPrivateDirectory
	}
	name := "origin-" + hex.EncodeToString(random[:])
	layout.root = filepath.Join(state, "grok-origin", name)
	if e = unix.Mkdirat(int(parent.Fd()), name, 0700); e != nil {
		return layout, errPrivateDirectory
	}
	layout.leaf, err = openPrivateDirectory(parent, name)
	if err != nil {
		return layout, err
	}
	layout.original, err = layout.leaf.Stat()
	if err != nil {
		return layout, errPrivateDirectory
	}
	layout.anchor, err = os.OpenRoot(layout.root)
	if err != nil {
		return layout, errPrivateDirectory
	}
	anchorInfo, e := layout.anchor.Stat(".")
	if e != nil || !os.SameFile(layout.original, anchorInfo) {
		return layout, errPrivateDirectory
	}
	for _, child := range []string{"home", "grok", "tmp"} {
		if e = unix.Mkdirat(int(layout.leaf.Fd()), child, 0700); e != nil {
			return layout, errPrivateDirectory
		}
		f, e := openPrivateDirectory(layout.leaf, child)
		if e != nil {
			return layout, errPrivateDirectory
		}
		info, e := f.Stat()
		if e != nil {
			f.Close()
			return layout, errPrivateDirectory
		}
		layout.children[child] = privateChild{file: f, info: info}
	}
	layout.home = filepath.Join(layout.root, "home")
	layout.grok = filepath.Join(layout.root, "grok")
	layout.tmp = filepath.Join(layout.root, "tmp")
	if !layout.same() {
		return layout, errPrivateDirectory
	}
	if e = layout.leaf.Sync(); e != nil {
		return layout, errPrivateDirectory
	}
	if e = parent.Sync(); e != nil {
		return layout, errPrivateDirectory
	}
	return layout, nil
}
func (l *privateLayout) same() bool {
	if !l.sameRoot() {
		return false
	}
	for name, child := range l.children {
		path := filepath.Join(l.root, name)
		held, e1 := child.file.Stat()
		named, e2 := os.Lstat(path)
		canonical, e3 := filepath.EvalSymlinks(path)
		if e1 != nil || e2 != nil || e3 != nil || canonical != path || !privateDirectory(held) || !privateDirectory(named) || !os.SameFile(child.info, held) || !os.SameFile(held, named) {
			return false
		}
	}
	return true
}

func (l *privateLayout) sameRoot() bool {
	if l == nil || l.leaf == nil || l.original == nil {
		return false
	}
	held, e1 := l.leaf.Stat()
	named, e2 := os.Lstat(l.root)
	canonical, e3 := filepath.EvalSymlinks(l.root)
	if e1 != nil || e2 != nil || e3 != nil || canonical != l.root || !privateDirectory(held) || !privateDirectory(named) || !os.SameFile(l.original, held) || !os.SameFile(held, named) {
		return false
	}
	return true
}

// Only the launcher calls remove, after all original process trees are proven
// exited (or before any spawn). A changed directory identity is never deleted.
func (l *privateLayout) remove() error {
	return l.removeObserved(nil)
}

func (l *privateLayout) removeObserved(afterCheck func()) error {
	return l.removeContext(context.Background(), afterCheck, nil)
}

func (l *privateLayout) removeContext(ctx context.Context, afterCheck, afterRemove func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.removed {
		return nil
	}
	if !l.sameRoot() || (!l.deleting && !l.same()) {
		return errPrivateDirectory
	}
	if afterCheck != nil {
		afterCheck()
	}
	if l.anchor == nil || !l.sameRoot() || (!l.deleting && !l.same()) {
		return errPrivateDirectory
	}
	// All recursive traversal is anchored to the originally opened leaf. A
	// concurrent rename/replacement of its public path cannot redirect deletion
	// into the replacement directory. The final unlink can remove only an empty
	// directory; unknown/replaced names are retained, never recursively retried.
	l.deleting = true
	if err := removeOriginContents(ctx, l.anchor, 0, afterRemove); err != nil {
		return err
	}
	// Child directories are now intentionally absent, so recheck the root alone.
	named, err := os.Lstat(l.root)
	if err != nil || !os.SameFile(l.original, named) || !privateDirectory(named) {
		return errPrivateDirectory
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(l.parent.Fd()), filepath.Base(l.root), unix.AT_REMOVEDIR); err != nil {
		return errPrivateDirectory
	}
	l.removed = true
	return ctx.Err()
}

// Read and delete in small batches, retaining the original Root handles across
// recursion. Cancellation stops before the next filesystem operation; no
// background deletion survives the caller. Individual OS calls are synchronous.
func removeOriginContents(ctx context.Context, root *os.Root, depth int, afterRemove func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 256 {
		return errPrivateDirectory
	}
	dir, err := root.Open(".")
	if err != nil {
		return errPrivateDirectory
	}
	defer dir.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(32)
		if readErr != nil && readErr != io.EOF {
			return errPrivateDirectory
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := root.Lstat(entry.Name())
			if err != nil {
				return errPrivateDirectory
			}
			if info.IsDir() {
				child, err := root.OpenRoot(entry.Name())
				if err != nil {
					return errPrivateDirectory
				}
				held, e := child.Stat(".")
				if e != nil || !os.SameFile(info, held) {
					child.Close()
					return errPrivateDirectory
				}
				err = removeOriginContents(ctx, child, depth+1, afterRemove)
				_ = child.Close()
				if err != nil {
					return err
				}
				named, e := root.Lstat(entry.Name())
				if e != nil || !os.SameFile(info, named) {
					return errPrivateDirectory
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err = root.Remove(entry.Name()); err != nil {
				return errPrivateDirectory
			}
			if afterRemove != nil {
				afterRemove()
			}
		}
		if readErr == io.EOF {
			return ctx.Err()
		}
	}
}
func (l *privateLayout) closeFiles() {
	if l == nil {
		return
	}
	for name, child := range l.children {
		_ = child.file.Close()
		delete(l.children, name)
	}
	if l.anchor != nil {
		_ = l.anchor.Close()
		l.anchor = nil
	}
	if l.leaf != nil {
		_ = l.leaf.Close()
		l.leaf = nil
	}
	if l.parent != nil {
		_ = l.parent.Close()
		l.parent = nil
	}
}
