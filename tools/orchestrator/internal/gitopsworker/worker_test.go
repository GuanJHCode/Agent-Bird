package gitopsworker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

type workerResponse struct {
	Status    string          `json:"status"`
	Operation string          `json:"operation"`
	Result    json.RawMessage `json:"result"`
}

func runWorkerOperation(t *testing.T, executable, spool, commandID, operation string, request any) workerResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"kind": "gitops", "operation": operation, "request": request})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := BuildInvocation(payload, executable, spool, commandID)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err = Run(context.Background(), invocation.Args()[3], &output); err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	var response workerResponse
	if err = json.Unmarshal(output.Bytes(), &response); err != nil || response.Status != "ok" || response.Operation != operation || len(response.Result) == 0 {
		t.Fatalf("%s response=%q err=%v", operation, output.String(), err)
	}
	return response
}

func directoryBinding(t *testing.T, path string) gitops.DirectoryIdentity {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("directory stat unavailable")
	}
	return gitops.DirectoryIdentity{Device: int64(stat.Dev), Inode: int64(stat.Ino), Mode: int64(info.Mode()), UID: int64(stat.Uid)}
}

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

func TestWorkerRoutesPrivateRefsAndComposeIntegrateCleanupChain(t *testing.T) {
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
	git("init", "-q", "-b", "main")
	git("config", "user.email", "test@example.invalid")
	git("config", "user.name", "worker chain test")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "README")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "candidate.txt"), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "candidate.txt")
	git("commit", "-qm", "candidate")
	inputOID := git("rev-parse", "HEAD")
	git("reset", "--hard", base)

	executable, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(t.TempDir(), "spool")
	if err = os.Mkdir(spool, 0700); err != nil {
		t.Fatal(err)
	}

	routeRef := "refs/orchestrator/g3/worker-route"
	create := runWorkerOperation(t, executable, spool, "route-create", "create_private_ref", map[string]any{
		"repo_root": repo, "private_ref": routeRef, "expected_old_oid": "", "oid": inputOID,
	})
	var createReceipt gitops.PrivateRefReceipt
	if err = json.Unmarshal(create.Result, &createReceipt); err != nil || createReceipt.State != "created" || git("rev-parse", routeRef) != inputOID {
		t.Fatalf("create route failed: %+v", create)
	}
	createJournalPath := filepath.Join(spool, "gitops-journal", digest("route-create"), "events.jsonl")
	createJournal, err := os.ReadFile(createJournalPath)
	if err != nil || !strings.Contains(string(createJournal), `"kind":"private_ref_intent"`) || !strings.Contains(string(createJournal), `"private_ref":"`+routeRef+`"`) || strings.Contains(string(createJournal), `"PrivateRef"`) {
		t.Fatalf("private ref journal is not durable snake_case: %q err=%v", createJournal, err)
	}
	deleted := runWorkerOperation(t, executable, spool, "route-delete", "delete_private_ref", map[string]any{
		"repo_root": repo, "private_ref": routeRef, "expected_oid": inputOID,
	})
	var deleteReceipt gitops.PrivateRefReceipt
	if err = json.Unmarshal(deleted.Result, &deleteReceipt); err != nil || deleteReceipt.State != "deleted" {
		t.Fatalf("delete route failed: %+v", deleted)
	}
	if command := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", routeRef); command.Run() == nil {
		t.Fatal("delete route retained private ref")
	}

	composeWorktree := filepath.Join(t.TempDir(), "composed")
	privateRef := "refs/orchestrator/g3/final-candidate"
	composed := runWorkerOperation(t, executable, spool, "chain-compose", "compose", map[string]any{
		"repo_root": repo, "worktree": composeWorktree, "attempt_id": "chain-attempt",
		"base_oid": base, "ordered_input_oids": []string{inputOID}, "plan_revision": 1, "private_ref": privateRef,
	})
	var receipt gitops.CandidateReceipt
	if err = json.Unmarshal(composed.Result, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.CandidateOID != inputOID || receipt.PrivateRefExpectedOID != inputOID || git("rev-parse", privateRef) != inputOID {
		t.Fatalf("candidate was not frozen: %+v", receipt)
	}

	target, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	integrated := runWorkerOperation(t, executable, spool, "chain-integrate", "integrate", map[string]any{
		"repo_root": repo, "target_worktree": repo, "target_ref": "refs/heads/main",
		"target_base_oid": base, "candidate_oid": receipt.CandidateOID,
		"review": map[string]any{
			"target_worktree_identity": target, "target_directory_identity": directoryBinding(t, repo),
			"target_ref": "refs/heads/main", "target_base_oid": base, "final_candidate_oid": receipt.CandidateOID,
			"ordered_input_oids": []string{inputOID}, "validation_digest": "validation-sha",
			"plan_revision": 1, "review_revision": 1,
		},
	})
	var integrationOutcome gitops.IntegrationOutcome
	if err = json.Unmarshal(integrated.Result, &integrationOutcome); err != nil || integrationOutcome.State != gitops.IntegrationIntegrated || git("rev-parse", "HEAD") != receipt.CandidateOID {
		t.Fatalf("integration failed: %+v", integrated)
	}

	cleaned := runWorkerOperation(t, executable, spool, "chain-cleanup", "cleanup", map[string]any{
		"root": composeWorktree, "candidate_receipt": receipt,
	})
	var cleanupOutcome gitops.CleanupOutcome
	if err = json.Unmarshal(cleaned.Result, &cleanupOutcome); err != nil || cleanupOutcome.State != gitops.CleanupRemoved || cleanupOutcome.PrivateRefState != "deleted" {
		t.Fatalf("cleanup failed: %+v", cleaned)
	}
	if _, err = os.Stat(composeWorktree); !os.IsNotExist(err) {
		t.Fatalf("composed worktree remains: %v", err)
	}
	if command := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", privateRef); command.Run() == nil {
		t.Fatal("candidate private ref remains")
	}
	for _, commandID := range []string{"route-create", "route-delete", "chain-compose", "chain-integrate", "chain-cleanup"} {
		journal := filepath.Join(spool, "gitops-journal", digest(commandID), "events.jsonl")
		if info, statErr := os.Stat(journal); statErr != nil || info.Size() == 0 {
			t.Fatalf("missing durable journal for %s: info=%v err=%v", commandID, info, statErr)
		}
	}
}
