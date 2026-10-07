package host

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func ownerRecoveryLeaseFixture(t *testing.T) (contract.OwnerRuntimeBootstrap, contract.OriginalOwnerBinding, *ownerRecoveryState) {
	t.Helper()
	e, bound, _ := ownerEnrollmentFixture(t)
	b := e.bootstrap
	if err := os.Chmod(b.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	dirs, err := openOwnerRuntimeDirectories(b)
	if err != nil {
		t.Fatal(err)
	}
	dirs.close()
	scope, err := originalOwnerRecoveryScope(bound.Binding.Scope, bound.Binding.Owner)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openOwnerRecovery(b.SpoolRoot, scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return b, bound.Binding, s
}

type recoveryLeaseSnapshotEntry struct {
	Mode     fs.FileMode
	Size     int64
	ModTime  time.Time
	Dev, Ino uint64
	Body     string
}

func recoveryLeaseSnapshot(t *testing.T, root string) map[string]recoveryLeaseSnapshotEntry {
	t.Helper()
	result := map[string]recoveryLeaseSnapshotEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		stat, ok := infoSyscall(info)
		if !ok {
			t.Fatal("missing inode metadata")
		}
		v := recoveryLeaseSnapshotEntry{Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime(), Dev: uint64(stat.Dev), Ino: uint64(stat.Ino)}
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			v.Body = string(body)
		} else if info.Mode()&os.ModeSymlink != 0 {
			v.Body, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		result[path] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOwnerRecoveryLeasePreservesBudgetAndHoldsOriginalLock(t *testing.T) {
	for _, kind := range []string{"initial", "spent", "reserved_coordinator", "spent_five", "reserved_five", "exhausted"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			count := 0
			if kind == "spent" || kind == "reserved_coordinator" {
				count = 1
			}
			if kind == "exhausted" || kind == "spent_five" || kind == "reserved_five" {
				count = 5
			}
			for i := 0; i < count; i++ {
				now := time.UnixMilli(int64(i+1) * 100_000)
				if kind == "reserved_coordinator" {
					now = time.Now()
				}
				if _, err := s.reserveCoordinator(now); err != nil {
					t.Fatal(err)
				}
				if kind != "reserved_coordinator" && !(kind == "reserved_five" && i == count-1) {
					if err := s.beginCoordinatorEnsure(); err != nil {
						t.Fatal(err)
					}
					if err := s.finishCoordinatorEnsure(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind == "exhausted" {
				if _, err := s.reserveCoordinator(time.UnixMilli(600_000)); err == nil || !s.record.Unavailable {
					t.Fatal("fixture did not exhaust shared budget")
				}
			}
			originalRecord := s.copyRecord()
			s.close()
			before := recoveryLeaseSnapshot(t, b.StateDir)
			lease, err := OpenExistingOwnerRecoveryLease(b.StateDir, binding.Scope, binding.Owner)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if err = lease.ValidateSuccession(); err != nil {
				t.Fatal(err)
			}
			if other, err := OpenExistingOwnerRecoveryLease(b.StateDir, binding.Scope, binding.Owner); err == nil || other != nil {
				if other != nil {
					other.Close()
				}
				t.Fatal("held lease did not exclude another retirement")
			}
			if other, err := OpenExistingOwnerRecoveryLease(b.StateDir, binding.Scope, binding.Owner); err == nil || other != nil {
				if other != nil {
					other.Close()
				}
				t.Fatal("failed contender released the original lock")
			}
			lease.Close()
			lease.Close()
			if err = lease.ValidateSuccession(); err == nil {
				t.Fatal("closed lease remained an authority")
			}
			if after := recoveryLeaseSnapshot(t, b.StateDir); !reflect.DeepEqual(before, after) {
				t.Fatal("read-only lease changed files, identities or mtime")
			}
			scope, _ := originalOwnerRecoveryScope(binding.Scope, binding.Owner)
			reopened, err := openOwnerRecovery(b.SpoolRoot, scope)
			if err != nil {
				t.Fatal("lease failed to release original lock", err)
			}
			defer reopened.close()
			if !reflect.DeepEqual(reopened.record, originalRecord) {
				t.Fatal("lease reset recovery allowance")
			}
		})
	}
}

func TestOwnerRecoveryLeaseRejectsReplacedIdentityPermanently(t *testing.T) {
	for _, kind := range []string{"state", "spool", "launch", "lock", "record"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			lease, err := OpenExistingOwnerRecoveryLease(b.StateDir, binding.Scope, binding.Owner)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			path := map[string]string{"state": b.StateDir, "spool": filepath.Dir(b.SpoolRoot), "launch": b.SpoolRoot, "lock": filepath.Join(b.SpoolRoot, recoveryLockName), "record": filepath.Join(b.SpoolRoot, recoveryLedgerName)}[kind]
			var body []byte
			if kind == "lock" || kind == "record" {
				body, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			original := path + ".original"
			if err = os.Rename(path, original); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(original) })
			if kind == "lock" || kind == "record" {
				err = os.WriteFile(path, body, 0600)
			} else {
				err = os.Mkdir(path, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := recoveryLeaseSnapshot(t, b.StateDir)
			if err = lease.ValidateSuccession(); err == nil {
				t.Fatal("same-mode identical clone replaced original lease authority")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("validation changed substituted files")
			}
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(original, path); err != nil {
				t.Fatal(err)
			}
			if err = lease.ValidateSuccession(); err == nil {
				t.Fatal("restoring original path revived rejected lease")
			}
		})
	}
}

func TestOwnerRecoveryLeaseRejectsUnsafeDirectoryAndLaunch(t *testing.T) {
	for _, kind := range []string{"relative_state", "unclean_state", "root_state", "dot", "dotdot", "slash", "backslash", "nul", "foreign_producer", "state_symlink", "spool_symlink", "launch_symlink", "state_mode", "spool_mode", "launch_mode"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			state := b.StateDir
			var err error
			switch kind {
			case "relative_state":
				state = "relative"
			case "unclean_state":
				state += "/."
			case "root_state":
				state = string(filepath.Separator)
			case "dot":
				binding.Scope.LaunchID = "."
			case "dotdot":
				binding.Scope.LaunchID = ".."
			case "slash":
				binding.Scope.LaunchID = "nested/launch"
			case "backslash":
				binding.Scope.LaunchID = `nested\launch`
			case "nul":
				binding.Scope.LaunchID = "bad\x00launch"
			case "foreign_producer":
				binding.Scope.ProducerID += "-foreign"
			default:
				path := b.SpoolRoot
				if strings.HasPrefix(kind, "state_") {
					path = b.StateDir
				}
				if strings.HasPrefix(kind, "spool_") {
					path = filepath.Dir(b.SpoolRoot)
				}
				if strings.HasSuffix(kind, "_mode") {
					err = os.Chmod(path, 0755)
				} else {
					original := path + ".original"
					err = os.Rename(path, original)
					t.Cleanup(func() { _ = os.RemoveAll(original) })
					if err == nil {
						err = os.Symlink(original, path)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "dot" || kind == "dotdot" || kind == "slash" || kind == "backslash" || kind == "nul" {
				binding.Scope.ProducerID = binding.Scope.LaunchID
			}
			before := recoveryLeaseSnapshot(t, b.StateDir)
			lease, err := OpenExistingOwnerRecoveryLease(state, binding.Scope, binding.Owner)
			if err == nil || lease != nil {
				if lease != nil {
					lease.Close()
				}
				t.Fatal("unsafe directory or launch scope admitted")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("unsafe open changed authority")
			}
		})
	}
}

func TestOwnerRecoveryLeaseNeverCreatesMissingAuthority(t *testing.T) {
	for _, kind := range []string{"state", "spool", "launch", "lock", "record"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			path := map[string]string{"state": b.StateDir, "spool": filepath.Dir(b.SpoolRoot), "launch": b.SpoolRoot, "lock": filepath.Join(b.SpoolRoot, recoveryLockName), "record": filepath.Join(b.SpoolRoot, recoveryLedgerName)}[kind]
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			before := recoveryLeaseSnapshot(t, b.StateDir)
			lease, err := OpenExistingOwnerRecoveryLease(b.StateDir, binding.Scope, binding.Owner)
			if err == nil || lease != nil {
				if lease != nil {
					lease.Close()
				}
				t.Fatal("missing recovery authority accepted")
			}
			if after := recoveryLeaseSnapshot(t, b.StateDir); !reflect.DeepEqual(before, after) {
				t.Fatal("missing-authority refusal created or changed files")
			}
		})
	}
}

func TestOwnerRecoveryLeaseRejectsPendingAndUncertainCoordinator(t *testing.T) {
	for _, phase := range []string{"reserved", "starting", "unknown", "started", "attached", "applied", "activated", "coordinator_starting"} {
		t.Run(phase, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			next := s.copyRecord()
			next.Attempts, next.NextAttemptUnixMS = 1, 101_000
			if phase == "coordinator_starting" {
				next.Reservation = &ownerRecoveryReservation{Slot: 1, Kind: "coordinator", Phase: "starting"}
			} else {
				next.OwnerPending = &ownerRecoveryPending{StartSlot: 1, RequestID: "pending", ExpectedControlRevision: 1, OldHost: binding.FirstHost, Phase: phase}
				switch phase {
				case "reserved", "starting":
					next.Reservation = &ownerRecoveryReservation{Slot: 1, Kind: "transport_start", Phase: phase}
				case "started", "attached", "applied", "activated":
					next.OwnerPending.NewHost = &contract.OriginalOwnerTransport{PID: binding.FirstHost.PID + 1, Birth: "pending-child"}
					if phase != "started" {
						next.OwnerPending.PermitSHA256 = strings.Repeat("c", 64)
					}
					if phase == "applied" || phase == "activated" {
						next.OwnerPending.AppliedSHA256 = strings.Repeat("d", 64)
					}
				}
			}
			if err := s.commit(next); err != nil {
				t.Fatal("invalid pending fixture", err)
			}
			s.close()
			before := recoveryLeaseSnapshot(t, b.StateDir)
			lease, err := OpenExistingOwnerRecoveryLease(b.StateDir, binding.Scope, binding.Owner)
			if err == nil || lease != nil {
				if lease != nil {
					lease.Close()
				}
				t.Fatal("uncertain prior authority permitted retirement")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("rejection erased prior authority")
			}
		})
	}
}

func TestOwnerRecoveryLeaseRejectsUnauthenticatedRecord(t *testing.T) {
	for _, kind := range []string{"scope", "identity", "authority", "noncanonical", "duplicate", "truncated", "empty", "limit", "oversized", "record_mode", "lock_mode", "record_hardlink", "lock_hardlink", "record_symlink", "lock_symlink"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			record := s.copyRecord()
			s.close()
			path := filepath.Join(b.SpoolRoot, recoveryLedgerName)
			lock := filepath.Join(b.SpoolRoot, recoveryLockName)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "scope":
				binding.Scope.OwnerID += "-foreign"
			case "identity":
				binding.Owner.PID++
			case "authority":
				record.AuthoritySHA256 = strings.Repeat("f", 64)
				body, _ = json.Marshal(record)
				body = append(body, '\n')
			case "noncanonical":
				body = append([]byte(" "), body...)
			case "duplicate":
				body = bytes.Replace(body, []byte(`"version":2`), []byte(`"version":2,"version":2`), 1)
			case "truncated":
				body = body[:len(body)/2]
			case "empty":
				body = nil
			case "limit":
				body = bytes.Repeat([]byte("x"), 4096)
			case "oversized":
				body = bytes.Repeat([]byte("x"), 4097)
			case "record_mode":
				err = os.Chmod(path, 0644)
			case "lock_mode":
				err = os.Chmod(lock, 0644)
			case "record_hardlink":
				err = os.Link(path, path+".extra")
			case "lock_hardlink":
				err = os.Link(lock, lock+".extra")
			case "record_symlink", "lock_symlink":
				target := path
				if kind == "lock_symlink" {
					target = lock
				}
				err = os.Rename(target, target+".original")
				if err == nil {
					err = os.Symlink(target+".original", target)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "authority" || kind == "noncanonical" || kind == "duplicate" || kind == "truncated" || kind == "empty" || kind == "limit" || kind == "oversized" {
				if err = os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := recoveryLeaseSnapshot(t, b.StateDir)
			lease, err := OpenExistingOwnerRecoveryLease(b.StateDir, binding.Scope, binding.Owner)
			if err == nil || lease != nil {
				if lease != nil {
					lease.Close()
				}
				t.Fatal("untrusted record accepted")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("refusal mutated untrusted record")
			}
		})
	}
}

func TestOwnerRecoveryLeaseRejectsLateMutationPermanently(t *testing.T) {
	for _, kind := range []string{"record_bytes", "record_mtime", "record_mode", "lock_mode", "record_link", "lock_link"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			lease, err := OpenExistingOwnerRecoveryLease(b.StateDir, binding.Scope, binding.Owner)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			path := filepath.Join(b.SpoolRoot, recoveryLedgerName)
			if strings.HasPrefix(kind, "lock_") {
				path = filepath.Join(b.SpoolRoot, recoveryLockName)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "record_bytes":
				err = os.WriteFile(path, bytes.Replace(body, []byte(`"attempts":0`), []byte(`"attempts":1`), 1), 0600)
			case "record_mtime":
				err = os.Chtimes(path, info.ModTime(), info.ModTime().Add(time.Second))
			case "record_mode", "lock_mode":
				err = os.Chmod(path, 0644)
			case "record_link", "lock_link":
				err = os.Link(path, path+".extra")
			}
			if err != nil {
				t.Fatal(err)
			}
			before := recoveryLeaseSnapshot(t, b.StateDir)
			if err = lease.ValidateSuccession(); err == nil {
				t.Fatal("changed recovery authority remained valid")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("lease rewrote changed authority")
			}
			switch kind {
			case "record_bytes":
				err = os.WriteFile(path, body, 0600)
			case "record_mtime":
				err = os.Chtimes(path, info.ModTime(), info.ModTime())
			case "record_mode", "lock_mode":
				err = os.Chmod(path, 0600)
			case "record_link", "lock_link":
				err = os.Remove(path + ".extra")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = lease.ValidateSuccession(); err == nil {
				t.Fatal("restored authority revived failed lease")
			}
		})
	}
}

func TestOwnerRecoveryLeaseRejectsSymlinkAboveState(t *testing.T) {
	b, binding, s := ownerRecoveryLeaseFixture(t)
	s.close()
	root := t.TempDir()
	alias := filepath.Join(root, "ancestor")
	if err := os.Symlink(filepath.Dir(b.StateDir), alias); err != nil {
		t.Fatal(err)
	}
	before := recoveryLeaseSnapshot(t, b.StateDir)
	lease, err := OpenExistingOwnerRecoveryLease(filepath.Join(alias, filepath.Base(b.StateDir)), binding.Scope, binding.Owner)
	if err == nil || lease != nil {
		if lease != nil {
			lease.Close()
		}
		t.Fatal("symlink ancestor bypassed canonical state authority")
	}
	if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
		t.Fatal("aliased state was modified")
	}
}
