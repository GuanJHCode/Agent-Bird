package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/host"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Public CLI, real Coordinator/Store/source-host, real Git and sandbox, with
// only the model replaced by protocol consumers of the actual prompt files.
func TestCLIIndexedCandidateDevelopmentDAG(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "indexed-cli-")
	if err != nil {
		t.Fatal(err)
	}
	clean := false
	t.Cleanup(func() {
		if clean && !t.Failed() {
			_ = os.RemoveAll(root)
		} else {
			t.Logf("retained indexed CLI runtime: %s", root)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	bin, state, repo := filepath.Join(root, "orchestrator"), filepath.Join(root, "state"), filepath.Join(root, "repo")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	for _, path := range []string{state, repo, filepath.Join(root, "grok-home")} {
		if err = os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"CANDIDATE_FIXTURE_MODE", "GROK_REVIEW_FIXTURE", "GROK_INDEXED_FIXTURE", "C04_FIXTURE_PROVIDER"} {
		t.Setenv(key, "")
	}
	t.Setenv("GROK_HOME", filepath.Join(root, "grok-home"))
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	fixture, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(fixture)
	lock := func(name, provider, protocol, version string) map[string]any {
		path := filepath.Join(root, name)
		if err = os.WriteFile(path, fixture, 0700); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"version": 1, "provider": provider, "protocol": protocol, "binary": map[string]string{"path": path, "version": version, "sha256": hex.EncodeToString(sum[:])}}
	}
	claude := lock("indexed-claude-fixture", "claude-code", "claude-stream-json-v1", "fixture-v1")
	grok := lock("indexed-grok-fixture", "grok-build", "grok-streaming-json-v1", "grok 1.0.34 (3736acbc8658)")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("/usr/bin/git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Indexed CLI Test")
	git("config", "user.email", "test@example.invalid")
	for _, name := range []string{"README", "obsolete.txt"} {
		if err = os.WriteFile(filepath.Join(repo, name), []byte("base\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 65; i++ {
		if err = os.WriteFile(filepath.Join(repo, fmt.Sprintf("context-%03d.txt", i)), []byte(strings.Repeat("é", 1100)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(repo, "wide-utf8.txt"), []byte(strings.Repeat("界", 22000)), 0600); err != nil {
		t.Fatal(err)
	}
	renameContent := "preserve renamed context 界 without a final newline"
	if err = os.WriteFile(filepath.Join(repo, "rename-before.txt"), []byte(renameContent), 0600); err != nil {
		t.Fatal(err)
	}
	renameBlob := git("hash-object", "rename-before.txt")
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	stop, epoch := startIndexedCLICoordinator(t, ctx, root, bin, state, 1)
	profile := func(role, permission string) map[string]any {
		return map[string]any{"version": 1, "role": role, "permission": permission, "timeout_ms": 15000}
	}
	action := func(op, source string, ordinal int) map[string]any {
		return map[string]any{"version": 1, "operation": op, "source_task": source, "target": map[string]string{"worktree": repo, "ref": "refs/heads/main", "base_oid": base}, "review_index": map[string]any{"version": 1, "mode": "indexed_candidate_review_v1", "shards": 3, "review_ordinal": ordinal}}
	}
	validate := action("validate", "implement", 0)
	validate["command"] = []string{"/usr/bin/grep", "-q", "worker implementation", "README"}
	tasks := []any{
		map[string]any{"id": "implement", "max_attempts": 1, "adapter": map[string]any{"provider": "claude-code", "provider_lock": claude, "profile": profile("implementer", "workspace-write"), "directory": filepath.Join(root, "worker"), "prompt": "fixture implementation", "candidate_workspace": map[string]any{"version": 1, "repo_root": repo, "base_oid": base, "paths": []string{"README", "added.txt", "obsolete.txt", "rename-before.txt", "rename-after.txt"}}}},
		map[string]any{"id": "validate", "dependencies": []string{"implement"}, "max_attempts": 1, "adapter": map[string]any{"kind": "candidate", "directory": filepath.Join(root, "validation"), "candidate_action": validate}},
	}
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("review-%d", i)
		p := profile("reviewer", "read-only")
		p["grok_session_write"] = true
		tasks = append(tasks, map[string]any{"id": name, "dependencies": []string{"validate"}, "max_attempts": 2, "max_active_ms": 15000, "execution_policy": map[string]any{"version": 1, "group_max_attempts": 2, "group_max_active_ms": 30000, "task_max_active_ms": 30000, "segment_max_active_ms": 15000, "retry": map[string]any{"codes": []string{"probe_start_eagain", "probe_start_eintr"}, "backoff_ms": []int64{1}}}, "adapter": map[string]any{"provider": "grok-build", "provider_lock": grok, "profile": p, "directory": filepath.Join(root, name), "prompt": "Review assigned immutable chunks", "candidate_action": action("review", "validate", i)}})
	}
	integrate := action("integrate", "review-1", 1)
	integrate["dependency_input_version"] = 1
	tasks = append(tasks, map[string]any{"id": "integrate", "dependencies": []string{"review-0", "review-1", "review-2"}, "max_attempts": 1, "adapter": map[string]any{"kind": "candidate", "directory": repo, "candidate_action": integrate}})
	request := filepath.Join(root, "request.json")
	write := func(v any) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(request, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	birth, err := process.Birth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	write(map[string]any{"controller_thread": "indexed-owner", "origin_pid": os.Getpid(), "origin_birth": birth})
	var owner map[string]any
	if err = json.Unmarshal(runBinary(t, ctx, bin, "owner-bind", "--state-dir", state, "--request", request), &owner); err != nil {
		t.Fatal(err)
	}
	delete(owner, "version")
	owner["run_id"], owner["plan_revision"], owner["delivery_mode"], owner["tasks"] = "indexed-development", 7, "collect", tasks
	write(owner)
	var submitted struct {
		ControlFile string `json:"control_file"`
	}
	if err = json.Unmarshal(runBinary(t, ctx, bin, "submit", "--state-dir", state, "--request", request), &submitted); err != nil {
		t.Fatal(err)
	}
	var cap controlCapability
	if err = readPrivateJSON(submitted.ControlFile, &cap); err != nil {
		t.Fatal(err)
	}
	var bootstrap hostBootstrap
	if err = readPrivateJSON(cap.BootstrapPath, &bootstrap); err != nil {
		t.Fatal(err)
	}
	var manifest, candidate string
	covered := map[int]bool{}
	renameConsumed := map[string]string{}
	chunkCount := 0
	var accepted []contract.Event
	for _, task := range []string{"implement", "validate", "review-0", "review-1", "review-2", "integrate"} {
		for {
			s := taskStatus(t, ctx, bin, state, task, submitted.ControlFile)
			if s.Status == "result_ready" {
				break
			}
			if s.Status == "failed" || s.Status == "unknown" || s.Status == "blocked_dependency" || ctx.Err() != nil {
				t.Fatalf("%s stopped: %+v", task, s)
			}
			time.Sleep(30 * time.Millisecond)
		}
		var collected struct {
			DeliveryID string           `json:"delivery_id"`
			Proof      string           `json:"collection_proof_sha256"`
			Events     []contract.Event `json:"events"`
		}
		if err = json.Unmarshal(runBinary(t, ctx, bin, "collect", "--state-dir", state, "--task-id", task, "--control-file", submitted.ControlFile), &collected); err != nil || len(collected.Events) != 1 || collected.Events[0].Artifact == nil {
			t.Fatalf("collection %s: %v", task, err)
		}
		e := collected.Events[0]
		accepted = append(accepted, e)
		raw, err := os.ReadFile(e.Artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		var d host.CandidateDelivery
		if err = json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if task == "implement" {
			candidate = d.Candidate.CandidateOID
		}
		if candidate == "" || d.Candidate.CandidateOID != candidate {
			t.Fatal("candidate changed across CLI stages")
		}
		if task == "validate" {
			if d.ReviewIndex == nil || len(d.ReviewIndex.Chunks) < 136 {
				t.Fatal("large context missing")
			}
			manifest = contract.IndexedReviewJSONSHA256(d.ReviewIndex)
			chunkCount = len(d.ReviewIndex.Chunks)
			renameSides := map[string]string{}
			for _, chunk := range d.ReviewIndex.Chunks {
				if chunk.Path == "rename-before.txt" || chunk.Path == "rename-after.txt" {
					if _, duplicate := renameSides[chunk.Path]; duplicate || chunk.BlobOID != renameBlob || chunk.Mode != "100644" || chunk.Offset != 0 || chunk.Length != len(renameContent) {
						t.Fatal("rename metadata changed or duplicated")
					}
					renameSides[chunk.Path] = chunk.Side
				}
			}
			if len(renameSides) != 2 || renameSides["rename-before.txt"] != "base" || renameSides["rename-after.txt"] != "candidate" {
				t.Fatal("index omitted a logical side of the rename")
			}
		}
		if task == "validate" {
			// Crash only the idle source-host after its original validation
			// worker exited. Recovery must consume the same accepted artifact,
			// not dispatch validation again or manufacture another manifest.
			before := recoveryTaskStatus(t, ctx, bin, state, task, submitted.ControlFile)
			if live, err := process.Birth(before.HostPID); err != nil || live != before.HostBirth {
				t.Fatal("source-host identity drift")
			}
			if err := syscall.Kill(before.HostPID, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			for {
				if observed := recoveryTaskStatus(t, ctx, bin, state, task, submitted.ControlFile); observed.HostStatus == "offline" {
					break
				}
				if ctx.Err() != nil {
					t.Fatal("source-host did not become offline")
				}
				time.Sleep(20 * time.Millisecond)
			}
			write(map[string]any{"version": 1, "task_id": task, "work_revision": e.WorkRevision, "control_file": submitted.ControlFile, "owner_capability": owner["owner_capability"], "host_sha256": testFileSHA(t, bin)})
			for i := 0; i < 2; i++ {
				runBinary(t, ctx, bin, "recover-host", "--state-dir", state, "--request", request)
			}
			after := recoveryTaskStatus(t, ctx, bin, state, task, submitted.ControlFile)
			if after.HostStatus != "ready" || after.HostPID == before.HostPID || after.Status != before.Status || after.AttemptID != before.AttemptID || after.SegmentID != before.SegmentID || after.WorkRevision != before.WorkRevision {
				t.Fatalf("recovery changed validation: before=%+v after=%+v", before, after)
			}
			if live, err := process.Birth(after.HostPID); err != nil || live != after.HostBirth {
				t.Fatal("recovered source-host birth changed", err)
			}
			if executable, err := process.ExecutablePath(after.HostPID); err != nil || executable != bin {
				t.Fatal("recovery changed pinned source-host executable", err)
			}
			again, err := os.ReadFile(e.Artifact.Path)
			if err != nil || string(again) != string(raw) {
				t.Fatal("source-host recovery resampled validation manifest", err)
			}
			t.Log("idle source-host crash recovered once; validation identity and artifact unchanged")
		}
		if strings.HasPrefix(task, "review-") {
			if d.ReviewIndex == nil || d.ReviewCoverage == nil || contract.ValidateIndexedReviewCoverage(*d.ReviewIndex, *d.ReviewCoverage) != nil || contract.IndexedReviewJSONSHA256(d.ReviewIndex) != manifest || d.Review.Summary != "consumed-prompt-sha256:"+d.ReviewInputSHA256 {
				t.Fatal("CLI shard lost immutable consumption proof")
			}
			prompt, err := os.ReadFile(filepath.Join(bootstrap.SpoolRoot, e.AttemptID, e.SegmentID, "scratch", "review-input.txt"))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(prompt)
			if len(prompt) > 384*1024 || hex.EncodeToString(digest[:]) != d.ReviewInputSHA256 {
				t.Fatal("actual prompt mismatch")
			}
			_, body, ok := strings.Cut(string(prompt), "Host snapshot JSON:\n")
			var supplied struct {
				Chunks []struct {
					Ordinal, Offset, Length      int
					Path, Side, Mode, Content    string
					BlobOID, WholeSHA256, SHA256 string
				}
			}
			if !ok || json.Unmarshal([]byte(body), &supplied) != nil {
				t.Fatal("actual chunks absent")
			}
			for _, chunk := range supplied.Chunks {
				if covered[chunk.Ordinal] || chunk.Ordinal%3 != d.ReviewCoverage.ReviewOrdinal {
					t.Fatal("duplicate or wrong actual assignment")
				}
				covered[chunk.Ordinal] = true
				if chunk.Path == "rename-before.txt" || chunk.Path == "rename-after.txt" {
					if chunk.Mode != "100644" || chunk.Offset != 0 || chunk.Length != len(renameContent) || chunk.Content != renameContent {
						t.Fatal("actual prompt lost renamed bytes or metadata")
					}
					renameConsumed[chunk.Path] = chunk.Side
				}
			}
		}
		if task != "integrate" && git("rev-parse", "HEAD") != base {
			t.Fatal("premature target mutation")
		}
		if task == "integrate" && (len(covered) != chunkCount || d.DependencyInputsSHA256 == "" || d.Integration == nil || string(d.Integration.State) != "integrated") {
			t.Fatal("incomplete CLI integration")
		}
		write(map[string]any{"version": 1, "task_id": task, "control_file": submitted.ControlFile, "delivery_id": collected.DeliveryID, "collection_proof_sha256": collected.Proof, "decisions": []map[string]any{{"event_id": e.EventID, "event_revision": e.EventRevision, "event_hash": e.PayloadHash, "action_slot": e.ActionSlot, "decision": "handled", "command_id": "ack-" + task}}})
		for i := 0; i < 2; i++ {
			runBinary(t, ctx, bin, "ack", "--state-dir", state, "--request", request)
			acceptIndexedCLIEvent(t, ctx, bin, state, submitted.ControlFile, e)
		}
	}
	if git("rev-parse", "HEAD") != candidate || git("status", "--porcelain") != "" {
		t.Fatal("final target differs from accepted candidate")
	}
	if renameConsumed["rename-before.txt"] != "base" || renameConsumed["rename-after.txt"] != "candidate" {
		t.Fatal("review shards did not consume both rename sides")
	}
	if renamed, err := os.ReadFile(filepath.Join(repo, "rename-after.txt")); err != nil || string(renamed) != renameContent {
		t.Fatal("integration lost renamed content", err)
	}
	if _, err := os.Lstat(filepath.Join(repo, "rename-before.txt")); !os.IsNotExist(err) {
		t.Fatal("integration retained old rename path", err)
	}
	before := git("reflog", "show", "--format=%H", "refs/heads/main")
	identity := coreCLIReadHostIdentity(t, state, cap.HostLaunchID)
	stop()
	for deadline := time.Now().Add(3 * time.Second); ; {
		if errors.Is(syscall.Kill(identity.PID, 0), syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("original source-host %d still alive after shutdown", identity.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// No receipt or Git source is needed for an already committed decision.
	for _, e := range accepted {
		if err = os.Remove(e.Artifact.Path); err != nil {
			t.Fatal(err)
		}
	}
	stopAgain, newEpoch := startIndexedCLICoordinator(t, ctx, root, bin, state, 2)
	if newEpoch <= epoch {
		t.Fatal("coordinator did not reopen a new epoch")
	}
	for _, e := range accepted {
		acceptIndexedCLIEvent(t, ctx, bin, state, submitted.ControlFile, e)
		if s := taskStatus(t, ctx, bin, state, e.TaskID, submitted.ControlFile); s.Status != "completed" {
			t.Fatalf("reopened %s=%s", e.TaskID, s.Status)
		}
	}
	if git("reflog", "show", "--format=%H", "refs/heads/main") != before {
		t.Fatal("replayed acceptance mutated Git")
	}
	stopAgain()
	clean = true
}

func acceptIndexedCLIEvent(t *testing.T, ctx context.Context, bin, state, control string, e contract.Event) {
	t.Helper()
	runBinary(t, ctx, bin, "accept", "--state-dir", state, "--task-id", e.TaskID, "--control-file", control, "--work-revision", strconv.Itoa(e.WorkRevision), "--event-id", e.EventID, "--event-revision", strconv.FormatInt(e.EventRevision, 10), "--event-hash", e.PayloadHash, "--action-slot", e.ActionSlot, "--decision", "accept", "--command-id", "accept-"+e.TaskID)
}

func startIndexedCLICoordinator(t *testing.T, ctx context.Context, root, bin, state string, n int) (func(), uint64) {
	t.Helper()
	log, err := os.OpenFile(filepath.Join(root, fmt.Sprintf("serve-%d.log", n)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "serve", "--state-dir", state)
	cmd.Env = append(os.Environ(), "ORCHESTRATOR_ENABLE_TEST_FAKE=1")
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-wait:
			if err != nil {
				t.Errorf("serve %d original Wait: %v", n, err)
			} else {
				t.Logf("serve %d original Wait=0", n)
			}
		case <-time.After(12 * time.Second):
			_ = cmd.Process.Kill()
			err := <-wait
			t.Errorf("serve %d forced cleanup, Wait=%v", n, err)
		}
		_ = log.Close()
	}
	t.Cleanup(stop)
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		reply, err := ipc.Call(probeCtx, filepath.Join(state, "coordinator.sock"), ipc.Envelope{Version: ipc.Version, Kind: ipc.KindReady, RequestID: "indexed-ready", Payload: json.RawMessage(`{}`)})
		cancel()
		if err == nil && reply.Kind == ipc.KindResponse && reply.Epoch > 0 {
			return stop, reply.Epoch
		}
		if ctx.Err() != nil {
			t.Fatal("serve readiness", ctx.Err())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
