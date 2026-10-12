package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func startCandidateReviewSession(t *testing.T, provider adapter.Provider) (reviewRetryFixture, contract.Event) {
	t.Helper()
	f := newReviewRetryFixtureForProvider(t, provider, "")
	g := f.grant
	if g.Input == nil || g.Input.Event.TaskID != "validate" || g.Input.DecisionCommandID != "accept-validate" {
		t.Fatal("review did not bind actual owner-accepted validation")
	}
	e := contract.Event{Version: 1, ProducerID: f.host, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, ExecutionEpoch: 1, CommandID: g.CommandID, Sequence: 4, PayloadHash: runtimeHash("candidate review runtime")}
	for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventSession} {
		e.Sequence++
		e.Kind, e.EventID = kind, "review-"+kind
		if kind == contract.EventSession {
			e.SessionKind, e.SessionID = "session-id", "native-review-session"
			if provider == adapter.ProviderAGY {
				e.SessionKind = "conversation-id"
			}
		}
		if _, err := f.db.CommitHostEvent(context.Background(), e); err != nil {
			t.Fatalf("ordinary candidate review %s rejected: %v", kind, err)
		}
	}
	var bindings int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM attempt_provider_bindings WHERE attempt_id=?`, g.AttemptID).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatalf("candidate action acquired unsupported continuation binding: %d %v", bindings, err)
	}
	return f, e
}

func TestCandidateReviewNativeSessionPreservesOwnerAcceptChain(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			f, e := startCandidateReviewSession(t, provider)
			ctx := context.Background()
			// Store-level receipt uses the existing review delivery schema. The
			// Host native structured-review decoder is unchanged by this test.
			body, _ := json.Marshal(map[string]any{"version": 1, "stage": "review", "input_event_id": f.grant.Input.Event.EventID, "input_sha256": f.grant.Input.Event.PayloadHash, "owner_decision_id": f.grant.Input.DecisionCommandID, "candidate": map[string]any{"candidate_oid": strings.Repeat("b", 40)}, "validation": map[string]any{"passed": true}, "review": map[string]any{"decision": "approve", "summary": "accepted candidate independently reviewed"}})
			e.Sequence++
			e.Kind, e.EventID, e.PayloadHash = contract.EventResult, "review-result", runtimeHash(string(body))
			e.Artifact = managedHostArtifactForTest(t, f.db, f.grant, "terminal", 0, body)
			if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			x := e
			x.Sequence++
			x.Kind, x.EventID, x.ActiveMS = contract.EventExited, "review-exited", 1234
			zero := 0
			x.ExitCode = &zero
			if _, err := f.db.CommitHostEvent(ctx, x); err != nil {
				t.Fatal(err)
			}
			page, err := f.db.CollectPending(ctx, "review", "", 0, false)
			if err != nil || len(page.Events) != 1 || page.Events[0].Kind != contract.EventResult {
				t.Fatalf("native review terminal unavailable to owner: %+v %v", page, err)
			}
			e = page.Events[0]
			if _, err = f.db.ReviewResult(ctx, ReviewSpec{TaskID: "review", WorkRevision: e.WorkRevision, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, Decision: "accept", CommandID: "accept-review"}); err != nil {
				t.Fatal(err)
			}
			integrate, err := f.db.ClaimReady(ctx, f.host, 1, 2)
			if err != nil || integrate.TaskID != "integrate" || integrate.Input == nil || integrate.Input.Event.EventID != e.EventID || integrate.Input.DecisionCommandID != "accept-review" {
				t.Fatalf("accepted native review did not release bound integration: %+v %v", integrate, err)
			}
		})
	}
}

func TestCandidateReviewNativeSessionDoesNotAuthorizeContinuation(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			for _, terminal := range []string{contract.EventQuestion, contract.EventStopped} {
				t.Run(terminal, func(t *testing.T) {
					f, e := startCandidateReviewSession(t, provider)
					ctx := context.Background()
					e.Sequence++
					e.Kind, e.EventID = terminal, "review-"+terminal
					if terminal == contract.EventQuestion {
						e.QuestionID, e.QuestionKind, e.QuestionRevision = "review-question", "technical", 1
					} else {
						e.StopReason = "owner_stop_requested"
					}
					if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
						t.Fatal(err)
					}
					x := e
					x.Sequence++
					x.Kind, x.EventID = contract.EventExited, "review-exited"
					x.StopReason = ""
					zero := 0
					x.ExitCode, x.ActiveMS = &zero, 1234
					if _, err := f.db.CommitHostEvent(ctx, x); err != nil {
						t.Fatal(err)
					}
					if terminal == contract.EventQuestion {
						if _, err := f.db.AnswerQuestion(ctx, AnswerSpec{TaskID: "review", WorkRevision: 1, QuestionID: e.QuestionID, QuestionRevision: 1, Answer: "continue"}); !errors.Is(err, CodeError("candidate_resume_unsupported")) {
							t.Fatalf("candidate answer admitted: %v", err)
						}
					} else {
						if err := f.db.QueueResume(ctx, "review", 1); !errors.Is(err, CodeError("candidate_resume_unsupported")) {
							t.Fatalf("legacy candidate resume admitted: %v", err)
						}
						if err := f.db.sql.QueryRow(`SELECT event_revision,action_slot FROM runtime_events WHERE event_id=?`, e.EventID).Scan(&e.EventRevision, &e.ActionSlot); err != nil {
							t.Fatal(err)
						}
						if _, err := f.db.QueueContinuation(ctx, ResumeContinuationSpec{TaskID: "review", WorkRevision: 1, SegmentID: e.SegmentID, EventID: e.EventID, EventRevision: e.EventRevision, EventHash: e.PayloadHash, ActionSlot: e.ActionSlot, CommandID: "resume-review"}); !errors.Is(err, CodeError("candidate_resume_unsupported")) {
							t.Fatalf("typed candidate resume admitted: %v", err)
						}
					}
					var decisions, segments int
					if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM continuation_decisions WHERE task_id='review'`).Scan(&decisions); err != nil {
						t.Fatal(err)
					}
					if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id='review'`).Scan(&segments); err != nil || decisions != 0 || segments != 1 {
						t.Fatalf("denied candidate continuation changed reservation/segment: decisions=%d segments=%d err=%v", decisions, segments, err)
					}
				})
			}
		})
	}
}
