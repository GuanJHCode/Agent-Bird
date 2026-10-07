package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerRuntimeDirectoriesCreatePrivateAnchoredSpool(t *testing.T) {
	e, _, _ := ownerEnrollmentFixture(t)
	b := e.bootstrap
	if err := os.Chmod(b.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	dirs, err := openOwnerRuntimeDirectories(b)
	if err != nil {
		t.Fatal(err)
	}
	defer dirs.close()
	if err = dirs.verify(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{b.StateDir, filepath.Join(b.StateDir, "host-spool"), b.SpoolRoot} {
		info, err := os.Lstat(path)
		if err != nil || !privateRecoveryInfo(info, true) {
			t.Fatal("runtime created unsafe directory", path, err)
		}
	}
	entries, err := os.ReadDir(b.SpoolRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("directory setup wrote ledger/spool before its owner", err)
	}
	reopened, err := openOwnerRuntimeDirectories(b)
	if err != nil {
		t.Fatal("idempotent private directory reopen rejected", err)
	}
	reopened.close()
}

func TestOwnerRuntimeDirectoriesRejectUnsafeAncestorsBeforeSpoolWrites(t *testing.T) {
	for _, kind := range []string{"state_mode", "parent_symlink", "parent_file", "parent_mode", "launch_symlink", "launch_file", "launch_mode"} {
		t.Run(kind, func(t *testing.T) {
			e, _, _ := ownerEnrollmentFixture(t)
			b := e.bootstrap
			if err := os.Chmod(b.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(b.StateDir, "host-spool")
			outside := t.TempDir()
			if err := os.Chmod(outside, 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "state_mode":
				err = os.Chmod(b.StateDir, 0755)
			case "parent_symlink":
				err = os.Symlink(outside, parent)
			case "parent_file":
				err = os.WriteFile(parent, []byte("keep"), 0600)
			case "parent_mode":
				err = os.Mkdir(parent, 0755)
			default:
				if err = os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "launch_symlink":
					err = os.Symlink(outside, b.SpoolRoot)
				case "launch_file":
					err = os.WriteFile(b.SpoolRoot, []byte("keep"), 0600)
				case "launch_mode":
					err = os.Mkdir(b.SpoolRoot, 0755)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if dirs, err := openOwnerRuntimeDirectories(b); err == nil || dirs != nil {
				if dirs != nil {
					dirs.close()
				}
				t.Fatal("unsafe runtime ancestry accepted")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("runtime followed foreign directory", err)
			}
			if _, err := os.Lstat(filepath.Join(b.SpoolRoot, recoveryLockName)); err == nil {
				t.Fatal("unsafe setup created recovery lock")
			}
		})
	}
}

func TestOwnerRuntimeDirectoriesRejectPathReplacement(t *testing.T) {
	for _, which := range []string{"state", "parent", "launch"} {
		t.Run(which, func(t *testing.T) {
			e, _, _ := ownerEnrollmentFixture(t)
			b := e.bootstrap
			if err := os.Chmod(b.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			dirs, err := openOwnerRuntimeDirectories(b)
			if err != nil {
				t.Fatal(err)
			}
			defer dirs.close()
			path := b.SpoolRoot
			if which == "parent" {
				path = filepath.Dir(path)
			}
			if which == "state" {
				path = b.StateDir
			}
			original := path + ".original"
			if err = os.Rename(path, original); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(original) })
			if err = os.MkdirAll(b.SpoolRoot, 0700); err != nil {
				t.Fatal(err)
			}
			if err = dirs.verify(); err == nil {
				t.Fatal("same-mode path clone replaced original runtime authority")
			}
		})
	}
}
