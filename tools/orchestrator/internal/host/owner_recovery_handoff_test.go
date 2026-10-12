package host

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func handoffSuccessor(binding contract.OriginalOwnerBinding) (contract.OriginalOwnerScope, contract.OriginalOwnerIdentity) {
	next, owner := binding.Scope, binding.Owner
	next.Incarnation++
	next.OwnerID += "-successor"
	// The Store controls valid origin/generation evolution; the file layer
	// must not require the successor's original origin to equal its predecessor.
	next.OriginalOriginContextID += "-next"
	next.OriginalOriginPID++
	next.OriginalOriginBirth += "-next"
	next.HostGeneration += "-next"
	owner.PID++
	owner.Birth += "-next"
	return next, owner
}

func handoffProof(f OwnerRecoveryHandoffFacts, applied bool) OwnerRecoveryHandoffProof {
	return OwnerRecoveryHandoffProof{PriorScopeSHA256: f.PriorScopeSHA256, ScopeSHA256: f.ScopeSHA256, PriorRecordSHA256: f.PriorRecordSHA256, RecordSHA256: f.RecordSHA256, AuthoritySHA256: f.AuthoritySHA256, Applied: applied}
}

func handoffDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func handoffReadRecord(t *testing.T, path string) (ownerRecoveryRecord, []byte) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record ownerRecoveryRecord
	if err = json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	return record, body
}

func TestOwnerRecoveryHandoffPreservesAllBudget(t *testing.T) {
	for _, kind := range []string{"initial", "spent", "reserved_coordinator", "spent_five", "reserved_five", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			count := 0
			if kind == "spent" || kind == "reserved_coordinator" {
				count = 1
			}
			if kind == "spent_five" || kind == "reserved_five" || kind == "unavailable" {
				count = 5
			}
			for i := 0; i < count; i++ {
				if _, err := s.reserveCoordinator(time.UnixMilli(int64(i+1) * 100_000)); err != nil {
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
			if kind == "unavailable" {
				if _, err := s.reserveCoordinator(time.UnixMilli(600_000)); err == nil || !s.record.Unavailable {
					t.Fatal("fixture budget not exhausted")
				}
			}
			original := s.copyRecord()
			nextScope, nextOwner := handoffSuccessor(binding)
			nextHash, err := originalOwnerRecoveryScope(nextScope, nextOwner)
			if err != nil {
				t.Fatal(err)
			}
			wrong := original
			wrong.ScopeSHA256 = nextHash
			if err = s.commit(wrong); err == nil {
				t.Fatal("ordinary commit changed scope")
			}
			s.close()
			before := recoveryLeaseSnapshot(t, b.StateDir)
			if other, err := openOwnerRecovery(b.SpoolRoot, nextHash); err == nil {
				other.close()
				t.Fatal("ordinary open accepted prior scope")
			}
			l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, nextScope, nextOwner, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			facts, err := l.Facts()
			if err != nil {
				t.Fatal(err)
			}
			_, body := handoffReadRecord(t, filepath.Join(b.SpoolRoot, recoveryLedgerName))
			nextRecord := original
			nextRecord.ScopeSHA256 = nextHash
			nextBody, _ := json.Marshal(nextRecord)
			nextBody = append(nextBody, '\n')
			if facts.PriorScopeSHA256 != original.ScopeSHA256 || facts.ScopeSHA256 != nextHash || facts.AuthoritySHA256 != original.AuthoritySHA256 || facts.PriorRecordSHA256 != handoffDigest(body) || facts.RecordSHA256 != handoffDigest(nextBody) {
				t.Fatalf("facts=%+v", facts)
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("open/facts wrote files")
			}
			if err = l.CommitPrepared(handoffProof(facts, false)); err != nil {
				t.Fatal(err)
			}
			got, actual := handoffReadRecord(t, filepath.Join(b.SpoolRoot, recoveryLedgerName))
			if !bytes.Equal(actual, nextBody) {
				t.Fatal("scope-only canonical publication changed other bytes")
			}
			got.ScopeSHA256 = original.ScopeSHA256
			if !reflect.DeepEqual(got, original) {
				t.Fatalf("budget reset: before=%+v after=%+v", original, got)
			}
			if again, err := l.Facts(); err != nil || again != facts {
				t.Fatalf("immutable facts changed: %+v %v", again, err)
			}
			l.Close()
			l.Close()
			if _, err = l.Facts(); err == nil {
				t.Fatal("closed lease authority retained")
			}
			if old, err := openOwnerRecovery(b.SpoolRoot, original.ScopeSHA256); err == nil {
				old.close()
				t.Fatal("ordinary prior reopened successor scope")
			}
			reopened, err := openOwnerRecovery(b.SpoolRoot, nextHash)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.close()
			if !reflect.DeepEqual(reopened.record, nextRecord) {
				t.Fatal("reopen reset allowance")
			}
		})
	}
}

func TestOwnerRecoveryHandoffPreparedAndAppliedReplay(t *testing.T) {
	b, binding, s := ownerRecoveryLeaseFixture(t)
	s.close()
	nextScope, nextOwner := handoffSuccessor(binding)
	l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, nextScope, nextOwner, nil)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := l.Facts()
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	prepared := handoffProof(facts, false)
	l, err = OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, nextScope, nextOwner, &prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.CommitPrepared(prepared); err != nil {
		t.Fatal(err)
	}
	l.Close()
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared_next_ack_loss", true: "applied_next_readonly"}[applied], func(t *testing.T) {
			proof := handoffProof(facts, applied)
			before := recoveryLeaseSnapshot(t, b.StateDir)
			l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, nextScope, nextOwner, &proof)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := l.Facts(); err != nil || got != facts {
				t.Fatalf("facts=%+v %v", got, err)
			}
			if err = l.CommitPrepared(proof); err != nil {
				t.Fatal(err)
			}
			l.Close()
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("read-only exact replay rewrote file/inode/mtime")
			}
		})
	}
	t.Run("nil_proof_cannot_accept_next", func(t *testing.T) {
		before := recoveryLeaseSnapshot(t, b.StateDir)
		if l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, nextScope, nextOwner, nil); err == nil {
			l.Close()
			t.Fatal("nil proof accepted next")
		}
		if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
			t.Fatal("refusal changed next")
		}
	})
	t.Run("next_replay_requires_prior_full_hash", func(t *testing.T) {
		proof := handoffProof(facts, false)
		proof.PriorRecordSHA256 = strings.Repeat("f", 64)
		before := recoveryLeaseSnapshot(t, b.StateDir)
		if l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, nextScope, nextOwner, &proof); err == nil {
			l.Close()
			t.Fatal("exact next hash alone accepted a mismatching prior clone")
		}
		if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
			t.Fatal("proof mismatch changed next ledger")
		}
	})
	t.Run("applied_rejects_prior_rollback", func(t *testing.T) {
		path := filepath.Join(b.SpoolRoot, recoveryLedgerName)
		r, _ := handoffReadRecord(t, path)
		r.ScopeSHA256 = facts.PriorScopeSHA256
		body, _ := json.Marshal(r)
		if err := os.WriteFile(path, append(body, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		before := recoveryLeaseSnapshot(t, b.StateDir)
		proof := handoffProof(facts, true)
		if l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, nextScope, nextOwner, &proof); err == nil {
			l.Close()
			t.Fatal("applied accepted rollback")
		}
		if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
			t.Fatal("rollback refusal wrote files")
		}
	})
}

func TestOwnerRecoveryHandoffRejectsFullRecordProofDrift(t *testing.T) {
	for _, current := range []string{"prior", "next"} {
		t.Run(current, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			next, owner := handoffSuccessor(binding)
			l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil)
			if err != nil {
				t.Fatal(err)
			}
			facts, err := l.Facts()
			if err != nil {
				t.Fatal(err)
			}
			proof := handoffProof(facts, false)
			if current == "next" {
				if err = l.CommitPrepared(proof); err != nil {
					t.Fatal(err)
				}
			}
			l.Close()
			path := filepath.Join(b.SpoolRoot, recoveryLedgerName)
			r, _ := handoffReadRecord(t, path)
			r.Attempts = 1
			r.NextAttemptUnixMS = 101_000
			if err = validateOwnerRecovery(r); err != nil {
				t.Fatal("record drift fixture must remain semantically valid", err)
			}
			body, _ := json.Marshal(r)
			if err = os.WriteFile(path, append(body, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			before := recoveryLeaseSnapshot(t, b.StateDir)
			if l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, &proof); err == nil {
				l.Close()
				t.Fatal("other valid record bytes accepted by persisted intent")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("nonexact record was repaired/reset")
			}
		})
	}
}

func TestOwnerRecoveryHandoffRejectsForeignUID(t *testing.T) {
	for _, kind := range []string{"state", "spool", "launch", "lock", "ledger"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			next, owner := handoffSuccessor(binding)
			path := map[string]string{"state": b.StateDir, "spool": filepath.Dir(b.SpoolRoot), "launch": b.SpoolRoot, "lock": filepath.Join(b.SpoolRoot, recoveryLockName), "ledger": filepath.Join(b.SpoolRoot, recoveryLedgerName)}[kind]
			before := recoveryLeaseSnapshot(t, b.StateDir)
			uid := os.Geteuid()
			if err := os.Chown(path, uid+1, -1); err != nil {
				if errors.Is(err, syscall.EPERM) {
					t.Skip("foreign UID matrix requires chown authority", err)
				}
				t.Fatal(err)
			}
			defer func() {
				if err := os.Chown(path, uid, -1); err != nil {
					t.Error("restore owned fixture", err)
				}
			}()
			if l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil); err == nil {
				l.Close()
				t.Fatal("foreign ownership accepted")
			}
			if err := os.Chown(path, uid, -1); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("foreign ownership refusal changed files")
			}
		})
	}
}

func TestOwnerRecoveryHandoffRejectsPendingAndStarting(t *testing.T) {
	for _, phase := range []string{"reserved", "starting", "unknown", "started", "attached", "applied", "activated", "coordinator_starting"} {
		t.Run(phase, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			r := s.copyRecord()
			r.Attempts = 1
			r.NextAttemptUnixMS = 101_000
			if phase == "coordinator_starting" {
				r.Reservation = &ownerRecoveryReservation{Slot: 1, Kind: "coordinator", Phase: "starting"}
			} else {
				r.OwnerPending = &ownerRecoveryPending{StartSlot: 1, RequestID: "pending", ExpectedControlRevision: 1, OldHost: binding.FirstHost, Phase: phase}
				switch phase {
				case "reserved", "starting":
					r.Reservation = &ownerRecoveryReservation{Slot: 1, Kind: "transport_start", Phase: phase}
				case "started", "attached", "applied", "activated":
					r.OwnerPending.NewHost = &contract.OriginalOwnerTransport{PID: binding.FirstHost.PID + 1, Birth: "retained-child"}
					if phase != "started" {
						r.OwnerPending.PermitSHA256 = strings.Repeat("c", 64)
					}
					if phase == "applied" || phase == "activated" {
						r.OwnerPending.AppliedSHA256 = strings.Repeat("d", 64)
					}
				}
			}
			if err := s.commit(r); err != nil {
				t.Fatal("invalid pending fixture", err)
			}
			s.close()
			before := recoveryLeaseSnapshot(t, b.StateDir)
			next, owner := handoffSuccessor(binding)
			if l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil); err == nil {
				l.Close()
				t.Fatal("pending predecessor accepted")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("rejection erased pending authority")
			}
		})
	}
}

func TestOwnerRecoveryHandoffPrivateTypes(t *testing.T) {
	f := OwnerRecoveryHandoffFacts{PriorScopeSHA256: "private", ScopeSHA256: "private", PriorRecordSHA256: "private", RecordSHA256: "private", AuthoritySHA256: "private"}
	for _, value := range []any{f, handoffProof(f, true)} {
		body, err := json.Marshal(value)
		if err != nil || string(body) != "{}" {
			t.Fatalf("exported proof serialization: %s %v", body, err)
		}
	}
}

func TestOwnerRecoveryHandoffScopeAndProofBounds(t *testing.T) {
	for _, kind := range []string{"same_incarnation", "skipped_incarnation", "ancestor_ledger", "different_run", "different_launch", "different_producer", "prior_producer", "producer_not_launch", "launch_dot", "launch_parent", "launch_slash", "launch_backslash", "launch_nul", "next_invalid_identity", "prior_invalid_identity"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			next, owner := handoffSuccessor(binding)
			switch kind {
			case "same_incarnation":
				next.Incarnation = binding.Scope.Incarnation
			case "skipped_incarnation":
				next.Incarnation++
			case "ancestor_ledger":
				binding.Scope.Incarnation++
				next.Incarnation++
			case "different_run":
				next.RunID += "-other"
			case "different_launch":
				next.LaunchID += "-other"
				next.ProducerID = next.LaunchID
			case "different_producer":
				next.ProducerID += "-other"
			case "prior_producer":
				binding.Scope.ProducerID += "-other"
			case "producer_not_launch":
				binding.Scope.ProducerID += "-other"
				next.ProducerID = binding.Scope.ProducerID
			case "launch_dot":
				binding.Scope.LaunchID = "."
			case "launch_parent":
				binding.Scope.LaunchID = ".."
			case "launch_slash":
				binding.Scope.LaunchID = "a/b"
			case "launch_backslash":
				binding.Scope.LaunchID = "a\\b"
			case "launch_nul":
				binding.Scope.LaunchID = "a\x00b"
			case "next_invalid_identity":
				owner.PublicKey = "invalid"
			case "prior_invalid_identity":
				binding.Owner.Birth = ""
			}
			if strings.HasPrefix(kind, "launch_") {
				binding.Scope.ProducerID = binding.Scope.LaunchID
				next.LaunchID, next.ProducerID = binding.Scope.LaunchID, binding.Scope.LaunchID
			}
			before := recoveryLeaseSnapshot(t, b.StateDir)
			if l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil); err == nil {
				l.Close()
				t.Fatal("scope/identity bounds accepted")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("scope rejection changed files")
			}
		})
	}
	for _, kind := range []string{"prior_scope", "next_scope", "prior_record", "next_record", "authority", "digest_empty", "digest_nonhex", "digest_uppercase", "applied_prior"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			next, owner := handoffSuccessor(binding)
			l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil)
			if err != nil {
				t.Fatal(err)
			}
			facts, err := l.Facts()
			if err != nil {
				t.Fatal(err)
			}
			l.Close()
			proof := handoffProof(facts, false)
			switch kind {
			case "prior_scope":
				proof.PriorScopeSHA256 = strings.Repeat("f", 64)
			case "next_scope":
				proof.ScopeSHA256 = strings.Repeat("f", 64)
			case "prior_record":
				proof.PriorRecordSHA256 = strings.Repeat("f", 64)
			case "next_record":
				proof.RecordSHA256 = strings.Repeat("f", 64)
			case "authority":
				proof.AuthoritySHA256 = strings.Repeat("f", 64)
			case "digest_empty":
				proof.PriorRecordSHA256 = ""
			case "digest_nonhex":
				proof.RecordSHA256 = strings.Repeat("g", 64)
			case "digest_uppercase":
				proof.AuthoritySHA256 = strings.ToUpper(proof.AuthoritySHA256)
			case "applied_prior":
				proof.Applied = true
			}
			before := recoveryLeaseSnapshot(t, b.StateDir)
			if l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, &proof); err == nil {
				l.Close()
				t.Fatal("nonexact persisted proof accepted")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("proof rejection changed files")
			}
		})
	}
}

func TestOwnerRecoveryHandoffNeverCreatesMissingFiles(t *testing.T) {
	for _, kind := range []string{"state", "spool", "launch", "lock", "ledger"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			next, owner := handoffSuccessor(binding)
			path := map[string]string{"state": b.StateDir, "spool": filepath.Dir(b.SpoolRoot), "launch": b.SpoolRoot, "lock": filepath.Join(b.SpoolRoot, recoveryLockName), "ledger": filepath.Join(b.SpoolRoot, recoveryLedgerName)}[kind]
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(b.StateDir)
			before := recoveryLeaseSnapshot(t, root)
			if l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil); err == nil {
				l.Close()
				t.Fatal("missing authority accepted")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, root)) {
				t.Fatal("missing directory/lock/ledger was recreated")
			}
		})
	}
}

func TestOwnerRecoveryHandoffHoldsExistingLock(t *testing.T) {
	b, binding, s := ownerRecoveryLeaseFixture(t)
	next, owner := handoffSuccessor(binding)
	if l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil); err == nil {
		l.Close()
		t.Fatal("writer lock bypassed")
	}
	s.close()
	l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 2; i++ {
		if other, err := OpenExistingOwnerRecoveryLease(b.StateDir, binding.Scope, binding.Owner); err == nil {
			other.Close()
			t.Fatal("handoff lock did not exclude competing readonly lease")
		}
	}
	facts, err := l.Facts()
	if err != nil {
		t.Fatal(err)
	}
	if err = l.CommitPrepared(handoffProof(facts, false)); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if reopened, err := OpenExistingOwnerRecoveryLease(b.StateDir, next, owner); err != nil {
		t.Fatal("close did not release exact original lock", err)
	} else {
		reopened.Close()
	}
}

func TestOwnerRecoveryHandoffConcurrentOpen(t *testing.T) {
	b, binding, s := ownerRecoveryLeaseFixture(t)
	s.close()
	next, owner := handoffSuccessor(binding)
	type result struct {
		lease *OwnerRecoveryHandoffLease
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil)
			results <- result{l, err}
		}()
	}
	close(start)
	winners, rejected := 0, 0
	var winner *OwnerRecoveryHandoffLease
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err == nil && r.lease != nil {
			winners++
			winner = r.lease
			defer r.lease.Close()
		} else {
			rejected++
			if r.lease != nil {
				r.lease.Close()
			}
		}
	}
	if winners != 1 || rejected != 1 {
		t.Fatalf("winners=%d rejected=%d", winners, rejected)
	}
	if _, err := winner.Facts(); err != nil {
		t.Fatal("failed contender released winning flock", err)
	}
}

func TestOwnerRecoveryHandoffRejectsUnsafeExistingAuthority(t *testing.T) {
	for _, kind := range []string{"state_mode", "spool_mode", "launch_mode", "ledger_mode", "lock_mode", "ledger_symlink", "lock_symlink", "ledger_hardlink", "lock_hardlink", "ancestor_symlink", "ledger_authority", "ledger_unknown", "ledger_duplicate", "ledger_noncanonical", "ledger_truncated", "ledger_oversize", "ledger_legacy"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			next, owner := handoffSuccessor(binding)
			path := filepath.Join(b.SpoolRoot, recoveryLedgerName)
			lock := filepath.Join(b.SpoolRoot, recoveryLockName)
			state := b.StateDir
			r, body := handoffReadRecord(t, path)
			var err error
			switch kind {
			case "state_mode":
				err = os.Chmod(b.StateDir, 0755)
			case "spool_mode":
				err = os.Chmod(filepath.Dir(b.SpoolRoot), 0755)
			case "launch_mode":
				err = os.Chmod(b.SpoolRoot, 0755)
			case "ledger_mode":
				err = os.Chmod(path, 0644)
			case "lock_mode":
				err = os.Chmod(lock, 0644)
			case "ledger_symlink", "lock_symlink":
				target := path
				if kind == "lock_symlink" {
					target = lock
				}
				err = os.Rename(target, target+".original")
				if err == nil {
					err = os.Symlink(target+".original", target)
				}
			case "ledger_hardlink":
				err = os.Link(path, path+".extra")
			case "lock_hardlink":
				err = os.Link(lock, lock+".extra")
			case "ancestor_symlink":
				alias := filepath.Join(filepath.Dir(b.StateDir), "handoff-alias")
				err = os.Symlink(filepath.Dir(b.StateDir), alias)
				state = filepath.Join(alias, filepath.Base(b.StateDir))
			case "ledger_authority":
				r.AuthoritySHA256 = strings.Repeat("f", 64)
				body, _ = json.Marshal(r)
				body = append(body, '\n')
			case "ledger_unknown":
				body = bytes.Replace(body, []byte(`"version":2`), []byte(`"version":2,"unknown":true`), 1)
			case "ledger_duplicate":
				body = bytes.Replace(body, []byte(`"version":2`), []byte(`"version":2,"version":2`), 1)
			case "ledger_noncanonical":
				body = append([]byte(" "), body...)
			case "ledger_truncated":
				body = body[:len(body)/2]
			case "ledger_oversize":
				body = bytes.Repeat([]byte("x"), 4097)
			case "ledger_legacy":
				r.Version = 1
				body, _ = json.Marshal(r)
				body = append(body, '\n')
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(kind, "ledger_") && kind != "ledger_mode" && kind != "ledger_symlink" && kind != "ledger_hardlink" {
				if err = os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := recoveryLeaseSnapshot(t, filepath.Dir(b.StateDir))
			if l, err := OpenExistingOwnerRecoveryHandoff(state, binding.Scope, binding.Owner, next, owner, nil); err == nil {
				l.Close()
				t.Fatal("unsafe existing authority accepted")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, filepath.Dir(b.StateDir))) {
				t.Fatal("unsafe authority rejection changed files")
			}
		})
	}
}

func TestOwnerRecoveryHandoffLateDriftIsSticky(t *testing.T) {
	for _, kind := range []string{"state", "spool", "launch", "lock", "ledger", "bytes", "mtime"} {
		t.Run(kind, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			next, owner := handoffSuccessor(binding)
			l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			facts, err := l.Facts()
			if err != nil {
				t.Fatal(err)
			}
			proof := handoffProof(facts, false)
			paths := map[string]string{"state": b.StateDir, "spool": filepath.Dir(b.SpoolRoot), "launch": b.SpoolRoot, "lock": filepath.Join(b.SpoolRoot, recoveryLockName), "ledger": filepath.Join(b.SpoolRoot, recoveryLedgerName), "bytes": filepath.Join(b.SpoolRoot, recoveryLedgerName), "mtime": filepath.Join(b.SpoolRoot, recoveryLedgerName)}
			path := paths[kind]
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			var body []byte
			if !info.IsDir() {
				body, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "bytes" {
				mutated := bytes.Replace(body, []byte(`"attempts":0`), []byte(`"attempts":1`), 1)
				if err = os.WriteFile(path, mutated, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			} else if kind == "mtime" {
				if err = os.Chtimes(path, info.ModTime(), info.ModTime().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if info.IsDir() {
					err = os.Mkdir(path, 0700)
				} else {
					err = os.WriteFile(path, body, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			before := recoveryLeaseSnapshot(t, filepath.Dir(b.StateDir))
			if _, err = l.Facts(); err == nil {
				t.Fatal("late drift accepted")
			}
			if err = l.CommitPrepared(proof); err == nil {
				t.Fatal("failed lease wrote")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, filepath.Dir(b.StateDir))) {
				t.Fatal("failed lease repaired/reset changed paths")
			}
			if kind == "bytes" || kind == "mtime" {
				if err = os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(path+".original", path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = l.Facts(); err == nil {
				t.Fatal("restored pathname revived poisoned lease")
			}
		})
	}
}

func handoffStages(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".owner-recovery-handoff-") {
			result = append(result, filepath.Join(dir, e.Name()))
		}
	}
	return result
}

func TestOwnerRecoveryHandoffPublicationFailurePreservesScene(t *testing.T) {
	for _, point := range []string{"before_create", "created", "written", "stage_synced", "before_rename", "renamed", "directory_synced", "actual_write_closed", "actual_sync_closed"} {
		t.Run(point, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			next, owner := handoffSuccessor(binding)
			l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil)
			if err != nil {
				t.Fatal(err)
			}
			facts, err := l.Facts()
			if err != nil {
				t.Fatal(err)
			}
			proof := handoffProof(facts, false)
			path := filepath.Join(b.SpoolRoot, recoveryLedgerName)
			_, prior := handoffReadRecord(t, path)
			called := false
			l.publicationHook = func(at string, stage *os.File) error {
				if point == "actual_write_closed" && at == "created" || point == "actual_sync_closed" && at == "written" {
					called = true
					return stage.Close()
				}
				if at == point {
					called = true
					return syscall.EIO
				}
				return nil
			}
			if err = l.CommitPrepared(proof); err == nil || !called {
				t.Fatalf("fault not observed: called=%t err=%v", called, err)
			}
			if _, err = l.Facts(); err == nil {
				t.Fatal("failed publication retained trusted lease")
			}
			if err = l.CommitPrepared(proof); err == nil {
				t.Fatal("sticky failure retried publication")
			}
			l.Close()
			actual, err := os.ReadFile(path)
			if err != nil {
				t.Fatal("uncertain failure deleted ledger", err)
			}
			nextPublished := point == "renamed" || point == "directory_synced"
			if nextPublished {
				if handoffDigest(actual) != facts.RecordSHA256 {
					t.Fatal("postrename failure restored prior bytes")
				}
			} else if !bytes.Equal(actual, prior) {
				t.Fatal("prepublication failure changed prior ledger")
			}
			stages := handoffStages(t, b.SpoolRoot)
			wantStages := 1
			if point == "before_create" || nextPublished {
				wantStages = 0
			}
			if len(stages) != wantStages {
				t.Fatalf("stage scene=%v want=%d", stages, wantStages)
			}
			if len(stages) == 1 {
				info, err := os.Stat(stages[0])
				if err != nil || !privateRecoveryInfo(info, false) {
					t.Fatalf("unsafe retained stage: %v", err)
				}
			}
			before := recoveryLeaseSnapshot(t, b.StateDir)
			reopened, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, &proof)
			if err != nil {
				t.Fatal("prepared exact scene not recoverable", err)
			}
			reopened.Close()
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("reopen rewrote/GCed uncertain scene")
			}
		})
	}
}

func TestOwnerRecoveryHandoffPublicationRejectsDrift(t *testing.T) {
	for _, point := range []string{"stage_synced", "before_rename", "renamed"} {
		for _, kind := range []string{"inode", "bytes", "hardlink", "mtime"} {
			t.Run(point+"/"+kind, func(t *testing.T) {
				b, binding, s := ownerRecoveryLeaseFixture(t)
				s.close()
				next, owner := handoffSuccessor(binding)
				l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				facts, err := l.Facts()
				if err != nil {
					t.Fatal(err)
				}
				proof := handoffProof(facts, false)
				path := filepath.Join(b.SpoolRoot, recoveryLedgerName)
				_, prior := handoffReadRecord(t, path)
				l.publicationHook = func(at string, stage *os.File) error {
					if at != point {
						return nil
					}
					target := filepath.Join(b.SpoolRoot, stage.Name())
					if point == "renamed" {
						target = path
					}
					switch kind {
					case "inode":
						body, err := os.ReadFile(target)
						if err != nil {
							return err
						}
						if err = os.Rename(target, target+".original"); err != nil {
							return err
						}
						return os.WriteFile(target, body, 0600)
					case "bytes":
						return os.WriteFile(target, []byte("untrusted publication\n"), 0600)
					case "hardlink":
						return os.Link(target, target+".linked")
					case "mtime":
						info, err := os.Stat(target)
						if err != nil {
							return err
						}
						return os.Chtimes(target, info.ModTime(), info.ModTime().Add(time.Second))
					}
					return nil
				}
				if err = l.CommitPrepared(proof); err == nil {
					t.Fatal("publication drift accepted")
				}
				if _, err = l.Facts(); err == nil {
					t.Fatal("drift did not poison lease")
				}
				actual, err := os.ReadFile(path)
				if err != nil {
					t.Fatal("uncertain ledger was deleted", err)
				}
				if point != "renamed" && !bytes.Equal(actual, prior) {
					t.Fatal("prepublication drift changed original ledger")
				}
				if point == "renamed" && bytes.Equal(actual, prior) {
					t.Fatal("postrename drift silently restored original ledger")
				}
				before := recoveryLeaseSnapshot(t, b.StateDir)
				l.Close()
				if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
					t.Fatal("Close modified uncertain scene")
				}
			})
		}
	}
}

func TestOwnerRecoveryHandoffCommitRequiresExactFacts(t *testing.T) {
	for _, field := range []string{"prior_scope", "next_scope", "prior_record", "next_record", "authority", "applied_prior"} {
		t.Run(field, func(t *testing.T) {
			b, binding, s := ownerRecoveryLeaseFixture(t)
			s.close()
			next, owner := handoffSuccessor(binding)
			l, err := OpenExistingOwnerRecoveryHandoff(b.StateDir, binding.Scope, binding.Owner, next, owner, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			facts, err := l.Facts()
			if err != nil {
				t.Fatal(err)
			}
			proof := handoffProof(facts, false)
			switch field {
			case "prior_scope":
				proof.PriorScopeSHA256 = strings.Repeat("f", 64)
			case "next_scope":
				proof.ScopeSHA256 = strings.Repeat("f", 64)
			case "prior_record":
				proof.PriorRecordSHA256 = strings.Repeat("f", 64)
			case "next_record":
				proof.RecordSHA256 = strings.Repeat("f", 64)
			case "authority":
				proof.AuthoritySHA256 = strings.Repeat("f", 64)
			case "applied_prior":
				proof.Applied = true
			}
			before := recoveryLeaseSnapshot(t, b.StateDir)
			if err = l.CommitPrepared(proof); err == nil {
				t.Fatal("commit accepted a different persisted intent/applied proof")
			}
			if !reflect.DeepEqual(before, recoveryLeaseSnapshot(t, b.StateDir)) {
				t.Fatal("nonexact commit modified ledger")
			}
		})
	}
}
