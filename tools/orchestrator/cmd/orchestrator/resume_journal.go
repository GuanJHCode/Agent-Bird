//go:build !windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const resumeJournalLimit = 16 * 1024

// This is a local replay reference, never an owner capability or receipt.
// The caller authenticates ControlFile before using the selected command.
type resumeJournalRequest struct {
	Version      int    `json:"version"`
	State        string `json:"state"`
	ControlFile  string `json:"control_file"`
	TaskID       string `json:"task_id"`
	WorkRevision int    `json:"work_revision"`
	CommandID    string `json:"command_id"`
}

type resumeJournalAmbiguityError struct{ CommandIDs []string }

func (e *resumeJournalAmbiguityError) Error() string {
	ids, _ := json.Marshal(e.CommandIDs)
	return "resume_journal_ambiguous_pending: " + string(ids)
}

func validResumeJournalCommandID(id string) bool {
	return id != "" && len(id) <= 128 && utf8.ValidString(id) && !strings.ContainsRune(id, 0) && strings.TrimSpace(id) == id
}

func validResumeJournalRequest(r resumeJournalRequest) bool {
	return r.Version == 1 && (r.State == "pending" || r.State == "completed") && filepath.IsAbs(r.ControlFile) && filepath.Clean(r.ControlFile) == r.ControlFile && utf8.ValidString(r.ControlFile) && !strings.ContainsRune(r.ControlFile, 0) && r.TaskID != "" && utf8.ValidString(r.TaskID) && r.WorkRevision > 0 && validResumeJournalCommandID(r.CommandID)
}

func sameResumeJournalTuple(a, b resumeJournalRequest) bool {
	return a.Version == b.Version && a.ControlFile == b.ControlFile && a.TaskID == b.TaskID && a.WorkRevision == b.WorkRevision && a.CommandID == b.CommandID
}

func resumeJournalName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]) + ".json"
}

func selectResumeJournalRequest(state, controlFile, taskID string, revision int, commandID string) (resumeJournalRequest, error) {
	return selectResumeJournalRequestWithSteps(state, controlFile, taskID, revision, commandID, nil)
}

// A private callback exposes durable I/O milestones to controlled crash tests.
// Runtime callers use the wrapper, which has no fault-injection input.
func selectResumeJournalRequestWithSteps(state, controlFile, taskID string, revision int, commandID string, afterStep func(string)) (resumeJournalRequest, error) {
	var selected resumeJournalRequest
	if taskID == "" || !utf8.ValidString(taskID) || revision < 1 || commandID != "" && !validResumeJournalCommandID(commandID) {
		return selected, codeError("resume_journal_request_invalid")
	}
	absolute, err := filepath.Abs(controlFile)
	if err != nil || controlFile == "" {
		return selected, codeError("resume_journal_request_invalid")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return selected, err
	}
	wanted := resumeJournalRequest{Version: 1, State: "pending", ControlFile: canonical, TaskID: taskID, WorkRevision: revision, CommandID: commandID}
	err = withResumeJournalFiles(state, true, func(f *resumeJournalFiles) error {
		f.afterStep = afterStep
		records, err := f.scan()
		if err != nil {
			return err
		}
		if commandID != "" {
			if existing, ok := records[commandID]; ok {
				if !sameResumeJournalTuple(existing, wanted) {
					return codeError("resume_journal_request_conflict")
				}
				selected = existing
				return nil
			}
		} else {
			var pending []resumeJournalRequest
			for _, record := range records {
				if record.State == "pending" && record.ControlFile == wanted.ControlFile && record.TaskID == wanted.TaskID && record.WorkRevision == wanted.WorkRevision {
					pending = append(pending, record)
				}
			}
			if len(pending) > 1 {
				ids := make([]string, 0, len(pending))
				for _, r := range pending {
					ids = append(ids, r.CommandID)
				}
				sort.Strings(ids)
				return &resumeJournalAmbiguityError{CommandIDs: ids}
			}
			if len(pending) == 1 {
				selected = pending[0]
				return nil
			}
			wanted.CommandID, err = randomValue("resume")
			if err != nil {
				return err
			}
		}
		if err := f.create(wanted); err != nil {
			return err
		}
		selected = wanted
		return nil
	})
	if err != nil {
		return resumeJournalRequest{}, err
	}
	return selected, nil
}

func completeResumeJournalRequest(state string, request resumeJournalRequest) error {
	return completeResumeJournalRequestWithSteps(state, request, nil)
}

func completeResumeJournalRequestWithSteps(state string, request resumeJournalRequest, afterStep func(string)) error {
	if !validResumeJournalRequest(request) {
		return codeError("resume_journal_request_invalid")
	}
	return withResumeJournalFiles(state, false, func(f *resumeJournalFiles) error {
		f.afterStep = afterStep
		records, err := f.scan()
		if err != nil {
			return err
		}
		existing, ok := records[request.CommandID]
		if !ok {
			return codeError("resume_journal_request_missing")
		}
		if !sameResumeJournalTuple(existing, request) {
			return codeError("resume_journal_request_conflict")
		}
		if existing.State == "completed" {
			return nil
		}
		existing.State = "completed"
		return f.complete(existing)
	})
}

type resumeJournalFiles struct {
	statePath              string
	state, directory, lock *os.File
	snapshots              map[string]unix.Stat_t
	afterStep              func(string)
}

func resumeJournalDirectoryTrusted(s unix.Stat_t) bool {
	return s.Mode&unix.S_IFMT == unix.S_IFDIR && s.Mode&07777 == 0700 && s.Uid == uint32(os.Geteuid())
}
func resumeJournalFileTrusted(s unix.Stat_t) bool {
	return s.Mode&unix.S_IFMT == unix.S_IFREG && s.Mode&07777 == 0600 && s.Uid == uint32(os.Geteuid()) && s.Nlink == 1
}
func sameResumeJournalInode(a, b unix.Stat_t) bool { return a.Dev == b.Dev && a.Ino == b.Ino }
func sameResumeJournalStat(a, b unix.Stat_t) bool {
	return sameResumeJournalInode(a, b) && a.Mode == b.Mode && a.Uid == b.Uid && a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func (f *resumeJournalFiles) close() {
	if f.lock != nil {
		_ = unix.Flock(int(f.lock.Fd()), unix.LOCK_UN)
		_ = f.lock.Close()
	}
	if f.directory != nil {
		_ = f.directory.Close()
	}
	if f.state != nil {
		_ = f.state.Close()
	}
}

func (f *resumeJournalFiles) validate() error {
	bad := codeError("resume_journal_changed")
	resolved, err := filepath.EvalSymlinks(f.statePath)
	if err != nil || resolved != f.statePath {
		return bad
	}
	var opened, named unix.Stat_t
	if unix.Fstat(int(f.state.Fd()), &opened) != nil || unix.Lstat(f.statePath, &named) != nil || !resumeJournalDirectoryTrusted(opened) || !resumeJournalDirectoryTrusted(named) || !sameResumeJournalInode(opened, named) {
		return bad
	}
	if f.directory != nil {
		if unix.Fstat(int(f.directory.Fd()), &opened) != nil || unix.Fstatat(int(f.state.Fd()), "resume-requests", &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !resumeJournalDirectoryTrusted(opened) || !resumeJournalDirectoryTrusted(named) || !sameResumeJournalInode(opened, named) {
			return bad
		}
	}
	if f.lock != nil {
		if unix.Fstat(int(f.lock.Fd()), &opened) != nil || unix.Fstatat(int(f.directory.Fd()), ".lock", &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !resumeJournalFileTrusted(opened) || !resumeJournalFileTrusted(named) || opened.Size != 0 || named.Size != 0 || !sameResumeJournalInode(opened, named) {
			return bad
		}
	}
	return nil
}

// The lock protects only local journal I/O; it is released before any RPC.
func withResumeJournalFiles(state string, createDirectory bool, action func(*resumeJournalFiles) error) error {
	absolute, err := filepath.Abs(state)
	if err != nil || state == "" {
		return codeError("resume_journal_state_invalid")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return err
	}
	f := &resumeJournalFiles{statePath: canonical, snapshots: map[string]unix.Stat_t{}}
	defer f.close()
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open(canonical, flags, 0)
	if err != nil {
		return err
	}
	f.state = os.NewFile(uintptr(fd), canonical)
	if err := f.validate(); err != nil {
		return err
	}
	if createDirectory {
		if err := unix.Mkdirat(fd, "resume-requests", 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		if err := f.state.Sync(); err != nil {
			return err
		}
	}
	directory, err := unix.Openat(fd, "resume-requests", flags, 0)
	if err != nil {
		return err
	}
	f.directory = os.NewFile(uintptr(directory), "resume-requests")
	if err := f.validate(); err != nil {
		return err
	}
	lock, err := unix.Openat(directory, ".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f.lock = os.NewFile(uintptr(lock), ".lock")
	if err := f.validate(); err != nil {
		return err
	}
	if err := unix.Flock(lock, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return codeError("resume_journal_busy")
		}
		return err
	}
	if err := f.validate(); err != nil {
		return err
	}
	if err := action(f); err != nil {
		return err
	}
	return f.validateSnapshots()
}

func (f *resumeJournalFiles) names() ([]os.DirEntry, error) {
	// Opening "." gives an independent directory cursor anchored to the held FD.
	fd, err := unix.Openat(int(f.directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	listing := os.NewFile(uintptr(fd), "resume-requests")
	defer listing.Close()
	return listing.ReadDir(-1)
}

func (f *resumeJournalFiles) validateSnapshots() error {
	if err := f.validate(); err != nil {
		return err
	}
	names, err := f.names()
	if err != nil {
		return err
	}
	for _, entry := range names {
		name := entry.Name()
		if name == ".lock" {
			continue
		}
		before, ok := f.snapshots[name]
		var named unix.Stat_t
		if !ok || unix.Fstatat(int(f.directory.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !resumeJournalFileTrusted(named) || !sameResumeJournalStat(before, named) {
			return codeError("resume_journal_changed")
		}
	}
	for name, before := range f.snapshots {
		var named unix.Stat_t
		if unix.Fstatat(int(f.directory.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !resumeJournalFileTrusted(named) || !sameResumeJournalStat(before, named) {
			return codeError("resume_journal_changed")
		}
	}
	return f.validate()
}

func (f *resumeJournalFiles) read(name string) (resumeJournalRequest, unix.Stat_t, error) {
	var r resumeJournalRequest
	var before, after, named unix.Stat_t
	bad := codeError("resume_journal_record_invalid")
	if err := f.validate(); err != nil {
		return r, before, err
	}
	fd, err := unix.Openat(int(f.directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return r, before, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	if unix.Fstat(fd, &before) != nil || !resumeJournalFileTrusted(before) || before.Size < 1 || before.Size > resumeJournalLimit {
		return r, before, bad
	}
	raw, err := io.ReadAll(io.LimitReader(file, resumeJournalLimit+1))
	if err != nil {
		return r, before, err
	}
	if unix.Fstat(fd, &after) != nil || unix.Fstatat(int(f.directory.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !resumeJournalFileTrusted(after) || !resumeJournalFileTrusted(named) || !sameResumeJournalStat(before, after) || !sameResumeJournalStat(after, named) || int64(len(raw)) != before.Size {
		return r, before, codeError("resume_journal_changed")
	}
	if !utf8.Valid(raw) || decode(raw, &r) != nil || !validResumeJournalRequest(r) || name != resumeJournalName(r.CommandID) {
		return r, before, bad
	}
	canonical, err := json.Marshal(r)
	if err != nil || !bytes.Equal(raw, append(canonical, '\n')) {
		return r, before, bad
	}
	// Reused requests must also be durable before the caller reaches its RPC.
	if err := file.Sync(); err != nil {
		return r, before, err
	}
	return r, before, f.validate()
}

func (f *resumeJournalFiles) scan() (map[string]resumeJournalRequest, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	names, err := f.names()
	if err != nil {
		return nil, err
	}
	records := map[string]resumeJournalRequest{}
	for _, entry := range names {
		name := entry.Name()
		if name == ".lock" {
			continue
		}
		if len(name) != 69 || !strings.HasSuffix(name, ".json") {
			return nil, codeError("resume_journal_record_invalid")
		}
		r, stat, err := f.read(name)
		if err != nil {
			return nil, err
		}
		records[r.CommandID] = r
		f.snapshots[name] = stat
	}
	if err := f.directory.Sync(); err != nil {
		return nil, err
	}
	return records, f.validateSnapshots()
}

func resumeJournalBody(r resumeJournalRequest) ([]byte, error) {
	if !validResumeJournalRequest(r) {
		return nil, codeError("resume_journal_request_invalid")
	}
	body, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	if len(body) > resumeJournalLimit {
		return nil, codeError("resume_journal_capacity")
	}
	return body, nil
}

// Temps stay outside the records scanning domain. An interrupted write cannot
// publish corrupt history or make an implicit request depend on orphan cleanup.
func (f *resumeJournalFiles) writeTemp(prefix string, body []byte) (string, unix.Stat_t, error) {
	var written, named unix.Stat_t
	name, err := randomValue(prefix)
	if err != nil {
		return "", written, err
	}
	name = "." + name
	fd, err := unix.Openat(int(f.state.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return "", written, err
	}
	file := os.NewFile(uintptr(fd), name)
	if err = unix.Fstat(fd, &written); err == nil && !resumeJournalFileTrusted(written) {
		err = codeError("resume_journal_changed")
	}
	if err == nil && f.afterStep != nil {
		f.afterStep("temp_created")
	}
	if err == nil {
		var n int
		n, err = file.Write(body)
		if err == nil && n != len(body) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = file.Sync()
	}
	if err == nil && f.afterStep != nil {
		f.afterStep("temp_synced")
	}
	if statErr := unix.Fstat(fd, &written); err == nil {
		err = statErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return name, written, err
	}
	if unix.Fstatat(int(f.state.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !resumeJournalFileTrusted(named) || !sameResumeJournalStat(written, named) {
		return name, written, codeError("resume_journal_changed")
	}
	return name, written, nil
}

// Cleanup is limited to this call's exclusive temp inode, while both named
// private directories still match their held descriptors. Crash or uncertain
// ownership leaves state temps for a separately authorized future GC.
func (f *resumeJournalFiles) cleanupTemp(name string, stat unix.Stat_t) {
	if name == "" || f.validate() != nil {
		return
	}
	var named unix.Stat_t
	if unix.Fstatat(int(f.state.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) == nil && resumeJournalFileTrusted(named) && sameResumeJournalStat(stat, named) {
		if unix.Unlinkat(int(f.state.Fd()), name, 0) == nil {
			_ = f.state.Sync()
		}
	}
}

func (f *resumeJournalFiles) validateTemp(name string, stat unix.Stat_t) error {
	if err := f.validateSnapshots(); err != nil {
		return err
	}
	var named unix.Stat_t
	if unix.Fstatat(int(f.state.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !resumeJournalFileTrusted(named) || !sameResumeJournalStat(stat, named) {
		return codeError("resume_journal_changed")
	}
	return nil
}

func (f *resumeJournalFiles) finishPublication(name string, r resumeJournalRequest, stat unix.Stat_t) error {
	current, committed, err := f.read(name)
	if err != nil || current != r || !sameResumeJournalInode(stat, committed) {
		return codeError("resume_journal_changed")
	}
	f.snapshots[name] = committed
	// Source removal and destination publication are durable only after both
	// directories are synced; no copy or hardlink fallback is permitted.
	if err := f.state.Sync(); err != nil {
		return err
	}
	if err := f.directory.Sync(); err != nil {
		return err
	}
	return f.validateSnapshots()
}

func (f *resumeJournalFiles) create(r resumeJournalRequest) error {
	body, err := resumeJournalBody(r)
	if err != nil {
		return err
	}
	if err := f.validateSnapshots(); err != nil {
		return err
	}
	temp, stat, err := f.writeTemp("resume-journal-pending", body)
	published := false
	defer func() {
		if !published {
			f.cleanupTemp(temp, stat)
		}
	}()
	if err != nil {
		return err
	}
	if err := f.validateTemp(temp, stat); err != nil {
		return err
	}
	name := resumeJournalName(r.CommandID)
	if err := resumeJournalPublishNoReplace(int(f.state.Fd()), temp, int(f.directory.Fd()), name); err != nil {
		return err
	}
	published = true // Never delete the request after a successful atomic publish.
	if f.afterStep != nil {
		f.afterStep("record_published")
	}
	return f.finishPublication(name, r, stat)
}

func (f *resumeJournalFiles) complete(r resumeJournalRequest) error {
	body, err := resumeJournalBody(r)
	if err != nil {
		return err
	}
	if err := f.validateSnapshots(); err != nil {
		return err
	}
	temp, stat, err := f.writeTemp("resume-journal-complete", body)
	renamed := false
	defer func() {
		if !renamed {
			f.cleanupTemp(temp, stat)
		}
	}()
	if err != nil {
		return err
	}
	if err := f.validateTemp(temp, stat); err != nil {
		return err
	}
	name := resumeJournalName(r.CommandID)
	if err := unix.Renameat(int(f.state.Fd()), temp, int(f.directory.Fd()), name); err != nil {
		return err
	}
	renamed = true
	if f.afterStep != nil {
		f.afterStep("record_renamed")
	}
	return f.finishPublication(name, r, stat)
}
