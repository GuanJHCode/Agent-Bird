package instance

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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
	parent, err := os.MkdirTemp("/tmp", "agent-bird-state-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(parent)
	root := filepath.Join(parent, "state")
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
	prepareStateDirAfterLeafLstat = func() {
		if err := os.Chmod(root, 0755); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { prepareStateDirAfterLeafLstat = old }()
	if err := PrepareStateDir(root); err == nil {
		t.Fatal("mode swap accepted")
	}
}
func TestPrepareStateDirRejectsInsertedPublicSuffix(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "mid", "state")
	old := prepareStateDirBeforeCreate
	prepareStateDirBeforeCreate = func() {
		if err := os.Mkdir(filepath.Join(base, "mid"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { prepareStateDirBeforeCreate = old }()
	if err := PrepareStateDir(root); err == nil {
		t.Fatal("public suffix accepted")
	}
}
func TestPrepareStateDirRejectsAncestorSwap(t *testing.T) {
	fixture := t.TempDir()
	base, out := filepath.Join(fixture, "ancestor"), filepath.Join(fixture, "outside")
	for _, dir := range []string{base, out} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(base, "state")
	old := prepareStateDirAfterAncestorStat
	prepareStateDirAfterAncestorStat = func() {
		if err := os.Rename(base, base+"-old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(out, base); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { prepareStateDirAfterAncestorStat = old }()
	if err := PrepareStateDir(root); err == nil {
		t.Fatal("ancestor swap accepted")
	}
	if _, err := os.Stat(filepath.Join(out, "state")); !os.IsNotExist(err) {
		t.Fatalf("created state outside original ancestor: %v", err)
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

func TestPrepareStateDirRejectsPrivateLeafReplacement(t *testing.T) {
	fixture := t.TempDir()
	root := filepath.Join(fixture, "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	old := prepareStateDirAfterLeafLstat
	prepareStateDirAfterLeafLstat = func() {
		if err := os.Rename(root, filepath.Join(fixture, "original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { prepareStateDirAfterLeafLstat = old }()
	if err := PrepareStateDir(root); err == nil {
		t.Fatal("replacement private leaf accepted")
	}
}

func TestPrepareStateDirDoesNotBlockOnReplacedAncestorFIFO(t *testing.T) {
	fixture := t.TempDir()
	ancestor := filepath.Join(fixture, "ancestor")
	if err := os.Mkdir(ancestor, 0700); err != nil {
		t.Fatal(err)
	}
	old := prepareStateDirAfterAncestorStat
	// The created FIFO is owned exclusively by this test and is explicitly
	// unblocked on RED so the calling goroutine is joined before cleanup.
	prepareStateDirAfterAncestorStat = func() {
		if err := os.Rename(ancestor, filepath.Join(fixture, "original")); err != nil {
			panic(err)
		}
		if err := syscall.Mkfifo(ancestor, 0600); err != nil {
			panic(err)
		}
	}
	defer func() { prepareStateDirAfterAncestorStat = old }()
	done := make(chan error, 1)
	go func() { done <- PrepareStateDir(filepath.Join(ancestor, "state")) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO ancestor accepted")
		}
	case <-time.After(500 * time.Millisecond):
		fd, err := syscall.Open(ancestor, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal("cannot unblock owned FIFO", err)
		}
		defer syscall.Close(fd)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("owned FIFO open did not join")
		}
		t.Fatal("ancestor open blocked on substituted FIFO")
	}
}
