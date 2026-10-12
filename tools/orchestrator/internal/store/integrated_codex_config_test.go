package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Historical config payload, actual SQLite decision/claim APIs and synthetic
// authenticated Host events. This is not new config admission or kernel proof.
func TestIntegratedCodexConfigKeepsCLIDefaultAcrossTwoQuestionCycles(t *testing.T) {
	ref := configOriginalRef()
	f := newTypedContinuationFixtureWithConfig(t, adapter.ProviderCodex, "task", ref)
	first := f.grant
	f.start(t)
	for revision := 1; revision <= 2; revision++ {
		question := f.question(t, revision)
		if _, err := f.db.AnswerQuestion(context.Background(), AnswerSpec{TaskID: first.TaskID, WorkRevision: 1, QuestionID: question.QuestionID, QuestionRevision: revision, Answer: fmt.Sprintf("answer %d", revision)}); err != nil {
			t.Fatal(err)
		}
		grant, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		if grant.AttemptID != first.AttemptID || grant.SegmentID == f.grant.SegmentID || grant.GrantedActiveMS != 1000-int64(revision)*200 || grant.SessionID != f.sessionID || grant.Continuation == nil || grant.Continuation.SourceEventID != question.EventID || string(grant.AdapterPayload) != string(f.payload) {
			t.Fatalf("original config/attempt/session/budget drift: %+v", grant)
		}
		payload, err := adapter.DecodeInvocationPayload(grant.AdapterPayload)
		if err != nil || payload.ConfigGeneration == nil || *payload.ConfigGeneration != *ref || payload.ConfigModelSource != "cli-default" || !payload.Profile.CLIModelDefault || payload.Profile.Model != "" {
			t.Fatalf("pinned metadata overrode CLI default: %+v err=%v", payload, err)
		}
		request := adapter.Request{Provider: adapter.ProviderCodex, Binary: payload.ProviderLock.Binary, Lock: payload.ProviderLock, Profile: payload.Profile, CWD: payload.Directory, Prompt: payload.Prompt, Continuation: grant.Continuation, Session: adapter.SessionRef{Kind: adapter.SessionID, ID: grant.SessionID}}
		invocation, err := adapter.BuildManagedCodexInvocation(request)
		if err != nil {
			t.Fatal("private continuation rejected pinned CLI default", err)
		}
		descriptor, ok := adapter.ManagedCodexDescriptorFor(invocation)
		if !ok || descriptor.ResumeThreadID != f.sessionID || descriptor.Backend != adapter.ManagedCodexBackend {
			t.Fatal("private native thread binding lost")
		}
		if _, err = adapter.BuildInvocation(request); err == nil || err.Error() != "profile_resume_not_verified" {
			t.Fatal("public Codex continuation guard opened", err)
		}
		initialPublic := request
		initialPublic.Continuation = nil
		initialPublic.Session = adapter.SessionRef{}
		if _, err = adapter.BuildInvocation(initialPublic); err == nil || err.Error() != "codex_trial_guard_not_ready" {
			t.Fatal("public initial Codex guard opened", err)
		}
		changed := *payload.Profile
		changed.Model = "saved-metadata-must-not-select-model"
		request.Profile = &changed
		if _, err = adapter.BuildManagedCodexInvocation(request); err == nil {
			t.Fatal("private continuation accepted a saved/explicit model override")
		}
		pending, err := f.db.PendingLaunches(context.Background(), f.host, 1)
		if err != nil || len(pending) != 1 || !reflect.DeepEqual(pending[0], grant) {
			t.Fatal("durable pending grant changed", err)
		}
		f.grant = grant
		f.start(t)
	}
	var count int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatal("question continuation created another attempt", err)
	}
	if first.Continuation != nil || first.SessionID != "" || f.grant.Continuation.Mode != contract.ContinuationQuestion {
		t.Fatal("initial/resume shape drift")
	}
}
