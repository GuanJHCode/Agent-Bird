package gitopsworker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBuildAndRunMaterializeUsesDurableSourceJournal(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return string(bytes.TrimSpace(output))
	}
	git("init", "-q")
	git("config", "user.email", "test@example.invalid")
	git("config", "user.name", "worker test")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "README")
	git("commit", "-qm", "base")
	oid := git("rev-parse", "HEAD")
	worktree := filepath.Join(t.TempDir(), "worktree")
	request := map[string]any{"kind": "gitops", "operation": "materialize", "request": map[string]any{"repo_root": repo, "worktree": worktree, "attempt_id": "worker-a", "base_oid": oid, "candidate_oid": oid, "ordered_input_oids": []string{oid}, "plan_revision": 1}}
	payload, _ := json.Marshal(request)
	executable, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(t.TempDir(), "spool")
	if err = os.Mkdir(spool, 0700); err != nil {
		t.Fatal(err)
	}
	invocation, err := BuildInvocation(payload, executable, spool, "command-a")
	if err != nil {
		t.Fatal(err)
	}
	args := invocation.Args()
	if len(args) != 4 || args[1] != "gitops-worker" || args[2] != "--request" {
		t.Fatalf("args=%v", args)
	}
	var output bytes.Buffer
	if err = Run(context.Background(), args[3], &output); err != nil {
		t.Fatal(err)
	}
	if gitOutput, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output(); err != nil || string(bytes.TrimSpace(gitOutput)) != oid {
		t.Fatalf("materialized head=%q err=%v", gitOutput, err)
	}
	journal := filepath.Join(spool, "gitops-journal", digest("command-a"), "events.jsonl")
	if info, err := os.Stat(journal); err != nil || info.Mode().Perm() != 0600 || info.Size() == 0 {
		t.Fatalf("journal info=%v err=%v", info, err)
	}
}
