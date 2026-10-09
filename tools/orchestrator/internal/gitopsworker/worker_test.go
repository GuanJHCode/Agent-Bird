package gitopsworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
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

func TestIntegrationPreflightRejectsUnfinishedGitLedger(t *testing.T) {
	identity := map[string]any{"PID": 99, "PGID": 99, "Birth": "fixture-birth", "BirthKnown": true, "Executable": "/usr/bin/git", "ExecutableSHA256": strings.Repeat("b", 64), "StartedAt": "2026-10-07T12:00:00Z"}
	git := func(phase string, process any) map[string]any {
		return map[string]any{"version": 1, "kind": "git_process", "value": map[string]any{"phase": phase, "command_digest": strings.Repeat("a", 64), "process": process, "exit_code": 0}}
	}
	for _, tc := range []struct {
		name     string
		rows     []map[string]any
		truncate bool
		want     bool
	}{
		{"empty", nil, false, false},
		{"complete", []map[string]any{git("intent", nil), git("spawned", identity), git("exited", identity)}, false, false},
		{"intent", []map[string]any{git("intent", nil)}, false, true},
		{"spawned", []map[string]any{git("spawned", identity)}, false, true},
		{"unknown", []map[string]any{git("unknown", identity)}, false, true},
		{"mismatched_exit", []map[string]any{git("intent", nil), git("spawned", identity), git("exited", map[string]any{"PID": 100, "Birth": "fixture-birth"})}, false, true},
		{"partial_identity", []map[string]any{git("intent", nil), git("spawned", map[string]any{"PID": 99, "Birth": "fixture-birth"}), git("exited", map[string]any{"PID": 99, "Birth": "fixture-birth"})}, false, true},
		{"orphan_exit", []map[string]any{git("exited", identity)}, false, true},
		{"unstarted_pair", []map[string]any{git("spawned", identity), git("exited", identity)}, false, true},
		{"duplicate", []map[string]any{git("intent", nil), git("spawned", identity), git("spawned", identity), git("exited", identity)}, false, true},
		{"valid_json_without_newline", []map[string]any{git("intent", nil), git("spawned", identity), git("exited", identity)}, false, true},
		{"integration", []map[string]any{{"kind": "integration_intent", "value": map[string]any{}}}, false, true},
		{"truncated", []map[string]any{git("intent", nil), git("spawned", identity), git("exited", identity)}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			j, err := openJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range tc.rows {
				if err = j.append(row["kind"].(string), row["value"]); err != nil {
					t.Fatal(err)
				}
			}
			path := j.file.Name()
			if tc.name == "valid_json_without_newline" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(path, info.Size()-1); err != nil {
					t.Fatal(err)
				}
			}
			if tc.truncate {
				if err = os.WriteFile(path, []byte(`{"version":1,"kind":"git_process"`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = j.integrationPreflight(context.Background())
			j.close()
			if (err != nil) != tc.want {
				t.Fatalf("err=%v wantReject=%t", err, tc.want)
			}
		})
	}
}

func TestComposePreflightRejectsExistingCandidateLedgerAsUncertain(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	if err = j.append("candidate_intent", map[string]any{"attempt_id": "already-started"}); err != nil {
		t.Fatal(err)
	}
	if err = j.composePreflight(context.Background()); !errors.Is(err, process.ErrProcessTreeUnknown) {
		t.Fatalf("candidate replay was not uncertain: %v", err)
	}
}

func TestWorkerIntegrationChecksOriginalJournalBeforeGit(t *testing.T) {
	for _, locked := range []bool{false, true} {
		t.Run(fmt.Sprint(locked), func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			binary, err := filepath.EvalSymlinks(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			// An invalid review would fail at a different boundary if Run failed
			// to check the old journal before entering Git integration.
			payload, _ := json.Marshal(map[string]any{"kind": "gitops", "operation": "integrate", "request": map[string]any{"repo_root": root, "target_worktree": root}})
			inv, err := BuildInvocation(payload, binary, root, "replayed")
			if err != nil {
				t.Fatal(err)
			}
			var envelope requestEnvelope
			if err := readPrivateJSON(inv.Args()[3], &envelope); err != nil {
				t.Fatal(err)
			}
			j, err := openJournal(envelope.JournalRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer j.close()
			if locked {
				if err := syscall.Flock(int(j.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			} else if err := j.PersistIntegrationIntent(context.Background(), gitops.IntegrationIntent{}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(j.file.Name())
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = Run(context.Background(), inv.Args()[3], &output)
			if locked && !errors.Is(err, syscall.EWOULDBLOCK) || !locked && !errors.Is(err, gitops.ErrIntegrationUncertain) {
				t.Fatalf("journal gate missed: %v", err)
			}
			after, readErr := os.ReadFile(j.file.Name())
			if readErr != nil || !bytes.Equal(before, after) || output.Len() != 0 {
				t.Fatal("replay changed durable evidence", readErr)
			}
		})
	}
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
	for _, environment := range []bool{false, true} {
		t.Run(fmt.Sprint(environment), func(t *testing.T) { workerIntegrationChain(t, environment) })
	}
}

func workerIntegrationChain(t *testing.T, environment bool) {
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
	hookRoot := t.TempDir()
	hookDir := filepath.Join(hookRoot, "final-hooks")
	marker := filepath.Join(hookRoot, "post-merge-ran")
	if err = os.Mkdir(hookDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(hookDir, "post-merge"), []byte("#!/bin/sh\nprintf hook > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if environment {
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
		t.Setenv("GIT_CONFIG_VALUE_0", hookDir)
	} else {
		git("config", "core.hooksPath", hookDir)
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
	integrationJournal, err := os.ReadFile(filepath.Join(spool, "gitops-journal", digest("chain-integrate"), "events.jsonl"))
	if err != nil || !strings.Contains(string(integrationJournal), `"kind":"git_process"`) || !strings.Contains(string(integrationJournal), `"phase":"spawned"`) || !strings.Contains(string(integrationJournal), `"phase":"exited"`) {
		t.Fatalf("final integrate lacks original Git process receipt: %q err=%v", integrationJournal, err)
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "hook" {
		t.Fatalf("final post-merge hook did not execute: %q %v", body, err)
	}
	pending := map[string]gitops.GitProcessRecord{}
	completed := 0
	for _, line := range bytes.Split(bytes.TrimSpace(integrationJournal), []byte("\n")) {
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
		if json.Unmarshal(record.Value, &value) != nil {
			t.Fatal("invalid git receipt")
		}
		if value.Phase == "intent" {
			continue
		}
		if value.Process == nil || value.Process.PID <= 0 || value.Process.Birth == "" {
			t.Fatal("unbound process receipt")
		}
		key := value.CommandDigest + ":" + fmt.Sprint(value.Process.PID) + ":" + value.Process.Birth
		switch value.Phase {
		case "spawned":
			if _, exists := pending[key]; exists {
				t.Fatal("duplicate spawn", key)
			}
			pending[key] = value
		case "exited":
			original, exists := pending[key]
			if !exists || *original.Process != *value.Process {
				t.Fatal("exit without matching original spawn", key)
			}
			delete(pending, key)
			completed++
		default:
			t.Fatal("unsettled Git receipt", value.Phase)
		}
	}
	if len(pending) != 0 || completed < 2 {
		t.Fatalf("unpaired Git receipts: pending=%d completed=%d", len(pending), completed)
	}
	// compose stdout intentionally omits the full inventory; cleanup uses the
	// durable compose journal's receipt rather than an unbounded terminal value.
	composeJournal, err := openJournal(filepath.Join(spool, "gitops-journal", digest("chain-compose")))
	if err != nil {
		t.Fatal(err)
	}
	cleanupReceipt, found, err := composeJournal.LoadCandidateFreeze(context.Background())
	composeJournal.close()
	if err != nil || !found || len(cleanupReceipt.Files) == 0 {
		t.Fatalf("missing full compose cleanup receipt: found=%v err=%v", found, err)
	}

	cleaned := runWorkerOperation(t, executable, spool, "chain-cleanup", "cleanup", map[string]any{
		"root": composeWorktree, "candidate_receipt": cleanupReceipt,
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
