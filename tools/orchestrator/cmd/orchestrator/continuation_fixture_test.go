package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type c04SessionState struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	Cycle     int    `json:"cycle"`
}

func c04NativeFixture() {
	provider := os.Getenv("C04_FIXTURE_PROVIDER")
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		if provider == "claude-code" {
			fmt.Println("2.1.285 (Claude Code)")
		} else {
			fmt.Println("1.2.16")
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("--input-format --output-format --permission-mode --disallowed-tools --mode --resume --conversation --model --effort --json-schema")
		os.Exit(0)
	}
	fail := func(err error) { fmt.Fprintln(os.Stderr, "c04_fixture_failure:", err); os.Exit(19) }
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		fail(fmt.Errorf("input invalid"))
	}
	schema := ""
	resume := ""
	for i, arg := range os.Args {
		if arg == "--json-schema" && i+1 < len(os.Args) {
			schema = os.Args[i+1]
		}
		if (arg == "--resume" || arg == "--conversation") && i+1 < len(os.Args) {
			resume = os.Args[i+1]
		}
	}
	if !strings.Contains(schema, `"technical_question"`) || !strings.Contains(schema, `"additionalProperties":false`) || !bytes.Contains(data, []byte("Agent Bird managed completion v1")) {
		fail(fmt.Errorf("fixed native task schema/instructions missing"))
	}
	if bytes.Contains(data, []byte("Agent Bird technical-question protocol v1")) {
		fail(fmt.Errorf("conflicting shell report instructions reached native task"))
	}
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	state := c04SessionState{SessionID: "c04-session", CWD: cwd, Cycle: 0}
	var transcript string
	if provider == "claude-code" {
		name, config := os.Getenv("CLAUDE_CODE_PROJECT_DIR_NAME"), os.Getenv("CLAUDE_CONFIG_DIR")
		if name == "" || config == "" {
			fail(fmt.Errorf("restricted history env missing"))
		}
		transcript = filepath.Join(config, "projects", name, state.SessionID+".jsonl")
		if resume != "" {
			if resume != state.SessionID {
				fail(fmt.Errorf("wrong native session"))
			}
			f, err := os.Open(transcript)
			if err != nil {
				fail(err)
			}
			dec := json.NewDecoder(f)
			for {
				var next c04SessionState
				err = dec.Decode(&next)
				if err == io.EOF {
					break
				}
				if err != nil {
					fail(err)
				}
				state = next
			}
			f.Close()
			if state.CWD != cwd {
				fail(fmt.Errorf("workspace changed"))
			}
		}
	} else {
		body, _ := json.Marshal(state)
		method := "POST"
		if resume != "" {
			method = "GET"
		}
		request, err := http.NewRequest(method, os.Getenv("C04_FIXTURE_BACKEND")+"/"+resume, bytes.NewReader(body))
		if err != nil {
			fail(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			fail(err)
		}
		if response.StatusCode != 200 {
			fail(fmt.Errorf("backend session missing"))
		}
		if err = json.NewDecoder(response.Body).Decode(&state); err != nil {
			fail(err)
		}
		response.Body.Close()
		if state.CWD != cwd {
			fail(fmt.Errorf("workspace changed"))
		}
	}
	if state.Cycle > 0 && resume == "" {
		fail(fmt.Errorf("session restarted"))
	}
	if state.Cycle > 0 && os.Getenv("C04_FIXTURE_MODE") != "interrupted" && !bytes.Contains(data, []byte(fmt.Sprintf("answer-%d", state.Cycle))) {
		fail(fmt.Errorf("answer missing"))
	}
	if os.Getenv("C04_FIXTURE_ROLE") == "implementer" {
		if state.Cycle > 0 {
			body, err := os.ReadFile("owned")
			if err != nil || !strings.Contains(string(body), fmt.Sprintf("dirty-%d", state.Cycle)) {
				fail(fmt.Errorf("uncommitted work lost"))
			}
		}
		if err = os.WriteFile("owned", []byte(fmt.Sprintf("dirty-%d\n", state.Cycle+1)), 0600); err != nil {
			fail(err)
		}
	}
	state.Cycle++
	if provider == "claude-code" {
		f, err := os.OpenFile(transcript, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			fail(err)
		}
		if err = json.NewEncoder(f).Encode(state); err != nil {
			fail(err)
		}
		f.Sync()
		f.Close()
		fmt.Printf("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":%q}\n", state.SessionID)
	} else {
		body, _ := json.Marshal(state)
		response, err := http.Post(os.Getenv("C04_FIXTURE_BACKEND")+"/save", "application/json", bytes.NewReader(body))
		if err != nil || response.StatusCode != 200 {
			fail(fmt.Errorf("backend save failed"))
		}
		response.Body.Close()
		fmt.Printf("{\"event\":\"init\",\"conversation_id\":%q}\n", state.SessionID)
	}
	if os.Getenv("C04_FIXTURE_MODE") == "report-bypass" {
		c04LegacyReportBypassFixture(provider, state.SessionID, fail)
		os.Exit(0)
	}
	if os.Getenv("C04_FIXTURE_MODE") == "interrupted" {
		if state.Cycle == 1 {
			time.Sleep(30 * time.Second)
			fail(fmt.Errorf("explicit interrupted tree was not stopped"))
		}
		if !bytes.Contains(data, []byte(interruptedContinuationPrompt)) || bytes.Contains(data, []byte("Original bounded task")) {
			fail(fmt.Errorf("missing explicit continuation or original task rerun"))
		}
	}
	kind, text := "result", "Completed after two answers"
	if state.Cycle <= 2 && os.Getenv("C04_FIXTURE_MODE") != "interrupted" {
		kind, text = "technical_question", fmt.Sprintf("Technical question %d", state.Cycle)
	}
	structured, err := json.Marshal(map[string]any{"version": 1, "kind": kind, "text": text})
	if err != nil {
		fail(err)
	}
	if provider == "claude-code" {
		fmt.Printf("{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":%q,\"is_error\":false,\"permission_denials\":[],\"result\":\"native aggregate commentary\",\"structured_output\":%s}\n", state.SessionID, structured)
	} else {
		fmt.Printf("{\"event\":\"result\",\"result\":{\"conversation_id\":%q,\"status\":\"SUCCESS\",\"response\":\"native aggregate commentary\",\"structured_output\":%s}}\n", state.SessionID, structured)
	}
	os.Exit(0)
}

func TestPublicTypedQuestionTwoCyclesAcrossRealSourceHostRestart(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		for _, role := range []adapter.Role{adapter.Reviewer, adapter.Implementer} {
			t.Run(string(provider)+"/"+string(role), func(t *testing.T) { runC04PublicContinuationFixture(t, provider, role, "question") })
		}
	}
}

func TestPublicTypedInterruptedContinuationKeepsSessionAndBudget(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		for _, role := range []adapter.Role{adapter.Reviewer, adapter.Implementer} {
			t.Run(string(provider)+"/"+string(role), func(t *testing.T) { runC04PublicContinuationFixture(t, provider, role, "interrupted") })
		}
	}
}

func TestPublicTypedAGYChangedOwnedHistoryRejectsBeforeNewWorker(t *testing.T) {
	runC04PublicContinuationFixture(t, adapter.ProviderAGY, adapter.Reviewer, "changed-history")
}

func runC04PublicContinuationFixture(t *testing.T, provider adapter.Provider, role adapter.Role, mode string) {
	root, err := os.MkdirTemp(os.TempDir(), "c04-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	sqliteAGY := provider == adapter.ProviderAGY && mode != "report-bypass"
	bin, native := c04BuildExecutables(t, root, sqliteAGY)
	state := filepath.Join(root, "state")
	defer func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	}()
	defer c04SaveFixtureFailure(t, root, state)
	t.Setenv("C04_FIXTURE_PROVIDER", string(provider))
	t.Setenv("C04_FIXTURE_ROLE", string(role))
	fixtureMode := mode
	if mode == "changed-history" {
		fixtureMode = "question"
	}
	t.Setenv("C04_FIXTURE_MODE", fixtureMode)
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	config := filepath.Join(root, "config")
	if err = os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("CLAUDE_CODE_SKIP_PROMPT_HISTORY", "")
	home := ""
	if sqliteAGY {
		home = c04SyntheticAGYHome(t, root)
		t.Setenv("HOME", home)
	} else {
		var mu sync.Mutex
		var session c04SessionState
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if r.Method == "POST" {
				var next c04SessionState
				if json.NewDecoder(r.Body).Decode(&next) != nil {
					w.WriteHeader(400)
					return
				}
				if r.URL.Path == "/save" {
					session = next
				} else if session.SessionID != "" {
					w.WriteHeader(409)
					return
				} else {
					session = next
				}
				json.NewEncoder(w).Encode(session)
				return
			}
			if strings.TrimPrefix(r.URL.Path, "/") != session.SessionID || session.SessionID == "" {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(session)
		}))
		defer backend.Close()
		t.Setenv("C04_FIXTURE_BACKEND", backend.URL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(root, "request.json")
	write := func(value any) {
		t.Helper()
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(request, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"controller_thread": "c04-owner", "origin_pid": os.Getpid(), "origin_birth": birth})
	var owner map[string]any
	if err = json.Unmarshal(runBinary(t, ctx, bin, "owner-bind", "--state-dir", state, "--request", request), &owner); err != nil {
		t.Fatal(err)
	}
	delete(owner, "version")
	owner["run_id"] = "run"
	owner["plan_revision"] = 1
	owner["delivery_mode"] = "collect"
	contents, err := os.ReadFile(native)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	version := "1.2.16"
	if provider == adapter.ProviderClaude {
		version = "2.1.285 (Claude Code)"
	}
	directory := filepath.Join(root, "workspace")
	permission := adapter.ReadOnly
	var workspace *adapter.CandidateWorkspace
	if role == adapter.Implementer {
		repo := filepath.Join(root, "repo")
		if err = os.Mkdir(repo, 0700); err != nil {
			t.Fatal(err)
		}
		git := func(args ...string) string {
			t.Helper()
			out, err := exec.Command("/usr/bin/git", append([]string{"-C", repo}, args...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("git: %s %v", out, err)
			}
			return strings.TrimSpace(string(out))
		}
		git("init", "-q")
		git("config", "user.name", "Test")
		git("config", "user.email", "test@example.invalid")
		os.WriteFile(filepath.Join(repo, "owned"), []byte("base\n"), 0600)
		git("add", ".")
		git("commit", "-qm", "base")
		workspace = &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: git("rev-parse", "HEAD"), Paths: []string{"owned"}, AutoDirectory: true}
		permission = adapter.WorkspaceWrite
	} else if err = os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(adapter.InvocationPayload{Provider: string(provider), Directory: directory, Prompt: "Original bounded task", ProviderLock: &adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: adapter.BinaryPin{Path: native, Version: version, SHA256: hex.EncodeToString(sum[:])}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: role, Permission: permission, TimeoutMS: 10000}, CandidateWorkspace: workspace})
	owner["tasks"] = []any{map[string]any{"id": "task", "max_attempts": 3, "max_active_ms": 20000, "adapter": json.RawMessage(raw)}}
	write(owner)
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", request), &submitted); err != nil {
		t.Fatal(err)
	}
	launch := c04FixtureLaunchFromStore(t, ctx, state, "run", "task")
	handle := filepath.Join(root, "handle.json")
	if err = writeExclusiveJSON(handle, taskHandle{Version: 1, RunID: "run", StateDir: state, ControlFile: submitted.ControlFile, TaskIDs: []string{"task"}, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if mode == "report-bypass" {
		c04AssertNoLegacyReportAuthority(t, ctx, state, provider)
		return
	}
	wait := func(want string) {
		t.Helper()
		for {
			status := taskStatus(t, ctx, bin, state, "task", submitted.ControlFile)
			if status.Status == want {
				return
			}
			if status.Status == "failed" || status.Status == "unknown" || ctx.Err() != nil {
				log, _ := os.ReadFile(filepath.Join(state, "source-host.log"))
				t.Fatalf("wanted %s got %+v: %s", want, status, log)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if mode == "interrupted" {
		wait("running")
		// Readiness of a worker is not a session binding; wait for trusted init.
		for {
			db, err := store.Open(filepath.Join(state, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			var count int
			err = db.SQL().QueryRow("SELECT COUNT(*) FROM segment_sessions").Scan(&count)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if count == 1 {
				break
			}
			if ctx.Err() != nil {
				t.Fatal("trusted native init not observed")
			}
			time.Sleep(10 * time.Millisecond)
		}
		runBinary(t, ctx, bin, "task", "stop", "--handle", handle, "--work-revision", "1")
		wait("interrupted")
		var summary store.RunSummary
		if err = json.Unmarshal(runBinary(t, ctx, bin, "task", "inspect", "--handle", handle), &summary); err != nil {
			t.Fatal(err)
		}
		if len(summary.Tasks) != 1 || summary.Tasks[0].ContinuationSource == nil {
			t.Fatalf("missing observed continuation source: %+v", summary)
		}
		source := summary.Tasks[0].ContinuationSource
		decision := store.ResumeContinuationSpec{TaskID: "task", WorkRevision: 1, SegmentID: source.SegmentID, EventID: source.EventID, EventRevision: source.EventRevision, EventHash: source.EventHash, ActionSlot: source.ActionSlot, CommandID: "resume-public"}
		// Public request cannot supply or change the native session or routing.
		for _, extra := range []map[string]any{{"session_id": "forged-session"}, {"control_file": submitted.ControlFile}} {
			raw, _ := json.Marshal(decision)
			var value map[string]any
			json.Unmarshal(raw, &value)
			for key, item := range extra {
				value[key] = item
			}
			write(value)
			out, err := exec.CommandContext(ctx, bin, "task", "resume", "--handle", handle, "--request", request).CombinedOutput()
			if err == nil {
				t.Fatalf("untrusted continuation fields admitted: %s", out)
			}
		}
		write(decision)
		var first, repeat store.ContinuationReceipt
		if err = json.Unmarshal(runBinary(t, ctx, bin, "task", "resume", "--handle", handle, "--request", request), &first); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(runBinary(t, ctx, bin, "task", "resume", "--handle", handle, "--request", request), &repeat); err != nil {
			t.Fatal(err)
		}
		if first.DecisionID == "" || first != repeat {
			t.Fatalf("decision replay changed identity: %+v/%+v", first, repeat)
		}
	} else {
		for cycle := 1; cycle <= 2; cycle++ {
			wait("waiting_question")
			var collection struct {
				Events []contract.Event `json:"events"`
			}
			if err = json.Unmarshal(runBinary(t, ctx, bin, "task", "collect", "--handle", handle), &collection); err != nil {
				t.Fatal(err)
			}
			var q contract.Event
			for _, e := range collection.Events {
				if e.Kind == contract.EventQuestion && e.QuestionRevision == cycle {
					q = e
				}
			}
			if q.EventID == "" || q.QuestionKind != "technical" {
				t.Fatalf("missing technical question: %+v", collection)
			}
			if sqliteAGY {
				c04WaitSegmentSettled(t, ctx, state, q.SegmentID)
				c04AssertQuestionLaunch(t, ctx, state, launch, q)
			}
			if mode == "changed-history" {
				seal := c04ChangeOwnedAGYDatabase(t, ctx, state, home, launch, q)
				write(map[string]any{"task_id": "task", "work_revision": 1, "question_id": q.QuestionID, "question_revision": q.QuestionRevision, "answer": "answer-1"})
				runBinary(t, ctx, bin, "task", "answer", "--handle", handle, "--request", request)
				wait("failed")
				c04AssertChangedHistoryRefused(t, ctx, state, launch, q, seal)
				return
			}
			if cycle == 1 {
				db, err := store.Open(filepath.Join(state, "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				var pid int
				var oldBirth string
				if err = db.SQL().QueryRow(`SELECT h.pid,h.birth FROM runtime_hosts h JOIN segment_runtime r ON r.host_id=h.id WHERE r.segment_id=?`, q.SegmentID).Scan(&pid, &oldBirth); err != nil {
					t.Fatal(err)
				}
				db.Close()
				if err = syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
					if current, err := process.Birth(pid); err != nil || current != oldBirth {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				time.Sleep(100 * time.Millisecond)
			}
			write(map[string]any{"task_id": "task", "work_revision": 1, "question_id": q.QuestionID, "question_revision": q.QuestionRevision, "answer": fmt.Sprintf("answer-%d", cycle)})
			runBinary(t, ctx, bin, "task", "answer", "--handle", handle, "--request", request)
		}
	}
	wait("result_ready")
	db, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attempts, segments, decisions int
	db.SQL().QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&attempts)
	db.SQL().QueryRow(`SELECT COUNT(*) FROM segments`).Scan(&segments)
	db.SQL().QueryRow(`SELECT COUNT(*) FROM continuation_decisions`).Scan(&decisions)
	wantSegments, wantDecisions := 3, 2
	if mode == "interrupted" {
		wantSegments, wantDecisions = 2, 1
	}
	if attempts != 1 || segments != wantSegments || decisions != wantDecisions {
		t.Fatalf("continuation restarted attempt or replayed decision: %d/%d/%d", attempts, segments, decisions)
	}
	var granted, used int64
	db.SQL().QueryRow(`SELECT SUM(granted_ms),SUM(used_ms) FROM budget_runtime`).Scan(&granted, &used)
	if used <= 0 || used >= 20000 || granted <= 20000 {
		t.Fatalf("cumulative budget invalid: granted=%d used=%d", granted, used)
	}
	if sqliteAGY {
		want := 2
		if mode == "interrupted" {
			want = 1
		}
		if got := c04LogAGYSeals(t, state, launch); got != want {
			t.Fatal("original completed AGY segments lack their own seals", got, want)
		}
	}
}
