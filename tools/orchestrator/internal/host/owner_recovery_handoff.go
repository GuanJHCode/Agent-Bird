package host

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"golang.org/x/sys/unix"
)

// Proof is a Coordinator-only mapping of an already durable Store intent or
// applied receipt. This file layer cannot establish that database authority.
type OwnerRecoveryHandoffProof struct {
	PriorScopeSHA256  string `json:"-"`
	ScopeSHA256       string `json:"-"`
	PriorRecordSHA256 string `json:"-"`
	RecordSHA256      string `json:"-"`
	AuthoritySHA256   string `json:"-"`
	Applied           bool   `json:"-"`
}

type OwnerRecoveryHandoffFacts struct {
	PriorScopeSHA256  string `json:"-"`
	ScopeSHA256       string `json:"-"`
	PriorRecordSHA256 string `json:"-"`
	RecordSHA256      string `json:"-"`
	AuthoritySHA256   string `json:"-"`
}

type OwnerRecoveryHandoffLease struct {
	lease     *OwnerRecoveryLease
	facts     OwnerRecoveryHandoffFacts
	next      ownerRecoveryRecord
	nextBytes []byte
	failed    bool
	// Private fault seam; production never selects it through flags or env.
	publicationHook func(string, *os.File) error
}

func OpenExistingOwnerRecoveryHandoff(stateDir string, priorScope contract.OriginalOwnerScope, priorOwner contract.OriginalOwnerIdentity, nextScope contract.OriginalOwnerScope, nextOwner contract.OriginalOwnerIdentity, proof *OwnerRecoveryHandoffProof) (*OwnerRecoveryHandoffLease, error) {
	priorHash, err := originalOwnerRecoveryScope(priorScope, priorOwner)
	if err != nil {
		return nil, ownerRecoveryRejected()
	}
	nextHash, err := originalOwnerRecoveryScope(nextScope, nextOwner)
	if err != nil || priorHash == nextHash || nextScope.Incarnation != priorScope.Incarnation+1 {
		return nil, ownerRecoveryRejected()
	}
	if priorScope.RunID != nextScope.RunID || priorScope.LaunchID != nextScope.LaunchID || priorScope.ProducerID != priorScope.LaunchID || nextScope.ProducerID != nextScope.LaunchID {
		return nil, ownerRecoveryRejected()
	}
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || filepath.Dir(stateDir) == stateDir || priorScope.LaunchID == "." || priorScope.LaunchID == ".." || filepath.Base(priorScope.LaunchID) != priorScope.LaunchID || strings.ContainsAny(priorScope.LaunchID, "/\\\x00") {
		return nil, ownerRecoveryRejected()
	}
	dirs, err := openExistingOwnerRuntimeDirectories(stateDir, priorScope.LaunchID)
	if err != nil {
		return nil, err
	}
	l := &OwnerRecoveryHandoffLease{lease: &OwnerRecoveryLease{directories: dirs}}
	file, err := lockExistingOwnerRecovery(dirs)
	if err != nil {
		l.Close()
		return nil, err
	}
	l.lease.recovery = &ownerRecoveryState{file: file}
	l.lease.recordFile, err = recoveryFileAt(file.directoryFile, recoveryLedgerName, unix.O_RDONLY, 0)
	if err != nil {
		l.Close()
		return nil, err
	}
	l.lease.recordInfo, err = l.lease.recordFile.Stat()
	if err != nil || !privateRecoveryInfo(l.lease.recordInfo, false) {
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
	if contract.DecodeOwnerRuntimeStrict(body, &record) != nil || validateOwnerRecovery(record) != nil || record.AuthoritySHA256 != authority || (record.ScopeSHA256 != priorHash && record.ScopeSHA256 != nextHash) {
		l.Close()
		return nil, ownerRecoveryRejected()
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(append(canonical, '\n'), body) {
		l.Close()
		return nil, ownerRecoveryRejected()
	}
	l.lease.recovery.record, file.bytes = record, body
	priorRecord, nextRecord := record, record
	priorRecord.ScopeSHA256, nextRecord.ScopeSHA256 = priorHash, nextHash
	priorBytes, err := json.Marshal(priorRecord)
	if err != nil {
		l.Close()
		return nil, err
	}
	priorBytes = append(priorBytes, '\n')
	nextBytes, err := json.Marshal(nextRecord)
	if err != nil || len(nextBytes)+1 > 4096 {
		l.Close()
		return nil, ownerRecoveryRejected()
	}
	nextBytes = append(nextBytes, '\n')
	l.facts = OwnerRecoveryHandoffFacts{PriorScopeSHA256: priorHash, ScopeSHA256: nextHash, PriorRecordSHA256: hashText(string(priorBytes)), RecordSHA256: hashText(string(nextBytes)), AuthoritySHA256: authority}
	l.next, l.nextBytes = nextRecord, nextBytes
	if proof == nil {
		if record.ScopeSHA256 != priorHash {
			l.Close()
			return nil, ownerRecoveryRejected()
		}
	} else if !l.matchesProof(*proof) || (proof.Applied && record.ScopeSHA256 != nextHash) {
		l.Close()
		return nil, ownerRecoveryRejected()
	}
	if err = l.verify(); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func (l *OwnerRecoveryHandoffLease) Facts() (OwnerRecoveryHandoffFacts, error) {
	if err := l.verify(); err != nil {
		return OwnerRecoveryHandoffFacts{}, err
	}
	return l.facts, nil
}

func (l *OwnerRecoveryHandoffLease) CommitPrepared(proof OwnerRecoveryHandoffProof) error {
	if err := l.verify(); err != nil {
		return err
	}
	if !l.matchesProof(proof) || (proof.Applied && l.lease.recovery.record.ScopeSHA256 != l.facts.ScopeSHA256) {
		l.failed = true
		return ownerRecoveryRejected()
	}
	if l.lease.recovery.record.ScopeSHA256 == l.facts.ScopeSHA256 {
		return nil
	}
	// The Coordinator persists the matching DB intent before calling this
	// dedicated scope CAS. Ordinary ownerRecoveryState.commit stays unchanged.
	if err := l.persistBytes(); err != nil {
		l.failed = true
		return err
	}
	return nil
}

func (l *OwnerRecoveryHandoffLease) matchesProof(p OwnerRecoveryHandoffProof) bool {
	for _, digest := range []string{p.PriorScopeSHA256, p.ScopeSHA256, p.PriorRecordSHA256, p.RecordSHA256, p.AuthoritySHA256} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest {
			return false
		}
	}
	f := l.facts
	return p.PriorScopeSHA256 == f.PriorScopeSHA256 && p.ScopeSHA256 == f.ScopeSHA256 && p.PriorRecordSHA256 == f.PriorRecordSHA256 && p.RecordSHA256 == f.RecordSHA256 && p.AuthoritySHA256 == f.AuthoritySHA256
}

func (l *OwnerRecoveryHandoffLease) verify() error {
	if l == nil || l.failed || l.lease == nil {
		return ownerRecoveryRejected()
	}
	if err := l.lease.ValidateSuccession(); err != nil {
		l.failed = true
		return err
	}
	r := l.lease.recovery.record
	digest := hashText(string(l.lease.recovery.file.bytes))
	if r.AuthoritySHA256 != l.facts.AuthoritySHA256 || !((r.ScopeSHA256 == l.facts.PriorScopeSHA256 && digest == l.facts.PriorRecordSHA256) || (r.ScopeSHA256 == l.facts.ScopeSHA256 && digest == l.facts.RecordSHA256)) {
		l.failed = true
		return ownerRecoveryRejected()
	}
	return nil
}

func (l *OwnerRecoveryHandoffLease) Close() {
	if l == nil {
		return
	}
	l.failed = true
	if l.lease != nil {
		l.lease.Close()
	}
}

func (l *OwnerRecoveryHandoffLease) publicationStep(point string, stage *os.File) error {
	if l.publicationHook != nil {
		return l.publicationHook(point, stage)
	}
	return nil
}

// Retain every uncertain scene: no deferred unlink, prefix GC, or prior-byte
// restoration. Only a fully verified rename moves this invocation's own stage.
func (l *OwnerRecoveryHandoffLease) persistBytes() error {
	if err := l.publicationStep("before_create", nil); err != nil {
		return err
	}
	if err := l.verify(); err != nil {
		return err
	}
	file := l.lease.recovery.file
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".owner-recovery-handoff-" + hex.EncodeToString(nonce[:]) + ".new"
	stage, err := recoveryFileAt(file.directoryFile, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	ownedStage := true
	defer func() {
		if ownedStage {
			_ = stage.Close()
		}
	}()
	if err = l.publicationStep("created", stage); err != nil {
		return err
	}
	n, err := stage.Write(l.nextBytes)
	if err != nil {
		return err
	}
	if n != len(l.nextBytes) {
		return io.ErrShortWrite
	}
	stageInfo, err := stage.Stat()
	if err != nil || !privateRecoveryInfo(stageInfo, false) {
		return ownerRecoveryRejected()
	}
	if err = l.publicationStep("written", stage); err != nil {
		return err
	}
	if err = stage.Sync(); err != nil {
		return err
	}
	if err = l.publicationStep("stage_synced", stage); err != nil {
		return err
	}
	if err = l.verifyStage(stage, name, stageInfo); err != nil {
		return err
	}
	if err = l.verify(); err != nil {
		return err
	}
	if err = l.publicationStep("before_rename", stage); err != nil {
		return err
	}
	if err = l.verifyStage(stage, name, stageInfo); err != nil {
		return err
	}
	if err = l.verify(); err != nil {
		return err
	}
	if err = unix.Renameat(int(file.directoryFile.Fd()), name, int(file.directoryFile.Fd()), recoveryLedgerName); err != nil {
		return err
	}
	if err = l.publicationStep("renamed", stage); err != nil {
		return err
	}
	if err = l.verifyStage(stage, recoveryLedgerName, stageInfo); err != nil {
		return err
	}
	if err = file.directoryFile.Sync(); err != nil {
		return err
	}
	if err = l.publicationStep("directory_synced", stage); err != nil {
		return err
	}
	if err = l.verifyStage(stage, recoveryLedgerName, stageInfo); err != nil {
		return err
	}
	actual, err := file.readFile()
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, l.nextBytes) {
		return ownerRecoveryRejected()
	}
	if err = l.verifyStage(stage, recoveryLedgerName, stageInfo); err != nil {
		return err
	}
	old := l.lease.recordFile
	l.lease.recordFile, l.lease.recordInfo = stage, stageInfo
	l.lease.recovery.record, file.bytes = l.next, append([]byte(nil), l.nextBytes...)
	ownedStage = false
	_ = old.Close()
	return l.verify()
}

func (l *OwnerRecoveryHandoffLease) verifyStage(stage *os.File, name string, expected os.FileInfo) error {
	if err := l.lease.directories.verify(); err != nil {
		return err
	}
	file := l.lease.recovery.file
	authority, err := file.authoritySHA256()
	if err != nil {
		return err
	}
	if authority != l.facts.AuthoritySHA256 {
		return ownerRecoveryRejected()
	}
	info, err := stage.Stat()
	if err != nil || !privateRecoveryInfo(info, false) || !os.SameFile(info, expected) || info.Size() != expected.Size() || !info.ModTime().Equal(expected.ModTime()) {
		return ownerRecoveryRejected()
	}
	if err = file.verifyNamedFile(stage, name, l.nextBytes); err != nil {
		return err
	}
	after, err := stage.Stat()
	if err != nil || !privateRecoveryInfo(after, false) || !os.SameFile(after, expected) || after.Size() != expected.Size() || !after.ModTime().Equal(expected.ModTime()) {
		return ownerRecoveryRejected()
	}
	return l.lease.directories.verify()
}
