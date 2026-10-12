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

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
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
		{name: "grok_review"},
		{name: "indexed_review"},
		{name: "indexed_review_config"},
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
			if strings.HasPrefix(tc.name, "indexed_review") {
				for i := 0; i < 65; i++ {
					if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("context-%03d.txt", i)), []byte(strings.Repeat("é", 1100)), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
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
			if tc.name == "indexed_review_config" {
				var configured map[string]any
				if err := json.Unmarshal(payload, &configured); err != nil {
					t.Fatal(err)
				}
				configured["config_generation"] = &contract.ConfigGenerationRef{SchemaVersion: 1, OwnerScopeSHA256: strings.Repeat("a", 64), GenerationID: strings.Repeat("b", 64), ManifestSHA256: strings.Repeat("c", 64), PackageVersion: "1.0.0"}
				configured["config_model_source"] = "cli-default"
				configured["profile"].(map[string]any)["cli_model_default"] = true
				payload, _ = json.Marshal(configured)
			}
			grant := contract.LaunchCommand{RunID: fmt.Sprintf("fixture-%x", sha256.Sum256([]byte(root))), TaskID: "implement", AttemptID: "attempt", SegmentID: "segment", CommandID: "command", ReservationID: "reservation", WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 15000, AdapterPayload: payload}
			grant = candidateHostGrant(t, grant, "launch")
			inv, err := invocationForGrant(context.Background(), grant)
			if err != nil {
				t.Fatalf("managed implementer request rejected: %v", err)
			}
			h, err := host.NewIPC(filepath.Join(root, "state", "host-spool", grant.HostID), grant.HostID)
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
			if artifact.ProviderResult != "implemented" || c.CandidateOID == "" || c.Binding.RunID != grant.RunID || c.Binding.TaskID != "implement" || c.Binding.AttemptID != "attempt" || c.Binding.SegmentID != "segment" || c.Binding.WorkRevision != 1 {
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

			if tc.name == "grok_review" {
				t.Setenv("GROK_REVIEW_FIXTURE", "1")
				home := filepath.Join(root, "grok-home")
				if err := os.Mkdir(home, 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GROK_HOME", home)
			}
			if strings.HasPrefix(tc.name, "indexed_review") {
				exerciseIndexedCandidateDelivery(t, root, repo, base, grant, result, git)
			} else {
				exerciseCandidateDelivery(t, root, repo, base, grant, result, git)
			}

		})
	}
}

// Reuse the native test executable: first execution of a fresh unsigned shell
// script can exceed the production 2s probe deadline under full-suite load.
func TestMain(m *testing.M) {
	// Exercise the same private worker route when the Host pins this test binary.
	if len(os.Args) > 1 && os.Args[1] == "gitops-worker" {
		if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"status": "error", "error": errorCode(err)})
			os.Exit(2)
		}
		os.Exit(0)
	}
	if filepath.Base(os.Args[0]) == "resource-memory-diagnostic-cli-fixture" {
		runResourceMemoryDiagnosticFixture()
		return
	}
	if filepath.Base(os.Args[0]) == "resource-soak-worker-fixture" {
		runResourceSoakWorkerFixture()
		return
	}
	if filepath.Base(os.Args[0]) == "supervised-config-worker-fixture" {
		runSupervisedConfigWorkerFixture()
		return
	}
	if filepath.Base(os.Args[0]) == "codex-orchestrator" && strings.Contains(filepath.ToSlash(os.Args[0]), "/supervised-config-entry-") {
		runSupervisedConfigEntryFixture()
		return
	}
	if filepath.Base(os.Args[0]) == "public-owner-succession-cli-fixture" {
		runPublicOwnerSuccessionFixture()
		return
	}
	if filepath.Base(os.Args[0]) == "public-owner-succession-wait-fixture" {
		os.Exit(17)
	}
	if filepath.Base(os.Args[0]) == "budget-revision-worker-fixture" {
		runBudgetRevisionWorkerFixture()
		return
	}
	if filepath.Base(os.Args[0]) == "protocol2-native-worker-fixture" {
		if os.Getenv("OWNER_LAUNCH_SOURCE_SENTINEL") != protocol2EnvironmentSentinel {
			os.Exit(87)
		}
		runOriginalOwnerWorkerFixture()
		os.Exit(0)
	}
	if filepath.Base(os.Args[0]) == "original-owner-worker-fixture" {
		runOriginalOwnerWorkerFixture()
		return
	}
	if filepath.Base(os.Args[0]) == "protocol2-native-cli-fixture" {
		runProtocol2NativeFixture()
		return
	}
	if filepath.Base(os.Args[0]) == "protocol2-loss-cli-fixture" {
		runProtocol2LossFixture()
		return
	}
	if os.Getenv("C04_FIXTURE_PROVIDER") != "" {
		c04NativeFixture()
		return
	}
	mode := os.Getenv("CANDIDATE_FIXTURE_MODE")
	grokFixture := os.Getenv("GROK_REVIEW_FIXTURE") != ""
	indexedFixture := os.Getenv("GROK_INDEXED_FIXTURE") != ""
	agyFixture := false
	// A single real source-host can run both providers. These copied test
	// executables select their protocol by identity, never shared task env.
	switch filepath.Base(os.Args[0]) {
	case "indexed-claude-fixture":
		mode, grokFixture, indexedFixture = "success", false, false
	case "indexed-grok-fixture", "indexed-grok-budget-fixture":
		mode, grokFixture, indexedFixture = "review_approve", true, true
	case "compose-cross-claude-fixture":
		mode, grokFixture, indexedFixture = "compose-a", false, false
	case "compose-cross-agy-fixture":
		mode, grokFixture, indexedFixture, agyFixture = "compose-b", false, false, true
	case "compose-cross-review-claude-fixture":
		mode, grokFixture, indexedFixture = "review_approve", false, false
	}
	if mode == "" {
		os.Exit(m.Run())
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		if grokFixture {
			fmt.Println("grok 1.0.34 (3736acbc8658)")
			os.Exit(0)
		}
		fmt.Println("fixture-v1")
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("--output-format --input-format --permission-mode --print --mode --disallowed-tools --json-schema --prompt-file --session-id --output --model --allowed-tools --denied-tools --no-subagents --disable-web-search --leader-socket --tools --deny --allow")
		os.Exit(0)
	}
	if grokFixture && len(os.Args) == 2 && os.Args[1] == "models" {
		fmt.Println("fixture model")
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
		if (mode == "success" || reworking || mode == "retry_review" || mode == "compose-public") && arg == "--permission-mode" && i+1 < len(os.Args) && os.Args[i+1] == "plan" {
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
		if grokFixture {
			var input, session string
			for i, arg := range os.Args {
				if i+1 < len(os.Args) {
					if arg == "--prompt-file" {
						input = os.Args[i+1]
					}
					if arg == "--session-id" {
						session = os.Args[i+1]
					}
				}
			}
			snapshot, err := os.ReadFile(input)
			if err != nil || session == "" || !strings.Contains(string(snapshot), "complete-tracked-text-base-and-candidate") || !indexedFixture && !strings.Contains(string(snapshot), "worker implementation") {
				os.Exit(8)
			}
			if indexedFixture {
				_, raw, ok := strings.Cut(string(snapshot), "Host snapshot JSON:\n")
				var supplied struct {
					Chunks []struct {
						Content, SHA256 string
						Length          int
					}
				}
				if !ok || json.Unmarshal([]byte(raw), &supplied) != nil || len(supplied.Chunks) == 0 {
					os.Exit(9)
				}
				for _, chunk := range supplied.Chunks {
					sum := sha256.Sum256([]byte(chunk.Content))
					if len(chunk.Content) != chunk.Length || hex.EncodeToString(sum[:]) != chunk.SHA256 {
						os.Exit(10)
					}
				}
				if filepath.Base(os.Args[0]) == "indexed-grok-budget-fixture" {
					cwd, err := os.Getwd()
					if err != nil {
						os.Exit(12)
					}
					if filepath.Base(cwd) == "review-1" {
						for end := time.Now().Add(30 * time.Second); time.Now().Before(end); {
							if _, err := os.Stat(filepath.Join(cwd, "release-first")); err == nil {
								os.Exit(7)
							}
							time.Sleep(10 * time.Millisecond)
						}
						os.Exit(13)
					}
				}
				sum := sha256.Sum256(snapshot)
				body, _ = json.Marshal(map[string]string{"decision": decision, "summary": "consumed-prompt-sha256:" + hex.EncodeToString(sum[:])})
			}
			envelope := map[string]any{"type": "end", "stopReason": "end_turn", "sessionId": session, "structuredOutput": json.RawMessage(body)}
			if mode == "review_unstructured" {
				delete(envelope, "structuredOutput")
			}
			if mode == "review_invalid" {
				envelope["structuredOutput"] = map[string]any{"decision": "approve", "summary": "reviewed", "untrusted_extra": true}
			}
			result, _ := json.Marshal(envelope)
			fmt.Println(string(result))
			os.Exit(0)
		}
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
	if mode == "compose-public" {
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
		if err != nil {
			os.Exit(3)
		}
		if strings.Contains(string(input), "COMPOSE_FIXTURE_A") {
			mode = "compose-a"
		}
		if strings.Contains(string(input), "COMPOSE_FIXTURE_B") {
			mode = "compose-b"
		}
		if mode == "compose-public" {
			os.Exit(3)
		}
	}
	if mode == "compose-a" || mode == "compose-b" {
		managedSchema := false
		for i, arg := range os.Args {
			if arg == "--json-schema" && i+1 < len(os.Args) && os.Args[i+1] == adapter.ManagedTaskSchema() {
				managedSchema = true
			}
		}
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
		var envelope struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err != nil || json.Unmarshal(input, &envelope) != nil || !managedSchema {
			os.Exit(11)
		}
		var prompt string
		if agyFixture {
			if json.Unmarshal(envelope.Message.Content, &prompt) != nil {
				os.Exit(11)
			}
		} else {
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(envelope.Message.Content, &content) != nil || len(content) != 1 || content[0].Type != "text" {
				os.Exit(11)
			}
			prompt = content[0].Text
		}
		newGeneration := strings.Contains(prompt, "Owner-authorized new generation.")
		if newGeneration && (!strings.Contains(prompt, "NEW_BASE_FEEDBACK_MARKER") || !strings.Contains(prompt, "NEW_BASE_ACCEPTANCE_MARKER")) {
			os.Exit(11)
		}
		if !newGeneration && prompt != "controlled implementation\n\n"+adapter.ManagedTaskInstruction() {
			os.Exit(11)
		}
		name := strings.TrimPrefix(mode, "compose-") + ".txt"
		content := mode + "\n"
		if newGeneration {
			content = mode + "-generation-two\n"
			if strings.Contains(prompt, "NEW_BASE_THIRD_MARKER") {
				content = mode + "-generation-three\n"
			}
		}
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			os.Exit(3)
		}
		if agyFixture {
			fmt.Println(`{"event":"init","conversation_id":"compose-fixture"}`)
			fmt.Println(`{"event":"result","result":{"status":"SUCCESS","conversation_id":"compose-fixture","response":"ordinary-agy-result-is-non-authoritative","structured_output":{"version":1,"kind":"result","text":"compose-b-structured"}}}`)
		} else {
			fmt.Println(`{"type":"system","subtype":"init","session_id":"compose-fixture"}`)
			fmt.Println(`{"type":"result","subtype":"success","session_id":"compose-fixture","result":"ordinary-claude-result-is-non-authoritative","structured_output":{"version":1,"kind":"result","text":"compose-a-structured"}}`)
		}
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
	if filepath.Base(os.Args[0]) == "indexed-claude-fixture" {
		if err := os.Rename("rename-before.txt", "rename-after.txt"); err != nil {
			os.Exit(3)
		}
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
	terminal := map[string]any{"type": "result", "subtype": "success", "session_id": "candidate-fixture", "result": "implemented"}
	for i, arg := range os.Args {
		if arg == "--json-schema" && i+1 < len(os.Args) && strings.Contains(os.Args[i+1], `"technical_question"`) {
			// Only ordinary managed implementation changed its completion
			// contract. Candidate action/review fixtures retain their own shape.
			terminal["structured_output"] = map[string]any{"version": 1, "kind": "result", "text": "implemented"}
			terminal["result"] = "native aggregate commentary"
		}
	}
	body, _ := json.Marshal(terminal)
	fmt.Println(string(body))
	if mode == "provider_nonzero" {
		os.Exit(7)
	}
	os.Exit(0)
}
