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

func TestPrepareStateDirRejectsSymlinkInsertedAfterAncestorOpen(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	root := filepath.Join(base, "missing", "state")
	old := prepareStateDirBeforeCreate
	prepareStateDirBeforeCreate = func() {
		if err := os.Symlink(outside, filepath.Join(base, "missing")); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { prepareStateDirBeforeCreate = old }()
	if err := PrepareStateDir(root); err == nil {
		t.Fatal("symlink replacement accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "state")); !os.IsNotExist(err) {
		t.Fatalf("created outside state: %v", err)
	}
}
func TestPrepareStateDirRejectsSymlinkLeafAndRegularFile(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"file", "link"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(base, name)
			var err error
			if name == "file" {
				err = os.WriteFile(path, []byte("x"), 0600)
			} else {
				err = os.Symlink(base, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = PrepareStateDir(path); err == nil {
				t.Fatal("unsafe leaf accepted")
			}
		})
	}
}

func TestPrepareStateDirAllowsExistingTmpAlias(t *testing.T) {
	root := filepath.Join("/tmp", "agent-bird-state-"+filepath.Base(t.TempDir()))
	defer os.RemoveAll(root)
	if err := PrepareStateDir(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("info=%v err=%v", info, err)
	}
}

func TestPrepareStateDirRejectsLeafModeSwap(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	old := prepareStateDirAfterLeafLstat
	prepareStateDirAfterLeafLstat = func() { _ = os.Chmod(root, 0755) }
	defer func() { prepareStateDirAfterLeafLstat = old }()
	if err := PrepareStateDir(root); err == nil {
		t.Fatal("mode swap accepted")
	}
}
func TestPrepareStateDirRejectsInsertedPublicSuffix(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "mid", "state")
	old := prepareStateDirBeforeOpenPart
	prepareStateDirBeforeOpenPart = func() { _ = os.Mkdir(filepath.Join(base, "mid"), 0700); _ = os.Chmod(filepath.Join(base, "mid"), 0755) }
	defer func() { prepareStateDirBeforeOpenPart = old }()
	if err := PrepareStateDir(root); err == nil {
		t.Fatal("public suffix accepted")
	}
}
func TestPrepareStateDirRejectsAncestorSwap(t *testing.T) {
	base, out := t.TempDir(), t.TempDir()
	root := filepath.Join(base, "state")
	old := prepareStateDirAfterAncestorStat
	prepareStateDirAfterAncestorStat = func() { _ = os.Rename(base, base+"-old"); _ = os.Symlink(out, base) }
	defer func() { prepareStateDirAfterAncestorStat = old }()
	if err := PrepareStateDir(root); err == nil {
		t.Fatal("ancestor swap accepted")
	}
}

func TestPrepareStateDirRejectsFinalSameInodeSymlinkLeaf(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "state")
	held := root + "-held"
	old := prepareStateDirBeforeFinalCheck
	prepareStateDirBeforeFinalCheck = func() {
		if err := os.Rename(root, held); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(held, root); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { prepareStateDirBeforeFinalCheck = old }()
	if err := PrepareStateDir(root); err == nil {
		t.Fatal("same-inode symlink leaf accepted")
	}
}
