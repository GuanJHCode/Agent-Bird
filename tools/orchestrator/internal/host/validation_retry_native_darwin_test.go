package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type validationNativeInvocation struct {
	launchInvocation
	payload adapter.InvocationPayload
}

func (i validationNativeInvocation) CandidateAction() *adapter.CandidateAction {
	return i.payload.CandidateAction
}
func (i validationNativeInvocation) CandidateWorkspace() *adapter.CandidateWorkspace {
	return i.payload.CandidateWorkspace
}
func (i validationNativeInvocation) ExecutionProfile() *adapter.ExecutionProfile {
	return i.payload.Profile
}
func (i validationNativeInvocation) ExecutablePin() (string, string) {
	if i.payload.ProviderLock != nil {
		return i.payload.ProviderLock.Binary.Path, i.payload.ProviderLock.Binary.SHA256
	}
	if i.payload.CandidateAction != nil {
		return i.payload.CandidateAction.Command[0], i.payload.CandidateAction.CommandSHA256
	}
	return "", ""
}
func validationNativeActor(t *testing.T, root string, guard *validationNativeCleanup) (string, string) {
	t.Helper()
	bin := filepath.Join(root, "native-validation-actor")
	cwd, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	guard.pending++
	original, e := process.Start(ctx, process.Command{Path: "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go", Args: []string{"build", "-o", bin, "./testdata/validation-tool-actor"}, Dir: cwd, Env: []string{"TMPDIR=/private/tmp"}})
	if original == nil || e != nil {
		if original != nil {
			validationNativeStop(original, guard)
		}
		t.Fatalf("actor build original Start: %v", e)
	}
	waitErr := original.Wait(ctx)
	treeErr := original.ConfirmTreeExited()
	if waitErr != nil || treeErr != nil {
		validationNativeStop(original, guard)
		t.Fatalf("actor build original Wait/tree: %v %v", waitErr, treeErr)
	}
	guard.pending--
	if original.ExitCode() != 0 {
		t.Fatalf("actor build exit: %s", original.Output())
	}
	if e := os.Chmod(bin, 0500); e != nil {
		t.Fatal(e)
	}
	pin, e := process.ExecutableDigest(context.Background(), bin)
	if e != nil {
		t.Fatal(e)
	}
	return bin, pin
}

// Test-only cleanup authority. Every actual Start/Host call is held until its
// original Wait/tree or live never-started proof is observed. Failures retain
// the owned root even when Stop succeeds; no t.TempDir teardown races an actor.
type validationNativeCleanup struct {
	root     string
	identity os.FileInfo
	pending  int
}

func (g *validationNativeCleanup) cleanup(failed bool) (bool, error) {
	if failed || g.pending != 0 {
		return false, nil
	}
	info, err := os.Lstat(g.root)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil || !os.SameFile(g.identity, info) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("native cleanup root identity changed")
	}
	err = filepath.WalkDir(g.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			st, e := os.Lstat(path)
			if e != nil {
				return e
			}
			owner, ok := st.Sys().(*syscall.Stat_t)
			if !ok || owner.Uid != uint32(os.Geteuid()) {
				return errors.New("foreign cleanup directory")
			}
			if st.Mode().Perm()&0700 != 0700 {
				return os.Chmod(path, 0700)
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	err = os.RemoveAll(g.root)
	return err == nil, err
}
func validationNativeRoot(t *testing.T) (string, *validationNativeCleanup) {
	t.Helper()
	root, e := os.MkdirTemp("/private/tmp", "agent-bird-validation-retry-native-")
	if e != nil {
		t.Fatal(e)
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		t.Fatal(e)
	}
	info, e := os.Lstat(root)
	if e != nil {
		t.Fatal(e)
	}
	guard := &validationNativeCleanup{root: root, identity: info}
	t.Cleanup(func() {
		removed, e := guard.cleanup(t.Failed())
		if !removed || e != nil {
			t.Logf("RETAINED native fixture root=%s pending=%d failed=%v cleanup_error=%v", root, guard.pending, t.Failed(), e)
		}
	})
	return root, guard
}
func validationNativeStop(h *process.Handle, guard *validationNativeCleanup) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopErr := h.Stop(ctx)
	waitErr := h.Wait(ctx)
	treeErr := h.ConfirmTreeExited()
	if stopErr == nil && waitErr == nil && treeErr == nil {
		guard.pending--
		return true
	}
	return false
}
func validationNativeGit(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s %v", args, out, e)
	}
	return strings.TrimSpace(string(out))
}
func validationCommitEvents(t *testing.T, ctx context.Context, db *store.DB, h *Host, g contract.LaunchCommand) []contract.Event {
	t.Helper()
	evs, e := h.Collect(ctx, store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID})
	if e != nil {
		t.Fatal(e)
	}
	for _, ev := range evs {
		if _, e = db.CommitHostEvent(ctx, ev); e != nil {
			t.Fatalf("intake %s: %v", ev.Kind, e)
		}
	}
	return evs
}
func validationAccept(t *testing.T, ctx context.Context, db *store.DB, g contract.LaunchCommand) {
	t.Helper()
	page, e := db.CollectPending(ctx, g.TaskID, "", 0, false)
	if e != nil {
		t.Fatal(e)
	}
	var matches []contract.Event
	for _, ev := range page.Events {
		if ev.Kind == contract.EventResult && ev.AttemptID == g.AttemptID && ev.SegmentID == g.SegmentID {
			matches = append(matches, ev)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected one exact new attempt Result among retained audit events: %+v", page)
	}
	ev := matches[0]
	if _, e = db.ReviewResult(ctx, store.ReviewSpec{TaskID: g.TaskID, WorkRevision: g.WorkRevision, EventID: ev.EventID, EventRevision: ev.EventRevision, EventHash: ev.PayloadHash, ActionSlot: ev.ActionSlot, Decision: "accept", CommandID: "accept-" + g.TaskID}); e != nil {
		t.Fatal(e)
	}
}

func TestValidationRetryActualHostGitQueueAndNextAttempt(t *testing.T) {
	root, guard := validationNativeRoot(t)
	tool, pin := validationNativeActor(t, root, guard)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	repo := filepath.Join(root, "repo")
	validationNativeGit(t, "init", "-b", "main", repo)
	if e := os.WriteFile(filepath.Join(repo, "file"), []byte("base\n"), 0600); e != nil {
		t.Fatal(e)
	}
	validationNativeGit(t, "-C", repo, "add", "file")
	validationNativeGit(t, "-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	base := validationNativeGit(t, "-C", repo, "rev-parse", "HEAD")
	source := adapter.InvocationPayload{Provider: string(adapter.ProviderClaude), Directory: filepath.Join(root, "edit"), ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: tool, SHA256: pin, Version: "controlled-native-fixture"}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 10000}, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: base, Paths: []string{"file"}, AutoDirectory: true}}
	validate := adapter.InvocationPayload{Kind: "candidate", Directory: filepath.Join(root, "validate"), CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", SourceTask: "edit", DependencyInputVersion: 1, AutoDirectory: true, Command: []string{tool, "validate"}, CommandSHA256: pin, Target: adapter.CandidateTarget{Worktree: repo, Ref: "refs/heads/main", BaseOID: base}}}
	sb, _ := json.Marshal(source)
	vb, _ := json.Marshal(validate)
	db, e := store.Open(filepath.Join(root, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	receipt, e := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "native-validation-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "test-owner"}, Host: store.HostLaunchSpec{OriginContextID: "test", HostGeneration: "generation", Executable: "/private/controlled-host"}, Tasks: []store.TaskSpec{{ID: "edit", RunID: "native-validation-run", MaxAttempts: 1, MaxActiveMS: 10000, AdapterPayload: sb}, {ID: "validate", RunID: "native-validation-run", MaxAttempts: 3, MaxActiveMS: 10000, Dependencies: []string{"edit"}, AdapterPayload: vb, ExecutionPolicy: &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 3, GroupMaxActiveMS: 30000, TaskMaxActiveMS: 20000, SegmentMaxActiveMS: 10000, Retry: &contract.RetryPolicy{Codes: []string{"validation_start_eagain", "validation_start_eintr"}, BackoffMS: []int64{1, 2}}}}}})
	if e != nil {
		t.Fatal(e)
	}
	hid, e := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "test-owner", HostGeneration: "generation", PID: os.Getpid(), Birth: "original-physical-host", Executable: "/private/controlled-host"}, 1)
	if e != nil {
		t.Fatal(e)
	}
	h, e := New(db, filepath.Join(root, "host-spool", hid))
	if e != nil {
		t.Fatal(e)
	}
	h.producerID = hid
	edit, e := db.ClaimReady(ctx, hid, 1, 2)
	if e != nil || edit.TaskID != "edit" {
		t.Fatal(edit, e)
	}
	guard.pending++
	r, e := h.ExecuteLaunch(ctx, edit, validationNativeInvocation{launchInvocation: launchInvocation{args: []string{tool, "produce"}, dir: source.Directory}, payload: source})
	if e != nil || r.Status != "result_ready" {
		t.Fatalf("source: %+v %v", r, e)
	}
	sourceEvents := validationCommitEvents(t, ctx, db, h, edit)
	sourceWaited := false
	for _, ev := range sourceEvents {
		if ev.Kind == contract.EventExited && ev.Process != nil && ev.ExitCode != nil && *ev.ExitCode == 0 {
			sourceWaited = true
		}
	}
	if !sourceWaited {
		t.Fatal("source original Host Wait/tree not observed")
	}
	guard.pending--
	validationAccept(t, ctx, db, edit)
	first, e := db.ClaimReady(ctx, hid, 1, 2)
	if e != nil || first.TaskID != "validate" || first.ValidationTool == nil {
		t.Fatal(first, e)
	}
	inv := validationNativeInvocation{launchInvocation: launchInvocation{args: validate.CandidateAction.Command, dir: validate.Directory}, payload: validate}
	called := false
	guard.pending++
	r, e = h.executeLaunch(ctx, first, inv, codexExecuteControl{validationStarter: func(cmd *exec.Cmd) error {
		called = true
		if cmd.Path != "/usr/bin/sandbox-exec" || len(cmd.Args) < 4 || cmd.Args[3] == tool || !strings.HasPrefix(cmd.Args[3], filepath.Join(h.spoolRoot, ".validation-tools")) {
			t.Fatalf("fault did not bind entire sealed sandbox wrapper: %v", cmd.Args)
		}
		return syscall.EAGAIN
	}})
	if !called || e == nil || r.Status != "failed" {
		t.Fatalf("injected original Start: called=%v %+v %v", called, r, e)
	}
	evs := validationCommitEvents(t, ctx, db, h, first)
	var rejected *contract.Event
	for i := range evs {
		ev := &evs[i]
		if ev.Kind == contract.EventSpawned || ev.Kind == contract.EventExited {
			t.Fatal("never-started actor forged process lifecycle", ev.Kind)
		}
		if ev.Kind == contract.EventLaunchRejected {
			rejected = ev
		}
	}
	if rejected == nil || rejected.Failure == nil || !rejected.Failure.ValidValidationStart() || rejected.ValidationToolCleanup == nil || rejected.ValidationToolCleanup.RemovedBytes <= 0 {
		t.Fatal("no authenticated live proof+copy cleanup", rejected)
	}
	guard.pending-- // original typed never-started + removed copy observed
	if _, e = db.CommitHostEvent(ctx, *rejected); e != nil {
		t.Fatal("exact terminal ACK replay", e)
	}
	page, e := db.CollectPending(ctx, first.TaskID, "", 0, false)
	if e != nil || len(page.Events) != 1 || page.Events[0].EventID != rejected.EventID || page.Events[0].Kind != contract.EventLaunchRejected {
		t.Fatal("original rejection delivery lost", page, e)
	}
	old := page.Events[0]
	if e = db.AckDecision(ctx, first.TaskID, store.AckDecision{EventID: old.EventID, EventRevision: old.EventRevision, EventHash: old.PayloadHash, ActionSlot: old.ActionSlot, Decision: "handled", CommandID: "ack-original-rejection"}); e != nil {
		t.Fatal("public owner rejection ACK", e)
	}
	beforeReplay, e := h.Collect(ctx, store.Attempt{ID: first.AttemptID, TaskID: first.TaskID, SegmentID: first.SegmentID})
	if e != nil {
		t.Fatal(e)
	}
	if e = h.recordLaunchFailed(store.Attempt{ID: first.AttemptID, TaskID: first.TaskID, SegmentID: first.SegmentID}, first.RunID, first.TaskID, metadataForLaunch(first), errors.New("outer source runner repeat")); e != nil {
		t.Fatal("exact Host terminal replay", e)
	}
	afterReplay, e := h.Collect(ctx, store.Attempt{ID: first.AttemptID, TaskID: first.TaskID, SegmentID: first.SegmentID})
	if e != nil || !reflect.DeepEqual(beforeReplay, afterReplay) {
		t.Fatal("duplicate terminal repeated cleanup/publication", e)
	}
	next, e := db.ClaimReady(ctx, hid, 1, 2)
	for errors.Is(e, store.ErrNoReady) && ctx.Err() == nil {
		time.Sleep(2 * time.Millisecond)
		next, e = db.ClaimReady(ctx, hid, 1, 2)
	}
	if e != nil || next.AttemptID == first.AttemptID || next.SegmentID == first.SegmentID || next.ValidationTool == nil || next.ValidationTool.ReservationID == first.ValidationTool.ReservationID || !reflect.DeepEqual(next.Input, first.Input) || !reflect.DeepEqual(next.DependencyInputs, first.DependencyInputs) || string(next.AdapterPayload) != string(first.AdapterPayload) {
		t.Fatal("frozen source/new grant drift", next, e)
	}
	guard.pending++
	r, e = h.ExecuteLaunch(ctx, next, inv)
	if e != nil || r.Status != "result_ready" || r.ExitCode != 0 {
		t.Fatalf("next actual actor: %+v %v", r, e)
	}
	body, e := os.ReadFile(r.ArtifactPath)
	if e != nil {
		t.Fatal(e)
	}
	var delivery CandidateDelivery
	if e = json.Unmarshal(body, &delivery); e != nil || delivery.Validation == nil || !delivery.Validation.Passed || delivery.Validation.ExecutableSHA256 != pin || !reflect.DeepEqual(delivery.Validation.Command, validate.CandidateAction.Command) {
		t.Fatal("validation evidence not bound to declared native pin", e)
	}
	evs = validationCommitEvents(t, ctx, db, h, next)
	waited := false
	for _, ev := range evs {
		if ev.Kind == contract.EventExited {
			waited = ev.Process != nil && ev.ValidationToolCleanup != nil && ev.ExitCode != nil && *ev.ExitCode == 0
		}
	}
	if !waited {
		t.Fatal("missing original Wait/tree+tool cleanup terminal")
	}
	guard.pending-- // original Host Wait/tree Exited observed
	validationAccept(t, ctx, db, next)
	q, e := sql.Open("sqlite", filepath.Join(root, "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	var held, decision int
	if e = q.QueryRow(`SELECT count(*) FROM validation_tool_reservations WHERE state='reserved'`).Scan(&held); e != nil || held != 0 {
		t.Fatal("raw escrow leaked", held, e)
	}
	if e = q.QueryRow(`SELECT count(*) FROM retry_decisions WHERE json_extract(revision_payload,'$.kind')='candidate_validation_retry'`).Scan(&decision); e != nil || decision != 1 {
		t.Fatal("retry count", decision, e)
	}
	var audit, metadata int
	if e = q.QueryRow(`SELECT count(*) FROM runtime_events WHERE event_id=? AND json_extract(body_json,'$.kind')='launch_rejected' AND delivery_status='acked'`, rejected.EventID).Scan(&audit); e != nil || audit != 1 {
		t.Fatal("original rejection audit removed", audit, e)
	}
	if e = q.QueryRow(`SELECT count(*) FROM validation_tool_reservations WHERE state='released' AND metadata_bytes=8192`).Scan(&metadata); e != nil || metadata != 2 {
		t.Fatal("retained journals lost metadata escrow", metadata, e)
	}
	dirs, _ := filepath.Glob(validate.Directory + ".attempt-*")
	if len(dirs) != 2 {
		t.Fatal("retry reused materialization", dirs)
	}
	if validationNativeGit(t, "-C", repo, "rev-parse", "HEAD") != base {
		t.Fatal("controller target changed")
	}
	t.Log("actual Host profile + managed Git producer/materialization + injected original wrapper EAGAIN + atomic Store retry + successful fresh native attempt; original Wait/tree confirmed; no provider/model")
}

func TestValidationSealedActualProfileHasPositiveControls(t *testing.T) {
	root, guard := validationNativeRoot(t)
	actor, pin := validationNativeActor(t, root, guard)
	for _, mode := range []string{"unsandboxed_control", "actual_host_profile"} {
		t.Run(mode, func(t *testing.T) {
			fixture := filepath.Join(root, mode)
			if e := os.Mkdir(fixture, 0700); e != nil {
				t.Fatal(e)
			}
			h, e := NewIPC(filepath.Join(fixture, "host-spool", "host"), "host")
			if e != nil {
				t.Fatal(e)
			}
			workspace := filepath.Join(fixture, "workspace")
			if e = os.Mkdir(workspace, 0700); e != nil {
				t.Fatal(e)
			}
			var self, peer string
			var g contract.LaunchCommand
			if mode == "actual_host_profile" {
				g = managedHostGrant(t, h, contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", CommandID: "command", PlanRevision: 1, WorkRevision: 1})
				g = validationToolGrantForTest(t, h, g, pin)
				self, e = h.sealValidationTool(context.Background(), g, actor, pin, workspace, filepath.Join(h.spoolRoot, "attempt", "segment", "scratch"))
				if e != nil {
					t.Fatal(e)
				}
				other := g
				other.AttemptID = "peer"
				other.SegmentID = "peer"
				other.CommandID = "peer"
				other = validationToolGrantForTest(t, h, other, pin)
				peer, e = h.sealValidationTool(context.Background(), other, actor, pin, workspace, filepath.Join(h.spoolRoot, "peer", "peer", "scratch"))
				if e != nil {
					t.Fatal(e)
				}
			} else {
				dir := filepath.Join(fixture, ".validation-tools", "private")
				if e = os.MkdirAll(dir, 0700); e != nil {
					t.Fatal(e)
				}
				self = filepath.Join(dir, "tool")
				body, e := os.ReadFile(actor)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(self, body, 0500); e != nil {
					t.Fatal(e)
				}
				peer = filepath.Join(dir, "peer")
				if e = os.WriteFile(peer, []byte("peer"), 0500); e != nil {
					t.Fatal(e)
				}
				if e = os.Chmod(dir, 0500); e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
			}
			cmd := process.Command{Path: self, Args: []string{"protect", peer}, Dir: workspace, PinnedPath: actor, PinnedSHA256: pin}
			if mode == "actual_host_profile" {
				cmd.SealedPath = self
				cmd.SealedSHA256 = pin
				cmd, e = h.sandboxProfileCommand(context.Background(), cmd, &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 10000}, filepath.Join(h.spoolRoot, "attempt", "segment", "scratch"), g)
				if e != nil {
					t.Fatal(e)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			guard.pending++
			original, stream, e := process.StartDuplex(ctx, cmd)
			if original == nil || e != nil {
				if original != nil {
					validationNativeStop(original, guard)
				}
				t.Fatal("original actor Start", e)
			}
			defer stream.Close()
			if _, e = stream.Write([]byte("x")); e != nil {
				validationNativeStop(original, guard)
				t.Fatal(e)
			}
			body, e := io.ReadAll(io.LimitReader(stream, 4096))
			waitErr := original.Wait(ctx)
			treeErr := original.ConfirmTreeExited()
			if e != nil || waitErr != nil || treeErr != nil || original.ExitCode() != 0 {
				t.Fatalf("original actor lifecycle %s %v %v %v", body, e, waitErr, treeErr)
			}
			guard.pending-- // original Wait and empty tree confirmed
			result := map[string]int{}
			if e = json.Unmarshal(body, &result); e != nil || len(result) != 7 {
				t.Fatal(string(body), e)
			}
			for op, errno := range result {
				if mode == "unsandboxed_control" && errno != 0 {
					t.Fatalf("positive control %s errno=%d; cannot attribute denial to sandbox", op, errno)
				}
				if mode == "actual_host_profile" && errno != int(syscall.EPERM) && errno != int(syscall.EACCES) {
					t.Fatalf("actual profile %s errno=%d", op, errno)
				}
			}
			if mode == "actual_host_profile" {
				for _, path := range []string{self, peer} {
					digest, e := process.ExecutableDigest(ctx, path)
					if e != nil || digest != pin {
						t.Fatal("protected copy changed", path, e)
					}
				}
				if _, e = h.removeValidationTool(g); e != nil {
					t.Fatal("original Wait cleanup", e)
				}
			}
			t.Logf("%s original Wait/tree proof and seven filesystem operation errno oracles: %s", mode, body)
		})
	}
}

func TestValidationNativeCleanupRetainsUnprovedOriginalHandle(t *testing.T) {
	root, guard := validationNativeRoot(t)
	actor, pin := validationNativeActor(t, root, guard)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	guard.pending++
	original, stream, err := process.StartDuplex(ctx, process.Command{Path: actor, Args: []string{"protect", filepath.Join(root, "peer")}, Dir: root, PinnedPath: actor, PinnedSHA256: pin})
	if original == nil || err != nil {
		if original != nil {
			validationNativeStop(original, guard)
		}
		t.Fatal(err)
	}
	defer stream.Close()
	removed, err := guard.cleanup(false)
	if removed || err != nil {
		validationNativeStop(original, guard)
		t.Fatal("unproved original actor root removed", removed, err)
	}
	if _, err = os.Stat(actor); err != nil {
		validationNativeStop(original, guard)
		t.Fatal("live actor copy lost", err)
	}
	t.Logf("RETAINED before original Wait/tree root=%s pending=%d", root, guard.pending)
	if !validationNativeStop(original, guard) {
		t.Fatal("original Stop/Wait/tree unproved; retain fixture")
	}
	removed, err = guard.cleanup(true)
	if removed || err != nil {
		t.Fatal("failed test cleanup removed forensic root", removed, err)
	}
	if _, err = os.Stat(actor); err != nil {
		t.Fatal("failed test forensic copy lost", err)
	}
	removed, err = guard.cleanup(false)
	if !removed || err != nil {
		t.Fatal("confirmed owned root not cleaned", removed, err)
	}
}
