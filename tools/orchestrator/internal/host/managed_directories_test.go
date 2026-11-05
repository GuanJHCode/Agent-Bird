package host

import (
	"bytes"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

func TestCandidatePreparesMissingParentsWithoutFiles(t *testing.T) {
	work, spec := managedPolicyFixture(t)
	spec.Paths = []string{"CHANGE.txt", "new/deep/output.txt", "new/second.txt"}
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "directories")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1}
	inv := automaticWriteInvocation{profiledInvocation: profiledInvocation{launchInvocation: launchInvocation{dir: work + "-fresh"}, profile: adapter.ExecutionProfile{Role: adapter.Implementer, Permission: adapter.WorkspaceWrite}}, workspace: spec}
	_, close, err := h.prepareCandidate(context.Background(), grant, inv, &inv.profile)
	defer close()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"new", "new/deep"} {
		info, err := os.Stat(filepath.Join(inv.dir, name))
		if err != nil || !info.IsDir() {
			t.Fatalf("missing required directory %s: %v", name, err)
		}
	}
	for _, name := range []string{"new/deep/output.txt", "new/second.txt"} {
		if _, err := os.Lstat(filepath.Join(inv.dir, name)); !os.IsNotExist(err) {
			t.Fatalf("target precreated %s: %v", name, err)
		}
	}
}

func TestManagedDeclarationConflictsRejectedBeforePreparation(t *testing.T) {
	for _, paths := range [][]string{{"foo", "foo/bar"}, {"foo/bar", "foo"}, {"Foo/a", "foo/b"}, {"A", "a"}, {"caf\u00e9/a", "cafe\u0301/b"}, {"caf\u00e9", "cafe\u0301/b"}, {"safe/new/a", ".ClAuDe/settings.json"}} {
		t.Run(paths[0]+"_"+paths[1], func(t *testing.T) {
			work, spec := managedPolicyFixture(t)
			spec.Paths = paths
			if _, _, err := managedWritePaths(context.Background(), work, spec); err == nil {
				t.Fatalf("accepted conflicting declarations %q", paths)
			}
			if _, err := os.Lstat(filepath.Join(work, "safe")); !os.IsNotExist(err) {
				t.Fatalf("partial preparation: %v", err)
			}
		})
	}
}

func TestManagedDirectoriesRejectDiskAliasesAndUnsafeTopology(t *testing.T) {
	for _, kind := range []string{"case", "unicode", "symlink", "file", "target-symlink", "escape"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			outside := t.TempDir()
			paths := []string{"safe/new/file", "new/deep/file"}
			switch kind {
			case "case":
				os.Mkdir(filepath.Join(root, "New"), 0700)
			case "unicode":
				os.Mkdir(filepath.Join(root, "cafe\u0301"), 0700)
				paths[1] = "caf\u00e9/file"
			case "symlink":
				os.Symlink(outside, filepath.Join(root, "new"))
			case "file":
				os.WriteFile(filepath.Join(root, "new"), []byte("old"), 0600)
			case "target-symlink":
				os.MkdirAll(filepath.Join(root, "new/deep"), 0700)
				os.Symlink(outside, filepath.Join(root, "new/deep/file"))
			case "escape":
				paths[1] = "../outside/file"
			}
			guard, err := prepareManagedDirectories(context.Background(), root, paths, func(string, any) error { return nil })
			if guard != nil {
				guard.Close()
			}
			if err == nil {
				t.Fatal("unsafe topology accepted")
			}
			if _, err := os.Lstat(filepath.Join(root, "safe")); !os.IsNotExist(err) {
				t.Fatalf("created before full validation: %v", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("created outside workspace: %v", err)
			}
		})
	}
}

func TestManagedDirectoryFailureRetainsIdentityAndUnknownContents(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	events := []string{}
	guard, err := prepareManagedDirectories(context.Background(), root, []string{"new/deep/file"}, func(phase string, value any) error {
		events = append(events, phase)
		if phase == "directory_created" {
			record := value.(map[string]any)
			if record["identity"] == nil || record["path"] != "new" {
				t.Fatalf("missing identity: %v", record)
			}
			if err := os.WriteFile(filepath.Join(root, "new/unknown"), []byte("retain"), 0600); err != nil {
				t.Fatal(err)
			}
			return errors.New("injected_journal_failure")
		}
		return nil
	})
	if guard != nil {
		guard.Close()
	}
	if err == nil {
		t.Fatal("injected failure ignored")
	}
	if !reflect.DeepEqual(events, []string{"directory_prepare_intent", "directory_created"}) {
		t.Fatalf("events=%v", events)
	}
	before, err := os.Stat(filepath.Join(root, "new"))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := prepareManagedDirectories(context.Background(), root, []string{"new/deep/file"}, func(string, any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	after, err := os.Stat(filepath.Join(root, "new"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("recovery replaced identity: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "new/unknown"))
	if err != nil || string(body) != "retain" {
		t.Fatal("unknown content deleted")
	}
}

func TestManagedDirectoryReplacementRejectedBeforeWorkerStarts(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "root", "target-symlink"} {
		t.Run(kind, func(t *testing.T) {
			work, spec := managedPolicyFixture(t)
			spec.Paths = []string{"new/deep/file"}
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "replace")
			if err != nil {
				t.Fatal(err)
			}
			grant := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 5000}
			dir := work + "-fresh"
			inv := automaticWriteInvocation{profiledInvocation: profiledInvocation{launchInvocation: launchInvocation{dir: dir}, profile: adapter.ExecutionProfile{Role: adapter.Implementer, Permission: adapter.WorkspaceWrite}}, workspace: spec}
			marker := filepath.Join(t.TempDir(), "worker-started")
			cmd := process.Command{Path: "/bin/sh", Args: []string{"-c", `printf started > "$1"`, "probe", marker}, Dir: dir}
			var validateWorkspace func() error
			meta := launchMetadata{validateWorkspace: &validateWorkspace}
			meta.prepareCandidate = func(ctx context.Context) (process.Command, func(context.Context, string) ([]byte, error), func(), error) {
				freeze, close, err := h.prepareCandidate(ctx, grant, inv, &inv.profile, meta.validateWorkspace)
				if err != nil {
					return cmd, freeze, close, err
				}
				switch kind {
				case "directory":
					err = os.Rename(filepath.Join(dir, "new"), filepath.Join(dir, "old"))
					if err == nil {
						err = os.MkdirAll(filepath.Join(dir, "new/deep"), 0700)
					}
				case "symlink":
					err = os.Rename(filepath.Join(dir, "new"), filepath.Join(dir, "old"))
					if err == nil {
						err = os.Symlink(t.TempDir(), filepath.Join(dir, "new"))
					}
				case "root":
					err = os.Rename(dir, dir+"-old")
					if err == nil {
						err = os.Mkdir(dir, 0700)
					}
				case "target-symlink":
					err = os.Symlink(t.TempDir(), filepath.Join(dir, "new/deep/file"))
				}
				return cmd, freeze, close, err
			}
			result, err := h.execute(context.Background(), store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}, grant.RunID, grant.TaskID, cmd, false, meta)
			if err == nil || result.Status != "failed" {
				t.Fatalf("replacement admitted: status=%s err=%v", result.Status, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("worker start count not zero: %v", err)
			}
		})
	}
}

// These are real local OS sandboxes, with shell probes and no provider/model
// request. Codex uses the production Python validator/config renderer.
func TestManagedNewDirectoriesNativeSandboxes(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS Seatbelt")
	}
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			work, spec := managedPolicyFixture(t)
			spec.Paths = []string{"CHANGE.txt", "new/deep/output.txt"}
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "native-directories")
			if err != nil {
				t.Fatal(err)
			}
			grant := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1}
			dir := work + "-fresh"
			profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 10000}
			inv := automaticWriteInvocation{profiledInvocation: profiledInvocation{launchInvocation: launchInvocation{dir: dir}, profile: profile}, workspace: spec}
			var validate func() error
			freeze, close, err := h.prepareCandidate(ctx, grant, inv, &profile, &validate)
			defer close()
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, spec.Paths[1])
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatal("empty target precreated")
			}
			writable, git, err := managedWritePaths(ctx, dir, spec)
			if err != nil {
				t.Fatal(err)
			}
			script := `printf changed > "$1" || exit 10; printf generated > "$2" || exit 11; for path in "$3/new/deep/adjacent" "$3/new/neighbor" "$4" "$5/config" "$6/HEAD"; do if (printf forbidden > "$path") 2>/dev/null; then exit 12; fi; done; if /bin/mkdir "$3/worker-directory" 2>/dev/null; then exit 13; fi; printf NATIVE_ISOLATED`
			args := []string{"-c", script, "probe", writable[0], writable[1], dir, git[0], git[1], git[2]}
			if provider == "claude" {
				cmd, p, scratch, source := claudeGatewayFixture(t)
				cmd.Dir = dir
				cmd.Args = args
				*p = profile
				source.workspace = spec
				source.help = "--tools"
				// Exercise the same exact derived atomic namespace in the new directory.
				cmd.Args[1] = strings.Replace(script, `printf generated > "$2" || exit 11;`, `printf generated > "$2.tmp.123.abcdef123456" || exit 11; /bin/mv "$2.tmp.123.abcdef123456" "$2" || exit 14;`, 1)
				prepared, err := prepareClaudeGateway(ctx, cmd, p, scratch, source)
				if err != nil {
					t.Fatal(err)
				}
				if err = validate(); err != nil {
					t.Fatal(err)
				}
				child, err := process.Start(ctx, prepared)
				if err != nil {
					t.Fatal(err)
				}
				if err = child.Wait(ctx); err != nil || child.ExitCode() != 0 || child.Output() != "NATIVE_ISOLATED" {
					t.Fatalf("sandbox exit=%d output=%q err=%v", child.ExitCode(), child.Output(), err)
				}
			} else {
				binary, err := exec.LookPath("codex")
				if err != nil {
					t.Fatal(err)
				}
				binary, err = filepath.EvalSymlinks(binary)
				if err != nil {
					t.Fatal(err)
				}
				root, _ := filepath.EvalSymlinks(t.TempDir())
				scratch := filepath.Join(root, "state/host-spool/launch/attempt/segment/scratch")
				if err = os.MkdirAll(scratch, 0700); err != nil {
					t.Fatal(err)
				}
				policy, err := codexNativePolicy(scratch, nil, codexPolicyOptions{profile: &profile, cwd: dir, workspace: spec, timeoutMS: 10000})
				if err != nil {
					t.Fatal(err)
				}
				home := filepath.Join(root, "codex-home")
				if err = os.Mkdir(home, 0700); err != nil {
					t.Fatal(err)
				}
				helper, err := filepath.Abs("../../../../plugins/codex-orchestrator/lib/trial")
				if err != nil {
					t.Fatal(err)
				}
				python, err := exec.LookPath("python3")
				if err != nil {
					t.Fatal(err)
				}
				render := exec.CommandContext(ctx, python, "-c", `import json,sys; from pathlib import Path; sys.path.insert(0,sys.argv[1]); import codex_exec; p=codex_exec.validate_host_policy(json.loads(sys.argv[2]),Path(sys.argv[3])); print(codex_exec.render_native_config(p,Path(sys.argv[4])/"source-auth",Path(sys.argv[4])))`, helper, policy, scratch, home)
				render.Env = []string{"PATH=/usr/bin:/bin", "PYTHONDONTWRITEBYTECODE=1"}
				config, err := render.CombinedOutput()
				if err != nil {
					t.Fatalf("production native renderer: %s %v", config, err)
				}
				if err = os.WriteFile(filepath.Join(home, "config.toml"), config, 0600); err != nil {
					t.Fatal(err)
				}
				command := exec.CommandContext(ctx, binary, append([]string{"sandbox", "-P", "orchestrator_workspace_write", "-C", dir, "/bin/sh"}, args...)...)
				command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + home, "CODEX_HOME=" + home, "TMPDIR=/private/tmp"}
				command.Dir = dir
				if err = validate(); err != nil {
					t.Fatal(err)
				}
				var stderr bytes.Buffer
				command.Stderr = &stderr
				output, err := command.Output()
				if err != nil || string(output) != "NATIVE_ISOLATED" {
					t.Fatalf("Codex native sandbox output=%q stderr=%q err=%v", output, stderr.String(), err)
				}
			}
			artifact, err := freeze(ctx, "RESULT")
			if err != nil || !strings.Contains(string(artifact), "candidate_oid") {
				t.Fatalf("freeze failed: %s %v", artifact, err)
			}
			if body, err := os.ReadFile(target); err != nil || string(body) != "generated" {
				t.Fatalf("new target: %q %v", body, err)
			}
		})
	}
}

func TestManagedInvalidDeclarationsNeverStartWorker(t *testing.T) {
	for _, paths := range [][]string{{"safe/new/file", "foo", "foo/bar"}, {"safe/new/file", "Foo/a", "foo/b"}, {"safe/new/file", "caf\u00e9/a", "cafe\u0301/b"}, {"safe/new/file", ".git/config"}, {"safe/new/file", "../escape"}} {
		t.Run(strings.Join(paths, "_"), func(t *testing.T) {
			work, spec := managedPolicyFixture(t)
			spec.Paths = paths
			h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "invalid-declarations")
			if err != nil {
				t.Fatal(err)
			}
			grant := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 5000}
			dir := work + "-fresh"
			inv := automaticWriteInvocation{profiledInvocation: profiledInvocation{launchInvocation: launchInvocation{dir: dir}, profile: adapter.ExecutionProfile{Role: adapter.Implementer, Permission: adapter.WorkspaceWrite}}, workspace: spec}
			marker := filepath.Join(t.TempDir(), "worker-started")
			cmd := process.Command{Path: "/bin/sh", Args: []string{"-c", `printf started > "$1"`, "probe", marker}, Dir: dir}
			meta := launchMetadata{prepareCandidate: func(ctx context.Context) (process.Command, func(context.Context, string) ([]byte, error), func(), error) {
				freeze, close, err := h.prepareCandidate(ctx, grant, inv, &inv.profile)
				return cmd, freeze, close, err
			}}
			result, err := h.execute(context.Background(), store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}, grant.RunID, grant.TaskID, cmd, false, meta)
			if err == nil || result.Status != "failed" {
				t.Fatalf("invalid declarations admitted: %s %v", result.Status, err)
			}
			for _, name := range []string{marker, dir} {
				if _, err := os.Lstat(name); !os.IsNotExist(err) {
					t.Fatalf("worker started or workspace created: %s %v", name, err)
				}
			}
		})
	}
}

func TestManagedDirectoryIntentFailureCreatesNothing(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	guard, err := prepareManagedDirectories(context.Background(), root, []string{"new/deep/file"}, func(string, any) error { return errors.New("intent_not_durable") })
	if guard != nil {
		guard.Close()
	}
	if err == nil {
		t.Fatal("failed intent ignored")
	}
	if _, err := os.Lstat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatalf("created without durable intent: %v", err)
	}
}
