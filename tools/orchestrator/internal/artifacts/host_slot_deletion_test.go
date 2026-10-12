//go:build darwin || linux

package artifacts

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func hostNamespaceProof(t *testing.T, root string) HostSlotNamespaceProof {
	t.Helper()
	var rootStat, parentStat unix.Stat_t
	if err := unix.Stat(root, &rootStat); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat(filepath.Join(root, "managed-artifacts"), &parentStat); err != nil {
		t.Fatal(err)
	}
	return HostSlotNamespaceProof{Version: 1, RootPath: root, RootDevice: uint64(rootStat.Dev), RootInode: uint64(rootStat.Ino), ParentDevice: uint64(parentStat.Dev), ParentInode: uint64(parentStat.Ino)}
}

func TestHostSlotDeletionRemovesDataAndPreventsResurrection(t *testing.T) {
	root := hostSlotRoot(t)
	body := []byte("terminal result")
	desc := hostSlot(t, "terminal", "terminal", 0, body)
	proof, err := MaterializeHostSlot(context.Background(), root, desc, body)
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenHostSlotDeletion(context.Background(), root, desc, proof, "request-one", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := d.Plan()
	if len(plan) == 0 || d.MarkerBytes() != 0 {
		t.Fatal("fresh deletion plan missing or unexpectedly marked")
	}
	if err = d.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.RemovedBytes() != int64(len(body)) || d.MarkerBytes() == 0 {
		t.Fatal("incorrect accounting")
	}
	if err = d.VerifyRemoved(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = MaterializeHostSlot(context.Background(), root, desc, body); err == nil {
		t.Fatal("deletion marker allowed materialize to recreate data")
	}
	if _, _, _, err = LoadHostSlot(context.Background(), root, desc.HostSlotBinding); err == nil {
		t.Fatal("deletion marker allowed load to recover data")
	}
	replay, err := OpenHostSlotDeletion(context.Background(), root, desc, proof, "request-one", plan)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	if err = replay.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(filepath.Dir(proof.Path), "deletion.marker"))
	if err != nil || !bytes.Equal(marker, plan) {
		t.Fatal("marker is not the durable deletion plan", err)
	}
}

func TestHostSlotDeletionRejectsForeignAndRecreatedData(t *testing.T) {
	root := hostSlotRoot(t)
	body := []byte("progress")
	desc := hostSlot(t, "progress", "progress", 0, body)
	proof, err := MaterializeHostSlot(context.Background(), root, desc, body)
	if err != nil {
		t.Fatal(err)
	}
	wrong := desc
	wrong.Kind = "progress"
	wrong.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err = OpenHostSlotDeletion(context.Background(), root, wrong, proof, "request-two", nil); err == nil {
		t.Fatal("foreign descriptor accepted")
	}
	d, err := OpenHostSlotDeletion(context.Background(), root, desc, proof, "request-two", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := d.Plan()
	if err = d.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(proof.Path, body, 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenHostSlotDeletion(context.Background(), root, desc, proof, "request-two", plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = replay.Remove(context.Background()); err == nil {
		t.Fatal("recreated data was deleted")
	}
	if err = replay.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHostSlotDeletionRejectsRootAndObjectReplacement(t *testing.T) {
	for _, replacement := range []string{"root", "object"} {
		t.Run(replacement, func(t *testing.T) {
			root := hostSlotRoot(t)
			body := []byte("terminal")
			desc := hostSlot(t, "terminal", "terminal", 0, body)
			proof, err := MaterializeHostSlot(context.Background(), root, desc, body)
			if err != nil {
				t.Fatal(err)
			}
			if replacement == "root" {
				moved := root + "-moved"
				if err = os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				id, err := desc.ObjectID()
				if err != nil {
					t.Fatal(err)
				}
				object := filepath.Join(root, "managed-artifacts", id)
				if err = os.Rename(object, object+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(object, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = OpenHostSlotDeletion(context.Background(), root, desc, proof, "request-four", nil); err == nil {
				t.Fatal("replacement namespace accepted")
			}
		})
	}
}

func TestHostSlotDeletionHoldsWriterLock(t *testing.T) {
	root := hostSlotRoot(t)
	body := []byte("terminal")
	desc := hostSlot(t, "terminal", "terminal", 0, body)
	proof, err := MaterializeHostSlot(context.Background(), root, desc, body)
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenHostSlotDeletion(context.Background(), root, desc, proof, "request-three", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := OpenHostSlotDeletion(ctx, root, desc, proof, "request-three", d.Plan()); done <- e }()
	select {
	case e := <-done:
		t.Fatal("writer lock was not held", e)
	default:
	}
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("waiting opener did not cancel", err)
	}
}

func TestHostSlotBindingDeletionAbsentAndManagedDiscovery(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		root := hostSlotRoot(t)
		if err := os.Mkdir(filepath.Join(root, "managed-artifacts"), 0700); err != nil {
			t.Fatal(err)
		}
		binding := hostSlot(t, "progress", "progress", 0, []byte("x")).HostSlotBinding
		d, plan, err := OpenHostSlotBindingDeletion(context.Background(), root, binding, hostNamespaceProof(t, root), HostSlotAbsentOnly, "request-five", nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Mode != string(HostSlotAbsentOnly) || plan.PlannedRemovedBytes != 0 {
			t.Fatal("wrong absent plan", plan)
		}
		if err = d.Remove(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = d.VerifyRemoved(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = d.Close(); err != nil {
			t.Fatal(err)
		}
		saved := d.Plan()
		if _, decodeErr := decodeHostSlotBindingPlan(saved); decodeErr != nil {
			t.Fatalf("bad saved plan %v", decodeErr)
		}
		replay, replayPlan, err := OpenHostSlotBindingDeletion(context.Background(), root, binding, hostNamespaceProof(t, root), HostSlotAbsentOnly, "request-five", saved)
		if err != nil || replayPlan != plan {
			t.Fatalf("exact absent replay rejected %v plan=%+v", err, plan)
		}
		if err = replay.VerifyRemoved(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = replay.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = MaterializeHostSlot(context.Background(), root, hostSlot(t, "progress", "progress", 0, []byte("x")), []byte("x")); err == nil {
			t.Fatal("absent-slot tombstone allowed materialization")
		}
	})
	t.Run("managed-orphan", func(t *testing.T) {
		root := hostSlotRoot(t)
		body := []byte("orphan")
		desc := hostSlot(t, "progress", "progress", 0, body)
		if _, err := MaterializeHostSlot(context.Background(), root, desc, body); err != nil {
			t.Fatal(err)
		}
		ns := hostNamespaceProof(t, root)
		if _, _, err := OpenHostSlotBindingDeletion(context.Background(), root, desc.HostSlotBinding, ns, HostSlotAbsentOnly, "request-six", nil); !errors.Is(err, ErrHostSlotUnowned) {
			t.Fatal("old unowned slot admitted", err)
		}
		d, plan, err := OpenHostSlotBindingDeletion(context.Background(), root, desc.HostSlotBinding, ns, HostSlotDiscoverManaged, "request-six", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if plan.Mode != "published_orphan" || plan.Descriptor == nil || plan.Proof == nil || plan.PlannedRemovedBytes != int64(len(body)) {
			t.Fatal("wrong managed discovery plan", plan)
		}
		if err = d.Remove(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = d.VerifyRemoved(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}
