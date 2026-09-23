package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/execbridge"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestCodexWorkerParentRetainsEveryMCPRestriction(t *testing.T) {
	home := &codexHome{path: "/private/task/home", runtime: "/private/task/runtime"}
	config := map[string]any{"mcp_servers": map[string]any{
		"http":  map[string]any{"enabled": true, "url": "https://example.invalid/mcp"},
		"stdio": map[string]any{"enabled": true, "command": "/bin/sh"},
	}}
	args, err := codexWorkerParentArgs(home, config)
	if err != nil {
		t.Fatal(err)
	}
	want := append(codexRuntimeFlags(home), "-c", `mcp_servers."http".enabled=false`, "-c", `mcp_servers."stdio".enabled=false`, "app-server", "--listen", "stdio://")
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("parent discarded source restrictions: %q", args)
	}
}

func TestCodexExecutorSandboxProtectsProjectSkillInputs(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, work, scratch, source := filepath.Join(root, "repo"), filepath.Join(root, "work"), filepath.Join(root, "scratch"), filepath.Join(root, "source")
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}, {"-C", repo, "worktree", "add", "--detach", work}} {
		if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	skill := filepath.Join(work, ".agents", "skills", "example", "SKILL.md")
	for _, dir := range []string{source, scratch, filepath.Dir(skill), filepath.Join(work, ".agents-other")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(source, "config.toml"), filepath.Join(source, "auth.json"), skill} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	home, err := createCodexHome(source, scratch, []byte("model='fixture'"))
	if err != nil {
		t.Fatal(err)
	}
	defer home.detachAuth()
	skills, err := execbridge.SealSkillRoots(work, []string{".git"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, operation, path string
		allowed               bool
	}{
		{"skill content", "write", skill, false},
		{"skill membership", "write", filepath.Join(filepath.Dir(skill), "new.md"), false},
		{"new project config root", "mkdir", filepath.Join(work, ".codex", "skills"), false},
		{"skill parent rename", "rename", filepath.Join(work, ".agents"), false},
		{"skill unlink", "unlink", skill, false},
		{"skill hardlink", "link", skill, false},
		{"skill read", "read", skill, true},
		{"ordinary code", "write", filepath.Join(work, "calc.py"), true},
		{"prefix sibling", "write", filepath.Join(work, ".agents-other", "ordinary"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			shell := `case "$1" in write) printf fixture > "$2";; mkdir) /bin/mkdir -p "$2";; rename) /bin/mv "$2" "$2.moved";; unlink) /bin/rm "$2";; link) /bin/ln "$2" ./alias;; read) /bin/cat "$2";; esac`
			cmd, err := codexSandboxCommand(context.Background(), process.Command{Path: "/bin/sh", Args: []string{"-c", shell, "fixture", test.operation, test.path}, Dir: work}, &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 1000}, scratch, home)
			if err != nil {
				t.Fatal(err)
			}
			cmd.Args[1] += codexSkillInputRules(skills)
			child := exec.Command(cmd.Path, cmd.Args...)
			child.Dir = work
			if err := child.Run(); (err == nil) != test.allowed {
				t.Fatalf("unexpected sandbox result: %v", err)
			}
			if err := skills.Verify(); err != nil {
				t.Fatal("sealed skill inputs changed:", err)
			}
		})
	}
}

func TestCodexKernelBlocksForkAndBothRolesCannotAccessExecutorJournal(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo, work, scratch, source := filepath.Join(root, "repo"), filepath.Join(root, "work"), filepath.Join(root, "scratch"), filepath.Join(root, "source")
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}, {"-C", repo, "worktree", "add", "--detach", work}} {
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, output)
		}
	}
	journal := filepath.Join(root, "codex-executor")
	for _, dir := range []string{scratch, source, journal} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(source, "config.toml"), filepath.Join(source, "auth.json"), filepath.Join(journal, "intent.json"), journal + ".required"} {
		if err := os.WriteFile(path, []byte("fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	home, err := createCodexHome(source, scratch, []byte("model='fixture'"))
	if err != nil {
		t.Fatal(err)
	}
	defer home.detachAuth()
	skills, err := execbridge.SealSkillRoots(work, []string{".git"})
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	python, err = filepath.EvalSymlinks(python)
	if err != nil {
		t.Fatal(err)
	}
	script := "import os,sys\ntry:\n if sys.argv[1]=='fork':\n  pid=os.fork()\n  if pid==0: os._exit(5)\n  os.waitpid(pid,0)\n elif sys.argv[1]=='read': open(sys.argv[2]).read()\n else: open(sys.argv[2],'w').write('changed')\nexcept PermissionError:\n sys.exit(0 if sys.argv[3]=='denied' else 4)\nelse:\n sys.exit(5 if sys.argv[3]=='denied' else 0)\n"
	for _, role := range []string{"parent", "executor"} {
		for _, item := range []struct{ operation, path, want string }{
			{"read", filepath.Join(journal, "intent.json"), "denied"}, {"write", filepath.Join(journal, "intent.json"), "denied"}, {"read", journal + ".required", "denied"}, {"write", journal + ".required", "denied"}, {"fork", work, "denied"}, {"write", filepath.Join(work, "code.py"), "allowed"},
		} {
			if role == "parent" && (item.operation == "fork" || item.want == "allowed") {
				continue
			}
			t.Run(role+"/"+item.operation+"/"+filepath.Base(item.path), func(t *testing.T) {
				permission := adapter.ReadOnly
				profileRole := adapter.Reviewer
				if role == "executor" {
					permission, profileRole = adapter.WorkspaceWrite, adapter.Implementer
				}
				cmd, err := codexSandboxCommand(context.Background(), process.Command{Path: python, Dir: work, Args: []string{"-c", script, item.operation, item.path, item.want}}, &adapter.ExecutionProfile{Version: 1, Role: profileRole, Permission: permission, TimeoutMS: 1000}, scratch, home)
				if err != nil {
					t.Fatal(err)
				}
				if role == "executor" {
					cmd.Args[1] += codexExecutorRestrictions(home, skills, journal)
				} else {
					cmd.Args[1] += codexJournalRules(journal)
				}
				child := exec.Command(cmd.Path, cmd.Args...)
				child.Dir, child.Env = work, []string{"PATH=/usr/bin:/bin", "HOME=" + root, "PYTHONDONTWRITEBYTECODE=1"}
				if output, err := child.CombinedOutput(); err != nil {
					t.Fatalf("native boundary failed: %v %s", err, output)
				}
			})
		}
	}
}
