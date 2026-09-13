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

// PrepareStateDir creates a missing private directory, but never repairs an
// existing directory by changing its permissions.
func PrepareStateDir(root string) error {
	if !filepath.IsAbs(root) {
		return CodeError("path_not_absolute")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(root, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(root)
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return CodeError("untrusted_state_dir")
	}
	return nil
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
