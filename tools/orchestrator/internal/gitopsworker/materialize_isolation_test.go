package gitopsworker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func TestWorkerMaterializeRejectsExecutionExtensions(t *testing.T) {
	for _, config := range []struct{ key, value string }{{"filter.worker.smudge", "cat"}, {"core.hooksPath", ".hooks"}, {"commit.gpgsign", "true"}} {
		t.Run(config.key, func(t *testing.T) {
			repo := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("/usr/bin/git", append([]string{"-C", repo}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("fixture git: %v %s", err, out)
				}
				return string(bytes.TrimSpace(out))
			}
			git("init", "-q")
			git("config", "user.name", "materialize test")
			git("config", "user.email", "test@example.invalid")
			if err := os.WriteFile(filepath.Join(repo, "README"), []byte("base\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", "README")
			git("commit", "-qm", "base")
			oid := git("rev-parse", "HEAD")
			git("config", config.key, config.value)
			worktree := filepath.Join(t.TempDir(), "candidate")
			payload, err := json.Marshal(map[string]any{"kind": "gitops", "operation": "materialize", "request": gitops.MaterializeRequest{RepoRoot: repo, Worktree: worktree, AttemptID: "extension", BaseOID: oid, CandidateOID: oid, PlanRevision: 1}})
			if err != nil {
				t.Fatal(err)
			}
			executable, err := filepath.EvalSymlinks(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			spool := filepath.Join(t.TempDir(), "spool")
			if err = os.Mkdir(spool, 0700); err != nil {
				t.Fatal(err)
			}
			invocation, err := BuildInvocation(payload, executable, spool, "extension")
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err = Run(context.Background(), invocation.Args()[3], &output); err == nil {
				t.Fatal("worker admitted Git execution extension", config.key, output.String())
			}
			if _, err = os.Lstat(worktree); !os.IsNotExist(err) {
				t.Fatal("unsafe repository materialized before rejection", err)
			}
		})
	}
}

func assertMaterializeGitProcessJournal(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pending := map[string]gitops.GitProcessRecord{}
	completed := 0
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var record struct {
			Kind  string          `json:"kind"`
			Value json.RawMessage `json:"value"`
		}
		if json.Unmarshal(line, &record) != nil {
			t.Fatal("invalid journal")
		}
		if record.Kind != "git_process" {
			continue
		}
		var value gitops.GitProcessRecord
		if json.Unmarshal(record.Value, &value) != nil || value.CommandDigest == "" {
			t.Fatal("invalid Git process fact")
		}
		prior, found := pending[value.CommandDigest]
		switch value.Phase {
		case "intent":
			if found || value.Process != nil {
				t.Fatal("Git command intent reused while unsettled")
			}
			pending[value.CommandDigest] = value
		case "spawned":
			if !found || prior.Phase != "intent" || value.Process == nil || value.Process.PID <= 0 || value.Process.Birth == "" || !value.Process.BirthKnown || value.Process.Executable != "/usr/bin/git" {
				t.Fatal("Git spawn lacks pinned original identity", value)
			}
			pending[value.CommandDigest] = value
		case "exited":
			if !found || prior.Phase != "spawned" || value.Process == nil || *prior.Process != *value.Process || value.ExitCode != 0 {
				t.Fatal("Git exit lacks matching original spawn", value)
			}
			delete(pending, value.CommandDigest)
			completed++
		default:
			t.Fatal("materialize left unsettled Git phase", value.Phase)
		}
	}
	if completed == 0 || len(pending) != 0 {
		t.Fatalf("missing complete original Git Wait chain: complete=%d pending=%d", completed, len(pending))
	}
}
