//go:build darwin

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestFilterReviewNativeHostAcceptedValidator(t *testing.T) {
	root, guard := filterPipelineRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	filterActor := filterPipelineBuildActor(t, ctx, root, guard)
	validatorActor, validatorPin := validationNativeActor(t, root, guard)
	gitPath := filterPipelineGitPath(t, ctx, guard)
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	filterPipelineGit(t, ctx, guard, gitPath, repo, "init", "-q", "-b", "main")
	filterPipelineGit(t, ctx, guard, gitPath, repo, "config", "user.name", "fixture")
	filterPipelineGit(t, ctx, guard, gitPath, repo, "config", "user.email", "fixture@example.invalid")
	baseB := []byte("base-raw\n")
	if err := os.WriteFile(filepath.Join(repo, "file"), baseB, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filterPipelineManifest(t, ctx, guard, gitPath, filterActor)
	filterPipelineGit(t, ctx, guard, gitPath, repo, "config", "filter.sealed.clean", filterPipelineShell(filterActor, "xor"))
	filterPipelineGit(t, ctx, guard, gitPath, repo, "config", "filter.sealed.smudge", filterPipelineShell(filterActor, "xor"))
	filterPipelineGit(t, ctx, guard, gitPath, repo, "config", "filter.sealed.required", "false")
	attrs := []byte("file filter=sealed\n")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), attrs, 0600); err != nil {
		t.Fatal(err)
	}
	manifest.Attributes = []contract.GitFilterAttributePin{{Path: ".gitattributes", SHA256: filterPipelineSHA(attrs)}}
	manifest.ConfigSHA256 = gitops.FilterConfigurationSHA256(manifest)
	filterPipelineGitNoFilter(t, ctx, guard, gitPath, repo, "add", "file", ".gitattributes")
	filterPipelineGitNoFilter(t, ctx, guard, gitPath, repo, "commit", "-q", "-m", "base")
	base := filterPipelineGitNoFilter(t, ctx, guard, gitPath, repo, "rev-parse", "HEAD")

	sourcePayload := adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderClaude), Directory: filepath.Join(root, "provider"), ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: validatorActor, SHA256: validatorPin, Version: "controlled-native"}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 300000}, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: base, Paths: []string{"file"}, AutoDirectory: true}, GitFilter: &manifest}
	validatePayload := adapter.InvocationPayload{Kind: "candidate", Directory: filepath.Join(root, "validate"), CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", SourceTask: "edit", DependencyInputVersion: 1, AutoDirectory: true, Command: []string{validatorActor, "validate"}, CommandSHA256: validatorPin}, GitFilter: &manifest}
	sourceRaw, _ := json.Marshal(sourcePayload)
	validateRaw, _ := json.Marshal(validatePayload)
	reviewPayload := adapter.InvocationPayload{Directory: filepath.Join(root, "review"), Provider: string(adapter.ProviderClaude), ProviderLock: sourcePayload.ProviderLock, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 300000}, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "validate", DependencyInputVersion: 1, AutoDirectory: true}, GitFilter: &manifest}
	reviewRaw, _ := json.Marshal(reviewPayload)
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "filter-native-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "fixture"}, Host: store.HostLaunchSpec{OriginContextID: "test", HostGeneration: "fixture", Executable: "/private/controlled-host"}, Tasks: []store.TaskSpec{{ID: "edit", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 300000, AdapterPayload: sourceRaw}, {ID: "validate", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 300000, Dependencies: []string{"edit"}, AdapterPayload: validateRaw}, {ID: "review", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 300000, Dependencies: []string{"validate"}, AdapterPayload: reviewRaw}}})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "fixture", HostGeneration: "fixture", PID: os.Getpid(), Birth: "host", Executable: "/private/controlled-host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(db, filepath.Join(root, "host-spool", hostID))
	if err != nil {
		t.Fatal(err)
	}
	h.producerID = hostID
	sourceGrant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil || sourceGrant.TaskID != "edit" || sourceGrant.GitFilter == nil {
		t.Fatalf("source grant=%+v err=%v", sourceGrant, err)
	}
	guard.pending++
	result, err := h.ExecuteLaunch(ctx, sourceGrant, validationNativeInvocation{launchInvocation: launchInvocation{args: []string{validatorActor, "produce"}, dir: sourcePayload.Directory}, payload: sourcePayload})
	if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
		t.Fatalf("source result=%+v err=%v", result, err)
	}
	sourceBody, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var sourceDelivery CandidateDelivery
	if err = json.Unmarshal(sourceBody, &sourceDelivery); err != nil || sourceDelivery.Candidate.Filter == nil || sourceDelivery.Candidate.Binding == nil || sourceDelivery.Candidate.Binding.AttemptID != sourceGrant.AttemptID {
		t.Fatalf("source delivery=%#v err=%v", sourceDelivery, err)
	}
	desiredR := []byte("candidate\n")
	wantB := filterPipelineXOR(desiredR)
	manifestSHA, manifestErr := manifest.Digest()
	views := sourceDelivery.Candidate.Filter.Views
	if manifestErr != nil || sourceDelivery.Candidate.Filter.ManifestSHA256 != manifestSHA || len(views) != 1 || views[0].Path != "file" || views[0].CanonicalSHA256 != filterPipelineSHA(wantB) || views[0].RenderedSHA256 != filterPipelineSHA(desiredR) {
		t.Fatalf("source filter=%#v", sourceDelivery.Candidate.Filter)
	}
	if len(sourceDelivery.Candidate.Changes) != 1 || sourceDelivery.Candidate.Changes[0].Path != "file" || sourceDelivery.Candidate.Changes[0].Mode != "100644" {
		t.Fatalf("source changes=%#v", sourceDelivery.Candidate.Changes)
	}
	if got := filterPipelineGitBytes(t, ctx, guard, gitPath, repo, []string{"cat-file", "blob", sourceDelivery.Candidate.Changes[0].BlobOID}); !bytes.Equal(got, wantB) {
		t.Fatalf("source canonical=%q want=%q", got, wantB)
	}
	if got, readErr := os.ReadFile(sourceDelivery.Candidate.Worktree + "/file"); readErr != nil || !bytes.Equal(got, desiredR) {
		t.Fatalf("producer rendered=%q err=%v", got, readErr)
	}
	sourceEvents := validationCommitEvents(t, ctx, db, h, sourceGrant)
	if !filterPipelineExited(sourceEvents, sourceGrant) {
		t.Fatal("source original Host Exited proof missing")
	}
	guard.pending--
	validationAccept(t, ctx, db, sourceGrant)
	validatorGrant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil || validatorGrant.TaskID != "validate" || validatorGrant.GitFilter == nil || validatorGrant.ValidationTool == nil || validatorGrant.GitFilter.ReservationID == sourceGrant.GitFilter.ReservationID || validatorGrant.GitFilter.ScopeSHA256 == sourceGrant.GitFilter.ScopeSHA256 {
		t.Fatalf("validator grant=%+v err=%v", validatorGrant, err)
	}
	expectedValidatorBinding := gitops.CandidateBinding{
		RunID:        validatorGrant.RunID,
		TaskID:       validatorGrant.TaskID,
		AttemptID:    validatorGrant.AttemptID,
		SegmentID:    validatorGrant.SegmentID,
		WorkRevision: validatorGrant.WorkRevision,
		PlanRevision: validatorGrant.PlanRevision,
	}
	guard.pending++
	result, err = h.ExecuteLaunch(ctx, validatorGrant, validationNativeInvocation{launchInvocation: launchInvocation{args: validatePayload.CandidateAction.Command, dir: validatePayload.Directory}, payload: validatePayload})
	if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
		t.Fatalf("validator result=%+v err=%v", result, err)
	}
	body, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var delivery CandidateDelivery
	if err = json.Unmarshal(body, &delivery); err != nil || delivery.Stage != "validate" || !reflect.DeepEqual(delivery.Target, gitops.ReviewBinding{}) || delivery.Candidate.Filter == nil || delivery.Validation == nil || !delivery.Validation.Passed || !reflect.DeepEqual(delivery.Binding, expectedValidatorBinding) || !reflect.DeepEqual(delivery.Validation.Binding, delivery.Binding) || !reflect.DeepEqual(delivery.Candidate.Filter, sourceDelivery.Candidate.Filter) {
		t.Fatalf("validator delivery=%#v err=%v", delivery, err)
	}
	if view := delivery.Candidate.Filter.Views; len(view) != 1 || view[0].Path != "file" || view[0].CanonicalSHA256 != filterPipelineSHA(wantB) || view[0].RenderedSHA256 != filterPipelineSHA(desiredR) {
		t.Fatalf("filter evidence=%#v", delivery.Candidate.Filter)
	}
	sourceEvent := filterPipelineResultEvent(t, sourceEvents)
	validationRaw, _ := json.Marshal(delivery.Validation)
	receiptHash, receiptHashErr := hex.DecodeString(delivery.CandidateReceiptSHA256)
	if delivery.InputEventID != sourceEvent.EventID || delivery.InputSHA256 != sourceEvent.Artifact.SHA256 || delivery.OwnerDecisionID != "accept-edit" || receiptHashErr != nil || len(receiptHash) != sha256.Size || delivery.CandidateReceiptSHA256 != sourceDelivery.CandidateReceiptSHA256 || delivery.ValidationDigest != hashBytes(validationRaw) || !reflect.DeepEqual(delivery.Candidate, sourceDelivery.Candidate) {
		t.Fatalf("accepted binding delivery=%#v source=%#v", delivery, sourceEvent)
	}
	if got, readErr := os.ReadFile(candidateDirectory(validatePayload.Directory, validatorGrant, true) + "/file"); readErr != nil || !bytes.Equal(got, desiredR) {
		t.Fatalf("validator rendered=%q err=%v", got, readErr)
	}
	validatorEvents := validationCommitEvents(t, ctx, db, h, validatorGrant)
	if !filterPipelineExited(validatorEvents, validatorGrant) {
		t.Fatal("validator original Host Exited proof missing")
	}
	filterPipelineReleasedReservations(t, filepath.Join(root, "state.db"), sourceGrant, filterPipelineExitedEvent(t, sourceEvents, sourceGrant), validatorGrant, filterPipelineExitedEvent(t, validatorEvents, validatorGrant))
	guard.pending--
	validationAccept(t, ctx, db, validatorGrant)
	t.Fatal("filtered review unexpectedly reached post-validator chain on unsupported baseline")
}
