package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var errCoordinatorUnavailable = errors.New("coordinator_unavailable")

type hostRecoveryRecord struct {
	Version           int    `json:"version"`
	ScopeSHA256       string `json:"scope_sha256"`
	Attempts          int    `json:"attempts"`
	NextAttemptUnixMS int64  `json:"next_attempt_unix_ms"`
	Unavailable       bool   `json:"unavailable"`
	Pending           bool   `json:"pending"`
	LastPID           int    `json:"last_pid"`
	LastBirth         string `json:"last_birth"`
}

type hostRecoveryState struct {
	directory string
	path      string
	lock      *os.File
	record    hostRecoveryRecord
	bytes     []byte
}

func validateRecoveryDirectory(path string) error {
	info, err := os.Lstat(path)
	identity, ok := infoSyscall(info)
	if err != nil || !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || identity.Uid != uint32(os.Geteuid()) {
		return errors.New("host_recovery_state_unsafe")
	}
	return nil
}

func readPrivateRecoveryFile(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	identity, ok := infoSyscall(info)
	if err != nil || !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || identity.Uid != uint32(os.Geteuid()) || identity.Nlink != 1 || info.Size() < 1 || info.Size() > 4096 {
		return nil, errors.New("host_recovery_state_unsafe")
	}
	body, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || int64(len(body)) != info.Size() {
		return nil, errors.New("host_recovery_state_unsafe")
	}
	return body, nil
}

func lockHostRecovery(directory string) (*hostRecoveryState, error) {
	if err := validateRecoveryDirectory(directory); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(directory, ".coordinator-recovery.lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errors.New("host_recovery_state_unsafe")
	}
	info, err := lock.Stat()
	identity, ok := infoSyscall(info)
	if err != nil || !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || identity.Uid != uint32(os.Geteuid()) || identity.Nlink != 1 {
		_ = lock.Close()
		return nil, errors.New("host_recovery_state_unsafe")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("host_recovery_state_busy")
	}
	state := &hostRecoveryState{directory: directory, path: filepath.Join(directory, ".coordinator-recovery.json"), lock: lock}
	return state, nil
}

func openHostRecovery(directory, scope string, pid int, birth string) (*hostRecoveryState, error) {
	return openHostRecoveryRecord(directory, scope, pid, birth, false)
}

// Only the Source's authenticated transition path may inspect a mismatched
// scope. All other canonical/file/record checks remain identical.
func openHostRecoveryRecord(directory, scope string, pid int, birth string, allowScopeTransition bool) (*hostRecoveryState, error) {
	if scope == "" || pid <= 0 || birth == "" {
		return nil, errors.New("host_recovery_state_unsafe")
	}
	state, err := lockHostRecovery(directory)
	if err != nil {
		return nil, err
	}
	body, err := readPrivateRecoveryFile(state.path)
	if errors.Is(err, os.ErrNotExist) {
		state.record = hostRecoveryRecord{Version: 1, ScopeSHA256: scope, LastPID: pid, LastBirth: birth}
		err = state.persist()
	} else if err == nil {
		var record hostRecoveryRecord
		if json.Unmarshal(body, &record) != nil || record.Version != 1 || record.ScopeSHA256 == "" || (!allowScopeTransition && record.ScopeSHA256 != scope) || record.Attempts < 0 || record.Attempts > 5 || record.NextAttemptUnixMS < 0 || record.LastPID <= 0 || record.LastBirth == "" || (record.Attempts == 0 && (record.NextAttemptUnixMS != 0 || record.Pending)) || (record.Attempts > 0 && record.NextAttemptUnixMS == 0) || (record.Unavailable && (record.Attempts != 5 || record.Pending)) {
			err = errors.New("host_recovery_state_unsafe")
		} else {
			canonical, _ := json.Marshal(record)
			if !bytes.Equal(append(canonical, '\n'), body) {
				err = errors.New("host_recovery_state_unsafe")
			} else {
				state.record, state.bytes = record, body
			}
		}
	}
	if err != nil {
		state.close()
		return nil, errors.New("host_recovery_state_unsafe")
	}
	return state, nil
}

func (s *hostRecoveryState) transitionScope(prior, next string) error {
	if s.record.ScopeSHA256 != prior || prior == next || next == "" {
		return errors.New("host_recovery_scope_unbound")
	}
	before := s.record
	s.record.ScopeSHA256 = next
	if err := s.persist(); err != nil {
		s.record = before
		return err
	}
	return nil
}

func (s *hostRecoveryState) close() {
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
		s.lock = nil
	}
}

func (s *hostRecoveryState) persist() error {
	body, err := json.Marshal(s.record)
	if err != nil {
		return err
	}
	return s.persistBytes(append(body, '\n'))
}

func (s *hostRecoveryState) persistBytes(body []byte) error {
	if err := validateRecoveryDirectory(s.directory); err != nil {
		return err
	}
	current, err := readPrivateRecoveryFile(s.path)
	if s.bytes == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("host_recovery_state_changed")
		}
	} else if err != nil || !bytes.Equal(current, s.bytes) {
		return errors.New("host_recovery_state_changed")
	}
	stage, err := os.CreateTemp(s.directory, ".coordinator-recovery-*.new")
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	if _, err = stage.Write(body); err == nil {
		err = stage.Sync()
	}
	if closeErr := stage.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(stage.Name(), s.path)
	}
	if err != nil {
		return err
	}
	directory, err := os.Open(s.directory)
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	if err == nil {
		s.bytes = body
	}
	return err
}

func (s *hostRecoveryState) reserve(now time.Time) (time.Time, error) {
	// Validate before changing any record bytes or counters. A retained timer
	// may occupy only its original 1/2/4/8/16s window. A consumed timer cannot
	// be in our future unless the wall clock regressed; never extend its base.
	nowMS := now.UnixMilli()
	if nowMS < 0 || nowMS > math.MaxInt64-16_000 {
		return time.Time{}, errors.New("host_recovery_time_untrusted")
	}
	if s.record.Attempts > 0 {
		maximumDelayMS := int64(1_000) << (s.record.Attempts - 1)
		if s.record.NextAttemptUnixMS > nowMS+maximumDelayMS {
			return time.Time{}, errors.New("host_recovery_time_untrusted")
		}
		if !s.record.Pending && s.record.NextAttemptUnixMS > nowMS {
			return time.Time{}, errors.New("host_recovery_clock_regressed")
		}
	}
	// A restart during backoff resumes its already persisted reservation.
	// It neither skips the timestamp nor charges the same pending attempt twice.
	if s.record.Pending {
		return time.UnixMilli(s.record.NextAttemptUnixMS), nil
	}
	if s.record.Unavailable || s.record.Attempts >= 5 {
		s.record.Unavailable = true
		if err := s.persist(); err != nil {
			return time.Time{}, err
		}
		return time.Time{}, errCoordinatorUnavailable
	}
	base := now
	if previous := time.UnixMilli(s.record.NextAttemptUnixMS); previous.After(base) {
		base = previous
	}
	delay := [...]time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}[s.record.Attempts]
	s.record.Attempts++
	s.record.Pending = true
	s.record.NextAttemptUnixMS = base.Add(delay).UnixMilli()
	if err := s.persist(); err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(s.record.NextAttemptUnixMS), nil
}

func (s *hostRecoveryState) attemptStarted() error {
	if !s.record.Pending {
		return errors.New("host_recovery_attempt_not_reserved")
	}
	s.record.Pending = false
	return s.persist()
}

func recoveryBackoffDelay(when, capturedNow time.Time, elapsed time.Duration) time.Duration {
	if elapsed < 0 {
		elapsed = 0
	}
	delay := when.Sub(capturedNow)
	if delay <= 0 || elapsed >= delay {
		return 0
	}
	return delay - elapsed
}

func (s *hostRecoveryState) authenticatedHost(pid int, birth string) error {
	if s.record.LastPID == pid && s.record.LastBirth == birth {
		return nil
	}
	s.record.LastPID, s.record.LastBirth = pid, birth
	return s.persist()
}
