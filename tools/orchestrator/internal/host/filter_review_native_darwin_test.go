//go:build darwin

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestFilterReviewNativeHostAcceptedValidator(t *testing.T) {
	filterReviewNativeChain(t, false)
}

// filterReviewNativeChain keeps the producer, validator, and readonly review
// acceptance chain identical for the optional target observation fixture.
func filterReviewNativeChain(t *testing.T, includeTarget bool, targetReview ...bool) {
	includeApproval := len(targetReview) >= 1 && targetReview[0]
	includeIntegration := len(targetReview) == 2 && targetReview[1]
	root, guard := filterPipelineRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	filterActor := filterPipelineBuildActor(t, ctx, root, guard)
	validatorActor, validatorPin := validationNativeActor(t, root, guard)
	reviewActor := filterReviewBuildActor(t, ctx, root, guard)
	reviewPin := filterPipelineDigest(t, reviewActor)
	approvalActor := ""
	if includeApproval {
		approvalActor = filterReviewBuildActor(t, ctx, root, guard, true)
	}
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
	reviewPayload := adapter.InvocationPayload{Directory: filepath.Join(root, "review"), Provider: string(adapter.ProviderClaude), ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: reviewActor, SHA256: reviewPin, Version: "controlled-readonly"}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 300000}, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "validate", DependencyInputVersion: 1, AutoDirectory: true}, GitFilter: &manifest}
	reviewRaw, _ := json.Marshal(reviewPayload)
	targetPayload := adapter.InvocationPayload{Kind: "candidate", Directory: filepath.Join(root, "preflight"), CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "filter_target_preflight", SourceTask: "review", DependencyInputVersion: 1, AutoDirectory: true, Target: adapter.CandidateTarget{Worktree: repo, Ref: "refs/heads/main", BaseOID: base}}, GitFilter: &manifest}
	targetRaw, _ := json.Marshal(targetPayload)
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tasks := []store.TaskSpec{{ID: "edit", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 300000, AdapterPayload: sourceRaw}, {ID: "validate", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 300000, Dependencies: []string{"edit"}, AdapterPayload: validateRaw}, {ID: "review", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 300000, Dependencies: []string{"validate"}, AdapterPayload: reviewRaw}}
	if includeTarget {
		tasks = append(tasks, store.TaskSpec{ID: "preflight", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 300000, Dependencies: []string{"review"}, AdapterPayload: targetRaw})
	}
	if includeApproval {
		approval := reviewPayload
		approval.Directory = filepath.Join(root, "target-review")
		approval.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 900000}
		approval.ProviderLock = &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: approvalActor, SHA256: filterPipelineDigest(t, approvalActor), Version: "controlled-target-review"}}
		approval.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "filter_target_review", SourceTask: "preflight", DependencyInputVersion: 1, AutoDirectory: true, Target: targetPayload.CandidateAction.Target}
		raw, e := json.Marshal(approval)
		if e != nil {
			t.Fatal(e)
		}
		tasks = append(tasks, store.TaskSpec{ID: "target-review", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 900000, Dependencies: []string{"preflight"}, AdapterPayload: raw})
	}

	if includeIntegration {
		integrate := targetPayload
		integrate.Directory = filepath.Join(root, "integrate")
		integrate.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "filter_integrate", SourceTask: "target-review", DependencyInputVersion: 1, AutoDirectory: true, Target: targetPayload.CandidateAction.Target}
		raw, _ := json.Marshal(integrate)
		tasks = append(tasks, store.TaskSpec{ID: "integrate", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 900000, Dependencies: []string{"preflight", "target-review"}, AdapterPayload: raw})
	}
	receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "filter-native-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "fixture"}, Host: store.HostLaunchSpec{OriginContextID: "test", HostGeneration: "fixture", Executable: "/private/controlled-host"}, Tasks: tasks})
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
	if includeIntegration {
		filterIntegrationBindNativeHelper(t, ctx, h, root, guard)
	}
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

	reviewGrant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil || reviewGrant.TaskID != "review" || reviewGrant.GitFilter == nil || reviewGrant.ValidationTool != nil || reviewGrant.Input == nil || reviewGrant.Input.Event.TaskID != "validate" {
		t.Fatalf("review grant=%+v err=%v", reviewGrant, err)
	}
	for _, prior := range []contract.LaunchCommand{sourceGrant, validatorGrant} {
		if reviewGrant.GitFilter.ReservationID == prior.GitFilter.ReservationID || reviewGrant.GitFilter.ScopeSHA256 == prior.GitFilter.ScopeSHA256 || reviewGrant.AttemptID == prior.AttemptID {
			t.Fatal("review reused prior authority")
		}
	}
	prompt, err := CandidateReviewPrompt(reviewGrant, filepath.Join(root, "host-spool"))
	if err != nil {
		t.Fatal(err)
	}
	guard.pending++
	result, err = h.ExecuteLaunch(ctx, reviewGrant, filterReviewNativeInvocation{validationNativeInvocation: validationNativeInvocation{launchInvocation: launchInvocation{args: []string{reviewActor}, dir: reviewPayload.Directory}, payload: reviewPayload}, prompt: prompt})
	if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
		t.Fatalf("review result=%+v err=%v", result, err)
	}
	reviewBody, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var reviewed CandidateDelivery
	if err = json.Unmarshal(reviewBody, &reviewed); err != nil {
		t.Fatal(err)
	}
	validatorEvent := filterPipelineResultEvent(t, validatorEvents)
	if reviewed.Stage != "review" || reviewed.Review == nil || reviewed.Review.Decision != "approve" || reviewed.Review.Summary != "read candidate; tracked write denied" || reviewed.Binding != filterReviewBinding(reviewGrant) || !reflect.DeepEqual(reviewed.ProviderLock, reviewPayload.ProviderLock) || !reflect.DeepEqual(reviewed.Target, gitops.ReviewBinding{}) || reviewed.InputEventID != validatorEvent.EventID || reviewed.InputSHA256 != validatorEvent.Artifact.SHA256 || reviewed.OwnerDecisionID != "accept-validate" {
		t.Fatalf("review envelope=%#v", reviewed)
	}
	if !reflect.DeepEqual(reviewed.Candidate, sourceDelivery.Candidate) || !reflect.DeepEqual(reviewed.Validation, delivery.Validation) || reviewed.ValidationDigest != delivery.ValidationDigest || reviewed.CandidateReceiptSHA256 != sourceDelivery.CandidateReceiptSHA256 || reviewed.Validation.Binding != expectedValidatorBinding || reviewed.Candidate.Binding.TaskID != "edit" {
		t.Fatal("review changed accepted producer/validator evidence")
	}
	reviewDir := candidateDirectory(reviewPayload.Directory, reviewGrant, true)
	if reviewDir == sourceDelivery.Candidate.Worktree || reviewDir == candidateDirectory(validatePayload.Directory, validatorGrant, true) {
		t.Fatal("review reused worktree")
	}
	if got, e := os.ReadFile(filepath.Join(reviewDir, "file")); e != nil || !bytes.Equal(got, desiredR) {
		t.Fatalf("review R=%q err=%v", got, e)
	}
	reviewEvents := validationCommitEvents(t, ctx, db, h, reviewGrant)
	if !filterPipelineExited(reviewEvents, reviewGrant) {
		t.Fatal("review original Wait/tree/cleanup Exited missing")
	}
	reviewExit := filterPipelineExitedEvent(t, reviewEvents, reviewGrant)
	for _, event := range reviewEvents {
		if event.ValidationToolCleanup != nil {
			t.Fatal("review acquired validation tool authority")
		}
	}
	readonly, err := sql.Open("sqlite", "file:"+filepath.Join(root, "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var state, release, cleanup string
	err = readonly.QueryRow("SELECT state,release_event_id,cleanup_json FROM git_filter_resource_reservations WHERE id=?", reviewGrant.GitFilter.ReservationID).Scan(&state, &release, &cleanup)
	closeErr := readonly.Close()
	if err != nil || closeErr != nil || state != "released" || release != reviewExit || cleanup == "" {
		t.Fatalf("review reservation state=%s release=%s err=%v close=%v", state, release, err, closeErr)
	}
	guard.pending--
	validationAccept(t, ctx, db, reviewGrant)
	if includeTarget {
		preflightGrant, preflight, preflightEvents := filterTargetNativeAcceptedReview(t, ctx, db, h, hostID, root, repo, baseB, guard, reviewGrant, reviewed, reviewEvents)
		if includeApproval {
			filterTargetReviewNativeAcceptedPreflight(t, ctx, db, h, hostID, root, repo, guard, preflightGrant, preflight, preflightEvents)
		}
	}
	if includeIntegration {
		filterIntegrationNativeFinal(t, ctx, db, h, hostID, root, repo, guard)
	}
	t.Logf("controlled_filtered_readonly_review original_wait_tree=true native_structured=true actual_rendered_read=true tracked_write_permission_denied=true distinct_filter_grants=true zero_target=true root=%s", root)

}

type filterReviewNativeInvocation struct {
	validationNativeInvocation
	prompt string
}

func (i filterReviewNativeInvocation) OutputProvider() string { return string(adapter.ProviderClaude) }
func (i filterReviewNativeInvocation) Stdin() []byte          { return []byte(i.prompt) }

func filterReviewBinding(g contract.LaunchCommand) gitops.CandidateBinding {
	return gitops.CandidateBinding{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision}
}

func filterReviewBuildActor(t *testing.T, ctx context.Context, root string, guard *validationNativeCleanup, targetReview ...bool) string {
	t.Helper()
	goPath := "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go"
	binary := filepath.Join(root, "native-review-actor")
	source := filepath.Join(filterPipelineModuleRoot(t), "..", "..", "tasks", "filter-review-native", "scripts", "claude-review-actor.go")
	if len(targetReview) == 1 && targetReview[0] {
		binary = filepath.Join(root, "native-target-review-actor")
		source = filepath.Join(filterPipelineModuleRoot(t), "..", "..", "tasks", "filter-target-review", "scripts", "claude-target-review-actor.go")
	}
	guard.pending++
	original, err := process.Start(ctx, process.Command{Path: goPath, PinnedPath: goPath, PinnedSHA256: filterPipelineDigest(t, goPath), Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", binary, source}, Dir: filterPipelineModuleRoot(t), ExactEnv: true, Env: []string{"PATH=" + filepath.Dir(goPath) + ":/usr/bin:/bin", "HOME=" + root, "GOCACHE=" + filepath.Join(root, "cache"), "GOPATH=" + filepath.Join(root, "gopath"), "GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "TMPDIR=/private/tmp"}})
	if err != nil || original == nil {
		if original != nil {
			validationNativeStop(original, guard)
		}
		t.Fatalf("review actor original Start: %v", err)
	}
	waitErr := original.Wait(ctx)
	treeErr := original.ConfirmTreeExited()
	if waitErr != nil || treeErr != nil {
		validationNativeStop(original, guard)
		t.Fatalf("review actor original Wait/tree: %v %v", waitErr, treeErr)
	}
	guard.pending--
	if original.ExitCode() != 0 {
		t.Fatalf("review actor build exit=%d output=%s", original.ExitCode(), strings.TrimSpace(original.Output()))
	}
	t.Logf("review_actor_build original_wait_tree=true exit=%d", original.ExitCode())
	if err = os.Chmod(binary, 0500); err != nil {
		t.Fatal(err)
	}
	return binary
}
