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

// This package has no TestMain or subprocess/Host dependency. Every peer below
// is an actual M1 reader/writer on net.Pipe, joined before its test returns.
type fixturePeer struct {
	enc    *json.Encoder
	dec    *json.Decoder
	writer io.Writer
}

func (p *fixturePeer) notify(method string, params any) error {
	return p.enc.Encode(map[string]any{"method": method, "params": params})
}
func (p *fixturePeer) terminal(turn map[string]any) error {
	return p.notify("turn/completed", map[string]any{"threadId": "thread-1", "turn": turn})
}
func (p *fixturePeer) approval(method string, id any, params map[string]any, want any) error {
	if err := p.enc.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	var reply map[string]json.RawMessage
	if err := p.dec.Decode(&reply); err != nil {
		return err
	}
	idRaw, _ := json.Marshal(id)
	wantRaw, _ := json.Marshal(want)
	if string(reply["id"]) != string(idRaw) || string(reply["result"]) != string(wantRaw) || len(reply) != 2 {
		return fmt.Errorf("original typed request or denial changed")
	}
	return nil
}

type fixtureScenario struct {
	resume       bool
	cancelAt     string
	checkRequest func(string, json.RawMessage, bool) error
	before       func(string, *fixturePeer) error
	mutate       func(string, map[string]any)
	after        func(*fixturePeer) error
	callback     func(context.Context, ThreadBinding) error
	input        func(*Options)
	projection   *codexconfig.Projection
}
type fixtureResult struct {
	out       Outcome
	err       error
	methods   []string
	callbacks int
}

func runScenario(t *testing.T, scenario fixtureScenario) fixtureResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	local, remote := net.Pipe()
	client, err := codexrpc.New(ctx, local, codexrpc.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	methods := []string{}
	var bound atomic.Bool
	go func() {
		defer remote.Close()
		p := &fixturePeer{json.NewEncoder(remote), json.NewDecoder(remote), remote}
		sequence := []string{"initialize", "initialized", "config/read", "thread/start", "turn/start"}
		if scenario.resume {
			sequence[3] = "thread/resume"
		}
		for _, method := range sequence {
			var frame map[string]json.RawMessage
			if err := p.dec.Decode(&frame); err != nil {
				done <- nil
				return
			}
			var actual string
			_ = json.Unmarshal(frame["method"], &actual)
			methods = append(methods, actual)
			if actual != method {
				done <- fmt.Errorf("unexpected extra or reordered call")
				return
			}
			checker := checkFixtureRequest
			if scenario.checkRequest != nil {
				checker = scenario.checkRequest
			}
			if err := checker(method, frame["params"], scenario.resume); err != nil {
				done <- err
				return
			}
			if method == "turn/start" && !bound.Load() {
				done <- fmt.Errorf("turn before callback")
				return
			}
			if scenario.cancelAt == method {
				cancel()
				done <- nil
				return
			}
			if scenario.before != nil {
				if err := scenario.before(method, p); err != nil {
					done <- nil
					return
				}
			}
			if method == "initialized" {
				continue
			}
			response := fixtureResponse(method)
			if scenario.mutate != nil {
				scenario.mutate(method, response)
			}
			if err := p.enc.Encode(map[string]any{"id": frame["id"], "result": response}); err != nil {
				done <- nil
				return
			}
		}
		var endingErr error
		if scenario.after != nil {
			endingErr = scenario.after(p)
		} else {
			endingErr = p.terminal(fixtureTerminal())
		}
		if endingErr != nil {
			done <- nil
			return
		}
		var extra any
		if err := p.dec.Decode(&extra); err != io.EOF {
			done <- fmt.Errorf("extra task call or failed close")
			return
		}
		done <- nil
	}()
	projection := scenario.projection
	if projection == nil {
		projection, err = codexconfig.Project([]byte("model = \"gpt-5.4\"\nmodel_reasoning_effort = \"high\"\nservice_tier = \"priority\"\n"), codexconfig.ReadOnly)
		if err != nil {
			t.Fatal(err)
		}
	}
	callbacks := 0
	options := Options{CWD: "/private/owned/project", OwnedHome: "/private/owned/codex", PlatformFamily: "unix", PlatformOS: "macos", Prompt: "Review API", OnThread: func(ctx context.Context, binding ThreadBinding) error {
		callbacks++
		if scenario.callback != nil {
			if err := scenario.callback(ctx, binding); err != nil {
				return err
			}
		}
		bound.Store(true)
		return nil
	}}
	if scenario.resume {
		options.ResumeThreadID = "thread-1"
	}
	if scenario.input != nil {
		scenario.input(&options)
	}
	out, runErr := Run(ctx, client, projection, options)
	peerErr := <-done
	if peerErr != nil {
		t.Fatal(peerErr)
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("transport not joined")
	}
	return fixtureResult{out: out, err: runErr, methods: methods, callbacks: callbacks}
}
func fixtureResponse(method string) map[string]any {
	switch method {
	case "initialize":
		return map[string]any{"codexHome": "/private/owned/codex", "platformFamily": "unix", "platformOs": "macos", "userAgent": "fixture"}
	case "config/read":
		return map[string]any{"config": fixtureEffective(), "origins": map[string]any{}}
	case "thread/start", "thread/resume":
		return map[string]any{"approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": "/private/owned/project", "model": "gpt-5.4", "modelProvider": "openai", "reasoningEffort": "high", "serviceTier": "priority", "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}, "thread": fixtureThread()}
	case "turn/start":
		return map[string]any{"turn": fixtureStartedTurn()}
	}
	return nil
}
func fixtureStartedTurn() map[string]any {
	return map[string]any{"id": "turn-1", "items": []any{}, "status": "inProgress", "startedAt": 2}
}
func fixtureFinal() map[string]any {
	return map[string]any{"type": "agentMessage", "id": "final", "phase": "final_answer", "text": "{\"version\":1,\"kind\":\"technical_question\",\"text\":\"Which API?\"}"}
}
func fixtureTerminal() map[string]any {
	return map[string]any{"id": "turn-1", "items": []any{fixtureFinal()}, "status": "completed", "startedAt": 2, "completedAt": 3, "error": nil}
}
func fixtureSettings() map[string]any {
	return map[string]any{"approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": "/private/owned/project", "model": "gpt-5.4", "modelProvider": "openai", "effort": "high", "serviceTier": "priority", "sandboxPolicy": map[string]any{"type": "readOnly", "networkAccess": false}, "collaborationMode": map[string]any{"mode": "default", "settings": map[string]any{"model": "gpt-5.4", "reasoning_effort": "high", "developer_instructions": nil}}}
}
func requireFailure(t *testing.T, result fixtureResult, want error) {
	t.Helper()
	if !errors.Is(result.err, want) || result.out.Completion != (adapter.TaskCompletion{}) {
		t.Fatalf("err=%v outcome=%#v want=%v", result.err, result.out, want)
	}
	turns := 0
	for _, method := range result.methods {
		if method == "turn/start" {
			turns++
		}
	}
	if turns > 1 {
		t.Fatal("extra recovery turn")
	}
}

func TestRunReconcilesEarlyEventsWithActualReplies(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprint(resume), func(t *testing.T) {
			result := runScenario(t, fixtureScenario{resume: resume, before: func(method string, p *fixturePeer) error {
				if method == "thread/start" || method == "thread/resume" {
					return p.notify("thread/started", map[string]any{"thread": fixtureThread()})
				}
				if method == "turn/start" {
					if err := p.notify("turn/started", map[string]any{"threadId": "thread-1", "turn": fixtureStartedTurn()}); err != nil {
						return err
					}
					if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": fixtureFinal()}); err != nil {
						return err
					}
					return p.terminal(fixtureTerminal())
				}
				return nil
			}, after: func(p *fixturePeer) error { return p.terminal(fixtureTerminal()) }})
			if result.err != nil || result.callbacks != 1 || result.out.Completion.Text != "Which API?" {
				t.Fatalf("%#v", result)
			}
		})
	}
	for _, where := range []string{"thread/start", "turn/start"} {
		t.Run("foreign_"+where, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{before: func(method string, p *fixturePeer) error {
				if method != where {
					return nil
				}
				if where == "thread/start" {
					thread := fixtureThread()
					thread["id"] = "foreign"
					return p.notify("thread/started", map[string]any{"thread": thread})
				}
				turn := fixtureStartedTurn()
				turn["id"] = "foreign"
				return p.notify("turn/started", map[string]any{"threadId": "thread-1", "turn": turn})
			}})
			requireFailure(t, result, ErrProtocol)
			if where == "thread/start" && result.callbacks != 0 {
				t.Fatal("callback accepted provisional foreign thread")
			}
		})
	}
}
func TestRunDeclinesOriginalPermissionBeforePendingTurnReply(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval"} {
		for _, id := range []any{"", "request-1", int64(9007199254740993), int64(-1)} {
			t.Run(method+fmt.Sprint(id), func(t *testing.T) {
				result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
					if phase != "turn/start" {
						return nil
					}
					params := map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "permission", "startedAtMs": 2}
					var denial any = map[string]any{"decision": "decline"}
					if method == "item/permissions/requestApproval" {
						params["cwd"] = "/private/owned/project"
						params["permissions"] = map[string]any{}
						denial = map[string]any{"permissions": map[string]any{}, "scope": "turn"}
					}
					if err := p.approval(method, id, params, denial); err != nil {
						return err
					}
					return p.notify("serverRequest/resolved", map[string]any{"threadId": "thread-1", "requestId": id})
				}})
				requireFailure(t, result, ErrPermission)
				if result.out.TurnID != "turn-1" || result.callbacks != 1 {
					t.Fatal("failed to reconcile denied early request")
				}
			})
		}
	}
}
func TestRunRejectsUnauthorizedRequestsAndResolution(t *testing.T) {
	cases := []struct {
		name, phase, method string
		params              map[string]any
	}{
		{"before_binding", "thread/start", "item/fileChange/requestApproval", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "permission", "startedAtMs": 2}},
		{"foreign_thread", "turn/start", "item/fileChange/requestApproval", map[string]any{"threadId": "foreign", "turnId": "turn-1", "itemId": "permission", "startedAtMs": 2}},
		{"unknown_request", "turn/start", "item/tool/requestUserInput", map[string]any{}},
		{"malformed_timestamp", "turn/start", "item/fileChange/requestApproval", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "permission", "startedAtMs": nil}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
				if phase == c.phase {
					return p.enc.Encode(map[string]any{"id": "native-id", "method": c.method, "params": c.params})
				}
				return nil
			}})
			requireFailure(t, result, ErrProtocol)
		})
	}
	t.Run("unobserved_resolution", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase == "turn/start" {
				return p.notify("serverRequest/resolved", map[string]any{"threadId": "thread-1", "requestId": "not-seen"})
			}
			return nil
		}})
		requireFailure(t, result, ErrProtocol)
	})
	t.Run("denied_turn_conflicts_with_reply", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase != "turn/start" {
				return nil
			}
			return p.approval("item/fileChange/requestApproval", "q", map[string]any{"threadId": "thread-1", "turnId": "foreign", "itemId": "permission", "startedAtMs": 2}, map[string]any{"decision": "decline"})
		}})
		requireFailure(t, result, ErrProtocol)
	})
}
func TestRunValidatesThreadAndEffectivePolicyBeforeCallback(t *testing.T) {
	cases := []struct {
		name, phase string
		mutate      func(map[string]any)
	}{
		{"home", "initialize", func(x map[string]any) { x["codexHome"] = "/private/foreign" }},
		{"platform", "initialize", func(x map[string]any) { x["platformOs"] = "linux" }},
		{"effective_model", "config/read", func(x map[string]any) { obj(x["config"])["model"] = "other" }},
		{"effective_web", "config/read", func(x map[string]any) { delete(obj(x["config"]), "web_search") }},
		{"model", "thread/start", func(x map[string]any) { x["model"] = "other" }},
		{"effort", "thread/start", func(x map[string]any) { x["reasoningEffort"] = "low" }},
		{"tier", "thread/start", func(x map[string]any) { x["serviceTier"] = "flex" }},
		{"policy", "thread/start", func(x map[string]any) { x["approvalPolicy"] = "never" }},
		{"reviewer", "thread/start", func(x map[string]any) { x["approvalsReviewer"] = "guardian_subagent" }},
		{"network", "thread/start", func(x map[string]any) { obj(x["sandbox"])["networkAccess"] = true }},
		{"thread_cwd", "thread/start", func(x map[string]any) { obj(x["thread"])["cwd"] = "/private/foreign" }},
		{"ephemeral", "thread/start", func(x map[string]any) { obj(x["thread"])["ephemeral"] = true }},
		{"active", "thread/start", func(x map[string]any) {
			obj(x["thread"])["status"] = map[string]any{"type": "active", "activeFlags": []any{}}
		}},
		{"fork", "thread/start", func(x map[string]any) { obj(x["thread"])["forkedFromId"] = "parent" }},
		{"subagent", "thread/start", func(x map[string]any) { obj(x["thread"])["source"] = map[string]any{"subAgent": "review"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{mutate: func(phase string, value map[string]any) {
				if phase == c.phase {
					c.mutate(value)
				}
			}})
			if result.err == nil || result.out.Completion != (adapter.TaskCompletion{}) || result.callbacks != 0 {
				t.Fatalf("unsafe binding: %#v", result)
			}
			for _, method := range result.methods {
				if method == "turn/start" {
					t.Fatal("turn after untrusted binding")
				}
			}
		})
	}
	t.Run("readonly_default_network", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{mutate: func(phase string, value map[string]any) {
			if phase == "thread/start" {
				delete(obj(value["sandbox"]), "networkAccess")
			}
		}})
		if result.err != nil {
			t.Fatal(result.err)
		}
	})
}
func TestRunAdmitsOnlyExactSettingsAndInformationalScope(t *testing.T) {
	t.Run("unchanged", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase == "turn/start" {
				return p.notify("thread/settings/updated", map[string]any{"threadId": "thread-1", "threadSettings": fixtureSettings()})
			}
			return nil
		}})
		if result.err != nil {
			t.Fatal(result.err)
		}
	})
	for field, value := range map[string]any{"cwd": "/private/foreign", "model": "other", "modelProvider": "other", "approvalPolicy": "never", "approvalsReviewer": "guardian_subagent", "effort": "other", "serviceTier": "flex", "summary": "concise", "personality": "friendly", "activePermissionProfile": map[string]any{"id": "different"}} {
		t.Run(field, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
				if phase != "turn/start" {
					return nil
				}
				settings := fixtureSettings()
				settings[field] = value
				return p.notify("thread/settings/updated", map[string]any{"threadId": "thread-1", "threadSettings": settings})
			}})
			requireFailure(t, result, ErrPolicy)
		})
	}
	for _, method := range []string{"model/rerouted", "thread/closed", "config/warning", "account/updated", "hooks/completed", "thread/unknown"} {
		t.Run(method, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error { return p.notify(method, map[string]any{}) }})
			requireFailure(t, result, ErrProtocol)
		})
	}
	t.Run("informational_foreign_scope", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			return p.notify("item/agentMessage/delta", map[string]any{"threadId": "foreign", "turnId": "turn-1", "itemId": "final", "delta": "ignored"})
		}})
		requireFailure(t, result, ErrProtocol)
	})
	t.Run("informational_missing_required", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error { return p.notify("warning", map[string]any{}) }})
		requireFailure(t, result, ErrProtocol)
	})
	t.Run("delta_never_output", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			if err := p.notify("item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "final", "delta": "SECRET_SENTINEL_DELTA"}); err != nil {
				return err
			}
			return p.terminal(fixtureTerminal())
		}})
		if result.err != nil || strings.Contains(fmt.Sprint(result.out), "SECRET") {
			t.Fatal("delta retained or sequence rejected")
		}
	})
}
func TestRunStrictFinalSnapshotsAndPartialViews(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
		want   error
	}{
		{"prose", func(x map[string]any) { obj(arr(x["items"])[0])["text"] = "Which API?" }, ErrOutput},
		{"duplicate_fields", func(x map[string]any) {
			obj(arr(x["items"])[0])["text"] = "{\"version\":1,\"kind\":\"result\",\"text\":\"a\",\"text\":\"b\"}"
		}, ErrOutput},
		{"commentary", func(x map[string]any) { obj(arr(x["items"])[0])["phase"] = "commentary" }, ErrOutput},
		{"async", func(x map[string]any) { obj(arr(x["items"])[0])["delivery"] = "async" }, ErrOutput},
		{"ambiguous", func(x map[string]any) {
			other := fixtureFinal()
			other["id"] = "second"
			x["items"] = append(arr(x["items"]), other)
		}, ErrOutput},
		{"competing_unphased", func(x map[string]any) {
			other := fixtureFinal()
			other["id"] = "prose"
			delete(other, "phase")
			other["text"] = "hello"
			x["items"] = append(arr(x["items"]), other)
		}, ErrOutput},
		{"partial_missing", func(x map[string]any) { x["itemsView"] = "summary" }, ErrOutput},
		{"not_loaded", func(x map[string]any) { x["itemsView"] = "notLoaded"; x["items"] = []any{} }, ErrOutput},
		{"contradictory_error", func(x map[string]any) {
			x["error"] = map[string]any{"message": "SECRET_NATIVE_ERROR", "codexErrorInfo": nil, "additionalDetails": nil}
		}, ErrProtocol},
		{"interrupted", func(x map[string]any) { x["status"] = "interrupted" }, ErrNativeFailure},
		{"failed", func(x map[string]any) {
			x["status"] = "failed"
			x["error"] = map[string]any{"message": "SECRET_NATIVE_ERROR", "codexErrorInfo": nil, "additionalDetails": nil}
		}, ErrNativeFailure},
		{"duplicate_item", func(x map[string]any) { x["items"] = append(arr(x["items"]), fixtureFinal()) }, ErrProtocol},
		{"unknown_type", func(x map[string]any) { x["items"] = []any{map[string]any{"type": "webSearch", "id": "web"}} }, ErrProtocol},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
				terminal := fixtureTerminal()
				c.change(terminal)
				return p.terminal(terminal)
			}})
			requireFailure(t, result, c.want)
			if strings.Contains(fmt.Sprint(result.err), "SECRET") {
				t.Fatal("unsafe remote error")
			}
		})
	}
	for _, phase := range []any{nil, "final_answer"} {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
				terminal := fixtureTerminal()
				obj(arr(terminal["items"])[0])["phase"] = phase
				return p.terminal(terminal)
			}})
			if result.err != nil {
				t.Fatal(result.err)
			}
		})
	}
	t.Run("partial_uses_prior_completed_snapshot", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": fixtureFinal()}); err != nil {
				return err
			}
			terminal := fixtureTerminal()
			terminal["itemsView"] = "notLoaded"
			terminal["items"] = []any{}
			return p.terminal(terminal)
		}})
		if result.err != nil {
			t.Fatal(result.err)
		}
	})
	t.Run("full_missing_completed_snapshot", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": fixtureFinal()}); err != nil {
				return err
			}
			terminal := fixtureTerminal()
			terminal["items"] = []any{}
			return p.terminal(terminal)
		}})
		requireFailure(t, result, ErrProtocol)
	})
	t.Run("conflicting_completed_snapshot", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": fixtureFinal()}); err != nil {
				return err
			}
			terminal := fixtureTerminal()
			obj(arr(terminal["items"])[0])["text"] = "changed"
			return p.terminal(terminal)
		}})
		requireFailure(t, result, ErrProtocol)
	})
}
func TestRunBoundedEarlyStateItemsAndFinalText(t *testing.T) {
	t.Run("events", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase != "thread/start" {
				return nil
			}
			for i := 0; i < maxEarlyEvents+1; i++ {
				if err := p.notify("thread/started", map[string]any{"thread": fixtureThread()}); err != nil {
					return err
				}
			}
			return nil
		}})
		requireFailure(t, result, ErrStateLimit)
	})
	t.Run("bytes", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{before: func(phase string, p *fixturePeer) error {
			if phase != "thread/start" {
				return nil
			}
			return p.notify("warning", map[string]any{"message": strings.Repeat("x", maxEarlyBytes+1)})
		}})
		requireFailure(t, result, ErrStateLimit)
	})
	t.Run("items", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			turn := fixtureTerminal()
			items := []any{}
			for i := 0; i < maxItems+1; i++ {
				items = append(items, map[string]any{"id": fmt.Sprint(i), "type": "contextCompaction"})
			}
			turn["items"] = items
			return p.terminal(turn)
		}})
		requireFailure(t, result, ErrStateLimit)
	})
	t.Run("single_text", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			turn := fixtureTerminal()
			obj(arr(turn["items"])[0])["text"] = strings.Repeat("x", maxFinalBytes+1)
			return p.terminal(turn)
		}})
		requireFailure(t, result, ErrStateLimit)
	})
	t.Run("total_text", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			for _, id := range []string{"one", "two"} {
				item := fixtureFinal()
				item["id"] = id
				item["text"] = strings.Repeat("x", maxFinalBytes/2+1)
				if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3, "item": item}); err != nil {
					return err
				}
			}
			return nil
		}})
		requireFailure(t, result, ErrStateLimit)
	})
	t.Run("prompt_before_rpc", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{input: func(o *Options) { o.Prompt = strings.Repeat("x", codexrpc.MaxFrameBytes) }})
		requireFailure(t, result, codexrpc.ErrFrameLimit)
		if len(result.methods) != 0 {
			t.Fatal("oversized prompt sent initialize")
		}
	})
}
func TestRunCallbackFailureStopsBeforeTurn(t *testing.T) {
	result := runScenario(t, fixtureScenario{callback: func(context.Context, ThreadBinding) error { return errors.New("SECRET_CALLBACK_ERROR") }})
	requireFailure(t, result, ErrCallback)
	if result.callbacks != 1 || result.out.Binding.ThreadID != "thread-1" || result.out.TurnID != "" {
		t.Fatal("callback barrier or observed binding")
	}
	for _, method := range result.methods {
		if method == "turn/start" {
			t.Fatal("turn after failed callback")
		}
	}
}
func TestRunCancellationClosesThenJoinsCooperativeCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	local, peer := net.Pipe()
	client, err := codexrpc.New(ctx, local, codexrpc.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	warningSent := make(chan struct{})
	release := make(chan struct{})
	joined := make(chan struct{})
	peerDone := make(chan error, 1)
	go func() {
		defer peer.Close()
		enc := json.NewEncoder(peer)
		dec := json.NewDecoder(peer)
		for _, method := range []string{"initialize", "initialized", "config/read", "thread/start"} {
			var frame map[string]json.RawMessage
			if err := dec.Decode(&frame); err != nil {
				peerDone <- err
				return
			}
			var actual string
			_ = json.Unmarshal(frame["method"], &actual)
			if actual != method {
				peerDone <- fmt.Errorf("sequence")
				return
			}
			if method != "initialized" {
				if err := enc.Encode(map[string]any{"id": frame["id"], "result": fixtureResponse(method)}); err != nil {
					peerDone <- err
					return
				}
			}
		}
		<-entered
		if err := enc.Encode(map[string]any{"method": "warning", "params": map[string]any{"message": "SECRET_DISCARDED"}}); err != nil {
			peerDone <- err
			return
		}
		close(warningSent)
		var frame any
		if err := dec.Decode(&frame); err != io.EOF {
			peerDone <- fmt.Errorf("turn after cancelled binding or no client close")
			return
		}
		peerDone <- nil
	}()
	projection, _ := codexconfig.Project([]byte("model=\"gpt-5.4\"\nmodel_reasoning_effort=\"high\"\nservice_tier=\"priority\"\n"), codexconfig.ReadOnly)
	done := make(chan fixtureResult, 1)
	go func() {
		out, err := Run(ctx, client, projection, Options{CWD: "/private/owned/project", OwnedHome: "/private/owned/codex", PlatformFamily: "unix", PlatformOS: "macos", Prompt: "Review API", OnThread: func(callbackCtx context.Context, _ ThreadBinding) error {
			close(entered)
			<-callbackCtx.Done()
			<-release
			close(joined)
			return callbackCtx.Err()
		}})
		done <- fixtureResult{out: out, err: err}
	}()
	<-entered
	<-warningSent
	cancel()
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel did not close client before callback join")
	}
	select {
	case <-done:
		t.Fatal("Run detached callback")
	default:
	}
	close(release)
	result := <-done
	requireFailure(t, result, context.Canceled)
	select {
	case <-joined:
	default:
		t.Fatal("callback not joined")
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}
func TestRunInvalidInputsCloseWithoutCalls(t *testing.T) {
	for _, name := range []string{"cwd", "home", "callback", "resume", "empty_prompt", "workspace_write"} {
		t.Run(name, func(t *testing.T) {
			scenario := fixtureScenario{input: func(o *Options) {
				switch name {
				case "cwd":
					o.CWD = "relative"
				case "home":
					o.OwnedHome = "/private/owned/../codex"
				case "callback":
					o.OnThread = nil
				case "resume":
					o.ResumeThreadID = strings.Repeat("x", codexrpc.MaxScalarBytes+1)
				case "empty_prompt":
					o.Prompt = " "
				}
			}}
			if name == "workspace_write" {
				scenario.projection, _ = codexconfig.Project(nil, codexconfig.WorkspaceWrite)
			}
			result := runScenario(t, scenario)
			if result.err == nil || len(result.methods) != 0 {
				t.Fatalf("invalid input opened RPC: %#v", result)
			}
		})
	}
}
func TestRunEOFAndNativeErrorNeverOpenRecoveryTurn(t *testing.T) {
	for _, phase := range []string{"initialize", "initialized", "config/read", "thread/start", "turn/start"} {
		t.Run(phase, func(t *testing.T) {
			result := runScenario(t, fixtureScenario{before: func(method string, _ *fixturePeer) error {
				if method == phase {
					return io.EOF
				}
				return nil
			}})
			requireFailure(t, result, ErrProtocol)
		})
	}
	t.Run("native_retry_not_local_retry", func(t *testing.T) {
		result := runScenario(t, fixtureScenario{after: func(p *fixturePeer) error {
			return p.notify("error", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "willRetry": true, "error": map[string]any{"message": "SECRET_NATIVE_ERROR", "codexErrorInfo": nil, "additionalDetails": nil}})
		}})
		requireFailure(t, result, ErrNativeFailure)
	})
}

// Compile-time assertions keep the public result free of raw native metadata.
var _ = reflect.TypeOf(ThreadBinding{})
