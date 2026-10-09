//go:build darwin || linux

package diagnostics

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	if err = replay.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err = replay.VerifyRemoved(ctx); err != nil {
		t.Fatal(err)
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
	if err = os.Rename(filepath.Join(dir, "manifest.json"), filepath.Join(dir, "manifest.old")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = l.Remove(ctx); !errors.Is(err, ErrUnsafeLayout) {
		t.Fatalf("replacement error %v", err)
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
		if point != "deletion:after-unlink-manifest.json" {
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
