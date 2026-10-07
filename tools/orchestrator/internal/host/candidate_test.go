package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestCandidateReviewUsesItsOwnProviderLock(t *testing.T) {
	implementer := &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderGrok, Protocol: adapter.ProtocolID(adapter.ProviderGrok), Binary: adapter.BinaryPin{Path: "/private/bin/grok", Version: "grok 1.0.34 (3736acbc8658)", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	reviewer := &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderAGY, Protocol: adapter.ProtocolID(adapter.ProviderAGY), Binary: adapter.BinaryPin{Path: "/private/bin/agy", Version: "1.2.5", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	if err := validateCandidateSourceProvider(implementer, string(adapter.ProviderGrok)); err != nil {
		t.Fatalf("Grok implementation lock rejected: %v", err)
	}
	if err := validateCandidateReviewProvider(reviewer, string(adapter.ProviderAGY), reviewer.Binary.Path, reviewer.Binary.SHA256); err != nil {
		t.Fatalf("AGY review lock rejected after Grok implementation: %v", err)
	}
	if err := validateCandidateReviewProvider(reviewer, string(adapter.ProviderAGY), implementer.Binary.Path, implementer.Binary.SHA256); err == nil || err.Error() != "candidate_review_provider_mismatch" {
		t.Fatalf("review accepted implementation pin: %v", err)
	}
	if err := validateCandidateReviewProvider(implementer, string(adapter.ProviderGrok), implementer.Binary.Path, implementer.Binary.SHA256); err != nil {
		t.Fatalf("verified Grok review lock rejected: %v", err)
	}
}

func TestCandidateReviewDirectoryUsesTheMaterializedAbsolutePath(t *testing.T) {
	grant := contract.LaunchCommand{RunID: "run", TaskID: "review", AttemptID: "attempt", SegmentID: "segment", AdapterPayload: []byte(`{"directory":"/private/reviews/candidate","candidate_action":{"version":1,"operation":"review","auto_directory":true}}`)}
	directory, err := candidateReviewDirectory(grant)
	if err != nil {
		t.Fatal(err)
	}
	want := candidateDirectory("/private/reviews/candidate", grant, true)
	if directory != want {
		t.Fatalf("review directory=%q want=%q", directory, want)
	}
}

func TestCandidateReviewReadInstructionIsProviderSpecific(t *testing.T) {
	if got := candidateReviewReadInstruction(adapter.ProviderAGY); got != "Begin with view_file on the relevant absolute paths under that directory" {
		t.Fatalf("AGY instruction=%q", got)
	}
	if got := candidateReviewReadInstruction(adapter.ProviderClaude); strings.Contains(got, "view_file") || !strings.Contains(got, "read-only tools") {
		t.Fatalf("Claude instruction=%q", got)
	}
}

func TestDecodeCandidateReviewRejectsAnythingButOneStrictSchemaValue(t *testing.T) {
	valid := `{"decision":"approve","summary":"reviewed"}`
	decision, err := decodeCandidateReview(valid)
	if err != nil || decision.Decision != "approve" || decision.Summary != "reviewed" {
		t.Fatalf("valid structured review=%+v err=%v", decision, err)
	}
	for _, body := range []string{
		"prose " + valid,
		`{"decision":"approve"}`,
		valid + "\n" + valid,
		`{"decision":"approve","summary":"reviewed","extra":true}`,
		`{"decision":"approve","summary":""}`,
	} {
		if _, err := decodeCandidateReview(body); err == nil || err.Error() != "candidate_review_invalid" {
			t.Fatalf("accepted non-schema review %q: %v", body, err)
		}
	}
}

func TestCandidateFailureClassifiesActionWithoutErrorText(t *testing.T) {
	for _, tc := range []struct{ message, code string }{
		{"candidate_review_invalid", "candidate_review_invalid"},
		{"candidate_action_artifact_too_large", "candidate_action_artifact_too_large"},
		{"PRIVATE_SENTINEL", "candidate_freeze_failed"},
	} {
		var result map[string]any
		if err := json.Unmarshal(candidateFailure(errors.New(tc.message)), &result); err != nil {
			t.Fatal(err)
		}
		if tc.code == "candidate_review_invalid" {
			if result["writer_version"] != float64(2) {
				t.Fatal("review diagnostic lacks integer v2", result)
			}
		} else if _, ok := result["writer_version"]; ok {
			t.Fatal("other diagnostic changed legacy format", result)
		}
		if result["code"] != tc.code {
			t.Fatalf("classification=%v, want code=%s", result, tc.code)
		}
	}
}

func TestStopSegmentCancelsCandidateFreezeAfterProviderExit(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	finished := make(chan contract.Result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	meta := launchMetadata{commandID: "command", workRevision: 1, freezeCandidate: func(ctx context.Context, _ string) ([]byte, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	go func() {
		defer close(done)
		result, _ := h.execute(ctx, store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}, "run", "task", process.Command{Path: "/usr/bin/true"}, false, meta)
		finished <- result
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("freeze not reached")
	}
	if err := h.StopSegment(context.Background(), "segment"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		if result.Status != "interrupted" {
			t.Fatalf("stopped freeze=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel freeze")
	}
}

func TestIntegratedResultJournalFailureRetainsUncertainEvidence(t *testing.T) {
	journal, err := gitopsworker.OpenJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	out := CandidateDelivery{Version: 1, Stage: "integrate", Integration: &gitops.IntegrationOutcome{State: gitops.IntegrationIntegrated, TargetHEAD: "accepted-candidate", CandidateOID: "accepted-candidate"}}
	_, err = finishCandidateDelivery(context.Background(), out, true, journal)
	var safe interface{ SafeArtifact() []byte }
	if !errors.Is(err, gitops.ErrIntegrationUncertain) || !errors.As(err, &safe) {
		t.Fatalf("integrated result downgraded after outer journal failed: %v", err)
	}
	var decoded CandidateDelivery
	if json.Unmarshal(safe.SafeArtifact(), &decoded) != nil || decoded.Integration == nil || decoded.Integration.TargetHEAD != "accepted-candidate" {
		t.Fatal("landed version evidence lost")
	}
}

func TestCancellationCannotEraseIntegratedResult(t *testing.T) {
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "producer")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, _ := json.Marshal(CandidateDelivery{Version: 1, Stage: "integrate", Integration: &gitops.IntegrationOutcome{State: gitops.IntegrationIntegrated, TargetHEAD: "accepted-candidate"}})
	meta := launchMetadata{commandID: "command", workRevision: 1, integrationAction: true, finalizeAction: func(context.Context, string, int, bool) ([]byte, error) { cancel(); return body, nil }}
	meta.artifactGrant = metadataForLaunch(managedHostGrant(t, h, contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", CommandID: "command", WorkRevision: 1})).artifactGrant
	result, err := h.execute(ctx, store.Attempt{ID: "attempt", TaskID: "task", SegmentID: "segment"}, "run", "task", process.Command{Path: "/usr/bin/true"}, false, meta)
	if result.Status != "unknown" || !errors.Is(err, gitops.ErrIntegrationUncertain) {
		t.Fatalf("cancellation erased landed result: %+v %v", result, err)
	}
	actual, err := os.ReadFile(result.ArtifactPath)
	if err != nil || string(actual) != string(body) {
		t.Fatal("integrated artifact not preserved")
	}
}

func TestCandidateReviewInvalidWorkerEmitsV2Lifecycle(t *testing.T) {
	if os.Getenv("F01_V2_LOCAL_WORKER") == "1" {
		_, _ = os.Stdout.WriteString(`{"type":"system","subtype":"init","session_id":"review-session"}` + "\n")
		_, _ = os.Stdout.WriteString(`{"type":"result","subtype":"success","session_id":"review-session","result":"ignored prose","structured_output":{"decision":"approve"}}` + "\n")
		os.Exit(0)
	}
	for _, session := range []bool{false, true} {
		t.Run(fmt.Sprint("session=", session), func(t *testing.T) { testCandidateReviewInvalidWorkerLifecycle(t, session) })
	}
}

func testCandidateReviewInvalidWorkerLifecycle(t *testing.T, session bool) {
	db, h, grant := managedReviewFailureFixture(t)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	meta := metadataForLaunch(grant)
	meta.finalizeAction = func(_ context.Context, text string, _ int, _ bool) ([]byte, error) {
		_, err := decodeCandidateReview(text)
		return nil, err
	}
	a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
	cmd := process.Command{Path: "/usr/bin/true"}
	if session {
		binary, err := filepath.EvalSymlinks(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		cmd = process.Command{Path: binary, Args: []string{"-test.run=^TestCandidateReviewInvalidWorkerEmitsV2Lifecycle$"}, Env: []string{"F01_V2_LOCAL_WORKER=1"}}
		meta.outputProvider = string(adapter.ProviderClaude)
		meta.structuredReview = true
	}
	result, err := h.execute(ctx, a, grant.RunID, grant.TaskID, cmd, false, meta)
	if err != nil || result.Status != "failed" || result.ExitCode != 0 {
		t.Fatal("invalid review worker outcome", result, err)
	}
	body, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic map[string]any
	if err := json.Unmarshal(body, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic["writer_version"] != float64(2) || diagnostic["code"] != "candidate_review_invalid" {
		t.Fatalf("missing v2 diagnostic: %s", body)
	}
	records, err := h.Collect(ctx, a)
	want := []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventFailed, contract.EventExited}
	if session {
		want = []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventSession, contract.EventFailed, contract.EventExited}
	}
	if err != nil || !reflect.DeepEqual(eventKinds(records), want) {
		t.Fatal("invalid actual worker lifecycle", records, err)
	}
	if records[0].Process != nil {
		t.Fatal("Prepared acquired process before Start")
	}
	for i, e := range records {
		if i > 0 && (e.Sequence <= records[i-1].Sequence || !reflect.DeepEqual(e.Process, records[1].Process)) {
			t.Fatal("unordered/incoherent actual worker", records)
		}
		if e.ProducerID != grant.HostID || e.RunID != grant.RunID || e.TaskID != grant.TaskID || e.AttemptID != grant.AttemptID || e.SegmentID != grant.SegmentID || e.CommandID != grant.CommandID || e.WorkRevision != grant.WorkRevision {
			t.Fatal("worker identity changed", e)
		}
		if _, err := db.CommitHostEvent(ctx, e); err != nil {
			t.Fatal("Store rejected actual Host event", e.Kind, err)
		}
	}
	failed, exited := records[len(records)-2], records[len(records)-1]
	if failed.ExitCode == nil || exited.ExitCode == nil || *failed.ExitCode != 0 || *exited.ExitCode != 0 || !reflect.DeepEqual(failed.Artifact, exited.Artifact) || failed.PayloadHash != hashBytes(body) || exited.PayloadHash != hashText("exited:"+grant.SegmentID) {
		t.Fatal("Host terminal evidence changed", failed, exited)
	}
	page, err := db.CollectPending(ctx, grant.TaskID, "", 0, false)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != contract.EventFailed || !reflect.DeepEqual(page.Events[0].Artifact, failed.Artifact) {
		t.Fatal("Store lost actual Host failure", page, err)
	}
	e := page.Events[0]
	if _, err := db.QueueRetry(ctx, store.RetrySpec{TaskID: grant.TaskID, WorkRevision: grant.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, SegmentID: grant.SegmentID, NextAttemptNo: 2, CommandID: "actual-review-retry"}); err != nil {
		t.Fatal("actual Host failure blocked owner retry", err)
	}
	next, err := db.ClaimReady(ctx, grant.HostID, 1, 2)
	if err != nil || next.AttemptID == grant.AttemptID || next.SegmentID == grant.SegmentID || !reflect.DeepEqual(next.Input, grant.Input) || !reflect.DeepEqual(next.DependencyInputs, grant.DependencyInputs) {
		t.Fatal("actual Host retry lost frozen input", next, err)
	}
}

// Dependency artifacts and the invalid review are produced by actual local
// workers. This exercises Store grants and retry without Provider/network calls.
func managedReviewFailureFixture(t *testing.T) (*store.DB, *Host, contract.LaunchCommand) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	pinBody, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	lock := &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: "/usr/bin/true", Version: "claude-test", SHA256: hashBytes(pinBody)}}
	var tasks []store.TaskSpec
	for i, id := range []string{"edit", "validate", "review"} {
		payload := adapter.InvocationPayload{Directory: filepath.Join(root, id)}
		task := store.TaskSpec{ID: id, RunID: "review-v2-run", MaxAttempts: 3, MaxActiveMS: 5000}
		if i == 0 {
			payload.CandidateWorkspace = &adapter.CandidateWorkspace{Version: 1}
		} else {
			task.Dependencies = []string{"edit"}
			if id == "review" {
				task.Dependencies = []string{"validate", "edit"}
			}
			payload.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: id, SourceTask: task.Dependencies[0]}
			if id == "review" {
				payload.CandidateAction.DependencyInputVersion = 1
				payload.Provider = string(adapter.ProviderClaude)
				payload.ProviderLock = lock
				payload.Profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}
				payload.Prompt = "controlled readonly review"
			} else {
				payload.Kind = "candidate"
				payload.CandidateAction.Command = []string{"/usr/bin/true"}
			}
		}
		task.AdapterPayload, _ = json.Marshal(payload)
		tasks = append(tasks, task)
	}
	receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "review-v2-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "birth"}, Host: store.HostLaunchSpec{OriginContextID: "test", HostGeneration: "generation", Executable: "/private/fixture/host"}, Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "birth", HostGeneration: "generation", PID: os.Getpid(), Birth: "host", Executable: "/private/fixture/host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(db, filepath.Join(root, "host-spool", hostID))
	if err != nil {
		t.Fatal(err)
	}
	h.producerID = hostID
	for _, id := range []string{"edit", "validate"} {
		grant, err := db.ClaimReady(ctx, hostID, 1, 1)
		if err != nil || grant.TaskID != id {
			t.Fatal("dependency grant", grant, err)
		}
		meta := metadataForLaunch(grant)
		body, _ := json.Marshal(map[string]any{"stage": id, "validation": map[string]any{"passed": true}, "candidate": map[string]any{"candidate_oid": strings.Repeat("b", 40)}})
		meta.finalizeAction = func(context.Context, string, int, bool) ([]byte, error) { return body, nil }
		a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
		if result, err := h.execute(ctx, a, grant.RunID, grant.TaskID, process.Command{Path: "/usr/bin/true"}, false, meta); err != nil || result.Status != "result_ready" {
			t.Fatal("dependency worker", result, err)
		}
		events, err := h.Collect(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if _, err := db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal("dependency event", err)
			}
		}
		page, err := db.CollectPending(ctx, id, "", 0, false)
		if err != nil || len(page.Events) != 1 {
			t.Fatal("dependency result", page, err)
		}
		e := page.Events[0]
		if _, err := db.ReviewResult(ctx, store.ReviewSpec{TaskID: id, WorkRevision: grant.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "accept", CommandID: "accept-" + id}); err != nil {
			t.Fatal("dependency acceptance", err)
		}
	}
	grant, err := db.ClaimReady(ctx, hostID, 1, 1)
	if err != nil || grant.TaskID != "review" {
		t.Fatal("review grant", grant, err)
	}
	return db, h, grant
}
