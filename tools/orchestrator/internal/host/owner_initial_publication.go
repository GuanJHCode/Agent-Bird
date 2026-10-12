package host

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"golang.org/x/sys/unix"
)

// InitialOwnerRecoveryPublication is a Coordinator-only, DB-authorized lease.
// It can publish only the original incarnation's canonical zero ledger. All
// ordinary recovery opens remain existing-only. Store must commit the matching
// prepared proof before PublishPrepared; no SQLite transaction spans file IO.
type InitialOwnerRecoveryPublication struct {
	authority  store.StoreRootAuthority
	dirs       *ownerRuntimeDirectories
	database   *os.File
	file       *hostRecoveryState
	record     *os.File
	recordInfo os.FileInfo
	facts      store.InitialOwnerRecoveryFacts
	body       []byte
	published  bool
	failed     bool
	// Private crash seam, never selected through an environment or public flag.
	stageName       string
	publicationHook func(string, *os.File) error
}

func OpenInitialOwnerRecoveryPublication(stateDir string, authority store.StoreRootAuthority, scope contract.OriginalOwnerScope, owner contract.OriginalOwnerIdentity, prepared *store.InitialOwnerRecoveryProof) (result *InitialOwnerRecoveryPublication, resultErr error) {
	root := authority.Nodes()
	scopeHash, err := originalOwnerRecoveryScope(scope, owner)
	if err != nil || scope.Incarnation != 1 || scope.ProducerID != scope.LaunchID || scope.LaunchID == "" || scope.LaunchID == "." || scope.LaunchID == ".." || filepath.Base(scope.LaunchID) != scope.LaunchID || strings.ContainsAny(scope.LaunchID, "/\\\x00") || stateDir != root.StateRoot || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || filepath.Dir(stateDir) == stateDir || authority.Validate() != nil {
		return nil, ownerRecoveryRejected()
	}
	l := &InitialOwnerRecoveryPublication{authority: authority}
	defer func() {
		if resultErr != nil {
			l.Close()
		}
	}()
	state, err := os.OpenFile(stateDir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	l.dirs = &ownerRuntimeDirectories{paths: []string{stateDir}, files: []*os.File{state}}
	l.database, err = recoveryFileAt(state, "state.db", unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	if err = l.verifyRoot(); err != nil {
		return nil, err
	}
	// Only a first preparation may create inert directories/lock. A persisted
	// proof never authorizes reconstruction of a missing authority node.
	for _, name := range []string{"host-spool", scope.LaunchID} {
		if err = l.verifyRoot(); err != nil {
			return nil, err
		}
		parent := l.dirs.files[len(l.dirs.files)-1]
		child, e := recoveryFileAt(parent, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if errors.Is(e, unix.ENOENT) && prepared == nil {
			if e = unix.Mkdirat(int(parent.Fd()), name, 0700); e != nil {
				return nil, e
			}
			if e = parent.Sync(); e != nil {
				return nil, e
			}
			child, e = recoveryFileAt(parent, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		}
		if e != nil {
			return nil, e
		}
		l.dirs.files = append(l.dirs.files, child)
		l.dirs.paths = append(l.dirs.paths, filepath.Join(l.dirs.paths[len(l.dirs.paths)-1], name))
		if err = l.verifyRoot(); err != nil {
			return nil, err
		}
	}
	dir, err := recoveryFileAt(l.dirs.files[1], scope.LaunchID, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	l.file = &hostRecoveryState{directory: l.dirs.paths[2], directoryFile: dir, path: filepath.Join(l.dirs.paths[2], recoveryLedgerName)}
	lock, err := recoveryFileAt(dir, recoveryLockName, unix.O_RDWR, 0)
	if errors.Is(err, unix.ENOENT) && prepared == nil {
		if err = l.verifyRoot(); err != nil {
			return nil, err
		}
		lock, err = recoveryFileAt(dir, recoveryLockName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
		if err == nil {
			err = dir.Sync()
		}
	}
	if err != nil {
		if lock != nil {
			_ = lock.Close()
		}
		return nil, err
	}
	l.file.lock = lock
	if err = l.file.verifyAuthorityPath(); err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, err
	}
	if err = l.verifyRoot(); err != nil {
		return nil, err
	}
	// Existing entries may be the result of a prior create whose parent fsync
	// failed. Confirm all three namespace levels before Facts can be persisted.
	if err = l.file.lock.Sync(); err != nil {
		return nil, err
	}
	for _, directory := range l.dirs.files {
		if err = l.verifyRoot(); err != nil {
			return nil, err
		}
		if err = l.file.verifyAuthorityPath(); err != nil {
			return nil, err
		}
		if err = directory.Sync(); err != nil {
			return nil, err
		}
	}
	if err = l.verifyRoot(); err != nil {
		return nil, err
	}
	authorityHash, err := l.file.authoritySHA256()
	if err != nil {
		return nil, err
	}
	l.body, err = json.Marshal(ownerRecoveryRecord{Version: 2, AuthoritySHA256: authorityHash, ScopeSHA256: scopeHash})
	if err != nil {
		return nil, err
	}
	l.body = append(l.body, '\n')
	l.facts = store.InitialOwnerRecoveryFacts{Root: root, ScopeSHA256: scopeHash, AuthoritySHA256: authorityHash, RecordSHA256: hashText(string(l.body))}
	for index, pair := range [][2]*uint64{{&l.facts.SpoolDevice, &l.facts.SpoolInode}, {&l.facts.DirectoryDevice, &l.facts.DirectoryInode}, {&l.facts.LockDevice, &l.facts.LockInode}} {
		f := lock
		if index < 2 {
			f = l.dirs.files[index+1]
		}
		info, e := f.Stat()
		if e != nil {
			return nil, e
		}
		st, ok := infoSyscall(info)
		if !ok {
			return nil, ownerRecoveryRejected()
		}
		*pair[0], *pair[1] = uint64(st.Dev), uint64(st.Ino)
	}
	l.record, err = recoveryFileAt(dir, recoveryLedgerName, unix.O_RDONLY, 0)
	if err == nil {
		l.recordInfo, err = l.record.Stat()
		if err != nil || !privateRecoveryInfo(l.recordInfo, false) || l.file.verifyNamedFile(l.record, recoveryLedgerName, l.body) != nil {
			return nil, ownerRecoveryRejected()
		}
		st, _ := infoSyscall(l.recordInfo)
		l.facts.ExistingRecordDevice, l.facts.ExistingRecordInode = uint64(st.Dev), uint64(st.Ino)
		l.published = true
	} else if !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	if prepared != nil {
		if !initialOwnerStageName(prepared.StageName) || (prepared.Phase != "prepared" && prepared.Phase != "finalized" && prepared.Phase != "activated") {
			return nil, ownerRecoveryRejected()
		}
		expected := prepared.Facts
		actual := l.facts
		if expected.ExistingRecordDevice == 0 && expected.ExistingRecordInode == 0 {
			actual.ExistingRecordDevice, actual.ExistingRecordInode = 0, 0
		} else if expected.ExistingRecordDevice == 0 || expected.ExistingRecordInode == 0 || !l.published {
			return nil, ownerRecoveryRejected()
		}
		if actual != expected || prepared.Phase != "prepared" && !l.published {
			return nil, ownerRecoveryRejected()
		}
		l.facts = expected
		l.stageName = prepared.StageName
	}
	if err = l.verify(); err != nil {
		return nil, err
	}
	return l, nil
}

func initialOwnerStageName(name string) bool {
	const prefix = ".initial-owner-"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".new") {
		return false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".new")
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func (l *InitialOwnerRecoveryPublication) verifyRoot() error {
	if l == nil || l.failed || l.dirs == nil || l.database == nil || l.authority.Validate() != nil {
		return ownerRecoveryRejected()
	}
	if err := l.dirs.verify(); err != nil {
		return err
	}
	nodes := l.authority.Nodes()
	for index, f := range []*os.File{l.dirs.files[0], l.database} {
		info, err := f.Stat()
		if err != nil || !privateRecoveryInfo(info, index == 0) {
			return ownerRecoveryRejected()
		}
		st, _ := infoSyscall(info)
		device, inode := nodes.StateDevice, nodes.StateInode
		if index == 1 {
			device, inode = nodes.DatabaseDevice, nodes.DatabaseInode
		}
		if uint64(st.Dev) != device || uint64(st.Ino) != inode {
			return ownerRecoveryRejected()
		}
	}
	named, err := recoveryFileAt(l.dirs.files[0], "state.db", unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer named.Close()
	held, err := l.database.Stat()
	if err != nil {
		return err
	}
	current, err := named.Stat()
	if err != nil || !privateRecoveryInfo(current, false) || !os.SameFile(held, current) {
		return ownerRecoveryRejected()
	}
	return l.authority.Validate()
}

func (l *InitialOwnerRecoveryPublication) verify() (err error) {
	defer func() {
		if err != nil && l != nil {
			l.failed = true
		}
	}()
	if err = l.verifyRoot(); err != nil {
		return err
	}
	if l.file == nil || l.file.verifyAuthorityPath() != nil {
		return ownerRecoveryRejected()
	}
	if l.record != nil {
		info, e := l.record.Stat()
		if e != nil || !privateRecoveryInfo(info, false) || !os.SameFile(info, l.recordInfo) || info.Size() != l.recordInfo.Size() || !info.ModTime().Equal(l.recordInfo.ModTime()) {
			return ownerRecoveryRejected()
		}
		return l.file.verifyNamedFile(l.record, recoveryLedgerName, l.body)
	}
	file, e := recoveryFileAt(l.file.directoryFile, recoveryLedgerName, unix.O_RDONLY, 0)
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(e, unix.ENOENT) {
		return ownerRecoveryRejected()
	}
	return nil
}

func (l *InitialOwnerRecoveryPublication) Facts() (store.InitialOwnerRecoveryFacts, error) {
	if err := l.verify(); err != nil {
		return store.InitialOwnerRecoveryFacts{}, err
	}
	return l.facts, nil
}

func (l *InitialOwnerRecoveryPublication) step(point string, stage *os.File) error {
	if l.publicationHook != nil {
		return l.publicationHook(point, stage)
	}
	return nil
}

func (l *InitialOwnerRecoveryPublication) PublishPrepared(proof store.InitialOwnerRecoveryProof) (err error) {
	defer func() {
		if err != nil && l != nil {
			l.failed = true
		}
	}()
	if err = l.verify(); err != nil {
		return err
	}
	if proof.Facts != l.facts || !initialOwnerStageName(proof.StageName) || (proof.Phase != "prepared" && proof.Phase != "finalized" && proof.Phase != "activated") {
		return ownerRecoveryRejected()
	}
	if l.stageName != "" && l.stageName != proof.StageName {
		return ownerRecoveryRejected()
	}
	l.stageName = proof.StageName
	if l.published {
		return l.file.directoryFile.Sync()
	}
	if proof.Phase != "prepared" {
		return ownerRecoveryRejected()
	}
	stage, err := recoveryFileAt(l.file.directoryFile, proof.StageName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
	if errors.Is(err, unix.EEXIST) {
		stage, err = recoveryFileAt(l.file.directoryFile, proof.StageName, unix.O_RDWR, 0)
	}
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = stage.Close()
		}
	}()
	info, err := stage.Stat()
	if err != nil || !privateRecoveryInfo(info, false) || info.Size() < 0 || info.Size() > int64(len(l.body)) {
		return ownerRecoveryRejected()
	}
	prefix, err := io.ReadAll(io.NewSectionReader(stage, 0, int64(len(l.body)+1)))
	if err != nil || len(prefix) != int(info.Size()) || !bytes.HasPrefix(l.body, prefix) || l.file.verifyNamedFile(stage, proof.StageName, prefix) != nil {
		return ownerRecoveryRejected()
	}
	if err = l.step("stage_opened", stage); err != nil {
		return err
	}
	if err = l.verify(); err != nil {
		return err
	}
	if err = l.file.verifyNamedFile(stage, proof.StageName, prefix); err != nil {
		return err
	}
	if len(prefix) < len(l.body) {
		n, e := stage.WriteAt(l.body[len(prefix):], int64(len(prefix)))
		if e != nil {
			return e
		}
		if n != len(l.body)-len(prefix) {
			return io.ErrShortWrite
		}
	}
	if err = l.file.verifyNamedFile(stage, proof.StageName, l.body); err != nil {
		return err
	}
	if err = l.step("before_stage_sync", stage); err != nil {
		return err
	}
	if err = stage.Sync(); err != nil {
		return err
	}
	if err = l.step("before_publish", stage); err != nil {
		return err
	}
	if err = l.verify(); err != nil {
		return err
	}
	if err = l.file.verifyNamedFile(stage, proof.StageName, l.body); err != nil {
		return err
	}
	if err = initialOwnerRenameNoReplace(int(l.file.directoryFile.Fd()), proof.StageName, recoveryLedgerName); err != nil {
		return err
	}
	l.record, l.published, keep = stage, true, true
	l.recordInfo, err = stage.Stat()
	if err != nil {
		return err
	}
	if err = l.verify(); err != nil {
		return err
	}
	if err = l.step("after_publish", stage); err != nil {
		return err
	}
	if err = l.file.directoryFile.Sync(); err != nil {
		return err
	}
	if err = l.step("directory_synced", stage); err != nil {
		return err
	}
	return l.ValidatePublished()
}

func (l *InitialOwnerRecoveryPublication) ValidatePublished() error {
	if err := l.verify(); err != nil {
		return err
	}
	if !l.published {
		l.failed = true
		return ownerRecoveryRejected()
	}
	return nil
}

func (l *InitialOwnerRecoveryPublication) Close() {
	if l == nil {
		return
	}
	l.failed = true
	if l.record != nil {
		_ = l.record.Close()
		l.record = nil
	}
	if l.file != nil {
		l.file.close()
	}
	if l.database != nil {
		_ = l.database.Close()
		l.database = nil
	}
	if l.dirs != nil {
		l.dirs.close()
	}
}
