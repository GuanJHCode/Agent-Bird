package host

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const recoveryLedgerName = ".coordinator-recovery.json"
const recoveryLockName = ".coordinator-recovery.lock"

func recoveryFileAt(directory *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func privateRecoveryInfo(info os.FileInfo, directory bool) bool {
	stat, ok := infoSyscall(info)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return false
	}
	if directory {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && stat.Nlink == 1
}

func lockHostRecovery(directory string) (*hostRecoveryState, error) {
	dir, err := os.OpenFile(directory, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("host_recovery_state_unsafe")
	}
	s := &hostRecoveryState{directory: directory, directoryFile: dir, path: filepath.Join(directory, recoveryLedgerName)}
	if err = s.verifyDirectoryPath(); err != nil {
		s.close()
		return nil, err
	}
	lock, err := recoveryFileAt(dir, recoveryLockName, unix.O_RDWR|unix.O_CREAT, 0600)
	if err != nil {
		s.close()
		return nil, errors.New("host_recovery_state_unsafe")
	}
	s.lock = lock
	if err = s.verifyAuthorityPath(); err != nil {
		s.close()
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		s.close()
		return nil, errors.New("host_recovery_state_busy")
	}
	if err = s.verifyAuthorityPath(); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *hostRecoveryState) close() {
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
		s.lock = nil
	}
	if s.directoryFile != nil {
		_ = s.directoryFile.Close()
		s.directoryFile = nil
	}
}

// Reads/writes stay relative to the original directory FD. Path checks before
// and after each operation reject redirection instead of following a new lock.
func (s *hostRecoveryState) verifyAuthorityPath() error {
	if s.directoryFile == nil || s.lock == nil {
		return errors.New("host_recovery_state_unsafe")
	}
	if err := s.verifyDirectoryPath(); err != nil {
		return err
	}
	lock, err := s.lock.Stat()
	if err != nil || !privateRecoveryInfo(lock, false) {
		return errors.New("host_recovery_state_unsafe")
	}
	current, err := recoveryFileAt(s.directoryFile, recoveryLockName, unix.O_RDONLY, 0)
	if err != nil {
		return errors.New("host_recovery_state_changed")
	}
	defer current.Close()
	currentInfo, err := current.Stat()
	if err != nil || !privateRecoveryInfo(currentInfo, false) || !os.SameFile(lock, currentInfo) {
		return errors.New("host_recovery_state_changed")
	}
	return nil
}

func (s *hostRecoveryState) verifyDirectoryPath() error {
	if s.directoryFile == nil {
		return errors.New("host_recovery_state_unsafe")
	}
	dir, err := s.directoryFile.Stat()
	if err != nil || !privateRecoveryInfo(dir, true) {
		return errors.New("host_recovery_state_unsafe")
	}
	path, err := os.Lstat(s.directory)
	if err != nil || !privateRecoveryInfo(path, true) || !os.SameFile(dir, path) {
		return errors.New("host_recovery_state_changed")
	}
	return nil
}

func (s *hostRecoveryState) authoritySHA256() (string, error) {
	if err := s.verifyAuthorityPath(); err != nil {
		return "", err
	}
	dir, err := s.directoryFile.Stat()
	if err != nil {
		return "", err
	}
	lock, err := s.lock.Stat()
	if err != nil {
		return "", err
	}
	d, _ := infoSyscall(dir)
	l, _ := infoSyscall(lock)
	return hashText(fmt.Sprintf("original-owner-recovery-authority-v2\x00%d:%d:%d:%d", d.Dev, d.Ino, l.Dev, l.Ino)), nil
}

func (s *hostRecoveryState) readFile() ([]byte, error) {
	if err := s.verifyAuthorityPath(); err != nil {
		return nil, err
	}
	file, err := recoveryFileAt(s.directoryFile, recoveryLedgerName, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !privateRecoveryInfo(info, false) || info.Size() < 1 || info.Size() > 4096 {
		return nil, errors.New("host_recovery_state_unsafe")
	}
	body, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || int64(len(body)) != info.Size() {
		return nil, errors.New("host_recovery_state_unsafe")
	}
	if err = s.verifyAuthorityPath(); err != nil {
		return nil, err
	}
	return body, nil
}

func (s *hostRecoveryState) persistBytes(body []byte) error {
	current, err := s.readFile()
	if s.bytes == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("host_recovery_state_changed")
		}
	} else if err != nil || !bytes.Equal(current, s.bytes) {
		return errors.New("host_recovery_state_changed")
	}
	if err = s.verifyAuthorityPath(); err != nil {
		return err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	name := ".coordinator-recovery-" + hex.EncodeToString(nonce) + ".new"
	stage, err := recoveryFileAt(s.directoryFile, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(s.directoryFile.Fd()), name, 0)
	if _, err = stage.Write(body); err == nil {
		err = stage.Sync()
	}
	if closeErr := stage.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = s.verifyAuthorityPath()
	}
	if err == nil {
		err = unix.Renameat(int(s.directoryFile.Fd()), name, int(s.directoryFile.Fd()), recoveryLedgerName)
	}
	if err == nil {
		err = s.verifyAuthorityPath()
	}
	if err == nil {
		err = s.directoryFile.Sync()
	}
	if err == nil {
		s.bytes = body
	}
	return err
}
