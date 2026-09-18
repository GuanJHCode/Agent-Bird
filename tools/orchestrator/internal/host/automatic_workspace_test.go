package host

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type automaticWriteInvocation struct {
	profiledInvocation
	workspace *adapter.CandidateWorkspace
}

func (i automaticWriteInvocation) CandidateWorkspace() *adapter.CandidateWorkspace {
	return i.workspace
}
func TestAutomaticWorkspacePreservesFailedAttemptAndAllocatesRetry(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(root, "repo")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("/usr/bin/git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", repo)
	os.WriteFile(filepath.Join(repo, "file"), []byte("base"), 0600)
	git("-C", repo, "add", "file")
	git("-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	base := git("-C", repo, "rev-parse", "HEAD")
	directory := filepath.Join(root, "workspace")
	h, err := NewIPC(filepath.Join(root, "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	inv := automaticWriteInvocation{profiledInvocation: profiledInvocation{launchInvocation: launchInvocation{args: []string{"/usr/bin/false"}, dir: directory}, profile: adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 10000}}, workspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: base, Paths: []string{"file"}, AutoDirectory: true}}
	for _, attempt := range []string{"first", "retry"} {
		_, err = h.ExecuteLaunch(context.Background(), contract.LaunchCommand{CommandID: attempt, ReservationID: attempt, RunID: "run", TaskID: "task", AttemptID: attempt, SegmentID: attempt, WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 10000}, inv)
		if err != nil {
			t.Fatalf("attempt %s: %v", attempt, err)
		}
	}
	dirs, _ := filepath.Glob(directory + ".attempt-*")
	if len(dirs) != 2 {
		t.Fatalf("expected retained independent attempt worktrees, got %v", dirs)
	}
	for _, dir := range dirs {
		if body, err := os.ReadFile(filepath.Join(dir, "file")); err != nil || string(body) != "base" {
			t.Fatalf("lost attempt input: %s %v", body, err)
		}
	}
}
