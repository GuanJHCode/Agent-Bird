package codexsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
)

const failureSecret = "PRIVATE_FAILURE_BODY_SENTINEL"

func failureNotifyBefore(stage, method string, params any) fixtureScenario {
	return fixtureScenario{before: func(phase string, p *fixturePeer) error {
		if phase == stage {
			return p.notify(method, params)
		}
		return nil
	}}
}

func requireFailureFacts(t *testing.T, result fixtureResult, want error, boundary, branch, method string, shape, scope FailureCheck) FailureFacts {
	t.Helper()
	if !errors.Is(result.err, want) || result.err.Error() != want.Error() || result.out.Completion != (adapter.TaskCompletion{}) {
		t.Fatal("original rejection/error text/output changed", result.err)
	}
	facts, ok := FailureFactsOf(errors.Join(result.err, errors.New(failureSecret)))
	if !ok || facts.Version != 1 || facts.Boundary != boundary || facts.Branch != branch || facts.Method != method || facts.Shape != shape || facts.Scope != scope {
		t.Fatalf("actual Run failure lacks discriminating immutable facts: %+v present=%v", facts, ok)
	}
	body, err := json.Marshal(facts)
	if err != nil || len(body) > 2048 {
		t.Fatal("failure facts not bounded")
	}
	for _, secret := range []string{failureSecret, "thread-1", "turn-1", "foreign-thread", "/private/owned/project", "gpt-5.4"} {
		if strings.Contains(string(body), secret) {
			t.Fatal("failure facts retain native/private values")
		}
	}
	copy := facts
	copy.Phase, copy.Method, copy.ItemKind = "changed", "changed", "changed"
	again, _ := FailureFactsOf(result.err)
	if again != facts {
		t.Fatal("returned copy mutated original failure facts")
	}
	return facts
}

func TestRunFailureFactsIdentifyActualConsumerBranch(t *testing.T) {
	for _, kind := range []string{"shape", "scope"} {
		t.Run("request_"+kind, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
				if phase != "turn/start" {
					return nil
				}
				params := map[string]any{"threadId": "foreign-thread", "turnId": "turn-1", "itemId": failureSecret, "startedAtMs": 2}
				if kind == "shape" {
					params["startedAtMs"] = nil
				}
				return p.enc.Encode(map[string]any{"id": failureSecret, "method": "item/fileChange/requestApproval", "params": params})
			}})
			shape, scope := FailurePassed, FailureFailed
			if kind == "shape" {
				shape, scope = FailureFailed, FailureNotChecked
			}
			requireFailureFacts(t, result, ErrProtocol, "request", kind, "item/fileChange/requestApproval", shape, scope)
		})
	}
	t.Run("notification_shape", func(t *testing.T) {
		result := runScenario(t, failureNotifyBefore("thread/start", "warning", map[string]any{"message": false}))
		facts := requireFailureFacts(t, result, ErrProtocol, "notification", "shape", "warning", FailureFailed, FailureNotChecked)
		if facts.Phase != "thread/start" || facts.ThreadBound || facts.TurnBound || facts.ItemKind != "none" {
			t.Fatal("notification shape facts imply checks/binding not reached")
		}
	})
	t.Run("scope", func(t *testing.T) {
		scenario := failureNotifyBefore("thread/resume", "warning", map[string]any{"message": failureSecret, "threadId": "foreign-thread"})
		scenario.resume = true
		result := runScenario(t, scenario)
		requireFailureFacts(t, result, ErrProtocol, "notification", "scope", "warning", FailurePassed, FailureFailed)
	})
	t.Run("unsupported_item", func(t *testing.T) {
		item := map[string]any{"type": "functionCallOutput", "id": failureSecret, "name": "exec", "output": failureSecret}
		result := runScenario(t, failureNotifyBefore("turn/start", "item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": item}))
		facts := requireFailureFacts(t, result, ErrProtocol, "notification", "shape", "item/completed", FailureFailed, FailureNotChecked)
		if facts.ItemKind != "none" || !facts.ThreadBound || !facts.CallbackDone || facts.TurnBound {
			t.Fatal("unsupported tool item gained authority or wrong branch")
		}
	})
	t.Run("item_view", func(t *testing.T) {
		item := map[string]any{"type": "commandExecution", "id": failureSecret, "command": failureSecret, "cwd": "/" + failureSecret, "commandActions": []any{}, "status": "completed", "aggregatedOutput": failureSecret, "exitCode": 0}
		result := runScenario(t, failureNotifyBefore("turn/start", "item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": item}))
		facts := requireFailureFacts(t, result, ErrPolicy, "item", "view", "item/completed", FailurePassed, FailurePassed)
		if facts.ItemKind != "commandExecution" || !facts.ThreadBound || !facts.CallbackDone || facts.TurnBound {
			t.Fatal("foreign cwd gained authority or item view branch missing")
		}
	})
	t.Run("item_lifecycle", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase != "turn/start" {
				return nil
			}
			item := fixtureFinal()
			params := map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": item}
			if err := p.notify("item/completed", params); err != nil {
				return err
			}
			item["text"] = failureSecret
			return p.notify("item/completed", params)
		}})
		facts := requireFailureFacts(t, result, ErrProtocol, "item", "lifecycle", "item/completed", FailurePassed, FailurePassed)
		if facts.ItemKind != "agentMessage" {
			t.Fatal("item conflict mislabeled")
		}
	})
	t.Run("turn_validation", func(t *testing.T) {
		turn := fixtureTerminal()
		turn["itemsView"] = "notLoaded"
		result := runScenario(t, failureNotifyBefore("turn/start", "turn/completed", map[string]any{"threadId": "thread-1", "turn": turn}))
		requireFailureFacts(t, result, ErrProtocol, "turn", "view", "turn/completed", FailurePassed, FailurePassed)
	})
	t.Run("response_shape", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{mutate: func(method string, response map[string]any) {
			if method == "initialize" {
				delete(response, "userAgent")
			}
		}})
		requireFailureFacts(t, result, ErrProtocol, "response", "shape", "initialize", FailureFailed, FailureNotChecked)
	})
	t.Run("response_policy", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{mutate: func(method string, response map[string]any) {
			if method == "initialize" {
				response["codexHome"] = "/" + failureSecret
			}
		}})
		requireFailureFacts(t, result, ErrPolicy, "response", "initialize", "initialize", FailurePassed, FailureNotChecked)
	})
	t.Run("callback", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{callback: func(context.Context, ThreadBinding) error { return errors.New(failureSecret) }})
		facts := requireFailureFacts(t, result, ErrCallback, "callback", "returned_error", "none", FailureNotChecked, FailureNotChecked)
		if facts.Phase != "binding" || !facts.ThreadBound || facts.TurnBound || facts.CallbackDone || result.callbacks != 1 {
			t.Fatal("callback facts gained turn/retained callback body")
		}
	})
	t.Run("finish", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			turn := fixtureTerminal()
			obj(arr(turn["items"])[0])["text"] = failureSecret
			return p.terminal(turn)
		}})
		requireFailureFacts(t, result, ErrOutput, "finish", "decode_final", "none", FailureNotChecked, FailureNotChecked)
	})
}

func TestRunFailureFactsUnknownMethodAndTransportRemainValueFree(t *testing.T) {
	t.Run("unknown_method", func(t *testing.T) {
		result := runScenario(t, failureNotifyBefore("thread/start", failureSecret, map[string]any{"secret": failureSecret}))
		facts := requireFailureFacts(t, result, ErrProtocol, "notification", "shape", "other", FailureFailed, FailureNotChecked)
		hash := sha256.Sum256([]byte(failureSecret))
		if facts.MethodSHA256 != hex.EncodeToString(hash[:]) {
			t.Fatal("unknown method identity missing or exposed")
		}
	})
	t.Run("transport", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase == "initialize" {
				return io.EOF
			}
			return nil
		}})
		if !errors.Is(result.err, ErrProtocol) || result.err.Error() != ErrProtocol.Error() {
			t.Fatal("original transport error changed")
		}
		facts, ok := FailureFactsOf(result.err)
		if !ok || facts.Boundary != "transport" || facts.Method != "none" || facts.Shape != FailureNotChecked || facts.Scope != FailureNotChecked || facts.ItemKind != "none" {
			t.Fatal("transport attributed a last event or missing diagnostic")
		}
	})
	t.Run("rpc_error", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		local, remote := net.Pipe()
		client, err := codexrpc.New(ctx, local, codexrpc.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			defer remote.Close()
			dec, enc := json.NewDecoder(remote), json.NewEncoder(remote)
			var frame map[string]json.RawMessage
			if err := dec.Decode(&frame); err != nil {
				done <- err
				return
			}
			if err := enc.Encode(map[string]any{"id": frame["id"], "error": map[string]any{"code": -32603, "message": failureSecret, "data": map[string]any{"secret": failureSecret}}}); err != nil {
				done <- err
				return
			}
			var extra any
			if err := dec.Decode(&extra); err != io.EOF {
				done <- errors.New("RPC error peer not joined")
				return
			}
			done <- nil
		}()
		projection, _ := codexconfig.Project(nil, codexconfig.ReadOnly)
		out, err := Run(ctx, client, projection, Options{CWD: "/private/owned/project", OwnedHome: "/private/owned/codex", PlatformFamily: "unix", PlatformOS: "macos", Prompt: "Review API", OnThread: func(context.Context, ThreadBinding) error { return nil }})
		if peerErr := <-done; peerErr != nil {
			t.Fatal(peerErr)
		}
		select {
		case <-client.Done():
		default:
			t.Fatal("original M1 jobs not joined")
		}
		requireFailureFacts(t, fixtureResult{out: out, err: err}, ErrProtocol, "call_result", "rpc_error", "initialize", FailureNotChecked, FailureNotChecked)
	})
}

func TestRunFailureFactsAbsentOnSuccessAndOriginalCancellation(t *testing.T) {
	for _, name := range []string{"success", "cancel"} {
		t.Run(name, func(t *testing.T) {
			scenario := fixtureScenario{}
			if name == "cancel" {
				scenario.cancelAt = "initialize"
			}
			result := runScenario(t, scenario)
			if name == "cancel" && (!errors.Is(result.err, context.Canceled) || result.err.Error() != context.Canceled.Error()) || name == "success" && (result.err != nil || result.out.Completion.Kind != "technical_question") {
				t.Fatal("original success/context cancellation changed")
			}
			if facts, ok := FailureFactsOf(result.err); ok || facts != (FailureFacts{}) {
				t.Fatal("success/cancellation invented protocol rejection facts")
			}
		})
	}
}
