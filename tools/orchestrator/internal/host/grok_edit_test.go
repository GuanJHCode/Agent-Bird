package host

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func grokEditFixture(t *testing.T) (process.Command, *adapter.ExecutionProfile, *gitops.PreparedWorkspace) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "calc.py"), []byte("baseline"), 0600); err != nil {
		t.Fatal(err)
	}
	return process.Command{Dir: root, Args: []string{"--permission-mode", "acceptEdits"}}, &adapter.ExecutionProfile{Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, GrokSessionWrite: true}, &gitops.PreparedWorkspace{Receipt: gitops.MaterializeReceipt{Worktree: root}, Paths: []string{"calc.py"}}
}

func TestGrokEditGrantsUseCurrentWorkspaceAndExactFiles(t *testing.T) {
	var previous string
	for attempt := 0; attempt < 2; attempt++ {
		cmd, profile, prepared := grokEditFixture(t)
		prepared.Paths = []string{"calc.py", "new dir/new.py"}
		before := append([]string(nil), cmd.Args...)
		got, err := authorizeGrokEdits(cmd, profile, prepared)
		if err != nil {
			t.Fatal(err)
		}
		want := append(append([]string(nil), before...), "--allow", "Edit("+filepath.Join(cmd.Dir, "calc.py")+")", "--allow", "Edit("+filepath.Join(cmd.Dir, "new dir/new.py")+")")
		if !reflect.DeepEqual(got.Args, want) || !reflect.DeepEqual(cmd.Args, before) {
			t.Fatalf("unexpected grants or input mutation: %v", got.Args)
		}
		if got.Args[len(before)+1] == previous {
			t.Fatal("reused prior workspace grant")
		}
		previous = got.Args[len(before)+1]
	}
}

func TestGrokEditRejectsUnrepresentablePathsWithoutPartialGrant(t *testing.T) {
	for _, path := range []string{"", ".", "..", "../other.py", "/tmp/other.py", "nested/../other.py", ".git/config", "nested/.GIT/config", "*.py", "file?.py", "[a].py", "{a}.py", "x),Bash(*)", "a,b", "a\\b", "a\nb", "a\x00b", " calc.py", "calc.py ", "calc.py"} {
		t.Run(path, func(t *testing.T) {
			cmd, profile, prepared := grokEditFixture(t)
			prepared.Paths = []string{"calc.py", path}
			got, err := authorizeGrokEdits(cmd, profile, prepared)
			if err == nil || !reflect.DeepEqual(got.Args, cmd.Args) {
				t.Fatalf("accepted invalid path or partial grant: %q %v", path, got.Args)
			}
		})
	}
}

func TestGrokEditRejectsAliasesAndNonRegularTargets(t *testing.T) {
	for _, kind := range []string{"symlink-file", "symlink-parent", "hardlink", "directory", "root-symlink", "root-glob"} {
		t.Run(kind, func(t *testing.T) {
			cmd, profile, prepared := grokEditFixture(t)
			target := filepath.Join(cmd.Dir, "other")
			var err error
			switch kind {
			case "symlink-file":
				err = os.Symlink(filepath.Join(cmd.Dir, "calc.py"), target)
				prepared.Paths = []string{"other"}
			case "symlink-parent":
				err = os.Symlink(cmd.Dir, target)
				prepared.Paths = []string{"other/calc.py"}
			case "hardlink":
				err = os.Link(filepath.Join(cmd.Dir, "calc.py"), target)
			case "directory":
				err = os.Mkdir(target, 0700)
				prepared.Paths = []string{"other"}
			case "root-symlink":
				target = filepath.Join(t.TempDir(), "alias")
				err = os.Symlink(cmd.Dir, target)
				cmd.Dir = target
				prepared.Receipt.Worktree = target
			case "root-glob":
				target = filepath.Join(cmd.Dir, "wild*")
				err = os.Mkdir(target, 0700)
				cmd.Dir = target
				prepared.Receipt.Worktree = target
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = authorizeGrokEdits(cmd, profile, prepared); err == nil {
				t.Fatal("unsafe target accepted")
			}
		})
	}
}

func TestGrokEditRejectsUnmanagedOrExpandedRequests(t *testing.T) {
	for _, kind := range []string{"nil-profile", "nil-workspace", "reviewer", "readonly", "other-provider", "wrong-dir", "no-files", "legacy-allow", "legacy-allow-equals", "legacy-tools"} {
		t.Run(kind, func(t *testing.T) {
			cmd, profile, prepared := grokEditFixture(t)
			switch kind {
			case "nil-profile":
				profile = nil
			case "nil-workspace":
				prepared = nil
			case "reviewer":
				profile.Role = adapter.Reviewer
			case "readonly":
				profile.Permission = adapter.ReadOnly
			case "other-provider":
				profile.GrokSessionWrite = false
			case "wrong-dir":
				cmd.Dir = t.TempDir()
			case "no-files":
				prepared.Paths = nil
			case "legacy-allow":
				cmd.Args = append(cmd.Args, "--allow", "Edit")
			case "legacy-allow-equals":
				cmd.Args = append(cmd.Args, "--allow=Edit")
			case "legacy-tools":
				cmd.Args = append(cmd.Args, "--allowedTools", "Edit")
			}
			if _, err := authorizeGrokEdits(cmd, profile, prepared); err == nil {
				t.Fatal("unauthorized grant accepted")
			}
		})
	}
}
