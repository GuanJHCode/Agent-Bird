//go:build darwin || linux

package diagnostics

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

func closedDeletionFixture(t *testing.T) (string, Scope) {
	t.Helper()
	root, scope := storageRoot(t), storageScope()
	w, err := OpenWriter(context.Background(), root, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Append(context.Background(), Record{SegmentID: "delete", Stream: Stdout, Data: []byte("durable diagnostics")}); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	return root, scope
}

func TestDeletionClosedNamespaceReplay(t *testing.T) {
	ctx := context.Background()
	root, scope := closedDeletionFixture(t)
	l, err := OpenDeletion(ctx, root, scope, "delete-command", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := l.Plan()
	if len(plan) == 0 || l.MarkerBytes() == 0 || l.RemovedBytes() == 0 {
		t.Fatalf("empty deletion evidence: %d %d %d", len(plan), l.MarkerBytes(), l.RemovedBytes())
	}
	if err = l.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err = l.VerifyRemoved(ctx); err != nil {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenReader(ctx, root, scope); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("retired reader error %v", err)
	}
	replay, err := OpenDeletion(ctx, root, scope, "delete-command", plan)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	if replay.RemovedBytes() != l.RemovedBytes() {
		t.Fatalf("replay removed bytes %d, want %d", replay.RemovedBytes(), l.RemovedBytes())
	}
	if err = replay.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err = replay.VerifyRemoved(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDeletionPlanMaximumInventoryFitsBound(t *testing.T) {
	scope := storageScope()
	p := deletionPlan{Version: 1, Scope: scope, CommandID: "maximum-command", Stage: ".delete-0123456789abcdef0123456789abcdef.stage"}
	for i := 0; i < SlotCount; i++ {
		p.Files = append(p.Files, deletionFile{Name: slotName(i), ID: deletionIdentity{Size: ChunkBytes}, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	}
	p.Files = append(p.Files, deletionFile{Name: "manifest.json", ID: deletionIdentity{Size: ManifestBytes}, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"})
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Name < p.Files[j].Name })
	raw, err := canonicalPlan(p)
	if err != nil || len(raw) > deletionPlanLimit {
		t.Fatalf("maximum plan %d %v", len(raw), err)
	}
}

func TestDeletionRejectsLiveUnknownAndReplacement(t *testing.T) {
	ctx := context.Background()
	root := storageRoot(t)
	w, err := OpenWriter(ctx, root, storageScope())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenDeletion(ctx, root, storageScope(), "live-command", nil); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("live writer error %v", err)
	}
	if err = w.Close(ctx); err != nil {
		t.Fatal(err)
	}

	hash, err := storageScope().Hash()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, hash)
	if err = os.WriteFile(filepath.Join(dir, "unexpected"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenDeletion(ctx, root, storageScope(), "unknown-command", nil); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("unknown file error %v", err)
	}
	if err = os.Remove(filepath.Join(dir, "unexpected")); err != nil {
		t.Fatal(err)
	}

	l, err := OpenDeletion(ctx, root, storageScope(), "replace-command", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.ns.step = func(point string) error {
		if point != "deletion:before-unlink-manifest.json" {
			return nil
		}
		if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0600)
	}
	if err = l.Remove(ctx); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("replacement error %v", err)
	}
}

func TestDeletionTornStageRecoveryAndForeignStageRefusal(t *testing.T) {
	ctx := context.Background()
	root, scope := closedDeletionFixture(t)
	fresh, err := OpenDeletion(ctx, root, scope, "torn-command", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, marker := fresh.Plan(), append([]byte(nil), fresh.markerRaw...)
	p, _, err := decodeDeletionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = fresh.Close(); err != nil {
		t.Fatal(err)
	}
	hash, err := scope.Hash()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, hash)
	if err = os.WriteFile(filepath.Join(dir, p.Stage), marker[:len(marker)/2], 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenDeletion(ctx, root, scope, "torn-command", plan)
	if err != nil {
		t.Fatal(err)
	}
	if err = replay.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err = replay.Close(); err != nil {
		t.Fatal(err)
	}

	root, scope = closedDeletionFixture(t)
	fresh, err = OpenDeletion(ctx, root, scope, "foreign-command", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan = fresh.Plan()
	if err = fresh.Close(); err != nil {
		t.Fatal(err)
	}
	hash, err = scope.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, hash, ".delete-ffffffffffffffffffffffffffffffff.stage"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenDeletion(ctx, root, scope, "foreign-command", plan); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("foreign stage %v", err)
	}
}

// This helper intentionally terminates the test child after a durable marker
// and an actual unlink.  It is a process boundary, not an in-process returned
// fault, so the parent exercises the only replay evidence a crash leaves.
func TestDeletionCrashHelper(t *testing.T) {
	if os.Getenv("DIAGNOSTICS_DELETE_CRASH") != "1" {
		return
	}
	plan, err := base64.RawStdEncoding.DecodeString(os.Getenv("DIAGNOSTICS_DELETE_PLAN"))
	if err != nil {
		os.Exit(92)
	}
	l, err := OpenDeletion(context.Background(), os.Getenv("DIAGNOSTICS_DELETE_ROOT"), storageScope(), "crash-command", plan)
	if err != nil {
		os.Exit(93)
	}
	l.ns.step = func(point string) error {
		if point == "deletion:after-unlink-chunk-000.bin" {
			os.Exit(91)
		}
		return nil
	}
	_ = l.Remove(context.Background())
	os.Exit(94)
}

func TestDeletionCrashReplayAfterChildWait(t *testing.T) {
	ctx := context.Background()
	root, scope := closedDeletionFixture(t)
	l, err := OpenDeletion(ctx, root, scope, "crash-command", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := l.Plan()
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDeletionCrashHelper$")
	cmd.Env = append(os.Environ(), "DIAGNOSTICS_DELETE_CRASH=1", "DIAGNOSTICS_DELETE_ROOT="+root, "DIAGNOSTICS_DELETE_PLAN="+base64.RawStdEncoding.EncodeToString(plan))
	err = cmd.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 91 {
		t.Fatalf("child wait %v", err)
	}
	p, _, err := decodeDeletionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := scope.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, hash, "manifest.json")); err != nil {
		t.Fatalf("manifest after partial crash %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, hash, p.Files[0].Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first planned chunk after crash %v", err)
	}
	replay, err := OpenDeletion(ctx, root, scope, "crash-command", plan)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	if err = replay.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err = replay.VerifyRemoved(ctx); err != nil {
		t.Fatal(err)
	}
}
