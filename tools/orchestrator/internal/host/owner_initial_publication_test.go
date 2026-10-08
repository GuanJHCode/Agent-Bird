package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type initialPublicationFixture struct {
	db          *store.DB
	root        string
	key         ed25519.PrivateKey
	request     store.OriginalOwnerResumeRequest
	observation store.OriginalOwnerSuccessionObservation
	resolved    store.InitialOwnerRecoveryContext
}

// This fixture authenticates through real Store APIs but uses synthetic process
// observations. Native PID death/original Wait is a separate Coordinator test.
func newInitialPublicationFixture(t *testing.T) initialPublicationFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err = db.BeginCoordinatorEpoch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	plan := store.PlanSpec{Run: store.RunSpec{ID: "initial-run", ControllerThread: "controller", PlanRevision: 1, OriginContextID: "origin", OriginPID: 7, OriginBirth: "origin-birth"}, Host: store.HostLaunchSpec{ExecutionOwnerMode: "supervised-v1", OriginContextID: "origin", HostGeneration: "origin", Executable: "/private/host"}, Tasks: []store.TaskSpec{{ID: "initial-task", RunID: "initial-run", MaxAttempts: 1, AdapterPayload: json.RawMessage(`{"provider":"grok-build"}`)}}}
	submitted, err := db.SubmitPlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	hello := contract.HostHello{LaunchID: submitted.LaunchID, LaunchToken: submitted.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "origin-birth", HostGeneration: "origin", PID: 101, Birth: "host-birth", Executable: "/private/host"}
	if _, err = db.RegisterHost(ctx, hello, 1); err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("c", 32)))
	keyID, _ := contract.OriginalOwnerKeyID(key.Public().(ed25519.PublicKey))
	token := strings.Repeat("ab", 32)
	tokenHash, _ := contract.OriginalOwnerBootstrapSHA256(token)
	intent, err := db.PrepareOriginalOwner(ctx, store.PrepareOriginalOwnerSpec{Host: hello, Epoch: 1, ExecutableSHA256: strings.Repeat("e", 64), BootstrapID: "bootstrap", BootstrapTokenSHA256: tokenHash, CoordinatorKeyID: keyID, RequestID: "initial", RequestSHA256: strings.Repeat("f", 64), StartupDeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	ownerKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("o", 32)))
	owner := contract.OriginalOwnerIdentity{PID: 202, Birth: "owner-birth", PublicKey: hex.EncodeToString(ownerKey.Public().(ed25519.PublicKey))}
	_, err = db.BindOriginalOwner(ctx, store.BindOriginalOwnerSpec{Hello: contract.OriginalOwnerHello{Scope: intent.Scope, BootstrapID: intent.BootstrapID, BootstrapToken: token, Owner: owner, FirstHost: intent.FirstHost, CoordinatorKeyID: keyID}, Host: hello, Epoch: 1, Authenticated: store.OriginalOwnerAuthentication{Owner: owner, OriginPID: 7, OriginBirth: "origin-birth", ExecutableSHA256: intent.Scope.ExecutableSHA256}}, key)
	if err != nil {
		t.Fatal(err)
	}
	request := store.OriginalOwnerResumeRequest{TaskID: "initial-task", ControllerThread: "controller", ControlToken: submitted.ControlToken, WorkRevision: 1, CommandID: "initial-recovery"}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	observation := store.OriginalOwnerSuccessionObservation{OwnerID: intent.Scope.OwnerID, Owner: contract.OriginalOwnerTransport{PID: owner.PID, Birth: owner.Birth}, Host: intent.FirstHost, Origin: store.RunOwnerBinding{OriginContextID: "origin", OriginPID: 7, OriginBirth: "origin-birth"}, Epoch: 1, ExecutableSHA256: intent.Scope.ExecutableSHA256, OwnerExitedAt: now, HostExitedAt: now}
	resolved, err := db.ResolveInitialOwnerRecovery(ctx, request, observation, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	return initialPublicationFixture{db, root, key, request, observation, resolved}
}

func (f initialPublicationFixture) open(t *testing.T, proof *store.InitialOwnerRecoveryProof) *InitialOwnerRecoveryPublication {
	t.Helper()
	l, err := OpenInitialOwnerRecoveryPublication(f.root, f.resolved.RootAuthority, f.resolved.Binding.Scope, f.resolved.Binding.Owner, proof)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.Close)
	return l
}
func (f initialPublicationFixture) prepare(t *testing.T, l *InitialOwnerRecoveryPublication) store.InitialOwnerRecoveryProof {
	t.Helper()
	facts, err := l.Facts()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := f.db.PrepareInitialOwnerRecovery(context.Background(), f.request, f.observation, facts, f.key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func TestInitialOwnerPublicationReplaysOneBoundStage(t *testing.T) {
	for _, point := range []string{"stage_opened", "before_stage_sync", "before_publish", "after_publish", "directory_synced"} {
		t.Run(point, func(t *testing.T) {
			f := newInitialPublicationFixture(t)
			if _, err := OpenExistingOwnerRecoveryLease(f.root, f.resolved.Binding.Scope, f.resolved.Binding.Owner); err == nil {
				t.Fatal("ordinary existing-only lease initialized absent state")
			}
			l := f.open(t, nil)
			proof := f.prepare(t, l)
			injected := errors.New("crash seam")
			l.publicationHook = func(at string, stage *os.File) error {
				if at != point {
					return nil
				}
				if point == "stage_opened" {
					if _, err := stage.Write(l.body[:len(l.body)/2]); err != nil {
						return err
					}
					if err := stage.Sync(); err != nil {
						return err
					}
				}
				return injected
			}
			if err := l.PublishPrepared(proof); !errors.Is(err, injected) {
				t.Fatalf("missing fault %s: %v", point, err)
			}
			body := append([]byte(nil), l.body...)
			l.Close()
			for i := 0; i < 2; i++ {
				replayed := f.open(t, &proof)
				if err := replayed.PublishPrepared(proof); err != nil {
					t.Fatal(err)
				}
				if err := replayed.ValidatePublished(); err != nil {
					t.Fatal(err)
				}
				facts, err := replayed.Facts()
				if err != nil || facts != proof.Facts {
					t.Fatal("replay reminted file authority", err)
				}
				replayed.Close()
			}
			path := filepath.Join(f.root, "host-spool", f.resolved.Binding.Scope.LaunchID, recoveryLedgerName)
			actual, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(actual, body) {
				t.Fatal("noncanonical ledger publication", err)
			}
			stages, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".initial-owner-*.new"))
			if err != nil || len(stages) != 0 {
				t.Fatal("stage growth across replay", stages, err)
			}
			if _, err = f.db.FinalizeInitialOwnerRecovery(context.Background(), f.request, f.observation, proof.Facts, f.key.Public().(ed25519.PublicKey)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInitialOwnerPublicationRejectsAuthorityAndForeignState(t *testing.T) {
	for _, mode := range []string{"root_before_open", "database_before_open", "prepared_lock_missing", "proof_stage_drift", "foreign_stage", "foreign_ledger", "spent_ledger", "root_before_publish", "database_before_publish"} {
		t.Run(mode, func(t *testing.T) {
			f := newInitialPublicationFixture(t)
			if mode == "root_before_open" || mode == "database_before_open" {
				path := f.root
				if mode == "database_before_open" {
					path = filepath.Join(f.root, "state.db")
				}
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(path); _ = os.Rename(path+".original", path) })
				if mode == "root_before_open" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
				before := recoveryLeaseSnapshot(t, f.root)
				if lease, err := OpenInitialOwnerRecoveryPublication(f.root, f.resolved.RootAuthority, f.resolved.Binding.Scope, f.resolved.Binding.Owner, nil); err == nil {
					lease.Close()
					t.Fatal("replaced authority accepted")
				}
				after := recoveryLeaseSnapshot(t, f.root)
				if !reflectInitialSnapshots(before, after) {
					t.Fatal("replacement touched before rejection")
				}
				return
			}
			l := f.open(t, nil)
			proof := f.prepare(t, l)
			dir := l.file.directory
			switch mode {
			case "prepared_lock_missing":
				l.Close()
				if err := os.Remove(filepath.Join(dir, recoveryLockName)); err != nil {
					t.Fatal(err)
				}
			case "proof_stage_drift":
				l.Close()
				l = f.open(t, &proof)
				proof.StageName = ".initial-owner-" + strings.Repeat("0", 64) + ".new"
				if err := l.PublishPrepared(proof); err == nil {
					t.Fatal("changed prepared stage accepted")
				}
				if _, err := os.Lstat(filepath.Join(dir, proof.StageName)); !os.IsNotExist(err) {
					t.Fatal("foreign proof stage touched", err)
				}
				return
			case "foreign_stage":
				if err := os.WriteFile(filepath.Join(dir, proof.StageName), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign_ledger":
				if err := os.WriteFile(filepath.Join(dir, recoveryLedgerName), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "spent_ledger":
				var record ownerRecoveryRecord
				if err := json.Unmarshal(l.body, &record); err != nil {
					t.Fatal(err)
				}
				record.Attempts = 1
				body, _ := json.Marshal(record)
				if err := os.WriteFile(filepath.Join(dir, recoveryLedgerName), append(body, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "root_before_publish", "database_before_publish":
				path := f.root
				if mode == "database_before_publish" {
					path = filepath.Join(f.root, "state.db")
				}
				l.publicationHook = func(point string, _ *os.File) error {
					if point != "before_publish" {
						return nil
					}
					if err := os.Rename(path, path+".original"); err != nil {
						return err
					}
					t.Cleanup(func() { _ = os.RemoveAll(path); _ = os.Rename(path+".original", path) })
					if mode == "root_before_publish" {
						return os.Mkdir(path, 0700)
					}
					return os.WriteFile(path, []byte("foreign"), 0600)
				}
			}
			if mode == "prepared_lock_missing" {
				if lease, err := OpenInitialOwnerRecoveryPublication(f.root, f.resolved.RootAuthority, f.resolved.Binding.Scope, f.resolved.Binding.Owner, &proof); err == nil {
					lease.Close()
					t.Fatal("recreated missing prepared lock")
				}
				if _, err := os.Lstat(filepath.Join(dir, recoveryLockName)); !os.IsNotExist(err) {
					t.Fatal("missing lock recreated", err)
				}
			} else if err := l.PublishPrepared(proof); err == nil {
				t.Fatal("foreign/drifted state published")
			}
		})
	}
}

func reflectInitialSnapshots(a, b map[string]recoveryLeaseSnapshotEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for path, value := range a {
		if next, ok := b[path]; !ok || next != value {
			return false
		}
	}
	return true
}
