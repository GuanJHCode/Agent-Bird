package gitopsworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

var errCandidateSeal = errors.New("candidate_resource_seal_invalid")

// SealCandidateJournal opens only the exact, already-existing source journal.
// It intentionally never adopts a sibling journal or creates recovery state.
func SealCandidateJournal(ctx context.Context, spoolRoot string, source gitops.CandidateResourceSource) (gitops.CandidateResourceHeader, error) {
	root, err := candidateJournalRoot(spoolRoot, source)
	if err != nil {
		return gitops.CandidateResourceHeader{}, err
	}
	j, err := openExistingJournal(root)
	if err != nil {
		return gitops.CandidateResourceHeader{}, err
	}
	defer j.close()
	return j.SealCandidateResource(ctx, spoolRoot, source)
}

func (j *durableJournal) SealCandidateResource(ctx context.Context, spoolRoot string, source gitops.CandidateResourceSource) (out gitops.CandidateResourceHeader, err error) {
	root, err := candidateJournalRoot(spoolRoot, source)
	if err != nil || filepath.Clean(j.file.Name()) != filepath.Join(root, "events.jsonl") {
		return out, errCandidateSeal
	}
	spool, namespace, directory, err := candidateSealDirectories(spoolRoot, root)
	if err != nil {
		return out, err
	}
	err = j.withLock(syscall.LOCK_EX, func() error {
		if existing, found, e := readCandidateSeal(filepath.Join(root, "seal.json")); e != nil {
			return e
		} else if found {
			// A seal is a permanent append fence, including a malformed one.
			if e = existing.Validate(); e != nil {
				return errCandidateSeal
			}
			if existing.Source != source {
				return errCandidateSeal
			}
			body, rows, e := j.snapshotLocked(ctx)
			if e != nil || !sameSealDirectories(existing, spool, namespace, directory) {
				return errCandidateSeal
			}
			receipt, e := sealReceipt(source, rows)
			if e != nil || gitops.CandidateResourceReceiptDigest(receipt) != existing.ReceiptSHA256 {
				return errCandidateSeal
			}
			projection, e := candidateProjection(source, receipt)
			if e != nil || projection != source.ProjectionSHA256 {
				return errCandidateSeal
			}
			sum := sha256.Sum256(body)
			st, e := j.validJournalFile()
			if e != nil || hex.EncodeToString(sum[:]) != existing.JournalSHA256 || sealFileIdentity(st) != existing.JournalIdentity {
				return errCandidateSeal
			}
			out = existing
			return nil
		}
		body, rows, e := j.snapshotLocked(ctx)
		if e != nil {
			return e
		}
		receipt, e := sealReceipt(source, rows)
		if e != nil {
			return e
		}
		if e = j.validJournalFileExact(int64(len(body))); e != nil {
			return e
		}
		fullDigest := gitops.CandidateResourceReceiptDigest(receipt)
		projection, e := candidateProjection(source, receipt)
		if e != nil || source.ProjectionSHA256 != projection {
			return errCandidateSeal
		}
		sum := sha256.Sum256(body)
		st, e := j.validJournalFile()
		if e != nil {
			return e
		}
		identity := sealFileIdentity(st)
		out = gitops.CandidateResourceHeader{Version: 1, ResourceID: gitops.CandidateResourceID(source, fullDigest), Source: source, ReceiptSHA256: fullDigest, JournalSHA256: hex.EncodeToString(sum[:]), JournalIdentity: identity, SpoolIdentity: spool, NamespaceIdentity: namespace, DirectoryIdentity: directory}
		out.SealSHA256 = gitops.CandidateResourceSealDigest(out)
		if e = out.Validate(); e != nil {
			return errCandidateSeal
		}
		return writeCandidateSeal(filepath.Join(root, "seal.json"), out)
	})
	return out, err
}

func sameSealDirectories(h gitops.CandidateResourceHeader, spool, namespace, directory gitops.DirectoryIdentity) bool {
	return h.SpoolIdentity == spool && h.NamespaceIdentity == namespace && h.DirectoryIdentity == directory
}

func (j *durableJournal) CandidateResourceSealed(ctx context.Context) (bool, error) {
	sealed := false
	err := j.withLock(syscall.LOCK_SH, func() error {
		h, found, err := readCandidateSeal(filepath.Join(filepath.Dir(j.file.Name()), "seal.json"))
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if err = h.Validate(); err != nil {
			return errCandidateSeal
		}
		root := filepath.Dir(j.file.Name())
		suffix := string(filepath.Separator) + filepath.FromSlash(h.Source.JournalRelative)
		spool := strings.TrimSuffix(root, suffix)
		if spool == root || filepath.Join(spool, filepath.FromSlash(h.Source.JournalRelative)) != root {
			return errCandidateSeal
		}
		spoolID, namespaceID, directoryID, err := candidateSealDirectories(spool, root)
		if err != nil || !sameSealDirectories(h, spoolID, namespaceID, directoryID) {
			return errCandidateSeal
		}
		body, rows, err := j.snapshotLocked(ctx)
		if err != nil {
			return err
		}
		receipt, err := sealReceipt(h.Source, rows)
		if err != nil || gitops.CandidateResourceReceiptDigest(receipt) != h.ReceiptSHA256 {
			return errCandidateSeal
		}
		projection, err := candidateProjection(h.Source, receipt)
		if err != nil || projection != h.Source.ProjectionSHA256 {
			return errCandidateSeal
		}
		sum := sha256.Sum256(body)
		st, err := j.validJournalFile()
		if err != nil || hex.EncodeToString(sum[:]) != h.JournalSHA256 || sealFileIdentity(st) != h.JournalIdentity {
			return errCandidateSeal
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		sealed = true
		return nil
	})
	return sealed, err
}

func OpenSealedCandidateResource(ctx context.Context, spoolRoot string, header gitops.CandidateResourceHeader) (gitops.CandidateReceipt, error) {
	if err := header.Validate(); err != nil {
		return gitops.CandidateReceipt{}, errCandidateSeal
	}
	root, err := candidateJournalRoot(spoolRoot, header.Source)
	if err != nil {
		return gitops.CandidateReceipt{}, err
	}
	spool, namespace, directory, err := candidateSealDirectories(spoolRoot, root)
	if err != nil || spool != header.SpoolIdentity || namespace != header.NamespaceIdentity || directory != header.DirectoryIdentity {
		return gitops.CandidateReceipt{}, errCandidateSeal
	}
	j, err := openExistingJournal(root)
	if err != nil {
		return gitops.CandidateReceipt{}, err
	}
	defer j.close()
	var receipt gitops.CandidateReceipt
	err = j.withLock(syscall.LOCK_SH, func() error {
		seal, found, e := readCandidateSeal(filepath.Join(root, "seal.json"))
		if e != nil || !found || !reflect.DeepEqual(seal, header) {
			return errCandidateSeal
		}
		body, rows, e := j.snapshotLocked(ctx)
		if e != nil {
			return e
		}
		if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != header.JournalSHA256 {
			return errCandidateSeal
		}
		if e = j.validJournalFileExact(header.JournalIdentity.Size); e != nil {
			return e
		}
		st, e := j.validJournalFile()
		if e != nil || sealFileIdentity(st) != header.JournalIdentity {
			return errCandidateSeal
		}
		receipt, e = sealReceipt(header.Source, rows)
		if e != nil || gitops.CandidateResourceReceiptDigest(receipt) != header.ReceiptSHA256 {
			return errCandidateSeal
		}
		projection, e := candidateProjection(header.Source, receipt)
		if e != nil || projection != header.Source.ProjectionSHA256 {
			return errCandidateSeal
		}
		return nil
	})
	return receipt, err
}

func candidateJournalRoot(spoolRoot string, source gitops.CandidateResourceSource) (string, error) {
	if source.Validate() != nil || !filepath.IsAbs(spoolRoot) {
		return "", errCandidateSeal
	}
	root := filepath.Join(filepath.Clean(spoolRoot), filepath.FromSlash(source.JournalRelative))
	if filepath.Clean(root) != root {
		return "", errCandidateSeal
	}
	return root, nil
}

func candidateSealDirectories(spoolRoot, root string) (gitops.DirectoryIdentity, gitops.DirectoryIdentity, gitops.DirectoryIdentity, error) {
	spool, err := sealDirectoryIdentity(filepath.Clean(spoolRoot))
	if err != nil {
		return gitops.DirectoryIdentity{}, gitops.DirectoryIdentity{}, gitops.DirectoryIdentity{}, err
	}
	namespace, err := sealDirectoryIdentity(filepath.Dir(root))
	if err != nil {
		return gitops.DirectoryIdentity{}, gitops.DirectoryIdentity{}, gitops.DirectoryIdentity{}, err
	}
	directory, err := sealDirectoryIdentity(root)
	if err != nil {
		return gitops.DirectoryIdentity{}, gitops.DirectoryIdentity{}, gitops.DirectoryIdentity{}, err
	}
	return spool, namespace, directory, nil
}

func sealDirectoryIdentity(path string) (gitops.DirectoryIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return gitops.DirectoryIdentity{}, errCandidateSeal
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || st.Uid != uint32(os.Geteuid()) {
		return gitops.DirectoryIdentity{}, errCandidateSeal
	}
	return gitops.DirectoryIdentity{Device: int64(st.Dev), Inode: int64(st.Ino), Mode: int64(info.Mode()), UID: int64(st.Uid)}, nil
}
func sealFileIdentity(info os.FileInfo) gitops.FileIdentity {
	st := info.Sys().(*syscall.Stat_t)
	return gitops.FileIdentity{Device: int64(st.Dev), Inode: int64(st.Ino), Mode: int64(info.Mode()), Size: info.Size(), ModUnixNano: info.ModTime().UnixNano()}
}
func (j *durableJournal) validJournalFileExact(size int64) error {
	st, err := j.validJournalFile()
	if err != nil || st.Size() != size {
		return errCandidateSeal
	}
	return nil
}

func readCandidateSeal(path string) (gitops.CandidateResourceHeader, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return gitops.CandidateResourceHeader{}, false, nil
	}
	if err != nil || !privateJournalFile(info) || info.Size() > 8192 {
		return gitops.CandidateResourceHeader{}, false, errCandidateSeal
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return gitops.CandidateResourceHeader{}, false, errCandidateSeal
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || int64(len(body)) != info.Size() || len(body) > 8192 {
		return gitops.CandidateResourceHeader{}, false, errCandidateSeal
	}
	var h gitops.CandidateResourceHeader
	if decodeJournalStrict(body, &h) != nil || h.Validate() != nil {
		return gitops.CandidateResourceHeader{}, false, errCandidateSeal
	}
	return h, true, nil
}
func writeCandidateSeal(path string, header gitops.CandidateResourceHeader) error {
	body, err := json.Marshal(header)
	if err != nil || len(body) > 8192 {
		return errCandidateSeal
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, found, e := readCandidateSeal(path)
		if e == nil && found && reflect.DeepEqual(existing, header) {
			return nil
		}
		return errCandidateSeal
	}
	if err != nil {
		return err
	}
	for written := 0; written < len(body); {
		n, e := f.Write(body[written:])
		written += n
		if e != nil {
			_ = f.Close()
			return e
		}
		if n == 0 {
			_ = f.Close()
			return io.ErrShortWrite
		}
	}
	if err = f.Sync(); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func sealReceipt(source gitops.CandidateResourceSource, rows []journalRecord) (gitops.CandidateReceipt, error) {
	var receipt gitops.CandidateReceipt
	count, completed := 0, 0
	var pending *gitops.GitProcessRecord
	for _, row := range rows {
		switch row.Kind {
		case "cleanup_intent", "cleanup_plan", "cleanup_step", "cleanup_outcome", "integration_intent", "integration_outcome":
			return receipt, errCandidateSeal
		case "candidate_receipt":
			if count != 0 || decodeJournalStrict(row.Value, &receipt) != nil {
				return receipt, errCandidateSeal
			}
			count++
		case "git_process":
			var p gitops.GitProcessRecord
			if decodeJournalStrict(row.Value, &p) != nil || !sealProcess(&pending, p, &completed) {
				return receipt, errCandidateSeal
			}
		}
	}
	if count != 1 || pending != nil || completed == 0 || receipt.Filter != nil || receipt.CandidateOID == "" || receipt.CandidateOID != receipt.PrivateRefExpectedOID || receipt.AttemptID != source.Binding.AttemptID || receipt.PlanRevision != int64(source.Binding.PlanRevision) || receipt.RootIdentity.Inode <= 0 || receipt.RepoRootIdentity.Inode <= 0 || receipt.CommonDirIdentity.Inode <= 0 || receipt.PrivateRef != sealedPrivateRef(source) {
		return receipt, errCandidateSeal
	}
	if source.Kind == "producer" {
		if receipt.Binding == nil || *receipt.Binding != source.Binding || gitops.CandidateResourceReceiptDigest(receipt) != source.ProjectionSHA256 {
			return receipt, errCandidateSeal
		}
	} else if receipt.Binding != nil {
		return receipt, errCandidateSeal
	}
	return receipt, nil
}
func sealedPrivateRef(source gitops.CandidateResourceSource) string {
	key := source.Binding.RunID + "\x00" + source.Binding.TaskID + "\x00" + source.Binding.AttemptID + "\x00" + source.Binding.SegmentID
	sum := sha256.Sum256([]byte(key))
	prefix := "refs/orchestrator/g3/candidate-"
	if source.Kind == "compose" {
		prefix = "refs/orchestrator/g3/compose-"
	}
	return prefix + hex.EncodeToString(sum[:])
}
func sealProcess(pending **gitops.GitProcessRecord, p gitops.GitProcessRecord, completed *int) bool {
	if len(p.CommandDigest) != 64 {
		return false
	}
	if _, err := hex.DecodeString(p.CommandDigest); err != nil {
		return false
	}
	switch p.Phase {
	case "intent":
		if *pending != nil || p.Process != nil {
			return false
		}
		*pending = &p
	case "spawned":
		if *pending == nil || (*pending).Phase != "intent" || (*pending).CommandDigest != p.CommandDigest || !validSealProcess(p.Process) {
			return false
		}
		*pending = &p
	case "exited":
		if *pending == nil || (*pending).Phase != "spawned" || (*pending).CommandDigest != p.CommandDigest || p.Process == nil || *(*pending).Process != *p.Process {
			return false
		}
		*pending = nil
		*completed++
	default:
		return false
	}
	return true
}
func validSealProcess(p *process.Identity) bool {
	if p == nil || p.PID <= 0 || p.PGID != p.PID || !p.BirthKnown || p.Birth == "" || !filepath.IsAbs(p.Executable) || len(p.ExecutableSHA256) != 64 {
		return false
	}
	_, err := hex.DecodeString(p.ExecutableSHA256)
	if err != nil {
		return false
	}
	_, err = time.Parse(time.RFC3339Nano, p.StartedAt)
	return err == nil
}
func candidateProjection(source gitops.CandidateResourceSource, receipt gitops.CandidateReceipt) (string, error) {
	if source.Kind == "producer" {
		return gitops.CandidateResourceReceiptDigest(receipt), nil
	}
	public := receipt
	public.Files = nil
	public.Binding = &source.Binding
	return gitops.CandidateResourceReceiptDigest(public), nil
}

func openExistingJournal(root string) (*durableJournal, error) {
	if _, _, _, err := candidateSealDirectories(filepath.Dir(filepath.Dir(root)), root); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, "events.jsonl"), os.O_RDWR|os.O_APPEND|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if !privateJournalFileMust(file) {
		_ = file.Close()
		return nil, errCandidateSeal
	}
	lock, err := os.OpenFile(filepath.Join(root, "events.lock"), os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !privateJournalFileMust(lock) {
		_ = lock.Close()
		_ = file.Close()
		return nil, errCandidateSeal
	}
	return &durableJournal{file: file, lock: lock}, nil
}
func privateJournalFileMust(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && privateJournalFile(info)
}

// Keep bytes exact while retaining the existing strict JSON scanner contract.
func (j *durableJournal) snapshotLocked(ctx context.Context) ([]byte, []journalRecord, error) {
	st, err := j.validJournalFile()
	if err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(j.file.Name(), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(st, opened) || opened.Size() != st.Size() {
		return nil, nil, errCandidateSeal
	}
	body, err := io.ReadAll(io.LimitReader(f, journalLimit+1))
	if err != nil || int64(len(body)) != st.Size() || len(body) > journalLimit || (len(body) > 0 && body[len(body)-1] != '\n') {
		return nil, nil, errCandidateSeal
	}
	rows := []journalRecord{}
	for _, raw := range bytes.Split(bytes.TrimSuffix(body, []byte("\n")), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var row journalRecord
		if len(raw) > journalRecordLimit || decodeJournalStrict(raw, &row) != nil || row.Version != 1 || row.Kind == "" {
			return nil, nil, errCandidateSeal
		}
		if err = ctx.Err(); err != nil {
			return nil, nil, err
		}
		rows = append(rows, row)
	}
	latest, err := j.validJournalFile()
	after, afterErr := f.Stat()
	if err != nil || afterErr != nil || latest.Size() != st.Size() || !latest.ModTime().Equal(st.ModTime()) || !os.SameFile(after, st) {
		return nil, nil, errCandidateSeal
	}
	return body, rows, nil
}
