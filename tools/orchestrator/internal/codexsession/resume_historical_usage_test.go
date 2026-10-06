package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
)

// Pure real Run/M1/net.Pipe. No Host, process, TestMain or native calls. These
// synthetic wire facts prove a protocol contract, not the actual native cause.
func historicalUsageParams() map[string]any {
	breakdown := func() map[string]any {
		return map[string]any{"totalTokens": 13, "inputTokens": 8, "cachedInputTokens": 0, "cacheWriteInputTokens": 0, "outputTokens": 5, "reasoningOutputTokens": 0}
	}
	return map[string]any{"threadId": "thread-1", "turnId": "historical-turn", "tokenUsage": map[string]any{"total": breakdown(), "last": breakdown(), "modelContextWindow": 32768}}
}

func historicalUsageRun(t *testing.T, when, method string, params map[string]any, plans ...historicalUsagePlan) (fixtureResult, bool) {
	t.Helper()
	var plan historicalUsagePlan
	if len(plans) > 0 {
		plan = plans[0]
	}
	threadMethod := "thread/resume"
	if plan.initial {
		threadMethod = "thread/start"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	projection, err := codexconfig.Project([]byte("model=\"gpt-5.4\"\nmodel_reasoning_effort=\"high\"\nservice_tier=\"priority\"\n"), codexconfig.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	deadline, _ := ctx.Deadline()
	_ = remote.SetDeadline(deadline)
	client, err := codexrpc.New(ctx, local, codexrpc.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	callbackReady := make(chan struct{})
	var callbacks atomic.Int32
	type observation struct {
		methods []string
		written bool
		err     error
	}
	done := make(chan observation, 1)
	go func() {
		var observed observation
		defer func() { _ = remote.Close(); done <- observed }()
		peer := &fixturePeer{json.NewEncoder(remote), json.NewDecoder(remote), remote}
		read := func(method string) (map[string]json.RawMessage, error) {
			var frame map[string]json.RawMessage
			if err := peer.dec.Decode(&frame); err != nil {
				return nil, err
			}
			var actual string
			_ = json.Unmarshal(frame["method"], &actual)
			observed.methods = append(observed.methods, actual)
			if actual != method || checkFixtureRequest(method, frame["params"], !plan.initial) != nil {
				return nil, fmt.Errorf("synthetic peer observed an unexpected call")
			}
			return frame, nil
		}
		notify := func(count int) error {
			for i := 0; i < count; i++ {
				err := peer.notify(method, params)
				observed.written = err == nil
				if err != nil {
					return err
				}
			}
			return nil
		}
		for _, stage := range []string{"initialize", "initialized", "config/read", threadMethod} {
			frame, err := read(stage)
			if err != nil {
				observed.err = err
				return
			}
			if stage == "initialized" {
				continue
			}
			if stage == threadMethod && plan.warningBytes > 0 {
				if observed.err = peer.notify("warning", map[string]any{"message": strings.Repeat("x", plan.warningBytes)}); observed.err != nil {
					return
				}
			}
			if stage == threadMethod && (when == "before_reply" || plan.beforeReplyCount > 0) {
				count := plan.beforeReplyCount
				if count == 0 {
					count = 1
				}
				if observed.err = notify(count); observed.err != nil {
					return
				}
			}
			response := fixtureResponse(stage)
			if stage == threadMethod {
				thread := obj(response["thread"])
				thread["historyMode"], thread["path"] = "paginated", "/private/owned/codex/sessions/rollout.jsonl"
			}
			if observed.err = peer.enc.Encode(map[string]any{"id": frame["id"], "result": response}); observed.err != nil {
				return
			}
		}
		if when == "callback_pending" {
			// The actual OnThread job is now held. Notification delivery never
			// depends on sleeps, select ordering, or a synthetic callback result.
			select {
			case <-callbackReady:
			case <-ctx.Done():
				observed.err = ctx.Err()
				return
			}
			count := plan.afterReplyCount
			if count == 0 {
				count = 1
			}
			if observed.err = notify(count); observed.err != nil {
				return
			}
		}
		if when == "turn_start_pending" {
			// The real original Call is in flight. Queue historical usage before
			// its matched reply, whose distinct turn must never inherit old ID.
			frame, err := read("turn/start")
			if err != nil {
				observed.err = err
				return
			}
			if plan.provisionalTurn {
				turn := fixtureStartedTurn()
				turn["id"] = "current-turn"
				if observed.err = peer.notify("turn/started", map[string]any{"threadId": "thread-1", "turn": turn}); observed.err != nil {
					return
				}
			}
			if observed.err = notify(1); observed.err != nil {
				return
			}
			turn := fixtureStartedTurn()
			turn["id"] = "current-turn"
			if observed.err = peer.enc.Encode(map[string]any{"id": frame["id"], "result": map[string]any{"turn": turn}}); observed.err != nil {
				return
			}
		}
		// No completion/item is emitted. The original 2s Run context bounds a
		// held cooperative callback/current pending turn and closes the pipe.
		var extra any
		if err := peer.dec.Decode(&extra); err == nil {
			observed.err = fmt.Errorf("unexpected later task call")
		} else {
			observed.err = err
		}
	}()
	options := Options{CWD: "/private/owned/project", OwnedHome: "/private/owned/codex", PlatformFamily: "unix", PlatformOS: "macos", Prompt: "Review API", ResumeThreadID: "thread-1", OnThread: func(callbackCtx context.Context, binding ThreadBinding) error {
		if callbacks.Add(1) != 1 || binding.ThreadID != "thread-1" || binding.HistoryMode != "paginated" {
			return ErrCallback
		}
		close(callbackReady)
		if when != "turn_start_pending" {
			<-callbackCtx.Done()
			return callbackCtx.Err()
		}
		return nil
	}}
	if plan.initial {
		options.ResumeThreadID = ""
	}
	out, runErr := Run(ctx, client, projection, options)
	observed := <-done
	if observed.err != nil && !errors.Is(observed.err, io.EOF) && !errors.Is(observed.err, io.ErrClosedPipe) {
		var e net.Error
		if !errors.As(observed.err, &e) || !e.Timeout() {
			t.Fatal("unexpected peer failure", observed.err)
		}
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("original M1 consumer/writer not joined")
	}
	return fixtureResult{out: out, err: runErr, methods: observed.methods, callbacks: int(callbacks.Load())}, observed.written
}

func TestRunResumeHistoricalUsageDoesNotBecomeCurrentTurn(t *testing.T) {
	for _, when := range []string{"before_reply", "callback_pending", "turn_start_pending"} {
		t.Run(when, func(t *testing.T) {
			params := historicalUsageParams()
			shapes, err := loadShapes()
			body, _ := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := shapes.object("notification:thread/tokenUsage/updated", body); err != nil {
				t.Fatal("fixed legitimate notification shape not admitted", err)
			}
			result, written := historicalUsageRun(t, when, "thread/tokenUsage/updated", params)
			if !written || result.callbacks != 1 || !errors.Is(result.err, context.DeadlineExceeded) || result.out.Binding.ThreadID != "thread-1" || result.out.Completion != (adapter.TaskCompletion{}) || result.out.Status != "" {
				t.Fatalf("historical display-only usage rejected or gained authority: written=%v callback=%d err=%v turn=%q status=%q", written, result.callbacks, result.err, result.out.TurnID, result.out.Status)
			}
			want := []string{"initialize", "initialized", "config/read", "thread/resume"}
			turnID := ""
			if when == "turn_start_pending" {
				want, turnID = append(want, "turn/start"), "current-turn"
			}
			if !reflect.DeepEqual(result.methods, want) || result.out.TurnID != turnID {
				t.Fatal("old usage bound a provisional/current turn or forwarded extra calls")
			}
		})
	}
}

func TestRunResumeHistoricalUsageDoesNotAuthorizeForeignOrContent(t *testing.T) {
	for _, kind := range []string{"foreign_thread", "malformed_usage", "oversized_turn_id", "old_turn_completion", "old_item_content"} {
		t.Run(kind, func(t *testing.T) {
			method, params := "thread/tokenUsage/updated", historicalUsageParams()
			switch kind {
			case "foreign_thread":
				params["threadId"] = "foreign-thread"
			case "malformed_usage":
				obj(obj(params["tokenUsage"])["total"])["totalTokens"] = "13"
			case "oversized_turn_id":
				params["turnId"] = strings.Repeat("x", codexrpc.MaxScalarBytes+1)
			case "old_turn_completion":
				turn := fixtureTerminal()
				turn["id"] = "historical-turn"
				method, params = "turn/completed", map[string]any{"threadId": "thread-1", "turn": turn}
			case "old_item_content":
				method, params = "item/completed", map[string]any{"threadId": "thread-1", "turnId": "historical-turn", "completedAtMs": 3, "item": fixtureFinal()}
			}
			result, written := historicalUsageRun(t, "before_reply", method, params)
			if !written || !errors.Is(result.err, ErrProtocol) || result.callbacks != 0 || result.out.Completion != (adapter.TaskCompletion{}) || result.out.TurnID != "" || result.out.Status != "" {
				t.Fatalf("foreign/malformed/historical content acquired authority: kind=%s written=%v callback=%d err=%v", kind, written, result.callbacks, result.err)
			}
			if !reflect.DeepEqual(result.methods, []string{"initialize", "initialized", "config/read", "thread/resume"}) {
				t.Fatal("refusal wrote a new task call")
			}
		})
	}
}
