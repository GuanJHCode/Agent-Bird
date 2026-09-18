package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Real CLI/coordinator/source-Host/ACK/owner decisions, with a protocol fixture
// replacing only the model. This is not autonomous main-agent acceptance.
func TestCLICandidateDevelopmentDAG(t *testing.T)       { exerciseCLICandidateDAG(t, false, false) }
func TestCLIReworkCandidateDevelopmentDAG(t *testing.T) { exerciseCLICandidateDAG(t, true, false) }

func TestCLIReviewRetryCandidateDevelopmentDAG(t *testing.T) { exerciseCLICandidateDAG(t, false, true) }

func TestCLITaskHandleReworkCandidateDAG(t *testing.T) { exerciseCLICandidateDAG(t, true, false, true) }

func exerciseCLICandidateDAG(t *testing.T, rework, retryReview bool, facade ...bool) {
	root, err := os.MkdirTemp("/tmp", "candidate-cli-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	clean := false
	defer func() {
		if clean {
			_ = os.RemoveAll(root)
		} else {
			t.Logf("retained failed runtime: %s", root)
		}
	}()
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "orchestrator")
	defer func() {
		if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
			stopCoordinator(t, state)
		}
	}()
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("/usr/bin/git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := filepath.Join(root, "repo")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.name", "CLI Test")
	git(repo, "config", "user.email", "test@example.invalid")
	for _, name := range []string{"README", "obsolete.txt"} {
		if err = os.WriteFile(filepath.Join(repo, name), []byte("base\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "base")
	base := git(repo, "rev-parse", "HEAD")
	binary, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	mode := "success"
	if rework {
		mode = "rework"
	}
	if retryReview {
		mode = "retry_review"
	}
	t.Setenv("CANDIDATE_FIXTURE_MODE", mode)
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	lock := map[string]any{"version": 1, "provider": "claude-code", "protocol": "claude-stream-json-v1", "binary": map[string]string{"path": binary, "version": "fixture-v1", "sha256": hex.EncodeToString(sum[:])}}
	profile := func(role, permission string) map[string]any {
		return map[string]any{"version": 1, "role": role, "permission": permission, "timeout_ms": 15000}
	}
	target := map[string]string{"worktree": repo, "ref": "refs/heads/main", "base_oid": base}
	action := func(operation, source string) map[string]any {
		return map[string]any{"version": 1, "operation": operation, "source_task": source, "target": target}
	}
	check := action("validate", "implement")
	check["command"] = []string{"/usr/bin/grep", "-q", "worker implementation", "README"}
	tasks := []any{
		map[string]any{"id": "implement", "max_attempts": 3, "adapter": map[string]any{"provider": "claude-code", "provider_lock": lock, "profile": profile("implementer", "workspace-write"), "directory": filepath.Join(root, "worker"), "prompt": "fixture implement", "candidate_workspace": map[string]any{"version": 1, "repo_root": repo, "base_oid": base, "paths": []string{"README", "added.txt", "obsolete.txt"}}}},
		map[string]any{"id": "validate", "dependencies": []string{"implement"}, "max_attempts": 3, "adapter": map[string]any{"kind": "candidate", "directory": filepath.Join(root, "validation"), "candidate_action": check}},
		map[string]any{"id": "review", "dependencies": []string{"validate"}, "max_attempts": 3, "adapter": map[string]any{"provider": "claude-code", "provider_lock": lock, "profile": profile("reviewer", "read-only"), "directory": filepath.Join(root, "review"), "prompt": "independent review", "candidate_action": action("review", "validate")}},
		map[string]any{"id": "integrate", "dependencies": []string{"review"}, "max_attempts": 3, "adapter": map[string]any{"kind": "candidate", "directory": repo, "candidate_action": action("integrate", "review")}},
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(root, "request.json")
	write := func(value any) {
		t.Helper()
		body, _ := json.Marshal(value)
		if err = os.WriteFile(requestPath, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"controller_thread": "main-agent", "origin_pid": os.Getpid(), "origin_birth": birth})
	var owner map[string]any
	if err = json.Unmarshal(runBinary(t, ctx, bin, "owner-bind", "--state-dir", state, "--request", requestPath), &owner); err != nil {
		t.Fatal(err)
	}
	delete(owner, "version")
	owner["run_id"] = "development"
	owner["plan_revision"] = 7
	owner["delivery_mode"] = "collect"
	owner["tasks"] = tasks
	write(owner)
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", requestPath), &submitted); err != nil {
		t.Fatal(err)
	}
	handle := filepath.Join(root, "task-handle.json")
	if len(facade) > 0 && facade[0] {
		if err = writeExclusiveJSON(handle, taskHandle{Version: 1, RunID: "development", StateDir: state, ControlFile: submitted.ControlFile, TaskIDs: []string{"implement", "validate", "review", "integrate"}, Status: "queued"}); err != nil {
			t.Fatal(err)
		}
	}
	candidateOID, firstCandidate := "", ""
	var firstEvent contract.Event
	revision := 1
	sequence := []string{"implement", "validate", "review", "integrate"}
	if rework {
		sequence = []string{"implement", "validate", "review", "implement", "validate", "review", "integrate"}
	}
	if retryReview {
		sequence = []string{"implement", "validate", "review", "review", "integrate"}
	}
	retriedReview := false
	for _, task := range sequence {
		interruptedReview := retryReview && !retriedReview && task == "review"
		rejectedReview := rework && revision == 1 && task == "review"
		for {
			s := taskStatus(t, ctx, bin, state, task, submitted.ControlFile)
			if s.Status == "result_ready" || rejectedReview && s.Status == "failed" || interruptedReview && s.Status == "interrupted" {
				break
			}
			if s.Status == "failed" || s.Status == "unknown" || s.Status == "blocked_dependency" || ctx.Err() != nil {
				t.Fatalf("%s stopped: %+v", task, s)
			}
			time.Sleep(50 * time.Millisecond)
		}
		var collection struct {
			DeliveryID string           `json:"delivery_id"`
			Proof      string           `json:"collection_proof_sha256"`
			Events     []contract.Event `json:"events"`
		}
		if err = json.Unmarshal(runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", task, "--control-file", submitted.ControlFile), &collection); err != nil {
			t.Fatal(err)
		}
		if interruptedReview {
			if len(collection.Events) != 1 || collection.Events[0].Kind != contract.EventStopped || collection.Events[0].Artifact != nil {
				t.Fatalf("missing technical interruption: %+v", collection)
			}
			e := collection.Events[0]
			write(map[string]any{"version": 1, "task_id": task, "control_file": submitted.ControlFile, "delivery_id": collection.DeliveryID, "collection_proof_sha256": collection.Proof, "decisions": []map[string]any{{"event_id": e.EventID, "event_revision": e.EventRevision, "event_hash": e.PayloadHash, "action_slot": e.ActionSlot, "decision": "handled", "command_id": "ack-review-stopped"}}})
			runBinary(t, ctx, bin, "ack", "--state-dir", state, "--request", requestPath)
			var receipt []byte
			for i := 0; i < 2; i++ {
				b := runBinary(t, ctx, bin, "retry", "--state-dir", state, "--task-id", task, "--control-file", submitted.ControlFile, "--work-revision", "1", "--event-id", e.EventID, "--event-revision", strconv.FormatInt(e.EventRevision, 10), "--event-hash", e.PayloadHash, "--action-slot", e.ActionSlot, "--segment-id", e.SegmentID, "--next-attempt", "2", "--command-id", "retry-review")
				if i == 0 {
					receipt = b
				} else if string(receipt) != string(b) {
					t.Fatal("duplicate review retry changed receipt")
				}
			}
			retriedReview = true
			continue
		}
		if len(collection.Events) != 1 || collection.Events[0].Artifact == nil {
			t.Fatalf("missing candidate result: %+v", collection)
		}
		event := collection.Events[0]
		data, err := os.ReadFile(event.Artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		var artifact struct {
			Candidate struct {
				OID string `json:"candidate_oid"`
			} `json:"candidate"`
		}
		if err = json.Unmarshal(data, &artifact); err != nil {
			t.Fatal(err)
		}
		if task == "implement" {
			candidateOID = artifact.Candidate.OID
			if revision == 1 {
				firstEvent, firstCandidate = event, candidateOID
			}
			if revision == 2 {
				var revised struct {
					Candidate struct {
						Binding struct {
							RevisionSHA256 string `json:"revision_sha256"`
						} `json:"binding"`
					} `json:"candidate"`
				}
				if json.Unmarshal(data, &revised) != nil || revised.Candidate.Binding.RevisionSHA256 == "" || candidateOID == firstCandidate {
					t.Fatal("rework did not freeze a new feedback-bound candidate")
				}
			}
		}
		if candidateOID == "" || artifact.Candidate.OID != candidateOID {
			t.Fatalf("%s changed candidate identity", task)
		}
		write(map[string]any{"version": 1, "task_id": task, "control_file": submitted.ControlFile, "delivery_id": collection.DeliveryID, "collection_proof_sha256": collection.Proof, "decisions": []map[string]any{{"event_id": event.EventID, "event_revision": event.EventRevision, "event_hash": event.PayloadHash, "action_slot": event.ActionSlot, "decision": "handled", "command_id": "ack-" + task + "-" + strconv.Itoa(revision)}}})
		for i := 0; i < 2; i++ {
			runBinary(t, ctx, bin, "ack", "--state-dir", state, "--request", requestPath)
		}
		if rejectedReview {
			write(map[string]any{"task_id": "implement", "work_revision": 1, "control_file": submitted.ControlFile,
				"candidate":     map[string]any{"event_id": firstEvent.EventID, "event_revision": firstEvent.EventRevision, "event_hash": firstEvent.PayloadHash},
				"candidate_oid": firstCandidate, "review_task_id": "review", "review_work_revision": 1,
				"review":      map[string]any{"event_id": event.EventID, "event_revision": event.EventRevision, "event_hash": event.PayloadHash},
				"action_slot": event.ActionSlot, "feedback": "Add the requested revised behavior", "acceptance": "ACCEPTANCE_MARKER: README must contain revised", "command_id": "rework-first"})
			var firstReceipt []byte
			for i := 0; i < 2; i++ {
				var b []byte
				if len(facade) > 0 && facade[0] {
					var decision map[string]any
					if err = readPrivateJSON(requestPath, &decision); err != nil {
						t.Fatal(err)
					}
					delete(decision, "control_file")
					facadePath := filepath.Join(root, "facade-decision.json")
					body, _ := json.Marshal(decision)
					if err = os.WriteFile(facadePath, body, 0600); err != nil {
						t.Fatal(err)
					}
					b = runBinary(t, ctx, bin, "task", "rework", "--handle", handle, "--request", facadePath)
				} else {
					b = runBinary(t, ctx, bin, "rework", "--state-dir", state, "--request", requestPath)
				}
				if i == 0 {
					firstReceipt = b
				} else if string(b) != string(firstReceipt) {
					t.Fatal("duplicate rework changed receipt")
				}
			}
			revision = 2
			continue
		}
		for i := 0; i < 2; i++ {
			runBinary(t, ctx, bin, "accept", "--state-dir", state, "--task-id", task, "--control-file", submitted.ControlFile, "--work-revision", strconv.Itoa(revision), "--event-id", event.EventID, "--event-revision", strconv.FormatInt(event.EventRevision, 10), "--event-hash", event.PayloadHash, "--action-slot", event.ActionSlot, "--decision", "accept", "--command-id", "accept-"+task+"-"+strconv.Itoa(revision))
		}
	}
	if git(repo, "rev-parse", "HEAD") != candidateOID || git(repo, "status", "--porcelain") != "" {
		t.Fatal("final target differs from accepted candidate")
	}
	clean = true
}
