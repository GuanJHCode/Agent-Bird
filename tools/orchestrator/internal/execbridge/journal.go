package execbridge

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// The journal is a Host-only sibling of scratch, never a worker output. These
// receipts prove auxiliary process exit only; parent exit, transport drain and
// input/business verification remain mandatory Host gates.
type executorJournal struct {
	path                string
	root                *os.Root
	identity            os.FileInfo
	intent, spawnedHash string
}

type executorIntent struct {
	Version          int    `json:"version"`
	LaunchID         string `json:"launch_id"`
	Scope            string `json:"scope_sha256"`
	Executable       string `json:"executable"`
	ExecutableSHA256 string `json:"executable_sha256"`
	ArgumentsSHA256  string `json:"arguments_sha256"`
}
type executorSpawned struct {
	Intent      string           `json:"intent_sha256"`
	KernelStart string           `json:"kernel_start"`
	Process     process.Identity `json:"process"`
}
type executorExited struct {
	Intent   string `json:"intent_sha256"`
	Spawned  string `json:"spawned_sha256"`
	ExitCode int    `json:"exit_code"`
}

func journalHash(raw []byte) string { value := sha256.Sum256(raw); return hex.EncodeToString(value[:]) }
func journalOwned(info os.FileInfo, directory bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return false
	}
	if directory {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && stat.Nlink == 1
}

func openExecutorJournal(path string) (*executorJournal, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || canonical != path || !journalOwned(info, true) {
		return nil, process.ErrProcessTreeUnknown
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, process.ErrProcessTreeUnknown
	}
	j := &executorJournal{path: path, root: root, identity: info}
	if err := j.check(); err != nil {
		root.Close()
		return nil, err
	}
	return j, nil
}

func newExecutorJournal(path string, spec process.Command) (*executorJournal, error) {
	parent := filepath.Dir(path)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, process.ErrProcessTreeUnknown
	}
	info, err := os.Lstat(parent)
	stat, ok := infoSys(info)
	if err != nil || !ok || !info.IsDir() || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0022 != 0 {
		return nil, process.ErrProcessTreeUnknown
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, process.ErrProcessTreeUnknown
	}
	defer root.Close()
	if opened, err := root.Stat("."); err != nil || !os.SameFile(info, opened) {
		return nil, process.ErrProcessTreeUnknown
	}
	// Keep the requirement outside the receipt directory: deleting the latter
	// must not turn an uncertain launch back into an unspawned one.
	marker, err := root.OpenFile(filepath.Base(path)+".required", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, process.ErrProcessTreeUnknown
	}
	_, markerErr := marker.Write([]byte("executor-exit-v1\n"))
	if errors.Join(markerErr, marker.Sync(), marker.Close(), syncJournalDirectory(root)) != nil {
		return nil, process.ErrProcessTreeUnknown
	}
	if err := root.Mkdir(filepath.Base(path), 0700); err != nil {
		return nil, process.ErrProcessTreeUnknown
	}
	if err := syncJournalDirectory(root); err != nil {
		return nil, process.ErrProcessTreeUnknown
	}
	j, err := openExecutorJournal(path)
	if err != nil {
		return nil, process.ErrProcessTreeUnknown
	}
	intent := executorIntent{Version: 1, LaunchID: rand.Text(), Scope: journalHash([]byte(parent)), Executable: spec.PinnedPath, ExecutableSHA256: spec.PinnedSHA256, ArgumentsSHA256: journalHash([]byte(strings.Join(spec.Args, "\x00")))}
	j.intent, err = j.write("intent.json", intent)
	if err != nil {
		j.close()
		return nil, process.ErrProcessTreeUnknown
	}
	return j, nil
}

func infoSys(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}
func (j *executorJournal) close() error { return j.root.Close() }
func (j *executorJournal) check() error {
	current, err := os.Lstat(j.path)
	if err != nil || !journalOwned(current, true) || !os.SameFile(j.identity, current) {
		return process.ErrProcessTreeUnknown
	}
	canonical, err := filepath.EvalSymlinks(j.path)
	if err != nil || canonical != j.path {
		return process.ErrProcessTreeUnknown
	}
	opened, err := j.root.Stat(".")
	if err != nil || !os.SameFile(current, opened) {
		return process.ErrProcessTreeUnknown
	}
	return nil
}
func syncJournalDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func (j *executorJournal) write(name string, value any) (string, error) {
	if err := j.check(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 16384 {
		return "", process.ErrProcessTreeUnknown
	}
	f, err := j.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", process.ErrProcessTreeUnknown
	}
	_, writeErr := f.Write(raw)
	err = errors.Join(writeErr, f.Sync(), f.Close(), syncJournalDirectory(j.root), j.check())
	if err != nil {
		return "", process.ErrProcessTreeUnknown
	}
	return journalHash(raw), nil
}
func (j *executorJournal) read(name string, target any) (string, error) {
	if err := j.check(); err != nil {
		return "", err
	}
	f, err := j.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", process.ErrProcessTreeUnknown
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !journalOwned(info, false) || info.Size() > 16384 {
		return "", process.ErrProcessTreeUnknown
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || int64(len(raw)) != info.Size() || !journalOwned(after, false) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return "", process.ErrProcessTreeUnknown
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF || j.check() != nil {
		return "", process.ErrProcessTreeUnknown
	}
	return journalHash(raw), nil
}
func (j *executorJournal) spawned(identity process.Identity) error {
	if identity.PID < 1 || identity.PGID != identity.PID || !identity.BirthKnown || identity.Birth == "" {
		return process.ErrProcessTreeUnknown
	}
	var intent executorIntent
	hash, err := j.read("intent.json", &intent)
	if err != nil || hash != j.intent {
		return process.ErrProcessTreeUnknown
	}
	kernel, err := process.KernelStartID(identity.PID)
	if err != nil || kernel == "" {
		return process.ErrProcessTreeUnknown
	}
	j.spawnedHash, err = j.write("spawned.json", executorSpawned{Intent: j.intent, KernelStart: kernel, Process: identity})
	return err
}
func (j *executorJournal) exited(handle *process.Handle) error {
	if handle == nil || handle.ExitCode() != 0 || handle.ConfirmTreeExited() != nil {
		return process.ErrProcessTreeUnknown
	}
	var spawned executorSpawned
	hash, err := j.read("spawned.json", &spawned)
	if err != nil || hash != j.spawnedHash || spawned.Process != handle.Identity() {
		return process.ErrProcessTreeUnknown
	}
	_, err = j.write("exited.json", executorExited{Intent: j.intent, Spawned: j.spawnedHash, ExitCode: 0})
	if err != nil {
		return err
	}
	return VerifyExecutorExit(j.path)
}

// ErrNotExist means no journal directory. Every incomplete or untrusted
// directory is UNKNOWN. An exit receipt is never a business success receipt.
func VerifyExecutorExit(path string) error {
	j, err := openExecutorJournal(path)
	if os.IsNotExist(err) {
		if _, markerErr := os.Lstat(path + ".required"); !os.IsNotExist(markerErr) {
			return process.ErrProcessTreeUnknown
		}
		return err
	}
	if err != nil {
		return process.ErrProcessTreeUnknown
	}
	defer j.close()
	marker, markerErr := os.Lstat(path + ".required")
	if markerErr != nil || !journalOwned(marker, false) {
		return process.ErrProcessTreeUnknown
	}
	var intent executorIntent
	intentHash, err := j.read("intent.json", &intent)
	if err != nil || intent.Version != 1 || len(intent.LaunchID) < 20 || intent.Scope != journalHash([]byte(filepath.Dir(path))) {
		return process.ErrProcessTreeUnknown
	}
	var spawned executorSpawned
	spawnedHash, err := j.read("spawned.json", &spawned)
	if err != nil || spawned.Intent != intentHash || spawned.KernelStart == "" || spawned.Process.PID < 1 || spawned.Process.PGID != spawned.Process.PID || !spawned.Process.BirthKnown || spawned.Process.Birth == "" {
		return process.ErrProcessTreeUnknown
	}
	var exited executorExited
	_, err = j.read("exited.json", &exited)
	if err != nil || exited.Intent != intentHash || exited.Spawned != spawnedHash || exited.ExitCode != 0 {
		return process.ErrProcessTreeUnknown
	}
	return nil
}
