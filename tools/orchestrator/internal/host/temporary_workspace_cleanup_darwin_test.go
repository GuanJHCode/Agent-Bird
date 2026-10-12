package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestTemporaryWorkspaceCleanupActualStoreSourceAcceptedValidate(t *testing.T) {
	root, guard := validationNativeRoot(t)
	tool, pin := validationNativeActor(t, root, guard)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	repo := filepath.Join(root, "repo")
	validationNativeGit(t, "init", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	validationNativeGit(t, "-C", repo, "add", "file")
	validationNativeGit(t, "-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	base := validationNativeGit(t, "-C", repo, "rev-parse", "HEAD")
	source := adapter.InvocationPayload{Provider: string(adapter.ProviderClaude), Directory: filepath.Join(root, "edit"), ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: tool, SHA256: pin, Version: "controlled-native-fixture"}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 10000}, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: base, Paths: []string{"file"}, AutoDirectory: true}}
	validate := adapter.InvocationPayload{Kind: "candidate", Directory: filepath.Join(root, "validate"), CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", SourceTask: "edit", DependencyInputVersion: 1, AutoDirectory: true, Command: []string{tool, "validate"}, CommandSHA256: pin, Target: adapter.CandidateTarget{Worktree: repo, Ref: "refs/heads/main", BaseOID: base}}}
	sb, _ := json.Marshal(source)
	vb, _ := json.Marshal(validate)
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	plan, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "temporary-cleanup-native", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "test-owner"}, Host: store.HostLaunchSpec{OriginContextID: "test", HostGeneration: "generation", Executable: "/private/controlled-host"}, Tasks: []store.TaskSpec{{ID: "edit", RunID: "temporary-cleanup-native", MaxAttempts: 1, MaxActiveMS: 10000, AdapterPayload: sb}, {ID: "validate", RunID: "temporary-cleanup-native", MaxAttempts: 1, MaxActiveMS: 10000, Dependencies: []string{"edit"}, AdapterPayload: vb}}})
	if err != nil {
		t.Fatal(err)
	}
	hid, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "test-owner", HostGeneration: "generation", PID: os.Getpid(), Birth: "original-physical-host", Executable: "/private/controlled-host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(db, filepath.Join(root, "host-spool", hid))
	if err != nil {
		t.Fatal(err)
	}
	h.producerID = hid
	edit, err := db.ClaimReady(ctx, hid, 1, 2)
	if err != nil || edit.TaskID != "edit" {
		t.Fatal(edit, err)
	}
	guard.pending++
	sourceResult, err := h.ExecuteLaunch(ctx, edit, validationNativeInvocation{launchInvocation: launchInvocation{args: []string{tool, "produce"}, dir: source.Directory}, payload: source})
	if err != nil || sourceResult.Status != "result_ready" {
		t.Fatal(sourceResult, err)
	}
	guard.pending--
	validationCommitEvents(t, ctx, db, h, edit)
	var produced CandidateDelivery
	body, err := os.ReadFile(sourceResult.ArtifactPath)
	if err != nil || json.Unmarshal(body, &produced) != nil {
		t.Fatal(err)
	}
	validationAccept(t, ctx, db, edit)
	adminRoot := filepath.Join(repo, ".git", "worktrees")
	adminsBefore, err := os.ReadDir(adminRoot)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := db.ClaimReady(ctx, hid, 1, 2)
	if err != nil || grant.TaskID != "validate" {
		t.Fatal(grant, err)
	}
	guard.pending++
	result, err := h.ExecuteLaunch(ctx, grant, validationNativeInvocation{launchInvocation: launchInvocation{args: validate.CandidateAction.Command, dir: validate.Directory}, payload: validate})
	if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
		t.Fatal(result, err)
	}
	guard.pending--
	artifact, err := os.ReadFile(result.ArtifactPath)
	if err != nil || hashBytes(artifact) != result.OutputHash {
		t.Fatalf("business artifact hash changed: %v", err)
	}
	events, err := h.Collect(ctx, store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if _, err = db.CommitHostEvent(ctx, event); err != nil {
			if event.TemporaryWorkspaceCleanup != nil {
				t.Fatalf("intake %s cleanup=%+v event=%+v: %v", event.Kind, *event.TemporaryWorkspaceCleanup, event, err)
			}
			t.Fatalf("intake %s event=%+v: %v", event.Kind, event, err)
		}
	}
	var pending, removed *contract.TemporaryWorkspaceCleanup
	for i := range events {
		if c := events[i].TemporaryWorkspaceCleanup; c != nil {
			if c.State == "pending" && c.Code == "" {
				pending = c
			} else if c.State == "removed" {
				removed = c
			}
		}
	}
	if pending == nil || removed == nil || pending.Worktree != removed.Worktree || pending.ReceiptSHA256 != removed.ReceiptSHA256 {
		t.Fatalf("missing cleanup lifecycle: %+v", events)
	}
	if _, err = os.Lstat(removed.Worktree); !os.IsNotExist(err) {
		t.Fatalf("validate workspace retained: %v", err)
	}
	adminsAfter, err := os.ReadDir(adminRoot)
	if err != nil || len(adminsAfter) != len(adminsBefore) {
		t.Fatalf("validation worktree admin retained: before=%d after=%d err=%v", len(adminsBefore), len(adminsAfter), err)
	}
	if _, err = os.Lstat(produced.Candidate.Worktree); err != nil {
		t.Fatalf("producer workspace lost: %v", err)
	}
	if validationNativeGit(t, "-C", repo, "show-ref", "--verify", produced.Candidate.PrivateRef) == "" {
		t.Fatal("producer private ref lost")
	}
	snapshot, err := db.TaskSnapshot(ctx, grant.TaskID)
	if err != nil || snapshot.TemporaryWorkspaceCleanup == nil || !reflect.DeepEqual(*snapshot.TemporaryWorkspaceCleanup, *removed) {
		t.Fatalf("cleanup projection or business hash changed: %+v %v", snapshot, err)
	}
	summary, err := db.SummarizeRun(ctx, grant.TaskID, "owner", plan.ControlToken)
	var summarized *contract.TemporaryWorkspaceCleanup
	for i := range summary.Tasks {
		if summary.Tasks[i].TaskID == grant.TaskID {
			summarized = summary.Tasks[i].TemporaryWorkspaceCleanup
		}
	}
	if err != nil || summarized == nil || !reflect.DeepEqual(*summarized, *removed) {
		t.Fatalf("cleanup summary missing: %+v %v", summary, err)
	}
}

func TestTemporaryWorkspaceCleanupRemovesPublishedOrdinaryMaterialization(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	validationNativeGit(t, "init", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	validationNativeGit(t, "-C", repo, "add", "file")
	validationNativeGit(t, "-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "base")
	base := validationNativeGit(t, "-C", repo, "rev-parse", "HEAD")
	h, err := NewIPC(filepath.Join(root, "spool"), "host")
	if err != nil {
		t.Fatal(err)
	}
	journal, err := gitopsworker.OpenJournal(filepath.Join(root, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	worktree := filepath.Join(root, "validate.attempt-attempt-segment")
	receipt, err := gitops.ManagedMaterialize(ctx, gitops.MaterializeRequest{RepoRoot: repo, Worktree: worktree, AttemptID: "attempt", BaseOID: base, CandidateOID: base, OrderedInputOIDs: []string{base}, PlanRevision: 1}, journal)
	if err != nil {
		t.Fatal(err)
	}
	a := store.Attempt{ID: "attempt", TaskID: "validate", SegmentID: "segment"}
	spool, err := h.spool(a)
	if err != nil {
		t.Fatal(err)
	}
	meta := launchMetadata{commandID: "command", workRevision: 1, temporaryWorkspace: &temporaryWorkspaceCleanup{operation: "validate", receipt: receipt, journal: journal}}
	body := []byte(`{"version":1,"stage":"validate"}`)
	result, err := h.appendStructuredEvent(spool, "run", "validate", a, meta, contract.EventResult, hashBytes(body), nil, nil, nil, structuredEventFields{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.appendStructuredEvent(spool, "run", "validate", a, meta, contract.EventExited, hashText("exited:segment"), nil, nil, nil, structuredEventFields{}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	h.cleanupPublishedTemporaryWorkspace(cancelled, spool, "run", "validate", a, meta)
	records, err := spool.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[0].PayloadHash != result.PayloadHash || records[0].Kind != contract.EventResult || records[2].TemporaryWorkspaceCleanup == nil || records[2].TemporaryWorkspaceCleanup.State != "pending" || records[2].TemporaryWorkspaceCleanup.Code != "" || records[3].TemporaryWorkspaceCleanup == nil || records[3].TemporaryWorkspaceCleanup.State != "removed" {
		t.Fatalf("unexpected cleanup lifecycle: %+v", records)
	}
	if _, err = os.Lstat(worktree); !os.IsNotExist(err) {
		t.Fatalf("materialized worktree retained: %v", err)
	}
	if validationNativeGit(t, "-C", repo, "rev-parse", "HEAD") != base {
		t.Fatal("producer repository changed")
	}
}
