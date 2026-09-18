package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
)

// This is a protocol fixture executing real Go/Git/sandbox operations, not
// a real-model acceptance test. Removing the Host's candidate handoff must fail it.
func TestManagedCandidateFreezesWorkerChangesWithoutUserChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed bool
	}{
		{name: "success"},
		{name: "undeclared", failed: true},
		{name: "ignored", failed: true},
		{name: "symlink", failed: true},
		{name: "provider_nonzero", failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			repo, work := filepath.Join(root, "repo"), filepath.Join(root, "worker")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			git := func(dir string, args ...string) string {
				t.Helper()
				out, err := exec.Command("/usr/bin/git", append([]string{"-C", dir}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git(repo, "init", "-q", "-b", "main")
			git(repo, "config", "user.name", "Candidate Test")
			git(repo, "config", "user.email", "test@example.invalid")
			for _, name := range []string{"README", "obsolete.txt"} {
				if err := os.WriteFile(filepath.Join(repo, name), []byte("base\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("cache/\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(repo, "add", ".")
			git(repo, "commit", "-qm", "base")
			base := git(repo, "rev-parse", "HEAD")
			if err := os.WriteFile(filepath.Join(repo, "README"), []byte("user unfinished work\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "user-only.txt"), []byte("keep me\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before := git(repo, "status", "--porcelain=v1")
			t.Setenv("CANDIDATE_FIXTURE_MODE", tc.name)
			// A race-instrumented fixture otherwise sleeps 1s at every exit,
			// competing with the real 2s provider probe limit. Keep detection
			// enabled; remove only this child-runtime shutdown delay.
			t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
			binary, err := filepath.EvalSymlinks(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.ReadFile(binary)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(executable)
			payload, _ := json.Marshal(map[string]any{
				"provider": "claude-code", "directory": work, "prompt": "fixture implementation",
				"provider_lock":       map[string]any{"version": 1, "provider": "claude-code", "protocol": "claude-stream-json-v1", "binary": map[string]string{"path": binary, "version": "fixture-v1", "sha256": hex.EncodeToString(sum[:])}},
				"profile":             map[string]any{"version": 1, "role": "implementer", "permission": "workspace-write", "timeout_ms": 15000},
				"candidate_workspace": map[string]any{"version": 1, "repo_root": repo, "base_oid": base, "paths": []string{"README", "added.txt", "obsolete.txt"}},
			})
			grant := contract.LaunchCommand{RunID: "run", TaskID: "implement", AttemptID: "attempt", SegmentID: "segment", CommandID: "command", ReservationID: "reservation", WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 15000, AdapterPayload: payload}
			inv, err := invocationForGrant(context.Background(), grant)
			if err != nil {
				t.Fatalf("managed implementer request rejected: %v", err)
			}
			h, err := host.NewIPC(filepath.Join(root, "state", "host-spool", "launch"), "producer")
			if err != nil {
				t.Fatal(err)
			}
			result, err := h.ExecuteLaunch(context.Background(), grant, inv)
			if tc.failed {
				if err != nil || result.Status != "failed" {
					t.Fatalf("failed candidate: %+v err=%v", result, err)
				}
				if git(repo, "for-each-ref", "--format=%(refname)", "refs/orchestrator/g3/") != "" {
					t.Fatal("failed candidate pinned")
				}
				if git(repo, "status", "--porcelain=v1") != before || git(repo, "rev-parse", "HEAD") != base {
					t.Fatal("failure changed user workspace")
				}
				if _, err := os.Stat(work); err != nil {
					t.Fatal("failed workspace not retained")
				}
				return
			}
			if err != nil || result.Status != "result_ready" {
				t.Fatalf("managed result=%+v err=%v", result, err)
			}
			body, err := os.ReadFile(result.ArtifactPath)
			if err != nil {
				t.Fatal(err)
			}
			var artifact struct {
				ProviderResult string `json:"provider_result"`
				Candidate      struct {
					CandidateOID string `json:"candidate_oid"`
					PrivateRef   string `json:"private_ref"`
					Binding      struct {
						RunID        string `json:"run_id"`
						TaskID       string `json:"task_id"`
						AttemptID    string `json:"attempt_id"`
						SegmentID    string `json:"segment_id"`
						WorkRevision int    `json:"work_revision"`
					} `json:"binding"`
				} `json:"candidate"`
			}
			if err := json.Unmarshal(body, &artifact); err != nil {
				t.Fatalf("candidate artifact: %v", err)
			}
			journalFiles, err := filepath.Glob(filepath.Join(root, "state", "host-spool", "launch", ".gitops-journal", "*", "events.jsonl"))
			if err != nil || len(journalFiles) != 1 {
				t.Fatalf("candidate journal missing: %v", err)
			}
			journal, err := os.ReadFile(journalFiles[0])
			if err != nil || !strings.Contains(string(journal), `"kind":"workspace_prepared"`) {
				t.Fatal("prepared workspace identity was not durably registered")
			}
			c := artifact.Candidate
			if artifact.ProviderResult != "implemented" || c.CandidateOID == "" || c.Binding.RunID != "run" || c.Binding.TaskID != "implement" || c.Binding.AttemptID != "attempt" || c.Binding.SegmentID != "segment" || c.Binding.WorkRevision != 1 {
				t.Fatalf("unbound candidate: %s", body)
			}
			if git(repo, "show", c.CandidateOID+":README") != "worker implementation" || git(repo, "show", c.CandidateOID+":added.txt") != "new code" {
				t.Fatal("candidate content mismatch")
			}
			if got := git(repo, "ls-tree", "--name-only", c.CandidateOID); strings.Contains(got, "obsolete.txt") || strings.Contains(got, "user-only.txt") {
				t.Fatalf("candidate inventory: %s", got)
			}
			if git(repo, "rev-parse", c.PrivateRef) != c.CandidateOID || git(repo, "rev-parse", "HEAD") != base || git(repo, "status", "--porcelain=v1") != before {
				t.Fatal("candidate pin or user's original worktree changed")
			}

			exerciseCandidateDelivery(t, root, repo, base, grant, result, git)

		})
	}
}

// Reuse the native test executable: first execution of a fresh unsigned shell
// script can exceed the production 2s probe deadline under full-suite load.
func TestMain(m *testing.M) {
	mode := os.Getenv("CANDIDATE_FIXTURE_MODE")
	if mode == "" {
		os.Exit(m.Run())
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("fixture-v1")
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("--output-format --input-format --permission-mode --disallowed-tools --json-schema")
		os.Exit(0)
	}
	if mode == "progress" {
		signal.Ignore(syscall.SIGTERM)
		fmt.Println(`{"type":"system","subtype":"init","session_id":"progress-fixture"}`)
		fmt.Println(`{"type":"assistant","message":{"content":[{"type":"text","text":"Partial CLI finding"}]}}`)
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	reworking := mode == "rework"
	revised := false
	if reworking {
		input, _ := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
		revised = strings.Contains(string(input), "Owner-authorized revision.")
		if revised && !strings.Contains(string(input), "ACCEPTANCE_MARKER") {
			os.Exit(6)
		}
	}
	for i, arg := range os.Args {
		if (mode == "success" || reworking || mode == "retry_review") && arg == "--permission-mode" && i+1 < len(os.Args) && os.Args[i+1] == "plan" {
			if mode == "retry_review" {
				cwd, err := os.Getwd()
				if err != nil {
					os.Exit(3)
				}
				if filepath.Base(cwd) == "review" {
					time.Sleep(2 * time.Second)
					_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
					time.Sleep(time.Second)
					os.Exit(3)
				}
				if !strings.HasPrefix(filepath.Base(cwd), ".orchestrator-review-") {
					os.Exit(3)
				}
			}
			mode = "review_approve"
			if reworking && !revised {
				mode = "review_reject"
			}
		}
	}
	if strings.HasPrefix(mode, "review_") {
		decision := "approve"
		if mode == "review_reject" {
			decision = "reject"
		}
		body, _ := json.Marshal(map[string]string{"decision": decision, "summary": "fixture independently inspected candidate"})
		fmt.Println(`{"type":"system","subtype":"init","session_id":"review-fixture"}`)
		envelope := map[string]any{"type": "result", "subtype": "success", "session_id": "review-fixture", "result": "```json\n" + string(body) + "\n```", "structured_output": json.RawMessage(body)}
		if mode == "review_unstructured" {
			delete(envelope, "structured_output")
			envelope["result"] = string(body)
		}
		if mode == "review_invalid" {
			envelope["structured_output"] = map[string]any{"decision": "approve", "summary": "reviewed", "untrusted_extra": true}
		}
		result, _ := json.Marshal(envelope)
		fmt.Println(string(result))
		os.Exit(0)
	}
	content := "worker implementation\n"
	if revised {
		content = "worker implementation revised\n"
	}
	if err := os.WriteFile("README", []byte(content), 0600); err != nil {
		os.Exit(3)
	}
	if err := os.WriteFile("added.txt", []byte("new code\n"), 0600); err != nil {
		os.Exit(3)
	}
	if err := os.Remove("obsolete.txt"); err != nil && !(reworking && os.IsNotExist(err)) {
		os.Exit(3)
	}
	switch mode {
	case "undeclared":
		if err := os.WriteFile("undeclared.txt", []byte("extra"), 0600); err != nil {
			os.Exit(3)
		}
	case "ignored":
		if err := os.Mkdir("cache", 0700); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile("cache/output", []byte("extra"), 0600); err != nil {
			os.Exit(3)
		}
	case "symlink":
		if err := os.Symlink("README", "link"); err != nil {
			os.Exit(3)
		}
	}
	fmt.Println(`{"type":"system","subtype":"init","session_id":"candidate-fixture"}`)
	fmt.Println(`{"type":"result","subtype":"success","session_id":"candidate-fixture","result":"implemented"}`)
	if mode == "provider_nonzero" {
		os.Exit(7)
	}
	os.Exit(0)
}
