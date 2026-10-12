//go:build darwin || linux

package artifacts

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeletionRemovesDataAndPreventsResurrection(t *testing.T) {
	root, intake, body := fixture(t)
	proof, err := Materialize(context.Background(), root, intake)
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenDeletion(context.Background(), root, intake, proof, "request-one", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := d.Plan()
	if len(plan) == 0 || d.MarkerBytes() != 0 {
		t.Fatal("fresh deletion plan missing or unexpectedly marked", len(plan), d.MarkerBytes())
	}
	if err = d.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.RemovedBytes() != int64(len(body)) || d.MarkerBytes() == 0 {
		t.Fatal("incorrect accounting", d.RemovedBytes(), d.MarkerBytes())
	}
	if _, err = os.Lstat(proof.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("data remains after deletion", err)
	}
	if err = d.VerifyRemoved(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Materialize(context.Background(), root, intake); err == nil {
		t.Fatal("deletion marker allowed materialize to recreate data")
	}
	if err = VerifyAndCommit(context.Background(), root, intake, proof, func() error { return nil }); err == nil {
		t.Fatal("deletion marker allowed verified commit")
	}
	replay, err := OpenDeletion(context.Background(), root, intake, proof, "request-one", plan)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	if err = replay.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = replay.VerifyRemoved(context.Background()); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(filepath.Dir(proof.Path), "deletion.marker"))
	if err != nil || !bytes.Equal(marker, plan) {
		t.Fatal("marker is not the durable deletion plan", err)
	}
}

func TestDeletionRejectsRecreatedDataAndPartialMarker(t *testing.T) {
	root, intake, body := fixture(t)
	proof, err := Materialize(context.Background(), root, intake)
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenDeletion(context.Background(), root, intake, proof, "request-two", nil)
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
	if err = os.WriteFile(proof.Path, append([]byte(nil), body...), 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenDeletion(context.Background(), root, intake, proof, "request-two", plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = replay.Remove(context.Background()); err == nil {
		t.Fatal("recreated data was deleted")
	}
	_ = replay.Close()
	if err = os.Remove(proof.Path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(proof.Path), "deletion.stage"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenDeletion(context.Background(), root, intake, proof, "request-two", plan); err == nil {
		t.Fatal("partial marker was accepted")
	}
}
