package instance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareStateDirDoesNotChmodExistingDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareStateDir(root); err == nil {
		t.Fatal("expected untrusted_state_dir")
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("mode changed to %o", info.Mode().Perm())
	}
}

func TestPrepareStateDirCreatesPrivateDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new", "state")
	if err := PrepareStateDir(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestAcquireEpochIsExclusiveAndIncrements(t *testing.T) {
	root := t.TempDir()
	root = filepath.Join(root, "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	first, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Epoch() != 1 {
		t.Fatalf("epoch=%d", first.Epoch())
	}
	if _, err := Acquire(root); err == nil {
		t.Fatal("expected busy")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.Epoch() != 2 {
		t.Fatalf("epoch=%d", second.Epoch())
	}
}
