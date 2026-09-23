package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestProviderUnknownNeverPublishesSlotRelease(t *testing.T) {
	for _, phase := range []string{"verification", "cleanup", "cancel_before_spawn", "preflight_unknown", "preflight_cleanup_unknown"} {
		t.Run(phase, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			h, err := NewIPC(filepath.Join(root, "spool"), "provider-unknown")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			command := process.Command{Path: "/bin/sh", Dir: root, Args: []string{"-c", "exit 0"}}
			calls := 0
			meta := launchMetadata{closeProvider: func() error { return process.ErrProcessTreeUnknown }}
			if phase == "verification" {
				meta.closeProvider = func() error { return nil }
				meta.verifyProvider = func() error {
					calls++
					if calls == 2 {
						return process.ErrProcessTreeUnknown
					}
					return nil
				}
			}
			if phase == "cancel_before_spawn" {
				meta.prepareProvider = func(context.Context) (process.Command, error) { cancel(); return command, nil }
			}
			if phase == "preflight_unknown" {
				meta.verifyProvider = func() error { return process.ErrProcessTreeUnknown }
				meta.closeProvider = func() error { return nil }
			}
			if phase == "preflight_cleanup_unknown" {
				meta.verifyProvider = func() error { return errors.New("provider_context_changed") }
			}
			a := store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment", Status: "running"}
			result, err := h.execute(ctx, a, "run", "task", command, false, meta)
			if result.Status != "unknown" || !errors.Is(err, process.ErrProcessTreeUnknown) {
				t.Errorf("auxiliary ownership was released: status=%s err=%v", result.Status, err)
			}
			spool, err := h.spool(a)
			if err != nil {
				t.Fatal(err)
			}
			records, err := spool.Read()
			if err != nil {
				t.Fatal(err)
			}
			unknown := false
			for _, record := range records {
				switch record.Kind {
				case contract.EventExited, contract.EventStopped, contract.EventFailed, contract.EventResult:
					t.Errorf("unsafe terminal event before auxiliary cleanup: %s", record.Kind)
				case contract.EventUnknown:
					unknown = true
				}
			}
			if !unknown {
				t.Error("no durable UNKNOWN evidence")
			}
		})
	}
}

func TestProviderContextVerifiedBeforeSpawnAndAfterExit(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			marker, config := filepath.Join(root, "started"), filepath.Join(root, "config")
			initial := "original"
			if phase == "before" {
				initial = "changed"
			}
			_ = os.WriteFile(config, []byte(initial), 0600)
			h, err := NewIPC(filepath.Join(root, "spool"), "verification-test")
			if err != nil {
				t.Fatal(err)
			}
			metadata := launchMetadata{verifyProvider: func() error {
				data, err := os.ReadFile(config)
				if err != nil || string(data) != "original" {
					return errors.New("provider_context_changed")
				}
				return nil
			}}
			command := process.Command{Path: "/bin/sh", Dir: root, Args: []string{"-c", `touch "$1"; printf changed > "$2"`, "fixture", marker, config}}
			result, err := h.execute(context.Background(), store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment", Status: "running"}, "run", "task", command, false, metadata)
			if err == nil || result.Status == "result_ready" {
				t.Fatalf("unverified context accepted: %s %v", result.Status, err)
			}
			_, statErr := os.Stat(marker)
			if (statErr == nil) != (phase == "after") {
				t.Fatal("preflight failed to prevent spawn")
			}
		})
	}
}

func TestProviderCleanupFailureCannotAcceptResult(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	h, err := NewIPC(filepath.Join(root, "spool"), "cleanup-test")
	if err != nil {
		t.Fatal(err)
	}
	metadata := launchMetadata{closeProvider: func() error { return nil }}
	// A real unremovable alias is represented by a nonempty directory: cleanup
	// must report it, rather than treating an unexpected node as already detached.
	home := &codexHome{path: filepath.Join(root, "home"), source: filepath.Join(root, "source")}
	if err := os.MkdirAll(filepath.Join(home.path, "auth.json"), 0700); err != nil {
		t.Fatal(err)
	}
	state := &codexRuntimeState{home: home}
	metadata.closeProvider = state.close
	result, err := h.execute(context.Background(), store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment", Status: "running"}, "run", "task", process.Command{Path: "/bin/sh", Dir: root, Args: []string{"-c", "printf completed"}}, false, metadata)
	if err == nil || result.Status == "result_ready" {
		t.Fatal("failed credential cleanup accepted a result")
	}
}

func TestCodexFailedPreparationReportsUnremovedAuthAlias(t *testing.T) {
	if root := os.Getenv("BIRD_CLEANUP_FIXTURE_ROOT"); root != "" {
		h, err := NewIPC(filepath.Join(root, "spool"), "cleanup-fixture")
		if err != nil {
			t.Fatal(err)
		}
		meta := launchMetadata{prepareProvider: func(context.Context) (process.Command, error) {
			_, err := createCodexHome(filepath.Join(root, "source"), filepath.Join(root, "scratch"), []byte("invalid [ toml"))
			return process.Command{}, err
		}}
		result, err := h.execute(context.Background(), store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment", Status: "running"}, "run", "task", process.Command{Path: "/bin/sh", Args: []string{"-c", "exit 0"}, Dir: root}, false, meta)
		if err == nil || result.Status != "unknown" || !strings.Contains(err.Error(), "codex_auth_cleanup_failed") {
			t.Fatalf("unremoved auth alias not reported unknown: %s %v", result.Status, err)
		}
		return
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"source", "scratch"} {
		if err := os.Mkdir(filepath.Join(root, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"config.toml", "auth.json"} {
		if err := os.WriteFile(filepath.Join(root, "source", name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	policy := `(version 1)(allow default)(deny file-write-unlink (literal ` + strconv.Quote(filepath.Join(root, "scratch/codex-home/auth.json")) + `))`
	cmd := exec.Command("/usr/bin/sandbox-exec", "-p", policy, os.Args[0], "-test.run=^TestCodexFailedPreparationReportsUnremovedAuthAlias$")
	cmd.Env = append(os.Environ(), "BIRD_CLEANUP_FIXTURE_ROOT="+root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cleanup fixture: %v\n%s", err, output)
	}
}
