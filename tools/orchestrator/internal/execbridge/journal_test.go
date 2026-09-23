package execbridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestExecutorJournalRequiresDurableBoundExit(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(parent, "codex-executor")
	if err := VerifyExecutorExit(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing journal: %v", err)
	}
	spec := process.Command{Path: "/bin/cat", PinnedPath: "/bin/cat", PinnedSHA256: "fixture", Dir: parent}
	j, err := newExecutorJournal(path, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	if err := VerifyExecutorExit(path); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("intent-only released: %v", err)
	}
	if _, err := newExecutorJournal(path, spec); err == nil {
		t.Fatal("reused launch intent")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	spec.PinnedPath, spec.PinnedSHA256, spec.StdinFile = "", "", reader
	handle, err := process.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	defer handle.Stop(context.Background())
	defer writer.Close()
	if err := j.spawned(handle.Identity()); err != nil {
		t.Fatal(err)
	}
	var identity executorSpawned
	if _, err := j.read("spawned.json", &identity); err != nil || identity.KernelStart == "" {
		t.Fatalf("missing kernel birth identity: %v", err)
	}
	if err := j.exited(handle); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("live executor released: %v", err)
	}
	if err := VerifyExecutorExit(path); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("spawn-only released: %v", err)
	}
	writer.Close()
	if err := handle.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := j.exited(handle); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExecutorExit(path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"intent.json", "spawned.json", "exited.json"} {
		info, err := os.Lstat(filepath.Join(path, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("receipt permissions: %s %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "spawned.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExecutorExit(path); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("tampered identity released: %v", err)
	}
}

func TestExecutorJournalRejectsAliasesReplacementAndForgedReceipt(t *testing.T) {
	for _, attack := range []string{"symlink", "hardlink", "mode", "replacement", "receipt"} {
		t.Run(attack, func(t *testing.T) {
			parent, _ := filepath.EvalSymlinks(t.TempDir())
			path := filepath.Join(parent, "codex-executor")
			j, err := newExecutorJournal(path, process.Command{Path: "/bin/cat", Dir: parent})
			if err != nil {
				t.Fatal(err)
			}
			defer j.close()
			intent := filepath.Join(path, "intent.json")
			switch attack {
			case "symlink":
				if err := os.Rename(intent, filepath.Join(parent, "intent")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(parent, "intent"), intent); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(intent, filepath.Join(parent, "alias")); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "receipt":
				if err := os.WriteFile(filepath.Join(path, "exited.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := VerifyExecutorExit(path); !errors.Is(err, process.ErrProcessTreeUnknown) {
				t.Fatalf("forged evidence accepted: %v", err)
			}
			if attack == "replacement" || attack == "mode" {
				if err := j.spawned(process.Identity{PID: 1, PGID: 1, Birth: "fixture", BirthKnown: true}); err == nil {
					t.Fatal("writer followed replaced/unsafe directory")
				}
			}
		})
	}
}
