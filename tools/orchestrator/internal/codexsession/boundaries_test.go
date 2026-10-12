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
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
)

func TestRunCancellationAtEveryRPCPhase(t *testing.T) {
	for _, phase := range []string{"initialize", "initialized", "config/read", "thread/start", "turn/start"} {
		t.Run(phase, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{cancelAt: phase})
			requireFailure(t, result, context.Canceled)
			if len(result.methods) == 0 || result.methods[len(result.methods)-1] != phase {
				t.Fatal("cancel fixture did not reach original phase")
			}
		})
	}
}
func TestRunRequiresOriginalDeadlineAndClosesInvalidClientUse(t *testing.T) {
	for _, name := range []string{"nil_context", "no_deadline", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			constructorCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			local, peer := net.Pipe()
			client, err := codexrpc.New(constructorCtx, local, codexrpc.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				defer peer.Close()
				var frame any
				err := json.NewDecoder(peer).Decode(&frame)
				if err != io.EOF {
					done <- fmt.Errorf("invalid input opened RPC")
					return
				}
				done <- nil
			}()
			projection, _ := codexconfig.Project(nil, codexconfig.ReadOnly)
			ctx := constructorCtx
			switch name {
			case "nil_context":
				ctx = nil
			case "no_deadline":
				ctx = context.Background()
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(constructorCtx)
				cancel()
			}
			_, err = Run(ctx, client, projection, Options{CWD: "/private/owned/project", OwnedHome: "/private/owned/codex", PlatformFamily: "unix", PlatformOS: "macos", Prompt: "Review API", OnThread: func(context.Context, ThreadBinding) error { return nil }})
			want := ErrInput
			if name == "cancelled" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("%v", err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			select {
			case <-client.Done():
			default:
				t.Fatal("invalid-input transport detached")
			}
		})
	}
}

func TestRunResolvesAbsentModelOnceWithoutSelectingFallback(t *testing.T) {
	projection, err := codexconfig.Project([]byte("model_reasoning_effort=\"high\"\nservice_tier=\"priority\"\n"), codexconfig.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	result := runScenario(t, fixtureScenario{projection: projection, mutate: func(method string, response map[string]any) {
		if method == "config/read" {
			delete(obj(response["config"]), "model")
		}
	}, checkRequest: func(method string, raw json.RawMessage, resume bool) error {
		if method != "thread/start" {
			return checkFixtureRequest(method, raw, resume)
		}
		var params map[string]any
		if json.Unmarshal(raw, &params) != nil {
			return fmt.Errorf("thread params")
		}
		want := map[string]any{"cwd": "/private/owned/project", "modelProvider": "openai", "approvalPolicy": "on-request", "approvalsReviewer": "user", "sandbox": "read-only", "ephemeral": false, "serviceTier": "priority"}
		if !reflect.DeepEqual(params, want) {
			return fmt.Errorf("absent model was overridden")
		}
		return nil
	}})
	if result.err != nil || result.out.Binding.Model != "gpt-5.4" {
		t.Fatalf("first native model not fixed: %#v", result)
	}
}
func TestRunRejectsTruncatedNativeFrame(t *testing.T) {
	result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
		if phase == "initialize" {
			_, err := p.writer.Write([]byte(`{"id":`))
			if err != nil {
				return err
			}
			return io.EOF
		}
		return nil
	}})
	requireFailure(t, result, ErrProtocol)
}

func TestRunInformationalAllowlistDiscardsOnlyValidatedNativeShapes(t *testing.T) {
	fixtures := map[string]map[string]any{}
	for _, method := range []string{"item/agentMessage/delta", "item/plan/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta", "item/commandExecution/outputDelta", "item/fileChange/outputDelta"} {
		params := map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "info", "delta": "PRIVATE_INFO_SENTINEL"}
		if method == "item/reasoning/summaryTextDelta" {
			params["summaryIndex"] = 1
		}
		if method == "item/reasoning/textDelta" {
			params["contentIndex"] = 1
		}
		fixtures[method] = params
	}
	fixtures["thread/name/updated"] = map[string]any{"threadId": "thread-1", "threadName": "PRIVATE_INFO_SENTINEL"}
	fixtures["thread/tokenUsage/updated"] = map[string]any{"threadId": "thread-1", "turnId": "turn-1", "tokenUsage": map[string]any{"last": fixtureTokenUsage(), "total": fixtureTokenUsage(), "modelContextWindow": nil}}
	fixtures["account/rateLimits/updated"] = map[string]any{"rateLimits": map[string]any{"limitId": nil, "limitName": nil, "primary": nil, "secondary": nil, "credits": nil, "planType": nil}}
	fixtures["turn/diff/updated"] = map[string]any{"threadId": "thread-1", "turnId": "turn-1", "diff": "PRIVATE_INFO_SENTINEL"}
	fixtures["turn/plan/updated"] = map[string]any{"threadId": "thread-1", "turnId": "turn-1", "explanation": "PRIVATE_INFO_SENTINEL", "plan": []any{}}
	fixtures["item/reasoning/summaryPartAdded"] = map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "info", "summaryIndex": 1}
	fixtures["item/commandExecution/terminalInteraction"] = map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "info", "processId": "native-only", "stdin": "PRIVATE_INFO_SENTINEL"}
	fixtures["item/fileChange/patchUpdated"] = map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "info", "changes": []any{}}
	fixtures["thread/compacted"] = map[string]any{"threadId": "thread-1", "turnId": "turn-1"}
	fixtures["model/verification"] = map[string]any{"threadId": "thread-1", "turnId": "turn-1", "verifications": []any{}}
	fixtures["turn/moderationMetadata"] = map[string]any{"threadId": "thread-1", "turnId": "turn-1", "metadata": map[string]any{"nativeUninterpreted": "PRIVATE_INFO_SENTINEL"}}
	fixtures["model/safetyBuffering/updated"] = map[string]any{"threadId": "thread-1", "turnId": "turn-1", "model": "gpt-5.4", "reasons": []any{}, "useCases": []any{}, "showBufferingUi": false}
	fixtures["warning"] = map[string]any{"message": "PRIVATE_INFO_SENTINEL", "threadId": "thread-1"}
	fixtures["deprecationNotice"] = map[string]any{"summary": "PRIVATE_INFO_SENTINEL", "details": nil}
	if len(fixtures) != 20 || len(informational) != 20 {
		t.Fatal("allowlist changed")
	}
	for method, params := range fixtures {
		t.Run(method, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
				if err := p.notify(method, params); err != nil {
					return err
				}
				return p.terminal(fixtureTerminal())
			}})
			if result.err != nil || result.out.Completion.Text != "Which API?" || strings.Contains(fmt.Sprint(result.out), "PRIVATE_INFO_SENTINEL") {
				t.Fatalf("shape or retention: %v", result.err)
			}
		})
	}
}
func fixtureTokenUsage() map[string]any {
	return map[string]any{"inputTokens": 1, "cachedInputTokens": 0, "outputTokens": 1, "reasoningOutputTokens": 0, "totalTokens": 2}
}
func TestRunRecognizesOrdinaryItemsWithoutPromotingToolOutput(t *testing.T) {
	items := []map[string]any{
		{"type": "userMessage", "id": "user", "content": []any{map[string]any{"type": "text", "text": "PRIVATE_USER_SENTINEL"}}},
		{"type": "reasoning", "id": "reasoning", "summary": []any{"PRIVATE_REASONING_SENTINEL"}, "content": []any{"PRIVATE_REASONING_SENTINEL"}},
		{"type": "plan", "id": "plan", "text": "PRIVATE_PLAN_SENTINEL"},
		{"type": "commandExecution", "id": "command", "command": "PRIVATE_TOOL_SENTINEL", "cwd": "/private/owned/project", "commandActions": []any{}, "status": "completed", "aggregatedOutput": "PRIVATE_TOOL_SENTINEL", "exitCode": 0},
		{"type": "fileChange", "id": "file", "changes": []any{}, "status": "completed"},
		{"type": "imageView", "id": "image", "path": "/private/owned/project/image.png"},
		{"type": "contextCompaction", "id": "compact"},
	}
	result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
		full := []any{}
		for _, item := range items {
			if err := p.notify("item/started", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "startedAtMs": 2, "item": item}); err != nil {
				return err
			}
			if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": item}); err != nil {
				return err
			}
			full = append(full, item)
		}
		turn := fixtureTerminal()
		turn["items"] = append(full, fixtureFinal())
		return p.terminal(turn)
	}})
	if result.err != nil || result.out.Completion.Text != "Which API?" || strings.Contains(fmt.Sprint(result.out), "PRIVATE_") {
		t.Fatalf("ordinary item subset: %#v", result)
	}
}
func TestRunRejectsConflictingLifecycleAndNativePermissionStatus(t *testing.T) {
	t.Run("terminal_conflict", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase != "turn/start" {
				return nil
			}
			if err := p.terminal(fixtureTerminal()); err != nil {
				return err
			}
			turn := fixtureTerminal()
			obj(arr(turn["items"])[0])["text"] = "changed"
			return p.terminal(turn)
		}})
		requireFailure(t, result, ErrProtocol)
	})
	t.Run("started_conflict", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase != "turn/start" {
				return nil
			}
			if err := p.notify("turn/started", map[string]any{"threadId": "thread-1", "turn": fixtureStartedTurn()}); err != nil {
				return err
			}
			turn := fixtureStartedTurn()
			turn["startedAt"] = 4
			return p.notify("turn/started", map[string]any{"threadId": "thread-1", "turn": turn})
		}})
		requireFailure(t, result, ErrProtocol)
	})
	t.Run("permission_status", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase != "turn/start" {
				return nil
			}
			return p.notify("thread/status/changed", map[string]any{"threadId": "thread-1", "status": map[string]any{"type": "active", "activeFlags": []any{"waitingOnApproval"}}})
		}})
		requireFailure(t, result, ErrPermission)
	})
	t.Run("native_user_input_status", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase != "turn/start" {
				return nil
			}
			return p.notify("thread/status/changed", map[string]any{"threadId": "thread-1", "status": map[string]any{"type": "active", "activeFlags": []any{"waitingOnUserInput"}}})
		}})
		requireFailure(t, result, ErrProtocol)
	})
	t.Run("partial_started_competing_final", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			other := fixtureFinal()
			other["id"] = "unfinished"
			if err := p.notify("item/started", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "startedAtMs": 2, "item": other}); err != nil {
				return err
			}
			if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": fixtureFinal()}); err != nil {
				return err
			}
			turn := fixtureTerminal()
			turn["itemsView"] = "notLoaded"
			turn["items"] = []any{}
			return p.terminal(turn)
		}})
		requireFailure(t, result, ErrOutput)
	})
	t.Run("old_start_response_cannot_overwrite_final", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase != "turn/start" {
				return nil
			}
			if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": fixtureFinal()}); err != nil {
				return err
			}
			return p.terminal(fixtureTerminal())
		}, mutate: func(phase string, response map[string]any) {
			if phase == "turn/start" {
				old := fixtureFinal()
				old["text"] = "earlier streaming text"
				obj(response["turn"])["items"] = []any{old}
			}
		}})
		if result.err != nil || result.out.Completion.Text != "Which API?" {
			t.Fatalf("late matched start response: %#v", result)
		}
	})
}
