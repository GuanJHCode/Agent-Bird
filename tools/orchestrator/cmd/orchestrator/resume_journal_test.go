//go:build !windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func resumeJournalFixture(t *testing.T) (string, string) {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	state, err := filepath.EvalSymlinks(state)
	if err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(state, "control.json")
	if err := os.WriteFile(control, []byte(`{"token":"PRIVATE_TOKEN_SENTINEL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return state, control
}

func resumeJournalRecordPathForTest(state, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(state, "resume-requests", hex.EncodeToString(sum[:])+".json")
}

func resumeJournalSelectForTest(t *testing.T, state, control, task, id string) resumeJournalRequest {
	t.Helper()
	r, err := selectResumeJournalRequest(state, control, task, 1, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResumeJournalLifecycle(t *testing.T) {
	state, control := resumeJournalFixture(t)
	first := resumeJournalSelectForTest(t, state, control, "task", "")
	if first.Version != 1 || first.State != "pending" || first.ControlFile != control || first.TaskID != "task" || first.WorkRevision != 1 || first.CommandID == "" {
		t.Fatal("wrong pending request", first)
	}
	body, err := os.ReadFile(resumeJournalRecordPathForTest(state, first.CommandID))
	if err != nil {
		t.Fatal("request was not persisted", err)
	}
	if bytes.Contains(body, []byte("PRIVATE_TOKEN_SENTINEL")) || !bytes.HasSuffix(body, []byte("\n")) {
		t.Fatal("journal retained secret or lacked canonical newline", string(body))
	}
	var onDisk resumeJournalRequest
	if err := json.Unmarshal(body, &onDisk); err != nil || onDisk != first {
		t.Fatal("wrong persisted pending", onDisk, err)
	}
	if replay := resumeJournalSelectForTest(t, state, control, "task", ""); replay != first {
		t.Fatal("implicit lost original pending", replay, first)
	}
	if err := completeResumeJournalRequest(state, first); err != nil {
		t.Fatal(err)
	}
	explicit := resumeJournalSelectForTest(t, state, control, "task", first.CommandID)
	if explicit.State != "completed" || explicit.CommandID != first.CommandID {
		t.Fatal("explicit completed replay changed request", explicit)
	}
	if err := completeResumeJournalRequest(state, explicit); err != nil {
		t.Fatal("completion was not idempotent", err)
	}
	next := resumeJournalSelectForTest(t, state, control, "task", "")
	if next.CommandID == first.CommandID || next.State != "pending" {
		t.Fatal("implicit reused completed request", next)
	}
	for _, path := range []string{state, filepath.Join(state, "resume-requests")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("private directory contract", path, info, err)
		}
	}
	for _, name := range []string{".lock", filepath.Base(resumeJournalRecordPathForTest(state, first.CommandID)), filepath.Base(resumeJournalRecordPathForTest(state, next.CommandID))} {
		info, err := os.Lstat(filepath.Join(state, "resume-requests", name))
		if err != nil || info.Mode().Perm() != 0600 || !info.Mode().IsRegular() {
			t.Fatal("private file contract", name, info, err)
		}
	}
}

func TestResumeJournalExplicitConflictAndAmbiguity(t *testing.T) {
	state, control := resumeJournalFixture(t)
	first := resumeJournalSelectForTest(t, state, control, "task", "explicit/编号:1")
	if replay := resumeJournalSelectForTest(t, state, control, "task", first.CommandID); replay != first {
		t.Fatal("explicit replay drift", replay)
	}
	if _, err := selectResumeJournalRequest(state, control, "other", 1, first.CommandID); err == nil {
		t.Fatal("cross-task ID reused")
	}
	if _, err := selectResumeJournalRequest(state, control, "task", 2, first.CommandID); err == nil {
		t.Fatal("cross-revision ID reused")
	}
	other := filepath.Join(state, "other-control.json")
	if err := os.WriteFile(other, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := selectResumeJournalRequest(state, other, "task", 1, first.CommandID); err == nil {
		t.Fatal("cross-control ID reused")
	}
	second := resumeJournalSelectForTest(t, state, control, "task", "second")
	_, err := selectResumeJournalRequest(state, control, "task", 1, "")
	var ambiguity *resumeJournalAmbiguityError
	if !errors.As(err, &ambiguity) {
		t.Fatal("multiple pending lost typed ambiguity", err)
	}
	want := []string{first.CommandID, second.CommandID}
	sort.Strings(want)
	if !reflect.DeepEqual(ambiguity.CommandIDs, want) {
		t.Fatal("ambiguity lost candidate IDs", ambiguity)
	}
	if replay := resumeJournalSelectForTest(t, state, control, "task", first.CommandID); replay != first {
		t.Fatal("ambiguity blocked explicit replay", replay)
	}
}

func TestResumeJournalCanonicalControlAndHistoricalPath(t *testing.T) {
	state, control := resumeJournalFixture(t)
	alias := filepath.Join(state, "alias.json")
	if err := os.Symlink(control, alias); err != nil {
		t.Fatal(err)
	}
	first := resumeJournalSelectForTest(t, state, alias, "task", "")
	if first.ControlFile != control {
		t.Fatal("control path was not canonical", first)
	}
	if replay := resumeJournalSelectForTest(t, state, control, "task", ""); replay != first {
		t.Fatal("alias split tuple", replay)
	}
	if err := completeResumeJournalRequest(state, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(control); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(state, "fresh.json")
	if err := os.WriteFile(fresh, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := resumeJournalSelectForTest(t, state, fresh, "task", ""); r.ControlFile != fresh {
		t.Fatal("removed historical cap blocked new healthy request", r)
	}
}

func TestResumeJournalCrashKeepsImplicitRequest(t *testing.T) {
	if state := os.Getenv("RESUME_JOURNAL_CRASH_STATE"); state != "" {
		if _, err := selectResumeJournalRequest(state, os.Getenv("RESUME_JOURNAL_CRASH_CONTROL"), "task", 1, ""); err != nil {
			t.Fatal(err)
		}
		os.Exit(23)
	}
	state, control := resumeJournalFixture(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestResumeJournalCrashKeepsImplicitRequest$")
	cmd.Env = append(os.Environ(), "RESUME_JOURNAL_CRASH_STATE="+state, "RESUME_JOURNAL_CRASH_CONTROL="+control)
	output, err := cmd.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 23 || len(output) != 0 {
		t.Fatal("controlled abrupt exit did not stay silent", string(output), err)
	}
	entries, err := os.ReadDir(filepath.Join(state, "resume-requests"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted resumeJournalRequest
	count := 0
	for _, entry := range entries {
		if entry.Name() == ".lock" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(state, "resume-requests", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &persisted); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 1 || persisted.State != "pending" {
		t.Fatal("crash lost durable pending", count, persisted)
	}
	if replay := resumeJournalSelectForTest(t, state, control, "task", ""); replay != persisted {
		t.Fatal("silent crash could not recover original request", replay, persisted)
	}
}

func TestResumeJournalCommandIDBounds(t *testing.T) {
	for _, id := range []string{"../path:编号", strings.Repeat("x", 128), "a\tb", "a\nb"} {
		t.Run("allowed_"+fmt.Sprint(len(id)), func(t *testing.T) {
			state, control := resumeJournalFixture(t)
			r := resumeJournalSelectForTest(t, state, control, "task", id)
			if r.CommandID != id {
				t.Fatal("public ID narrowed", r)
			}
			if _, err := os.Stat(resumeJournalRecordPathForTest(state, id)); err != nil {
				t.Fatal("ID was not hashed safely", err)
			}
		})
	}
	for _, id := range []string{strings.Repeat("x", 129), " leading", "trailing ", "a\x00b", string([]byte{0xff})} {
		t.Run("rejected_"+fmt.Sprintf("%x", id), func(t *testing.T) {
			state, control := resumeJournalFixture(t)
			if _, err := selectResumeJournalRequest(state, control, "task", 1, id); err == nil {
				t.Fatal("invalid ID accepted")
			}
			if _, err := os.Stat(filepath.Join(state, "resume-requests")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid input mutated state", err)
			}
		})
	}
}

func TestResumeJournalCompleteMissingAndConflict(t *testing.T) {
	state, control := resumeJournalFixture(t)
	missing := resumeJournalRequest{Version: 1, State: "pending", ControlFile: control, TaskID: "task", WorkRevision: 1, CommandID: "missing"}
	if err := completeResumeJournalRequest(state, missing); err == nil {
		t.Fatal("completion created missing journal")
	}
	if _, err := os.Stat(filepath.Join(state, "resume-requests")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("complete created child directory", err)
	}
	first := resumeJournalSelectForTest(t, state, control, "task", "")
	wrong := first
	wrong.TaskID = "other"
	if err := completeResumeJournalRequest(state, wrong); err == nil {
		t.Fatal("complete changed tuple")
	}
	if replay := resumeJournalSelectForTest(t, state, control, "task", first.CommandID); replay.State != "pending" {
		t.Fatal("conflicting completion mutated record", replay)
	}
	if err := completeResumeJournalRequest(state, missing); err == nil {
		t.Fatal("completion created missing record in healthy directory")
	}
}

func TestResumeJournalBusyAndConcurrentImplicit(t *testing.T) {
	state, control := resumeJournalFixture(t)
	first := resumeJournalSelectForTest(t, state, control, "task", "")
	lock, err := os.OpenFile(filepath.Join(state, "resume-requests", ".lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := selectResumeJournalRequest(state, control, "task", 1, ""); err == nil || err.Error() != "resume_journal_busy" {
		t.Fatal("busy was not explicit", err)
	}
	if err := completeResumeJournalRequest(state, first); err == nil || err.Error() != "resume_journal_busy" {
		t.Fatal("completion bypassed busy lock", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := completeResumeJournalRequest(state, first); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	requests := make(chan resumeJournalRequest, 12)
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 1000; attempt++ {
				r, err := selectResumeJournalRequest(state, control, "task", 1, "")
				if err == nil {
					requests <- r
					return
				}
				if err.Error() != "resume_journal_busy" {
					failures <- err
					return
				}
				time.Sleep(time.Millisecond)
			}
			failures <- errors.New("concurrent retry exhausted")
		}()
	}
	wg.Wait()
	close(requests)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	count := 0
	var pending resumeJournalRequest
	for r := range requests {
		count++
		if pending.CommandID == "" {
			pending = r
		}
		if r != pending || r.CommandID == first.CommandID || r.State != "pending" {
			t.Error("concurrent first selection split pending or reused completed", r)
		}
	}
	if count != 12 {
		t.Fatal("concurrent requests incomplete", count)
	}
	entries, err := os.ReadDir(filepath.Join(state, "resume-requests"))
	if err != nil || len(entries) != 3 {
		t.Fatal("concurrent implicit created duplicates", entries, err)
	}
}

func TestResumeJournalRejectsUntrustedFilesystem(t *testing.T) {
	cases := []string{"state_mode", "directory_mode", "directory_symlink", "lock_symlink", "lock_hardlink", "lock_mode", "lock_content", "lock_fifo", "record_symlink", "record_hardlink", "record_mode", "record_directory", "record_fifo", "record_oversize", "record_corrupt", "record_unknown_field", "record_duplicate_field", "record_case_field", "record_noncanonical", "record_missing_newline", "record_unknown_version", "record_unknown_state", "record_wrong_filename", "unknown_file", "orphan_temp"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			state, control := resumeJournalFixture(t)
			first := resumeJournalSelectForTest(t, state, control, "task", "")
			dir := filepath.Join(state, "resume-requests")
			path := resumeJournalRecordPathForTest(state, first.CommandID)
			lock := filepath.Join(dir, ".lock")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "state_mode":
				must(os.Chmod(state, 0755))
			case "directory_mode":
				must(os.Chmod(dir, 0755))
			case "directory_symlink":
				must(os.Rename(dir, dir+".old"))
				must(os.Symlink(dir+".old", dir))
			case "lock_symlink":
				must(os.Rename(lock, filepath.Join(state, "old-lock")))
				must(os.Symlink(filepath.Join(state, "old-lock"), lock))
			case "lock_hardlink":
				must(os.Link(lock, filepath.Join(state, "lock.link")))
			case "lock_mode":
				must(os.Chmod(lock, 0644))
			case "lock_content":
				must(os.WriteFile(lock, []byte("bad"), 0600))
			case "lock_fifo":
				must(os.Remove(lock))
				must(unix.Mkfifo(lock, 0600))
			case "record_symlink":
				must(os.Rename(path, filepath.Join(state, "old-record")))
				must(os.Symlink(filepath.Join(state, "old-record"), path))
			case "record_hardlink":
				must(os.Link(path, filepath.Join(state, "record.link")))
			case "record_mode":
				must(os.Chmod(path, 0644))
			case "record_directory":
				must(os.Remove(path))
				must(os.Mkdir(path, 0700))
			case "record_fifo":
				must(os.Remove(path))
				must(unix.Mkfifo(path, 0600))
			case "record_oversize":
				must(os.WriteFile(path, bytes.Repeat([]byte("x"), 16*1024+1), 0600))
			case "record_corrupt":
				must(os.WriteFile(path, []byte("{"), 0600))
			case "record_unknown_field":
				must(os.WriteFile(path, bytes.Replace(body, []byte("}\n"), []byte(",\"token\":\"forged\"}\n"), 1), 0600))
			case "record_duplicate_field":
				must(os.WriteFile(path, bytes.Replace(body, []byte("}\n"), []byte(",\"version\":1}\n"), 1), 0600))
			case "record_case_field":
				must(os.WriteFile(path, bytes.Replace(body, []byte(`"version"`), []byte(`"Version"`), 1), 0600))
			case "record_noncanonical":
				must(os.WriteFile(path, append([]byte(" "), body...), 0600))
			case "record_missing_newline":
				must(os.WriteFile(path, bytes.TrimSuffix(body, []byte("\n")), 0600))
			case "record_unknown_version":
				must(os.WriteFile(path, bytes.Replace(body, []byte(`"version":1`), []byte(`"version":2`), 1), 0600))
			case "record_unknown_state":
				must(os.WriteFile(path, bytes.Replace(body, []byte(`"state":"pending"`), []byte(`"state":"unknown"`), 1), 0600))
			case "record_wrong_filename":
				must(os.Rename(path, resumeJournalRecordPathForTest(state, "other")))
			case "unknown_file":
				must(os.WriteFile(filepath.Join(dir, "unknown"), []byte("do not delete"), 0600))
			case "orphan_temp":
				must(os.WriteFile(filepath.Join(dir, ".resume-tmp-orphan"), []byte("do not delete"), 0600))
			}
			before, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"", first.CommandID, "new-explicit"} {
				if _, err := selectResumeJournalRequest(state, control, "task", 1, id); err == nil {
					t.Fatal("untrusted filesystem accepted", id)
				}
			}
			if err := completeResumeJournalRequest(state, first); err == nil {
				t.Fatal("untrusted completion accepted")
			}
			after, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatal("rejection silently removed or created records", before, after)
			}
		})
	}
}

func TestResumeJournalPublicationCrashWindows(t *testing.T) {
	if state := os.Getenv("RESUME_JOURNAL_PUBLICATION_CRASH_STATE"); state != "" {
		operation, point := os.Getenv("RESUME_JOURNAL_PUBLICATION_CRASH_OPERATION"), os.Getenv("RESUME_JOURNAL_PUBLICATION_CRASH_POINT")
		after := func(step string) {
			if step == point {
				os.Exit(37)
			}
		}
		control := os.Getenv("RESUME_JOURNAL_PUBLICATION_CRASH_CONTROL")
		if operation == "pending" {
			if _, err := selectResumeJournalRequestWithSteps(state, control, "task", 1, "crash-pending", after); err != nil {
				t.Fatal(err)
			}
		} else {
			request := resumeJournalRequest{Version: 1, State: "pending", ControlFile: control, TaskID: "task", WorkRevision: 1, CommandID: "crash-complete"}
			if err := completeResumeJournalRequestWithSteps(state, request, after); err != nil {
				t.Fatal(err)
			}
		}
		os.Exit(0)
	}
	for _, operation := range []string{"pending", "complete"} {
		points := []string{"temp_created", "temp_synced", "record_published"}
		if operation == "complete" {
			points[2] = "record_renamed"
		}
		for _, point := range points {
			t.Run(operation+"/"+point, func(t *testing.T) {
				state, control := resumeJournalFixture(t)
				var original resumeJournalRequest
				if operation == "complete" {
					original = resumeJournalSelectForTest(t, state, control, "task", "crash-complete")
				}
				binary, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(binary, "-test.run=^TestResumeJournalPublicationCrashWindows$")
				cmd.Env = append(os.Environ(), "RESUME_JOURNAL_PUBLICATION_CRASH_STATE="+state, "RESUME_JOURNAL_PUBLICATION_CRASH_CONTROL="+control, "RESUME_JOURNAL_PUBLICATION_CRASH_OPERATION="+operation, "RESUME_JOURNAL_PUBLICATION_CRASH_POINT="+point)
				output, err := cmd.CombinedOutput()
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != 37 || len(output) != 0 {
					t.Fatal("controlled publication crash did not stay silent", string(output), err)
				}
				stateEntries, err := os.ReadDir(state)
				if err != nil {
					t.Fatal(err)
				}
				prefix := ".resume-journal-" + operation + "-"
				orphans := 0
				for _, entry := range stateEntries {
					if !strings.HasPrefix(entry.Name(), prefix) {
						continue
					}
					orphans++
					var stat unix.Stat_t
					if err := unix.Lstat(filepath.Join(state, entry.Name()), &stat); err != nil || stat.Mode&07777 != 0600 || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
						t.Fatal("crash temp lost private provenance", entry.Name(), stat, err)
					}
					if point == "temp_created" && stat.Size != 0 {
						t.Fatal("created crash point was not before write", stat.Size)
					}
					if point == "temp_synced" && stat.Size == 0 {
						t.Fatal("synced crash point lacked full body")
					}
				}
				wantOrphans := 1
				if point == "record_published" || point == "record_renamed" {
					wantOrphans = 0
				}
				if orphans != wantOrphans {
					t.Fatal("wrong state orphan count", orphans, wantOrphans)
				}
				replay, err := selectResumeJournalRequest(state, control, "task", 1, "")
				if err != nil {
					t.Fatal("partial publication blocked implicit recovery", err)
				}
				if operation == "pending" {
					if point == "record_published" && replay.CommandID != "crash-pending" {
						t.Fatal("published pending lost original command", replay)
					}
					if point != "record_published" && replay.CommandID == "crash-pending" {
						t.Fatal("unpublished command appeared committed", replay)
					}
				} else {
					if point != "record_renamed" && replay != original {
						t.Fatal("failed completion lost original pending", replay, original)
					}
					if point == "record_renamed" && replay.CommandID == original.CommandID {
						t.Fatal("implicit reused atomically completed request", replay)
					}
				}
				entries, err := os.ReadDir(filepath.Join(state, "resume-requests"))
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Name() != ".lock" && !strings.HasSuffix(entry.Name(), ".json") {
						t.Fatal("orphan entered records scanning domain", entry.Name())
					}
				}
			})
		}
	}
}

func TestResumeJournalRejectsInodeDrift(t *testing.T) {
	for _, name := range []string{"state", "directory", "lock", "record", "record_in_place"} {
		t.Run(name, func(t *testing.T) {
			state, control := resumeJournalFixture(t)
			first := resumeJournalSelectForTest(t, state, control, "task", "")
			record := resumeJournalRecordPathForTest(state, first.CommandID)
			original, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			err = withResumeJournalFiles(state, false, func(f *resumeJournalFiles) error {
				if _, err := f.scan(); err != nil {
					return err
				}
				switch name {
				case "state":
					if err := os.Rename(state, state+".old"); err != nil {
						return err
					}
					t.Cleanup(func() { os.RemoveAll(state + ".old") })
					return os.Mkdir(state, 0700)
				case "directory":
					dir := filepath.Join(state, "resume-requests")
					if err := os.Rename(dir, dir+".old"); err != nil {
						return err
					}
					return os.Mkdir(dir, 0700)
				case "lock":
					path := filepath.Join(state, "resume-requests", ".lock")
					if err := os.Rename(path, filepath.Join(state, "old-lock")); err != nil {
						return err
					}
					return os.WriteFile(path, nil, 0600)
				case "record":
					if err := os.Rename(record, filepath.Join(state, "old-record")); err != nil {
						return err
					}
					return os.WriteFile(record, original, 0600)
				case "record_in_place":
					return os.WriteFile(record, bytes.Replace(original, []byte(`"state":"pending"`), []byte(`"state":"completed"`), 1), 0600)
				}
				return nil
			})
			if err == nil || err.Error() != "resume_journal_changed" {
				t.Fatal("inode/snapshot drift was accepted", err)
			}
		})
	}
}

func TestResumeJournalRejectsForeignOwnership(t *testing.T) {
	for _, name := range []string{"state", "directory", "lock", "record"} {
		t.Run(name, func(t *testing.T) {
			state, control := resumeJournalFixture(t)
			first := resumeJournalSelectForTest(t, state, control, "task", "")
			path := state
			switch name {
			case "directory":
				path = filepath.Join(state, "resume-requests")
			case "lock":
				path = filepath.Join(state, "resume-requests", ".lock")
			case "record":
				path = resumeJournalRecordPathForTest(state, first.CommandID)
			}
			if err := os.Chown(path, os.Geteuid()+1, -1); err != nil {
				if errors.Is(err, unix.EPERM) {
					t.Skip("filesystem owner mutation requires privileged test environment")
				}
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chown(path, os.Geteuid(), -1) })
			if _, err := selectResumeJournalRequest(state, control, "task", 1, ""); err == nil {
				t.Fatal("foreign-owned journal accepted")
			}
			if err := completeResumeJournalRequest(state, first); err == nil {
				t.Fatal("foreign-owned completion accepted")
			}
		})
	}
}

func TestResumeJournalPublicationRefusesOverwrite(t *testing.T) {
	state, control := resumeJournalFixture(t)
	first := resumeJournalSelectForTest(t, state, control, "task", "same-ID")
	path := resumeJournalRecordPathForTest(state, first.CommandID)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := first
	replacement.TaskID = "other"
	err = withResumeJournalFiles(state, false, func(f *resumeJournalFiles) error {
		if _, err := f.scan(); err != nil {
			return err
		}
		return f.create(replacement)
	})
	if !errors.Is(err, unix.EEXIST) {
		t.Fatal("exclusive publication did not reject destination", err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("existing record inode was replaced", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(body, original) {
		t.Fatal("existing record body changed", string(body), err)
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".resume-journal-pending-") {
			t.Fatal("owned failed publication temp was not cleaned", entry.Name())
		}
	}
}

func TestResumeJournalKeepsPublishedRequestAfterFailure(t *testing.T) {
	state, control := resumeJournalFixture(t)
	marker := filepath.Join(state, "resume-requests", "unexpected")
	_, err := selectResumeJournalRequestWithSteps(state, control, "task", 1, "keep-published", func(step string) {
		if step == "record_published" {
			if err := os.WriteFile(marker, []byte("owned test marker"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err == nil {
		t.Fatal("post-publish directory drift was accepted")
	}
	body, readErr := os.ReadFile(resumeJournalRecordPathForTest(state, "keep-published"))
	if readErr != nil {
		t.Fatal("error deleted published pending", readErr)
	}
	var committed resumeJournalRequest
	if err := json.Unmarshal(body, &committed); err != nil || committed.CommandID != "keep-published" || committed.State != "pending" {
		t.Fatal("published pending changed", committed, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	} // Only the test's own marker.
	if replay := resumeJournalSelectForTest(t, state, control, "task", ""); replay != committed {
		t.Fatal("published failure could not replay original command", replay, committed)
	}
}
