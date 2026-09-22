package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestCodexSandboxProtectsSourceAndSnapshotButAllowsPrivateRuntime(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, scratch, work := filepath.Join(root, "source"), filepath.Join(root, "scratch"), filepath.Join(root, "work")
	for _, p := range []string{source, scratch, work} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"config.toml", "auth.json"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	home, err := createCodexHome(source, scratch, []byte("model='fixture'"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home.path, "skills/.system"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home.path, "skills/.system/SKILL.md"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, path, operation string
		allowed               bool
	}{
		{"source auth write", filepath.Join(source, "auth.json"), "write", false},
		{"sealed system skill write", filepath.Join(home.path, "skills/.system/SKILL.md"), "write", false},
		{"snapshot write", filepath.Join(home.path, "config.toml"), "write", false},
		{"auth link replace", filepath.Join(home.path, "auth.json"), "unlink", false},
		{"home rename", home.path, "rename", false},
		{"unknown home slot", filepath.Join(home.path, "unexpected"), "write", false},
		{"installation id", filepath.Join(home.path, "installation_id"), "write", true},
		{"runtime", filepath.Join(home.runtime, "tmp", "runtime"), "write", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			shell := `case "$1" in write) printf test > "$2";; unlink) /bin/rm "$2";; rename) /bin/mv "$2" "$2.moved";; esac`
			command, err := codexSandboxCommand(context.Background(), process.Command{Path: "/bin/sh", Args: []string{"-c", shell, "fixture", test.operation, test.path}, Dir: work}, &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}, scratch, home)
			if err != nil {
				t.Fatal(err)
			}
			native := exec.Command(command.Path, command.Args...)
			native.Dir = work
			err = native.Run()
			if (err == nil) != test.allowed {
				t.Fatalf("unexpected write permission: %v", err)
			}
		})
	}
}

func TestCodexImplementerCannotWriteSourceHomeInsideWorkspace(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo, work, scratch := filepath.Join(root, "repo"), filepath.Join(root, "work"), filepath.Join(root, "scratch")
	for _, args := range [][]string{{"init", repo}, {"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}, {"-C", repo, "worktree", "add", "--detach", work}} {
		if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	source := filepath.Join(work, "provider-home")
	for _, p := range []string{source, scratch} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"config.toml", "auth.json"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	home, err := createCodexHome(source, scratch, []byte("model='fixture'"))
	if err != nil {
		t.Fatal(err)
	}
	defer home.detachAuth()
	cmd, err := codexSandboxCommand(context.Background(), process.Command{Path: "/bin/sh", Dir: work, Args: []string{"-c", `printf overwritten > "$1"`, "fixture", filepath.Join(source, "auth.json")}}, &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 1000}, scratch, home)
	if err != nil {
		if err.Error() != "codex_source_home_overlap" {
			t.Fatal(err)
		}
		return
	}
	child := exec.Command(cmd.Path, cmd.Args...)
	child.Dir = work
	if err := child.Run(); err == nil {
		t.Fatal("implementer wrote original source home")
	}
	data, err := os.ReadFile(filepath.Join(source, "auth.json"))
	if err != nil || string(data) != "fixture" {
		t.Fatal("source auth changed")
	}
}
