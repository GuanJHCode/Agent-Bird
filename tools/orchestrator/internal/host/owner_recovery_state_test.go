package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestOriginalOwnerRecoveryKeepsChildAcrossSharedFiveAttempts(t *testing.T) {
	dir := privateRecoveryTestDirectory(t)
	scope := strings.Repeat("a", 64)
	s, err := openOwnerRecovery(dir, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.close() }()
	old := contract.OriginalOwnerTransport{PID: 10, Birth: "old"}
	child := contract.OriginalOwnerTransport{PID: 20, Birth: "child"}
	when, err := s.reserveTransport(time.UnixMilli(100_000), "replacement", 1, old)
	if err != nil || when.UnixMilli() != 101_000 || s.record.Attempts != 1 {
		t.Fatal("preStart reservation", when, err)
	}
	if err = s.beginTransportStart(); err != nil {
		t.Fatal(err)
	}
	if err = s.recordTransportStarted(child); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ticketPending()
	if err != nil {
		t.Fatal(err)
	}
	for i, now := range []int64{200_000, 300_000, 400_000, 500_000} {
		delay := []int64{2_000, 4_000, 8_000, 16_000}[i]
		when, err = s.reserveCoordinator(time.UnixMilli(now))
		if err != nil || when.UnixMilli() != now+delay || s.record.Attempts != i+2 {
			t.Fatal("shared retry", i, when, err)
		}
		s.close()
		s, err = openOwnerRecovery(dir, scope)
		if err != nil {
			t.Fatal(err)
		}
		again, err := s.reserveCoordinator(time.UnixMilli(now + 100))
		if err != nil || again != when || s.record.Attempts != i+2 {
			t.Fatal("reservation recharged", err)
		}
		if err = s.beginCoordinatorEnsure(); err != nil {
			t.Fatal(err)
		}
		if err = s.finishCoordinatorEnsure(); err != nil {
			t.Fatal(err)
		}
		got, err := s.ticketPending()
		if err != nil || got != pending || s.record.Unavailable {
			t.Fatal("coordinator retry changed original child", err)
		}
	}
	if _, err = s.reserveCoordinator(time.UnixMilli(600_000)); !errors.Is(err, errCoordinatorUnavailable) || !s.record.Unavailable {
		t.Fatal("sixth operation permitted", err)
	}
	if s.record.OwnerPending.NewHost == nil || *s.record.OwnerPending.NewHost != child {
		t.Fatal("exhaustion erased owned child")
	}
}

func TestOriginalOwnerRecoveryRejectsCorruptOrChangedState(t *testing.T) {
	for _, kind := range []string{"version", "foreign_scope", "slot", "missing_child", "starting_without_reservation", "unknown_with_child", "duplicate_field", "symlink", "hardlink", "public_mode"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateRecoveryTestDirectory(t)
			scope := strings.Repeat("a", 64)
			s, err := openOwnerRecovery(dir, scope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.reserveTransport(time.UnixMilli(100_000), "pending", 1, contract.OriginalOwnerTransport{PID: 10, Birth: "old"}); err != nil {
				t.Fatal(err)
			}
			if err = s.beginTransportStart(); err != nil {
				t.Fatal(err)
			}
			if err = s.recordTransportStarted(contract.OriginalOwnerTransport{PID: 20, Birth: "child"}); err != nil {
				t.Fatal(err)
			}
			path := s.file.path
			r := s.copyRecord()
			s.close()
			switch kind {
			case "version":
				r.Version = 1
			case "foreign_scope":
				r.ScopeSHA256 = strings.Repeat("b", 64)
			case "slot":
				r.OwnerPending.StartSlot = 2
			case "missing_child":
				r.OwnerPending.NewHost = nil
			case "starting_without_reservation":
				r.OwnerPending.Phase = "starting"
				r.OwnerPending.NewHost = nil
			case "unknown_with_child":
				r.OwnerPending.Phase = "unknown"
			}
			body, _ := json.Marshal(r)
			body = append(body, '\n')
			if kind == "duplicate_field" {
				body = bytes.Replace(body, []byte(`"version":2`), []byte(`"version":2,"version":2`), 1)
			}
			if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err = os.Rename(path, path+".original"); err == nil {
					err = os.Symlink(path+".original", path)
				}
			case "hardlink":
				err = os.Link(path, path+".linked")
			case "public_mode":
				err = os.Chmod(path, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if reopened, err := openOwnerRecovery(dir, scope); err == nil {
				reopened.close()
				t.Fatal("corrupt owner ledger accepted")
			}
		})
	}
	// Even if external bytes are later restored, a failed CAS poisons this
	// in-memory authority. It cannot execute a side effect on an uncertain write.
	dir := privateRecoveryTestDirectory(t)
	s, err := openOwnerRecovery(dir, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if _, err = s.reserveCoordinator(time.UnixMilli(100_000)); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.file.path)
	if err = os.WriteFile(s.file.path, append(append([]byte(nil), before...), ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.beginCoordinatorEnsure(); err == nil {
		t.Fatal("changed ledger allowed Ensure")
	}
	if err = os.WriteFile(s.file.path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.beginCoordinatorEnsure(); err == nil {
		t.Fatal("uncertain write authority resumed")
	}
}

func TestOriginalOwnerRecoveryStartingAndUnknownNeverStartAgain(t *testing.T) {
	for _, phase := range []string{"starting", "unknown", "started"} {
		t.Run(phase, func(t *testing.T) {
			dir := privateRecoveryTestDirectory(t)
			scope := strings.Repeat("a", 64)
			s, err := openOwnerRecovery(dir, scope)
			if err != nil {
				t.Fatal(err)
			}
			old := contract.OriginalOwnerTransport{PID: 10, Birth: "old"}
			if _, err = s.reserveTransport(time.UnixMilli(100_000), "original", 1, old); err != nil {
				t.Fatal(err)
			}
			if err = s.beginTransportStart(); err != nil {
				t.Fatal(err)
			}
			if phase == "unknown" {
				if err = s.markTransportUnknown(); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "started" {
				if err = s.recordTransportStarted(contract.OriginalOwnerTransport{PID: 20, Birth: "child"}); err != nil {
					t.Fatal(err)
				}
			}
			s.close()
			s, err = openOwnerRecovery(dir, scope)
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			before, _ := os.ReadFile(s.file.path)
			if err = s.beginTransportStart(); err == nil {
				t.Fatal("second Start permitted")
			}
			if _, err = s.reserveTransport(time.UnixMilli(200_000), "another", 1, old); err == nil {
				t.Fatal("pending child replaced")
			}
			if phase != "started" {
				if _, err = s.ticketPending(); err == nil {
					t.Fatal("uncertain Start got ticket tuple")
				}
			}
			after, _ := os.ReadFile(s.file.path)
			if !bytes.Equal(before, after) || s.record.Attempts != 1 {
				t.Fatal("rejected operation changed ledger")
			}
		})
	}
}

func TestOriginalOwnerRecoverySharesFlockAndPreservesLegacyBytes(t *testing.T) {
	dir := privateRecoveryTestDirectory(t)
	legacy, err := openHostRecovery(dir, "legacy-scope", 10, "birth")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = openOwnerRecovery(dir, strings.Repeat("a", 64)); err == nil {
		t.Fatal("owner acquired legacy flock")
	}
	legacy.close()
	path := filepath.Join(dir, ".coordinator-recovery.json")
	before, _ := os.ReadFile(path)
	if _, err = openOwnerRecovery(dir, strings.Repeat("a", 64)); err == nil {
		t.Fatal("owner reset legacy ledger")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("owner rewrote legacy bytes")
	}
	legacy, err = openHostRecovery(dir, "legacy-scope", 10, "birth")
	if err != nil {
		t.Fatal(err)
	}
	legacy.close()
	dir = privateRecoveryTestDirectory(t)
	owner, err := openOwnerRecovery(dir, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	if other, err := openOwnerRecovery(dir, strings.Repeat("a", 64)); err == nil {
		other.close()
		t.Fatal("second owner writer")
	}
	if old, err := openHostRecovery(dir, "legacy-scope", 10, "birth"); err == nil {
		old.close()
		t.Fatal("legacy acquired owner flock")
	}
}

func TestOriginalOwnerRecoveryTicketReadsRejectLedgerDrift(t *testing.T) {
	for _, kind := range []string{"deleted", "replaced", "corrupt", "hardlink", "public_mode"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateRecoveryTestDirectory(t)
			s, err := openOwnerRecovery(dir, strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if _, err = s.reserveTransport(time.UnixMilli(100_000), "original", 1, contract.OriginalOwnerTransport{PID: 10, Birth: "old"}); err != nil {
				t.Fatal(err)
			}
			if err = s.beginTransportStart(); err != nil {
				t.Fatal(err)
			}
			if err = s.recordTransportStarted(contract.OriginalOwnerTransport{PID: 20, Birth: "child"}); err != nil {
				t.Fatal(err)
			}
			path := s.file.path
			before, _ := os.ReadFile(path)
			switch kind {
			case "deleted":
				err = os.Remove(path)
			case "replaced":
				changed := s.copyRecord()
				changed.OwnerPending.RequestID = "foreign"
				body, _ := json.Marshal(changed)
				err = os.WriteFile(path+".foreign", append(body, '\n'), 0600)
				if err == nil {
					err = os.Rename(path+".foreign", path)
				}
			case "corrupt":
				err = os.WriteFile(path, []byte("broken\n"), 0600)
			case "hardlink":
				err = os.Link(path, path+".linked")
			case "public_mode":
				err = os.Chmod(path, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ticketPending(); err == nil {
				t.Fatal("ticket read ignored durable ledger drift")
			}
			if kind == "hardlink" {
				if err = os.Remove(path + ".linked"); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "public_mode" {
				if err = os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = s.ticketPending(); err == nil {
				t.Fatal("restoring bytes revived poisoned authority")
			}
		})
	}
}

func TestOriginalOwnerRecoveryExactReservationReplayDoesNotRewrite(t *testing.T) {
	dir := privateRecoveryTestDirectory(t)
	scope := strings.Repeat("a", 64)
	s, err := openOwnerRecovery(dir, scope)
	if err != nil {
		t.Fatal(err)
	}
	when, err := s.reserveCoordinator(time.UnixMilli(100_000))
	if err != nil {
		t.Fatal(err)
	}
	s.close()
	s, err = openOwnerRecovery(dir, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	before, err := os.Stat(s.file.path)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(s.file.path)
	again, err := s.reserveCoordinator(time.UnixMilli(100_500))
	if err != nil || again != when || s.record.Attempts != 1 {
		t.Fatal("exact replay", err)
	}
	after, err := os.Stat(s.file.path)
	if err != nil {
		t.Fatal(err)
	}
	newContent, _ := os.ReadFile(s.file.path)
	if !os.SameFile(before, after) || !bytes.Equal(content, newContent) {
		t.Fatal("exact reservation replay rewrote persistent ledger")
	}
}

func TestOriginalOwnerRecoveryRejectsReplacedDirectoryOrLock(t *testing.T) {
	for _, kind := range []string{"directory", "lock"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateRecoveryTestDirectory(t)
			scope := strings.Repeat("a", 64)
			s, err := openOwnerRecovery(dir, scope)
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if _, err = s.reserveTransport(time.UnixMilli(100_000), "pending", 1, contract.OriginalOwnerTransport{PID: 10, Birth: "old"}); err != nil {
				t.Fatal(err)
			}
			if err = s.beginTransportStart(); err != nil {
				t.Fatal(err)
			}
			if err = s.recordTransportStarted(contract.OriginalOwnerTransport{PID: 20, Birth: "child"}); err != nil {
				t.Fatal(err)
			}
			original, _ := os.ReadFile(s.file.path)
			if kind == "directory" {
				parked := dir + ".parked"
				t.Cleanup(func() { _ = os.RemoveAll(parked) })
				if err = os.Rename(dir, parked); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(dir, ".coordinator-recovery.json"), original, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				lock := filepath.Join(dir, ".coordinator-recovery.lock")
				if err = os.Rename(lock, lock+".parked"); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(lock, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if clone, err := openOwnerRecovery(dir, scope); err == nil {
				clone.close()
				t.Error("cloned ledger accepted a different directory/flock authority")
			}
			if _, err = s.ticketPending(); err == nil {
				t.Error("original authority followed a replacement directory/flock")
			}
			if _, err = s.reserveCoordinator(time.UnixMilli(200_000)); err == nil {
				t.Error("replacement path accepted mutation")
			}
		})
	}
}

func TestOriginalOwnerRecoveryUnsafeDirectoryDoesNotCreateLock(t *testing.T) {
	dir := privateRecoveryTestDirectory(t)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if opened, err := openOwnerRecovery(dir, strings.Repeat("a", 64)); err == nil {
		opened.close()
		t.Fatal("unsafe directory accepted")
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryLockName)); !os.IsNotExist(err) {
		t.Fatal("unsafe directory rejection created a lock", err)
	}
}
