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
	"golang.org/x/sys/unix"
)

var errCandidateSeal = errors.New("candidate_resource_seal_invalid")

type MaintenanceJournalIdentity struct {
	Directory gitops.DirectoryIdentity `json:"directory"`
	Events    gitops.FileIdentity      `json:"events"`
	Lock      gitops.FileIdentity      `json:"lock"`
}
type sourceOrigin struct {
	Spool     gitops.DirectoryIdentity `json:"spool"`
	Namespace gitops.DirectoryIdentity `json:"namespace"`
	Directory gitops.DirectoryIdentity `json:"directory"`
	Events    gitops.FileIdentity      `json:"events"`
	Lock      gitops.FileIdentity      `json:"lock"`
}

func sourceJournalPath(root string) bool {
	n := filepath.Base(root)
	if len(n) != 64 {
		return false
	}
	if _, e := hex.DecodeString(n); e != nil {
		return false
	}
	p := filepath.Base(filepath.Dir(root))
	return p == ".gitops-journal" || p == "gitops-journal"
}
func writeSourceOrigin(a *sealAuthority, file, lock *os.File) error {
	if e := a.verify(); e != nil {
		return e
	}
	fs, e := file.Stat()
	if e != nil {
		return e
	}
	ls, e := lock.Stat()
	if e != nil {
		return e
	}
	if !privateJournalFile(fs) || !privateJournalFile(ls) || !sameAtFile(a.root, "events.jsonl", file) || !sameAtFile(a.root, "events.lock", lock) {
		return errCandidateSeal
	}
	raw, e := json.Marshal(sourceOrigin{a.spool, a.namespace, a.directory, sealFileIdentity(fs), sealFileIdentity(ls)})
	if e != nil {
		return e
	}
	if e = writePrivateAt(a.root, "origin.json", raw); e != nil {
		return e
	}
	return a.verify()
}

func verifySourceOrigin(a *sealAuthority, file, lock *os.File) error {
	f, e := openAtFile(a.root, "origin.json", unix.O_RDONLY)
	if e != nil {
		return errCandidateSeal
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !privateJournalFile(info) || info.Size() > 8192 {
		return errCandidateSeal
	}
	raw, e := io.ReadAll(io.LimitReader(f, 8193))
	if e != nil || int64(len(raw)) != info.Size() || len(raw) > 8192 || !sameAtFile(a.root, "origin.json", f) {
		return errCandidateSeal
	}
	var o sourceOrigin
	if decodeJournalStrict(raw, &o) != nil {
		return errCandidateSeal
	}
	fs, e := file.Stat()
	if e != nil {
		return errCandidateSeal
	}
	ls, e := lock.Stat()
	if e != nil {
		return errCandidateSeal
	}
	match := func(x, y gitops.FileIdentity) bool {
		return x.Device == y.Device && x.Inode == y.Inode && x.Mode == y.Mode
	}
	if o.Spool != a.spool || o.Namespace != a.namespace || o.Directory != a.directory || !match(o.Events, sealFileIdentity(fs)) || !match(o.Lock, sealFileIdentity(ls)) {
		return errCandidateSeal
	}
	return nil
}
func OpenExistingMaintenanceJournal(root string, expected MaintenanceJournalIdentity) (*durableJournal, error) {
	if !filepath.IsAbs(root) {
		return nil, errCandidateSeal
	}
	fd, e := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return nil, errCandidateSeal
	}
	d := os.NewFile(uintptr(fd), root)
	id, e := heldSealDirectory(d)
	if e != nil || id != expected.Directory {
		d.Close()
		return nil, errCandidateSeal
	}
	f, e := openAtFile(d, "events.jsonl", unix.O_RDWR|unix.O_APPEND)
	if e != nil || !maintenanceFile(f, expected.Events) {
		if f != nil {
			f.Close()
		}
		d.Close()
		return nil, errCandidateSeal
	}
	l, e := openAtFile(d, "events.lock", unix.O_RDWR)
	if e != nil || !maintenanceFile(l, expected.Lock) {
		if l != nil {
			l.Close()
		}
		f.Close()
		d.Close()
		return nil, errCandidateSeal
	}
	named, e := sealDirectoryIdentity(root)
	if e != nil || named != id || !sameAtFile(d, "events.jsonl", f) || !sameAtFile(d, "events.lock", l) {
		f.Close()
		l.Close()
		d.Close()
		return nil, errCandidateSeal
	}
	return &durableJournal{file: f, lock: l, dir: d}, nil
}
func maintenanceFile(f *os.File, want gitops.FileIdentity) bool {
	if f == nil {
		return false
	}
	i, e := f.Stat()
	if e != nil || !privateJournalFile(i) {
		return false
	}
	got := sealFileIdentity(i)
	return got.Device == want.Device && got.Inode == want.Inode && got.Mode == want.Mode
}

type sealAuthority struct {
	spool, namespace, directory gitops.DirectoryIdentity
	spoolFD, namespaceFD, root  *os.File
	path                        string
}

func sealDirectoryInfo(info os.FileInfo) (gitops.DirectoryIdentity, error) {
	if info == nil {
		return gitops.DirectoryIdentity{}, errCandidateSeal
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || st.Uid != uint32(os.Geteuid()) {
		return gitops.DirectoryIdentity{}, errCandidateSeal
	}
	return gitops.DirectoryIdentity{Device: int64(st.Dev), Inode: int64(st.Ino), Mode: int64(info.Mode()), UID: int64(st.Uid)}, nil
}
func heldSealDirectory(f *os.File) (gitops.DirectoryIdentity, error) {
	if f == nil {
		return gitops.DirectoryIdentity{}, errCandidateSeal
	}
	i, e := f.Stat()
	if e != nil {
		return gitops.DirectoryIdentity{}, e
	}
	return sealDirectoryInfo(i)
}
func (a *sealAuthority) close() {
	if a.root != nil {
		a.root.Close()
	}
	if a.namespaceFD != nil {
		a.namespaceFD.Close()
	}
	if a.spoolFD != nil {
		a.spoolFD.Close()
	}
}
func (a *sealAuthority) verify() error {
	for _, x := range []struct {
		f  *os.File
		id gitops.DirectoryIdentity
	}{{a.spoolFD, a.spool}, {a.namespaceFD, a.namespace}, {a.root, a.directory}} {
		id, e := heldSealDirectory(x.f)
		if e != nil || id != x.id {
			return errCandidateSeal
		}
	}
	named, e := sealDirectoryIdentity(a.spoolFD.Name())
	if e != nil || named != a.spool {
		return errCandidateSeal
	}
	if !sameAtFile(a.spoolFD, filepath.Base(a.namespaceFD.Name()), a.namespaceFD) || !sameAtFile(a.namespaceFD, filepath.Base(a.path), a.root) {
		return errCandidateSeal
	}
	return nil
}
func openSealDirectories(spoolRoot, root string) (a *sealAuthority, err error) {
	if !filepath.IsAbs(spoolRoot) || filepath.Dir(filepath.Dir(root)) != filepath.Clean(spoolRoot) {
		return nil, errCandidateSeal
	}
	a = &sealAuthority{path: root}
	defer func() {
		if err != nil {
			a.close()
			a = nil
		}
	}()
	fd, e := unix.Open(filepath.Clean(spoolRoot), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return a, errCandidateSeal
	}
	a.spoolFD = os.NewFile(uintptr(fd), filepath.Clean(spoolRoot))
	if a.spool, err = heldSealDirectory(a.spoolFD); err != nil {
		return
	}
	ns := filepath.Base(filepath.Dir(root))
	a.namespaceFD, err = openAtFile(a.spoolFD, ns, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return
	}
	if a.namespace, err = heldSealDirectory(a.namespaceFD); err != nil {
		return
	}
	a.root, err = openAtFile(a.namespaceFD, filepath.Base(root), unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return
	}
	if a.directory, err = heldSealDirectory(a.root); err != nil {
		return
	}
	err = a.verify()
	return
}
func openSealAuthority(spoolRoot string, source gitops.CandidateResourceSource) (*sealAuthority, error) {
	root, e := candidateJournalRoot(spoolRoot, source)
	if e != nil {
		return nil, e
	}
	return openSealDirectories(spoolRoot, root)
}

func openAtFile(dir *os.File, name string, flags int) (*os.File, error) {
	fd, e := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name)), nil
}
func sameAtFile(dir *os.File, name string, f *os.File) bool {
	x, e := openAtFile(dir, name, unix.O_RDONLY)
	if e != nil {
		return false
	}
	defer x.Close()
	a, e := x.Stat()
	b, be := f.Stat()
	return e == nil && be == nil && os.SameFile(a, b)
}

// SealCandidateJournal opens only the exact, already-existing source journal.
// It intentionally never adopts a sibling journal or creates recovery state.
func SealCandidateJournal(ctx context.Context, spoolRoot string, source gitops.CandidateResourceSource) (gitops.CandidateResourceHeader, error) {
	a, err := openSealAuthority(spoolRoot, source)
	if err != nil {
		return gitops.CandidateResourceHeader{}, err
	}
	defer a.close()
	j, err := openExistingJournalAt(a)
	if err != nil {
		return gitops.CandidateResourceHeader{}, err
	}
	defer j.close()
	return j.sealCandidateResourceAt(ctx, a, source)
}

func (j *durableJournal) SealCandidateResource(ctx context.Context, spoolRoot string, source gitops.CandidateResourceSource) (out gitops.CandidateResourceHeader, err error) {
	a, err := openSealAuthority(spoolRoot, source)
	if err != nil {
		return out, err
	}
	defer a.close()
	if !sameAtFile(a.root, "events.jsonl", j.file) || !sameAtFile(a.root, "events.lock", j.lock) {
		return out, errCandidateSeal
	}
	return j.sealCandidateResourceAt(ctx, a, source)
}

func (j *durableJournal) sealCandidateResourceAt(ctx context.Context, a *sealAuthority, source gitops.CandidateResourceSource) (out gitops.CandidateResourceHeader, err error) {
	spool, namespace, directory := a.spool, a.namespace, a.directory
	err = j.withLock(syscall.LOCK_EX, func() error {
		if e := a.verify(); e != nil {
			return e
		}
		if e := verifySourceOrigin(a, j.file, j.lock); e != nil {
			return e
		}
		if existing, found, e := readCandidateSealAt(a.root); e != nil {
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
			return a.verify()
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
		if e = writeCandidateSealAt(a.root, out); e != nil {
			return e
		}
		return a.verify()
	})
	return out, err
}

func sameSealDirectories(h gitops.CandidateResourceHeader, spool, namespace, directory gitops.DirectoryIdentity) bool {
	return h.SpoolIdentity == spool && h.NamespaceIdentity == namespace && h.DirectoryIdentity == directory
}

func (j *durableJournal) CandidateResourceSealed(ctx context.Context) (bool, error) {
	h, found, e := readCandidateSeal(filepath.Join(filepath.Dir(j.file.Name()), "seal.json"))
	if e != nil || !found {
		return false, e
	}
	root := filepath.Dir(j.file.Name())
	suffix := string(filepath.Separator) + filepath.FromSlash(h.Source.JournalRelative)
	spool := strings.TrimSuffix(root, suffix)
	if spool == root {
		return false, errCandidateSeal
	}
	a, e := openSealAuthority(spool, h.Source)
	if e != nil {
		return false, e
	}
	defer a.close()
	if !sameAtFile(a.root, "events.jsonl", j.file) || !sameAtFile(a.root, "events.lock", j.lock) {
		return false, errCandidateSeal
	}
	_, e = OpenSealedCandidateResource(ctx, spool, h)
	return e == nil, e
}

func OpenSealedCandidateResource(ctx context.Context, spoolRoot string, header gitops.CandidateResourceHeader) (gitops.CandidateReceipt, error) {
	if err := header.Validate(); err != nil {
		return gitops.CandidateReceipt{}, errCandidateSeal
	}
	a, err := openSealAuthority(spoolRoot, header.Source)
	if err != nil {
		return gitops.CandidateReceipt{}, err
	}
	defer a.close()
	if !sameSealDirectories(header, a.spool, a.namespace, a.directory) {
		return gitops.CandidateReceipt{}, errCandidateSeal
	}
	j, err := openExistingJournalAt(a)
	if err != nil {
		return gitops.CandidateReceipt{}, err
	}
	defer j.close()
	var receipt gitops.CandidateReceipt
	err = j.withLock(syscall.LOCK_SH, func() error {
		if e := a.verify(); e != nil {
			return e
		}
		seal, found, e := readCandidateSealAt(a.root)
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
		return a.verify()
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

func sealDirectoryIdentity(path string) (gitops.DirectoryIdentity, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return gitops.DirectoryIdentity{}, errCandidateSeal
	}
	return sealDirectoryInfo(info)
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
func readCandidateSealAt(dir *os.File) (gitops.CandidateResourceHeader, bool, error) {
	f, err := openAtFile(dir, "seal.json", unix.O_RDONLY)
	if errors.Is(err, unix.ENOENT) {
		return gitops.CandidateResourceHeader{}, false, nil
	}
	if err != nil {
		return gitops.CandidateResourceHeader{}, false, errCandidateSeal
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !privateJournalFile(info) || info.Size() > 8192 {
		return gitops.CandidateResourceHeader{}, false, errCandidateSeal
	}
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
func writePrivateAt(dir *os.File, name string, body []byte) error {
	fd, e := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name))
	defer f.Close()
	for n := 0; n < len(body); {
		k, e := f.Write(body[n:])
		n += k
		if e != nil {
			return e
		}
		if k == 0 {
			return io.ErrShortWrite
		}
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if !sameAtFile(dir, name, f) {
		return errCandidateSeal
	}
	return dir.Sync()
}
func writeCandidateSealAt(dir *os.File, header gitops.CandidateResourceHeader) error {
	b, e := json.Marshal(header)
	if e != nil || len(b) > 8192 {
		return errCandidateSeal
	}
	return writePrivateAt(dir, "seal.json", b)
}

func sealReceipt(source gitops.CandidateResourceSource, rows []journalRecord) (gitops.CandidateReceipt, error) {
	var receipt gitops.CandidateReceipt
	count, completed := 0, 0
	var pending *gitops.GitProcessRecord
	for _, row := range rows {
		if count > 0 {
			return receipt, errCandidateSeal
		}
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

func openExistingJournalAt(a *sealAuthority) (*durableJournal, error) {
	file, e := openAtFile(a.root, "events.jsonl", unix.O_RDWR|unix.O_APPEND)
	if e != nil {
		return nil, e
	}
	if !privateJournalFileMust(file) {
		file.Close()
		return nil, errCandidateSeal
	}
	lock, e := openAtFile(a.root, "events.lock", unix.O_RDWR)
	if e != nil {
		file.Close()
		return nil, e
	}
	if !privateJournalFileMust(lock) {
		lock.Close()
		file.Close()
		return nil, errCandidateSeal
	}
	if err := verifySourceOrigin(a, file, lock); err != nil {
		_ = lock.Close()
		_ = file.Close()
		return nil, err
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

func openSourceJournal(root string, extra int) (j *durableJournal, err error) {
	a, err := openSealDirectories(filepath.Dir(filepath.Dir(root)), root)
	if err != nil {
		return nil, err
	}
	defer a.close()
	fd, e := unix.Openat(int(a.root.Fd()), "events.jsonl", unix.O_CREAT|unix.O_EXCL|unix.O_APPEND|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	created := e == nil
	if e == unix.EEXIST && extra&os.O_EXCL == 0 {
		fd, e = unix.Openat(int(a.root.Fd()), "events.jsonl", unix.O_APPEND|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	}
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), filepath.Join(root, "events.jsonl"))
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	if !privateJournalFileMust(f) {
		return nil, errCandidateSeal
	}
	lockFlags := unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if created {
		lockFlags |= unix.O_CREAT | unix.O_EXCL
	}
	lfd, e := unix.Openat(int(a.root.Fd()), "events.lock", lockFlags, 0600)
	if e != nil {
		return nil, e
	}
	l := os.NewFile(uintptr(lfd), filepath.Join(root, "events.lock"))
	defer func() {
		if err != nil {
			l.Close()
		}
	}()
	if !privateJournalFileMust(l) {
		return nil, errCandidateSeal
	}
	if created {
		if err = f.Sync(); err != nil {
			return
		}
		if err = l.Sync(); err != nil {
			return
		}
		if err = writeSourceOrigin(a, f, l); err != nil {
			return
		}
	}
	if err = a.verify(); err != nil {
		return
	}
	return &durableJournal{file: f, lock: l}, nil
}
