package host

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"golang.org/x/sys/unix"
)

// OwnerRecoveryLease holds the predecessor's existing file authority while the
// Coordinator authorizes succession. It never creates or updates recovery state.
// Callers serialize registration before opening it and keep it through Resume.
type OwnerRecoveryLease struct {
	directories *ownerRuntimeDirectories
	recovery    *ownerRecoveryState
	recordFile  *os.File
	recordInfo  os.FileInfo
	failed      bool
}

// OpenExistingOwnerRecoveryLease accepts the Coordinator's durable binding,
// not a caller-selected spool path or an assertion of process death.
func OpenExistingOwnerRecoveryLease(stateDir string, scope contract.OriginalOwnerScope, owner contract.OriginalOwnerIdentity) (*OwnerRecoveryLease, error) {
	scopeHash, err := originalOwnerRecoveryScope(scope, owner)
	if err != nil || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || filepath.Dir(stateDir) == stateDir || scope.LaunchID == "." || scope.LaunchID == ".." || scope.LaunchID == "" || filepath.Base(scope.LaunchID) != scope.LaunchID || strings.ContainsAny(scope.LaunchID, "/\\\x00") || scope.ProducerID != scope.LaunchID {
		return nil, ownerRecoveryRejected()
	}
	dirs, err := openExistingOwnerRuntimeDirectories(stateDir, scope.LaunchID)
	if err != nil {
		return nil, err
	}
	l := &OwnerRecoveryLease{directories: dirs}
	file, err := lockExistingOwnerRecovery(dirs)
	if err != nil {
		l.Close()
		return nil, err
	}
	l.recovery = &ownerRecoveryState{file: file}
	l.recordFile, err = recoveryFileAt(file.directoryFile, recoveryLedgerName, unix.O_RDONLY, 0)
	if err != nil {
		l.Close()
		return nil, err
	}
	l.recordInfo, err = l.recordFile.Stat()
	if err != nil || !privateRecoveryInfo(l.recordInfo, false) {
		l.Close()
		return nil, ownerRecoveryRejected()
	}
	authority, err := file.authoritySHA256()
	if err != nil {
		l.Close()
		return nil, err
	}
	body, err := file.readFile()
	if err != nil {
		l.Close()
		return nil, err
	}
	var record ownerRecoveryRecord
	if contract.DecodeOwnerRuntimeStrict(body, &record) != nil || validateOwnerRecovery(record) != nil || record.ScopeSHA256 != scopeHash || record.AuthoritySHA256 != authority {
		l.Close()
		return nil, ownerRecoveryRejected()
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(append(canonical, '\n'), body) {
		l.Close()
		return nil, ownerRecoveryRejected()
	}
	l.recovery.record, file.bytes = record, body
	if err = l.ValidateSuccession(); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func openExistingOwnerRuntimeDirectories(stateDir, launchID string) (*ownerRuntimeDirectories, error) {
	state, err := os.OpenFile(stateDir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errOwnerRuntimeDirectory
	}
	dirs := &ownerRuntimeDirectories{paths: []string{stateDir}, files: []*os.File{state}}
	if err = dirs.verify(); err != nil {
		dirs.close()
		return nil, err
	}
	for _, name := range []string{"host-spool", launchID} {
		child, err := recoveryFileAt(dirs.files[len(dirs.files)-1], name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			dirs.close()
			return nil, errOwnerRuntimeDirectory
		}
		dirs.files = append(dirs.files, child)
		dirs.paths = append(dirs.paths, filepath.Join(dirs.paths[len(dirs.paths)-1], name))
		if err = dirs.verify(); err != nil {
			dirs.close()
			return nil, err
		}
	}
	return dirs, nil
}

func lockExistingOwnerRecovery(dirs *ownerRuntimeDirectories) (*hostRecoveryState, error) {
	if err := dirs.verify(); err != nil {
		return nil, err
	}
	path := dirs.paths[2]
	// Reopen relative to the held parent. Verification below requires this
	// duplicate descriptor and the held launch descriptor to name one inode.
	dir, err := recoveryFileAt(dirs.files[1], filepath.Base(path), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	s := &hostRecoveryState{directory: path, directoryFile: dir, path: filepath.Join(path, recoveryLedgerName)}
	lock, err := recoveryFileAt(dir, recoveryLockName, unix.O_RDWR, 0)
	if err != nil {
		s.close()
		return nil, err
	}
	s.lock = lock
	if err = s.verifyAuthorityPath(); err != nil {
		s.close()
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		s.close()
		return nil, err
	}
	if err = s.verifyAuthorityPath(); err == nil {
		err = dirs.verify()
	}
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// ValidateSuccession is read-only and sticky: restoring a replaced path or file
// never revives this lease. Original child authority requires original Wait;
// even an applied or activated predecessor OwnerPending cannot be discarded.
func (l *OwnerRecoveryLease) ValidateSuccession() error {
	if l == nil || l.failed || l.directories == nil || l.recovery == nil || l.recordFile == nil || l.recordInfo == nil {
		return ownerRecoveryRejected()
	}
	if err := l.directories.verify(); err != nil {
		l.failed = true
		return err
	}
	info, err := l.recordFile.Stat()
	if err != nil || !privateRecoveryInfo(info, false) || !os.SameFile(info, l.recordInfo) || info.Size() != l.recordInfo.Size() || !info.ModTime().Equal(l.recordInfo.ModTime()) {
		l.failed = true
		return ownerRecoveryRejected()
	}
	if err = l.recovery.file.verifyNamedFile(l.recordFile, recoveryLedgerName, l.recovery.file.bytes); err != nil {
		l.failed = true
		return err
	}
	if err := l.recovery.verifyCurrent(); err != nil {
		l.failed = true
		return err
	}
	r := l.recovery.record
	if validateOwnerRecovery(r) != nil || r.OwnerPending != nil || (r.Reservation != nil && (r.Reservation.Kind != "coordinator" || r.Reservation.Phase != "reserved")) {
		l.failed = true
		return ownerRecoveryRejected()
	}
	return nil
}

func (l *OwnerRecoveryLease) Close() {
	if l == nil {
		return
	}
	l.failed = true
	if l.recordFile != nil {
		_ = l.recordFile.Close()
		l.recordFile = nil
	}
	if l.recovery != nil {
		l.recovery.close()
	}
	if l.directories != nil {
		l.directories.close()
	}
}
