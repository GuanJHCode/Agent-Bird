package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
)

type historicalUsagePlan struct {
	initial                           bool
	beforeReplyCount, afterReplyCount int
	warningBytes                      int
	provisionalTurn                   bool
}

func TestRunResumeHistoricalUsageKeepsSharedEarlyBudget(t *testing.T) {
	for _, test := range []struct {
		name string
		when string
		plan historicalUsagePlan
		want error
	}{
		{"exact_32", "before_reply", historicalUsagePlan{beforeReplyCount: 32}, context.DeadlineExceeded},
		{"reject_33", "before_reply", historicalUsagePlan{beforeReplyCount: 33}, ErrStateLimit},
		{"cross_phase_32", "callback_pending", historicalUsagePlan{beforeReplyCount: 16, afterReplyCount: 16}, context.DeadlineExceeded},
		{"cross_phase_33", "callback_pending", historicalUsagePlan{beforeReplyCount: 16, afterReplyCount: 17}, ErrStateLimit},
		{"cross_phase_bytes", "callback_pending", historicalUsagePlan{warningBytes: (256 << 10) - 512, afterReplyCount: 3}, ErrStateLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, _ := historicalUsageRun(t, test.when, "thread/tokenUsage/updated", historicalUsageParams(), test.plan)
			if !errors.Is(result.err, test.want) || result.out.Completion != (adapter.TaskCompletion{}) || result.out.TurnID != "" || result.out.Status != "" {
				t.Fatalf("restored usage reset/skipped shared early budget: err=%v want=%v turn=%q", result.err, test.want, result.out.TurnID)
			}
			if test.name != "reject_33" && result.callbacks != 1 || test.name == "reject_33" && result.callbacks != 0 {
				t.Fatal("early budget failed at an unexpected binding phase")
			}
		})
	}
}

func TestRunResumeHistoricalUsageCannotOverwriteProvisionalTurn(t *testing.T) {
	result, written := historicalUsageRun(t, "turn_start_pending", "thread/tokenUsage/updated", historicalUsageParams(), historicalUsagePlan{provisionalTurn: true})
	if !written || !errors.Is(result.err, context.DeadlineExceeded) || result.callbacks != 1 || result.out.TurnID != "current-turn" || result.out.Completion != (adapter.TaskCompletion{}) || result.out.Status != "" {
		t.Fatalf("historical usage changed real pending lifecycle turn: %#v", result)
	}
}

func TestRunResumeHistoricalUsageDoesNotApplyToInitialThread(t *testing.T) {
	result, written := historicalUsageRun(t, "before_reply", "thread/tokenUsage/updated", historicalUsageParams(), historicalUsagePlan{initial: true})
	if !written || !errors.Is(result.err, ErrProtocol) || result.callbacks != 0 || result.out.TurnID != "" || result.out.Completion != (adapter.TaskCompletion{}) {
		t.Fatal("initial thread acquired historical resume authority")
	}
}

// Focused real event consumer boundary, complementing Run/net.Pipe phases:
// actual bindThread/bindTurn reducers construct the established-current state.
func TestRunResumeHistoricalUsageDoesNotApplyAfterCurrentTurnBinding(t *testing.T) {
	shapes, err := loadShapes()
	if err != nil {
		t.Fatal(err)
	}
	s := newSessionState(context.Background(), nil, shapes, nil, Options{CWD: "/private/owned/project", ResumeThreadID: "thread-1"})
	s.phase = "thread/resume"
	if err := s.bindThread(fixtureResponse("thread/resume")); err != nil {
		t.Fatal(err)
	}
	s.callbackDone, s.phase = true, "turn/start"
	if err := s.bindTurn(fixtureResponse("turn/start")); err != nil {
		t.Fatal(err)
	}
	s.phase = "running"
	before := s.out
	params, _ := json.Marshal(historicalUsageParams())
	err = s.event(codexrpc.Event{Notification: &codexrpc.Notification{Method: "thread/tokenUsage/updated", Params: params}})
	if !errors.Is(err, ErrProtocol) || s.out != before || s.provisionalTurn != "" {
		t.Fatal("established current turn accepted foreign historical turn")
	}
}
