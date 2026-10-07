package instance

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type CodeError string

func (e CodeError) Error() string { return string(e) }

const ErrBusy CodeError = "coordinator_busy"

type Lock struct {
	file   *os.File
	root   string
	epoch  uint64
	closed bool
}

// prepareStateDirBeforeCreate is test-only synchronization after the existing
// ancestor is held and before any missing path component is created.
var prepareStateDirBeforeCreate = func() {}

// PrepareStateDir creates a missing private directory, but never repairs an
// existing directory by changing its permissions. Existing ancestor aliases
// are allowed; every newly-created suffix is anchored to an already-open FD.
func PrepareStateDir(root string) (err error) {
	if !filepath.IsAbs(root) {
		return CodeError("path_not_absolute")
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err == nil {
		if !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
			return CodeError("untrusted_state_dir")
		}
		f, openErr := os.OpenFile(root, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		if openErr != nil {
			return CodeError("untrusted_state_dir")
		}
		defer func() {
			if closeErr := f.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
		}()
		opened, statErr := f.Stat()
		current, pathErr := os.Lstat(root)
		if statErr != nil || pathErr != nil || !os.SameFile(opened, current) {
			return CodeError("untrusted_state_dir")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return CodeError("untrusted_state_dir")
	}
	parts := strings.Split(strings.TrimPrefix(root, string(filepath.Separator)), string(filepath.Separator))
	ancestor, at := string(filepath.Separator), 0
	for i, part := range parts {
		next := filepath.Join(ancestor, part)
		if _, e := os.Stat(next); e == nil {
			ancestor, at = next, i+1
			continue
		}
		break
	}
	parent, err := os.Open(ancestor)
	if err != nil {
		return CodeError("untrusted_state_dir")
	}
	defer func() {
		if closeErr := parent.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	prepareStateDirBeforeCreate()
	for _, part := range parts[at:] {
		if part == "" || part == "." || part == ".." {
			return CodeError("untrusted_state_dir")
		}
		if mkErr := unix.Mkdirat(int(parent.Fd()), part, 0700); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
			return CodeError("untrusted_state_dir")
		}
		fd, openErr := unix.Openat(int(parent.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return CodeError("untrusted_state_dir")
		}
		next := os.NewFile(uintptr(fd), part)
		var named unix.Stat_t
		held, statErr := next.Stat()
		if statErr != nil || unix.Fstatat(int(parent.Fd()), part, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameDir(held, named) {
			next.Close()
			return CodeError("untrusted_state_dir")
		}
		if closeErr := parent.Close(); closeErr != nil {
			next.Close()
			return closeErr
		}
		parent = next
	}
	opened, statErr := parent.Stat()
	current, pathErr := os.Stat(root)
	if statErr != nil || pathErr != nil || !os.SameFile(opened, current) || opened.Mode().Perm() != 0700 || !owned(opened) {
		return CodeError("untrusted_state_dir")
	}
	return nil
}

func sameDir(info os.FileInfo, st unix.Stat_t) bool {
	got, ok := info.Sys().(*syscall.Stat_t)
	return ok && got.Dev == st.Dev && got.Ino == st.Ino && uint32(st.Mode)&unix.S_IFMT == unix.S_IFDIR
}

func Acquire(root string) (*Lock, error) {
	if !filepath.IsAbs(root) {
		return nil, CodeError("path_not_absolute")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return nil, CodeError("untrusted_state_dir")
	}
	path := filepath.Join(root, "coordinator.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode().Perm() != 0600 || !owned(fi) {
		f.Close()
		return nil, CodeError("untrusted_lock")
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	epoch, err := nextEpoch(root)
	if err != nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		return nil, err
	}
	return &Lock{file: f, root: root, epoch: epoch}, nil
}
func (l *Lock) Epoch() uint64 { return l.epoch }
func (l *Lock) Close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	cerr := l.file.Close()
	if err != nil {
		return err
	}
	return cerr
}
func owned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}
func nextEpoch(root string) (uint64, error) {
	path := filepath.Join(root, "coordinator.epoch")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Mode().Perm() != 0600 || !owned(info) {
		return 0, CodeError("untrusted_epoch")
	}
	var old uint64
	scanner := bufio.NewScanner(f)
	if scanner.Scan() {
		old, _ = strconv.ParseUint(strings.TrimSpace(scanner.Text()), 10, 64)
	}
	if old == ^uint64(0) {
		return 0, CodeError("epoch_overflow")
	}
	next := old + 1
	if err = f.Truncate(0); err != nil {
		return 0, err
	}
	if _, err = f.Seek(0, 0); err != nil {
		return 0, err
	}
	if _, err = fmt.Fprintf(f, "%d\n", next); err != nil {
		return 0, err
	}
	if err = f.Sync(); err != nil {
		return 0, err
	}
	return next, nil
}
