//go:build darwin || linux

package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func hostSlot(t *testing.T, class, kind string, index int, body []byte) HostSlotDescriptor {
	t.Helper()
	sum := sha256.Sum256(body)
	return HostSlotDescriptor{HostSlotBinding: HostSlotBinding{Version: 1, ReservationID: "reservation-1", ScopeSHA256: stringsRepeat("a", 64), Class: class, Index: index}, Kind: kind, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}

func stringsRepeat(s string, n int) string { // keeps fixture imports focused on artifact values.
	v := ""
	for i := 0; i < n; i++ {
		v += s
	}
	return v
}

func hostSlotRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestHostSlotTerminalSharesFirstDescriptor(t *testing.T) {
	root := hostSlotRoot(t)
	terminal := hostSlot(t, "terminal", "terminal", 0, []byte("terminal"))
	proof, err := MaterializeHostSlot(context.Background(), root, terminal, []byte("terminal"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = MaterializeHostSlot(context.Background(), root, terminal, []byte("terminal")); err != nil {
		t.Fatal(err)
	}
	prelaunch := hostSlot(t, "terminal", "prelaunch", 0, []byte("prelaunch"))
	if _, err = MaterializeHostSlot(context.Background(), root, prelaunch, []byte("prelaunch")); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal/prelaunch shared slot accepted", err)
	}
	if err = VerifyHostSlotAndCommit(context.Background(), root, terminal, proof, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestHostSlotProgressBoundsAndBindingLoad(t *testing.T) {
	root := hostSlotRoot(t)
	body := make([]byte, HostProgressSlotBytes)
	desc := hostSlot(t, "progress", "progress", 20, body)
	if _, err := MaterializeHostSlot(context.Background(), root, desc, body); err != nil {
		t.Fatal(err)
	}
	wrong := desc.HostSlotBinding
	wrong.Index = 19
	if _, _, _, err := LoadHostSlot(context.Background(), root, wrong); err == nil {
		t.Fatal("foreign progress slot loaded")
	}
	over := hostSlot(t, "progress", "progress", 0, make([]byte, HostProgressSlotBytes+1))
	if _, err := MaterializeHostSlot(context.Background(), root, over, make([]byte, HostProgressSlotBytes+1)); err == nil {
		t.Fatal("oversized progress accepted")
	}
	id, _ := over.ObjectID()
	if _, err := os.Lstat(filepath.Join(root, "managed-artifacts", id)); !os.IsNotExist(err) {
		t.Fatal("invalid progress created files", err)
	}
}

func TestHostSlotRequiresPrivateSpoolRootAndRejectsRootDrift(t *testing.T) {
	root, body := hostSlotRoot(t), []byte("private root")
	desc := hostSlot(t, "terminal", "terminal", 0, body)
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeHostSlot(context.Background(), root, desc, body); err == nil {
		t.Fatal("readable spool root admitted")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	proof, err := MaterializeHostSlot(context.Background(), root, desc, body)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err = VerifyHostSlotAndCommit(context.Background(), root, desc, proof, func() error { return nil }); err == nil {
		t.Fatal("root mode drift committed")
	}
}

func TestHostSlotLoadRecoversCompleteStageAndRejectsPartial(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "partial"}[partial], func(t *testing.T) {
			root, body := hostSlotRoot(t), []byte("durable host bytes")
			desc := hostSlot(t, "terminal", "prelaunch", 0, body)
			raw, err := desc.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			id, _ := desc.ObjectID()
			n, err := openHostObjectMode(root, id, true)
			if err != nil {
				t.Fatal(err)
			}
			if err = n.lock(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err = n.ensureIntentLimit(context.Background(), raw, MaxHostSlotManifestBytes); err != nil {
				t.Fatal(err)
			}
			stage := body
			if partial {
				stage = stage[:len(stage)-1]
			}
			if err = n.writeHostStage(context.Background(), desc, stage); err != nil && !partial {
				t.Fatal(err)
			}
			if partial { // A short staged write models a process crash after its first write.
				f, _, openErr := n.regular("data.stage", unix.O_RDWR, desc.limit(), true)
				if openErr != nil {
					t.Fatal(openErr)
				}
				if _, openErr = f.Write(stage); openErr == nil {
					openErr = f.Sync()
				}
				_ = f.Close()
				if openErr != nil {
					t.Fatal(openErr)
				}
			}
			if err = n.close(); err != nil {
				t.Fatal(err)
			}
			_, _, got, err := LoadHostSlot(context.Background(), root, desc.HostSlotBinding)
			if partial {
				if err == nil {
					t.Fatal("partial stage loaded")
				}
				return
			}
			if err != nil || string(got) != string(body) {
				t.Fatal(string(got), err)
			}
		})
	}
}

func TestHostSlotVerifiedCommitRejectsReplacementAndMissingNamespace(t *testing.T) {
	root, body := hostSlotRoot(t), []byte("original")
	desc := hostSlot(t, "terminal", "terminal", 0, body)
	proof, err := MaterializeHostSlot(context.Background(), root, desc, body)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(proof.Path, []byte("changed!"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err = VerifyHostSlotAndCommit(context.Background(), root, desc, proof, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("replacement committed", err)
	}
	if err = os.RemoveAll(filepath.Dir(proof.Path)); err != nil {
		t.Fatal(err)
	}
	if err = VerifyHostSlotAndCommit(context.Background(), root, desc, proof, func() error { called = true; return nil }); err == nil {
		t.Fatal("missing namespace recreated")
	}
}

func TestHostSlotVerifiedCommitHoldsLockAndCancels(t *testing.T) {
	root, body := hostSlotRoot(t), []byte("lock")
	desc := hostSlot(t, "terminal", "terminal", 0, body)
	proof, err := MaterializeHostSlot(context.Background(), root, desc, body)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- VerifyHostSlotAndCommit(ctx, root, desc, proof, func() error { close(entered); <-ctx.Done(); return ctx.Err() })
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback not entered")
	}
	blocked, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err = MaterializeHostSlot(blocked, root, desc, body)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("slot writer lock leaked", err)
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLoadHostSlotRejectsReplacementAfterReadingOriginal(t *testing.T) {
	root, body := hostSlotRoot(t), []byte("same bytes cannot replace inode")
	desc := hostSlot(t, "terminal", "terminal", 0, body)
	proof, err := MaterializeHostSlot(context.Background(), root, desc, body)
	if err != nil {
		t.Fatal(err)
	}
	replaced := false
	_, _, _, err = loadHostSlot(context.Background(), root, desc.HostSlotBinding, func(point string) error {
		if point != "data.bin:after-read" || replaced {
			return nil
		}
		replaced = true
		if err := os.Rename(proof.Path, proof.Path+".old"); err != nil {
			return err
		}
		return os.WriteFile(proof.Path, body, 0600)
	})
	if err == nil || !replaced {
		t.Fatal("replaced data.bin returned after proof", err)
	}
}

func TestHostSlotPartialStageCanOnlyBeRewrittenWithOriginalBytes(t *testing.T) {
	root, body := hostSlotRoot(t), []byte("original durable body")
	desc := hostSlot(t, "terminal", "prelaunch", 0, body)
	raw, err := desc.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := desc.ObjectID()
	n, err := openHostObjectMode(root, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = n.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = n.ensureIntentLimit(context.Background(), raw, MaxHostSlotManifestBytes); err != nil {
		t.Fatal(err)
	}
	f, _, err := n.regular("data.stage", unix.O_RDWR, desc.limit(), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(body[:3]); err == nil {
		err = f.Sync()
	}
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = n.close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = LoadHostSlot(context.Background(), root, desc.HostSlotBinding); err == nil {
		t.Fatal("partial stage loaded without original bytes")
	}
	if _, err = MaterializeHostSlot(context.Background(), root, desc, []byte("different durable body")); !errors.Is(err, ErrContent) {
		t.Fatal("different bytes rewrote partial stage", err)
	}
	if _, err = MaterializeHostSlot(context.Background(), root, desc, body); err != nil {
		t.Fatal("original bytes did not repair partial stage", err)
	}
}

func TestHostSlotPublicationRecoversEveryDurableBoundary(t *testing.T) {
	points := []string{
		"intent.stage:before-truncate", "intent.stage:after-write", "intent.stage:after-sync",
		"intent.stage:before-rename", "intent.stage:after-rename", "intent.stage:before-dir-sync", "intent.stage:after-dir-sync",
		"data.stage:before-truncate", "data.stage:after-write", "data.stage:after-sync",
		"data.stage:before-rename", "data.stage:after-rename", "data.stage:before-dir-sync", "data.stage:after-dir-sync",
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			root, body := hostSlotRoot(t), []byte("same bytes across every interrupted publication")
			desc := hostSlot(t, "terminal", "terminal", 0, body)
			injected := errors.New("durable boundary")
			hit := false
			proof, err := materializeHostSlot(context.Background(), root, desc, body, func(at string) error {
				if at == point {
					hit = true
					return injected
				}
				return nil
			})
			if !hit || !errors.Is(err, injected) || proof != (Proof{}) {
				t.Fatal("boundary emitted a receipt", hit, proof, err)
			}
			proof, err = MaterializeHostSlot(context.Background(), root, desc, body)
			if err != nil || proof.SHA256 != desc.SHA256 {
				t.Fatal("same descriptor did not recover", proof, err)
			}
		})
	}
}

// A new open during publication must not turn unverified staged bytes into an
// immutable final object. The original descriptor remains the content authority.
func TestHostSlotPublicationRejectsChangedStageBeforeOpening(t *testing.T) {
	for _, from := range []string{"intent.stage", "data.stage"} {
		t.Run(from, func(t *testing.T) {
			root, body := hostSlotRoot(t), []byte("original slot content")
			desc := hostSlot(t, "terminal", "terminal", 0, body)
			id, _ := desc.ObjectID()
			dir := filepath.Join(root, "managed-artifacts", id)
			hit := false
			proof, err := materializeHostSlot(context.Background(), root, desc, body, func(at string) error {
				if at != from+":after-sync" {
					return nil
				}
				hit = true
				return os.WriteFile(filepath.Join(dir, from), []byte("foreign"), 0600)
			})
			if !hit || err == nil || proof != (Proof{}) {
				t.Fatal("changed stage returned proof", hit, proof, err)
			}
			to := "data.bin"
			if from == "intent.stage" {
				to = "intent.json"
			}
			if _, err = os.Lstat(filepath.Join(dir, to)); !os.IsNotExist(err) {
				t.Fatal("unverified stage became immutable", to, err)
			}
			if _, err = MaterializeHostSlot(context.Background(), root, desc, body); err != nil {
				t.Fatal("same authorized bytes could not recover", err)
			}
		})
	}
}

func TestHostSlotVerifiedSnapshotDoesNotAcquireWriterLock(t *testing.T) {
	root := hostSlotRoot(t)
	body := []byte("readonly evidence")
	d := hostSlot(t, "terminal", "terminal", 0, body)
	p, err := MaterializeHostSlot(context.Background(), root, d, body)
	if err != nil {
		t.Fatal(err)
	}
	// The Store consumer may hold its DB mutex while a commit owns this lock.
	// A snapshot must not wait for that writer, mutate, or manufacture authority.
	err = VerifyHostSlotAndCommit(context.Background(), root, d, p, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		got, err := ReadVerifiedHostSlot(ctx, root, d, p)
		if err != nil || string(got) != string(body) {
			t.Fatal("snapshot waited on writer or changed bytes", string(got), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"proof", "replacement", "stage"} {
		t.Run(mode, func(t *testing.T) {
			root := hostSlotRoot(t)
			p, err := MaterializeHostSlot(context.Background(), root, d, body)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "proof":
				p.Inode++
			case "replacement":
				if err = os.Rename(p.Path, p.Path+".old"); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(p.Path, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(p.Path + ".old"); err != nil {
					t.Fatal(err)
				}
			case "stage":
				if err = os.Rename(p.Path, filepath.Join(filepath.Dir(p.Path), "data.stage")); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ReadVerifiedHostSlot(context.Background(), root, d, p)
			if err == nil || got != nil {
				t.Fatal("unverified snapshot returned bytes", string(got), err)
			}
			if mode == "stage" {
				if _, err = os.Stat(p.Path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("readonly consumer recovered publication", err)
				}
			}
		})
	}
}
