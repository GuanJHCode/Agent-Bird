//go:build darwin || linux

package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaterializeByteBoundariesAndPrivateDestination(t *testing.T) {
	for _, size := range []int{0, int(MaxBytes), int(MaxBytes) + 1} {
		t.Run(stringSize(size), func(t *testing.T) {
			root, s, _ := fixture(t)
			if err := os.Chmod(root, 0755); err != nil {
				t.Fatal(err)
			}
			body := bytes.Repeat([]byte("x"), size)
			sum := sha256.Sum256(body)
			s.Size = int64(size)
			s.SHA256 = hex.EncodeToString(sum[:])
			if err := os.WriteFile(s.SourcePath, body, 0600); err != nil {
				t.Fatal(err)
			}
			p, err := Materialize(context.Background(), root, s)
			if size > int(MaxBytes) {
				if err == nil {
					t.Fatal("oversized artifact admitted")
				}
				if _, err = os.Lstat(filepath.Join(root, "managed-artifacts")); !os.IsNotExist(err) {
					t.Fatal("invalid descriptor created a namespace", err)
				}
				return
			}
			if err != nil || p.Size != int64(size) {
				t.Fatal(p, err)
			}
			for _, path := range []string{filepath.Join(root, "managed-artifacts"), filepath.Dir(p.Path)} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatal("managed directory not private", path, err)
				}
			}
		})
	}
}
func stringSize(n int) string {
	if n == 0 {
		return "empty"
	}
	if int64(n) == MaxBytes {
		return "maximum"
	}
	return "oversized"
}

func TestVerifiedCommitHoldsLockAndCancellationReleasesIt(t *testing.T) {
	root, s, _ := fixture(t)
	proof, err := Materialize(context.Background(), root, s)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- VerifyAndCommit(ctx, root, s, proof, func() error { close(entered); <-ctx.Done(); return ctx.Err() })
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("callback not reached", err)
	case <-time.After(time.Second):
		t.Fatal("verification stalled")
	}
	blocked, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err = Materialize(blocked, root, s)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("callback released the original artifact lock", err)
	}
	cancel()
	select {
	case err = <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("original verified commit did not join")
	}
	again, err := Materialize(context.Background(), root, s)
	if err != nil || again != proof {
		t.Fatal("cancelled commit leaked its lock", again, err)
	}
}

func TestVerifiedCommitDoesNotRecreateMissingObject(t *testing.T) {
	root, s, _ := fixture(t)
	proof, err := Materialize(context.Background(), root, s)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(proof.Path)
	if err = os.Rename(dir, dir+"-original"); err != nil {
		t.Fatal(err)
	}
	called := false
	err = VerifyAndCommit(context.Background(), root, s, proof, func() error { called = true; return nil })
	if err == nil || called {
		t.Fatal("missing object gained commit authority", err)
	}
	if _, err = os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("verifier recreated missing namespace", err)
	}
}

func TestMaterializeRejectsWritableAnchorAndDescriptorConflict(t *testing.T) {
	root, s, _ := fixture(t)
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(context.Background(), root, s); err == nil {
		t.Fatal("writable anchor admitted")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	proof, err := Materialize(context.Background(), root, s)
	if err != nil {
		t.Fatal(err)
	}
	changed := s
	changed.SourcePath = filepath.Join(s.SourceRoot, "other")
	called := false
	if err = VerifyAndCommit(context.Background(), root, changed, proof, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("foreign descriptor verified", err)
	}
}
