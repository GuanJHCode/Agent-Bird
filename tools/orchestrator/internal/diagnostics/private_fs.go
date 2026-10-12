//go:build darwin || linux

package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var (
	ErrUnsafeLayout = errors.New("diagnostics_unsafe_layout")
	ErrLockBusy     = errors.New("diagnostics_lock_busy")
)

type namespace struct {
	rootPath, name                    string
	root, dir, writer, update         *os.File
	rootID, dirID, writerID, updateID unix.Stat_t
	afterReadOpen                     func(string)
	step                              func(string) error
}

func unsafe(err error) error { return errors.Join(ErrUnsafeLayout, err) }

func openAt(dir *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// Walk from /, rejecting symlinks in ancestors as well as the final root.
// Only the caller-owned final directory must be private.
func openPrivateRoot(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, ErrUnsafeLayout
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "/")
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, err := openAt(f, part, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		_ = f.Close()
		if err != nil {
			return nil, unsafe(err)
		}
		f = next
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil || !privateStat(st, true) {
		_ = f.Close()
		return nil, unsafe(err)
	}
	return f, nil
}

func privateStat(st unix.Stat_t, dir bool) bool {
	if st.Uid != uint32(os.Geteuid()) {
		return false
	}
	if dir {
		return uint32(st.Mode)&unix.S_IFMT == unix.S_IFDIR && uint32(st.Mode)&07777 == 0700
	}
	return uint32(st.Mode)&unix.S_IFMT == unix.S_IFREG && uint32(st.Mode)&07777 == 0600 && st.Nlink == 1 && st.Size >= 0
}

func sameIdentity(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Uid == b.Uid && a.Mode == b.Mode
}

func sameFileState(a, b unix.Stat_t) bool {
	return sameIdentity(a, b) && a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func openNamespace(ctx context.Context, rootPath, name string, writer bool) (n *namespace, fresh bool, err error) {
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	if !validPolicyHash(name) {
		return nil, false, ErrInvalidScope
	}
	root, err := openPrivateRoot(rootPath)
	if err != nil {
		return nil, false, err
	}
	n = &namespace{rootPath: rootPath, name: name, root: root}
	defer func() {
		if err != nil {
			_ = n.close()
			n = nil
		}
	}()
	if err = unix.Fstat(int(root.Fd()), &n.rootID); err != nil {
		return n, false, err
	}
	if writer {
		err = unix.Mkdirat(int(root.Fd()), name, 0700)
		if err == nil {
			fresh = true
		} else if !errors.Is(err, unix.EEXIST) {
			return n, false, err
		}
	}
	n.dir, err = openAt(root, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return n, fresh, unsafe(err)
	}
	if err = unix.Fstat(int(n.dir.Fd()), &n.dirID); err != nil || !privateStat(n.dirID, true) {
		return n, fresh, unsafe(err)
	}
	flags := unix.O_RDWR
	if fresh {
		flags |= unix.O_CREAT | unix.O_EXCL
	}
	for _, item := range []struct {
		name string
		file **os.File
		id   *unix.Stat_t
	}{
		{"writer.lock", &n.writer, &n.writerID}, {"update.lock", &n.update, &n.updateID},
	} {
		*item.file, err = openAt(n.dir, item.name, flags, 0600)
		if err != nil {
			return n, fresh, unsafe(err)
		}
		if err = unix.Fstat(int((*item.file).Fd()), item.id); err != nil || !privateStat(*item.id, false) || item.id.Size != 0 {
			return n, fresh, unsafe(err)
		}
	}
	if err = n.check(); err != nil {
		return n, fresh, err
	}
	if writer {
		if err = unix.Flock(int(n.writer.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			if errors.Is(err, unix.EWOULDBLOCK) {
				err = errors.Join(ErrLockBusy, err)
			}
			return n, fresh, err
		}
	}
	if fresh {
		if err = n.dir.Sync(); err != nil {
			return n, fresh, err
		}
		if err = n.root.Sync(); err != nil {
			return n, fresh, err
		}
	}
	// Reader layout inspection belongs under its shared update lock; otherwise
	// a legitimate writer rename could race an unlocked directory enumeration.
	if err = n.check(); err != nil {
		return n, fresh, err
	}
	if err = ctx.Err(); err != nil {
		return n, fresh, err
	}
	return n, fresh, nil
}

func (n *namespace) close() error {
	var errs []error
	for _, f := range []*os.File{n.update, n.writer, n.dir, n.root} {
		if f != nil {
			errs = append(errs, f.Close())
		}
	}
	n.update, n.writer, n.dir, n.root = nil, nil, nil, nil
	return errors.Join(errs...)
}

func (n *namespace) check() error {
	if n.root == nil || n.dir == nil || n.writer == nil || n.update == nil {
		return ErrUnsafeLayout
	}
	root, err := openPrivateRoot(n.rootPath)
	if err != nil {
		return unsafe(err)
	}
	defer root.Close()
	var current unix.Stat_t
	if err = unix.Fstat(int(root.Fd()), &current); err != nil || !sameIdentity(current, n.rootID) {
		return unsafe(err)
	}
	for _, item := range []struct {
		file   *os.File
		parent *os.File
		name   string
		id     unix.Stat_t
		dir    bool
	}{
		{n.dir, n.root, n.name, n.dirID, true},
		{n.writer, n.dir, "writer.lock", n.writerID, false},
		{n.update, n.dir, "update.lock", n.updateID, false},
	} {
		var held, named unix.Stat_t
		if err := unix.Fstat(int(item.file.Fd()), &held); err != nil {
			return unsafe(err)
		}
		if err := unix.Fstatat(int(item.parent.Fd()), item.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return unsafe(err)
		}
		if !privateStat(held, item.dir) || !sameIdentity(held, item.id) || !sameIdentity(held, named) || (!item.dir && (held.Size != 0 || named.Size != 0)) {
			return ErrUnsafeLayout
		}
	}
	return nil
}

func fileLimit(name string) (int, bool) {
	switch name {
	case "writer.lock", "update.lock":
		return 0, true
	case "manifest.json", ".manifest.stage":
		return ManifestBytes, true
	case "chunk.stage":
		return ChunkBytes, true
	}
	if len(name) == 13 && strings.HasPrefix(name, "chunk-") && strings.HasSuffix(name, ".bin") {
		index := 0
		for _, c := range name[6:9] {
			if c < '0' || c > '9' {
				return 0, false
			}
			index = index*10 + int(c-'0')
		}
		if index < SlotCount {
			return ChunkBytes, true
		}
	}
	return 0, false
}

func slotName(i int) string { return fmt.Sprintf("chunk-%03d.bin", i) }

func (n *namespace) checkLayout() error {
	if err := n.check(); err != nil {
		return err
	}
	// A separate open file description avoids sharing directory seek offsets.
	d, err := openAt(n.dir, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(SlotCount + 6)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > SlotCount+5 {
		return ErrUnsafeLayout
	}
	for _, name := range names {
		limit, ok := fileLimit(name)
		if !ok {
			return ErrUnsafeLayout
		}
		var st unix.Stat_t
		if err := unix.Fstatat(int(n.dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return unsafe(err)
		}
		if !privateStat(st, false) || st.Size > int64(limit) {
			return ErrUnsafeLayout
		}
	}
	return n.check()
}

func (n *namespace) verifyFile(f *os.File, name string, before unix.Stat_t, exact bool) error {
	var held, named unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &held); err != nil {
		return unsafe(err)
	}
	if err := unix.Fstatat(int(n.dir.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unsafe(err)
	}
	limit, ok := fileLimit(name)
	if !ok || !privateStat(held, false) || held.Size > int64(limit) || !sameFileState(held, named) || !sameIdentity(held, before) || (exact && !sameFileState(held, before)) {
		return ErrUnsafeLayout
	}
	return n.check()
}

func (n *namespace) readFile(name string, maxBytes int) ([]byte, error) {
	return n.readInto(name, maxBytes, nil)
}

func (n *namespace) readInto(name string, maxBytes int, buffer []byte) ([]byte, error) {
	limit, ok := fileLimit(name)
	if !ok || maxBytes < 0 || maxBytes > limit {
		return nil, ErrUnsafeLayout
	}
	if err := n.check(); err != nil {
		return nil, err
	}
	var before unix.Stat_t
	if err := unix.Fstatat(int(n.dir.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if !privateStat(before, false) || before.Size > int64(maxBytes) {
		return nil, ErrUnsafeLayout
	}
	f, err := openAt(n.dir, name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, unsafe(err)
	}
	defer f.Close()
	if err = n.verifyFile(f, name, before, true); err != nil {
		return nil, err
	}
	if n.afterReadOpen != nil {
		n.afterReadOpen(name)
	}
	if cap(buffer) < int(before.Size) {
		buffer = make([]byte, int(before.Size))
	} else {
		buffer = buffer[:int(before.Size)]
	}
	if _, err = io.ReadFull(f, buffer); err != nil {
		return nil, err
	}
	if err = n.verifyFile(f, name, before, true); err != nil {
		return nil, err
	}
	return buffer, nil
}

// The caller serializes operations on this namespace instance. Independent
// readers use separate descriptions; sharing one flock FD is not a mutex.
func (n *namespace) lockUpdate(ctx context.Context, exclusive bool) (func() error, error) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := n.check(); err != nil {
			return nil, err
		}
		err := unix.Flock(int(n.update.Fd()), mode|unix.LOCK_NB)
		if err == nil {
			unlock := func() error { return unix.Flock(int(n.update.Fd()), unix.LOCK_UN) }
			if err = errors.Join(ctx.Err(), n.check()); err != nil {
				_ = unlock()
				return nil, err
			}
			return unlock, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, err
		}
		timer := time.NewTimer(2 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (n *namespace) checkpoint(stage, action string) error {
	if n.step != nil {
		return n.step(stage + ":" + action)
	}
	return nil
}

// Missing is returned unchanged; callers decide whether absence is legal.
func (n *namespace) openChecked(name string, flags int) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	limit, ok := fileLimit(name)
	if !ok {
		return nil, st, ErrUnsafeLayout
	}
	if err := n.check(); err != nil {
		return nil, st, err
	}
	if err := unix.Fstatat(int(n.dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, st, err
	}
	if !privateStat(st, false) || st.Size > int64(limit) {
		return nil, st, ErrUnsafeLayout
	}
	f, err := openAt(n.dir, name, flags, 0)
	if err != nil {
		return nil, st, unsafe(err)
	}
	if err = n.verifyFile(f, name, st, true); err != nil {
		_ = f.Close()
		return nil, st, err
	}
	return f, st, nil
}

// publish is called only under the exclusive update lock. A pre-existing safe
// partial stage is scratch, never evidence for the authoritative target.
func (n *namespace) publish(stage, target string, body []byte) error {
	limit, ok := fileLimit(target)
	if !ok || len(body) > limit || !((stage == ".manifest.stage" && target == "manifest.json") || (stage == "chunk.stage" && strings.HasPrefix(target, "chunk-") && strings.HasSuffix(target, ".bin"))) {
		return ErrUnsafeLayout
	}
	old, oldStat, err := n.openChecked(target, unix.O_RDONLY)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if old != nil {
		defer old.Close()
	}
	f, before, err := n.openChecked(stage, unix.O_RDWR)
	if errors.Is(err, unix.ENOENT) {
		f, err = openAt(n.dir, stage, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if err = unix.Fstat(int(f.Fd()), &before); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if err = n.verifyFile(f, stage, before, true); err != nil {
		return err
	}
	if err = n.checkpoint(stage, "before-truncate"); err != nil {
		return err
	}
	if err = n.verifyFile(f, stage, before, true); err != nil {
		return err
	}
	if err = f.Truncate(0); err != nil {
		return err
	}
	if err = n.checkpoint(stage, "after-truncate"); err != nil {
		return err
	}
	written, err := f.WriteAt(body, 0)
	if err != nil {
		return err
	}
	if written != len(body) {
		return io.ErrShortWrite
	}
	if err = n.checkpoint(stage, "after-write"); err != nil {
		return err
	}
	if err = n.verifyFile(f, stage, before, false); err != nil {
		return err
	}
	if err = n.checkpoint(stage, "before-file-sync"); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = n.checkpoint(stage, "after-file-sync"); err != nil {
		return err
	}
	if err = n.checkpoint(stage, "before-rename"); err != nil {
		return err
	}
	if err = n.verifyFile(f, stage, before, false); err != nil {
		return err
	}
	if old != nil {
		if err = n.verifyFile(old, target, oldStat, true); err != nil {
			return err
		}
	} else {
		var current unix.Stat_t
		if err = unix.Fstatat(int(n.dir.Fd()), target, &current, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
			return unsafe(err)
		}
	}
	if err = n.checkpoint(stage, "before-rename-syscall"); err != nil {
		return err
	}
	if old == nil {
		err = renameAbsent(int(n.dir.Fd()), stage, target)
	} else {
		err = unix.Renameat(int(n.dir.Fd()), stage, int(n.dir.Fd()), target)
	}
	if err != nil {
		return err
	}
	if err = n.checkpoint(stage, "after-rename"); err != nil {
		return err
	}
	if err = n.verifyFile(f, target, before, false); err != nil {
		return err
	}
	if err = n.checkpoint(stage, "before-dir-sync"); err != nil {
		return err
	}
	if err = n.dir.Sync(); err != nil {
		return err
	}
	if err = n.checkpoint(stage, "after-dir-sync"); err != nil {
		return err
	}
	return n.verifyFile(f, target, before, false)
}

// Only recovery after a durable incomplete manifest may discard known scratch.
func (n *namespace) removeStage(name string) error {
	if name != "chunk.stage" && name != ".manifest.stage" {
		return ErrUnsafeLayout
	}
	f, st, err := n.openChecked(name, unix.O_RDONLY)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if err = n.checkpoint(name, "before-unlink"); err != nil {
		return err
	}
	if err = n.verifyFile(f, name, st, true); err != nil {
		return err
	}
	if err = unix.Unlinkat(int(n.dir.Fd()), name, 0); err != nil {
		return err
	}
	if err = n.checkpoint(name, "after-unlink"); err != nil {
		return err
	}
	if err = n.dir.Sync(); err != nil {
		return err
	}
	return n.check()
}
