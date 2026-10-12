package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func hostToolRequest(tool string) map[string]any {
	return map[string]any{"threadId": "thread-1", "turnId": "turn-1", "callId": "tool-1", "namespace": "agent_bird", "tool": tool, "arguments": map[string]any{"path": "README.md"}}
}

func hostToolChecker(method string, raw json.RawMessage, resume bool) error {
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil && method != "initialized" {
		return err
	}
	if method == "initialize" {
		caps := obj(params["capabilities"])
		if caps["experimentalApi"] != true {
			return fmt.Errorf("host tools require explicit experimental API")
		}
		caps["experimentalApi"] = false
	}
	if method == "thread/start" {
		if len(arr(params["dynamicTools"])) != 1 {
			return fmt.Errorf("missing fixed namespace")
		}
		delete(params, "dynamicTools")
	}
	if method != "initialized" {
		raw, _ = json.Marshal(params)
	}
	return checkFixtureRequest(method, raw, resume)
}

func TestHostToolsBoundTurnAndNativeResult(t *testing.T) {
	for _, tc := range []struct {
		early, success bool
		tool, args     string
	}{
		{false, true, "read_file", `{"path":"README.md"}`}, {true, true, "read_file", `{"path":"README.md"}`}, {false, false, "read_file", `{"path":"README.md"}`},
		{false, true, "read_file", `{"path":"README.md","offset":0,"limit_bytes":4096}`},
		{false, true, "search_file", `{"path":"README.md","query":"example","context_bytes":64}`},
		{false, true, "replace_text", `{"path":"README.md","expectedSHA256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","old_text":"before","new_text":"after","verify_bytes":64}`},
	} {
		early, success := tc.early, tc.success
		t.Run(fmt.Sprintf("early_%v_success_%v", early, success), func(t *testing.T) {
			calls := 0
			request := hostToolRequest(tc.tool)
			arguments, err := strictValue([]byte(tc.args))
			if err != nil {
				t.Fatal(err)
			}
			request["arguments"] = arguments
			send := func(p *fixturePeer) error {
				return p.enc.Encode(map[string]any{"id": 17, "method": "item/tool/call", "params": request})
			}
			result := runScenario(t, fixtureScenario{
				checkRequest: hostToolChecker,
				input: func(o *Options) {
					o.HostTools = &HostTools{Writable: tc.tool == "replace_text", OnTool: func(_ context.Context, call ToolCall) (ToolResult, error) {
						calls++
						if call.ThreadID != "thread-1" || call.TurnID != "turn-1" || call.CallID != "tool-1" || call.Tool != tc.tool {
							return ToolResult{}, ErrProtocol
						}
						return ToolResult{Text: "bounded file", Success: success}, nil
					}}
				},
				before: func(method string, p *fixturePeer) error {
					if early && method == "turn/start" {
						return send(p)
					}
					return nil
				},
				after: func(p *fixturePeer) error {
					if !early {
						if err := send(p); err != nil {
							return err
						}
					}
					var reply map[string]json.RawMessage
					if err := p.dec.Decode(&reply); err != nil {
						return err
					}
					if string(reply["id"]) != "17" {
						return fmt.Errorf("lost typed request id")
					}
					var response map[string]any
					if json.Unmarshal(reply["result"], &response) != nil || response["success"] != success {
						return fmt.Errorf("tool reply missing")
					}
					status := "completed"
					if !success {
						status = "failed"
					}
					item := map[string]any{"type": "dynamicToolCall", "id": "tool-1", "namespace": "agent_bird", "tool": tc.tool, "arguments": request["arguments"], "status": status, "success": success, "contentItems": response["contentItems"], "durationMs": 1}
					if err := p.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": item, "completedAtMs": 3}); err != nil {
						return err
					}
					terminal := fixtureTerminal()
					terminal["items"] = []any{item, fixtureFinal()}
					return p.terminal(terminal)
				},
			})
			if result.err != nil || calls != 1 {
				facts, _ := FailureFactsOf(result.err)
				t.Fatalf("calls=%d err=%v facts=%+v", calls, result.err, facts)
			}
		})
	}
}

func TestHostToolsRejectUnboundAndReadOnlyWrite(t *testing.T) {
	for _, kind := range []string{"wrong_turn", "write", "wrong_namespace", "duplicate_call"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			request := hostToolRequest("read_file")
			if kind == "write" {
				request["tool"] = "write_file"
			}
			if kind == "wrong_namespace" {
				request["namespace"] = "foreign"
			}
			result := runScenario(t, fixtureScenario{
				checkRequest: hostToolChecker,
				input: func(o *Options) {
					o.HostTools = &HostTools{OnTool: func(context.Context, ToolCall) (ToolResult, error) {
						calls++
						return ToolResult{Text: "ok", Success: true}, nil
					}}
				},
				before: func(method string, p *fixturePeer) error {
					if method == "turn/start" {
						return p.enc.Encode(map[string]any{"id": "rpc-1", "method": "item/tool/call", "params": request})
					}
					return nil
				},
				mutate: func(method string, v map[string]any) {
					if kind == "wrong_turn" && method == "turn/start" {
						obj(v["turn"])["id"] = "foreign-turn"
					}
				},
				after: func(p *fixturePeer) error {
					var reply any
					if err := p.dec.Decode(&reply); err != nil {
						return err
					}
					if kind == "duplicate_call" {
						return p.enc.Encode(map[string]any{"id": "rpc-2", "method": "item/tool/call", "params": request})
					}
					return nil
				},
			})
			wantCalls := 0
			if kind == "duplicate_call" {
				wantCalls = 1
			}
			if !errors.Is(result.err, ErrProtocol) && !errors.Is(result.err, ErrPolicy) {
				t.Fatalf("expected rejection, got %v", result.err)
			}
			if calls != wantCalls {
				t.Fatalf("unauthorized callback count %d", calls)
			}
		})
	}
}

func TestHostToolsRejectFabricatedOrMissingResult(t *testing.T) {
	for _, kind := range []string{"forged_result", "changed_arguments", "missing_item", "early_result"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			result := runScenario(t, fixtureScenario{
				checkRequest: hostToolChecker,
				input: func(o *Options) {
					o.HostTools = &HostTools{OnTool: func(context.Context, ToolCall) (ToolResult, error) {
						calls++
						return ToolResult{Text: "actual", Success: true}, nil
					}}
				},
				after: func(p *fixturePeer) error {
					if kind != "early_result" {
						if err := p.enc.Encode(map[string]any{"id": 23, "method": "item/tool/call", "params": hostToolRequest("read_file")}); err != nil {
							return err
						}
						var reply any
						if err := p.dec.Decode(&reply); err != nil {
							return err
						}
					}
					terminal := fixtureTerminal()
					if kind != "missing_item" {
						text := "actual"
						if kind == "forged_result" {
							text = "invented"
						}
						path := "README.md"
						if kind == "changed_arguments" {
							path = "foreign"
						}
						item := map[string]any{"type": "dynamicToolCall", "id": "tool-1", "namespace": "agent_bird", "tool": "read_file", "arguments": map[string]any{"path": path}, "status": "completed", "success": true, "contentItems": []any{map[string]any{"type": "inputText", "text": text}}, "durationMs": 1}
						terminal["items"] = []any{item, fixtureFinal()}
					}
					return p.terminal(terminal)
				},
			})
			want := 1
			if kind == "early_result" {
				want = 0
			}
			if !errors.Is(result.err, ErrProtocol) || calls != want {
				t.Fatalf("calls=%d err=%v", calls, result.err)
			}
		})
	}
}

func TestHostToolsResumeRequiresSameContract(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			result := runScenario(t, fixtureScenario{resume: true, checkRequest: hostToolChecker, input: func(o *Options) {
				hash := ""
				if same {
					hash = HostToolContractSHA256(false)
				}
				o.HostTools = &HostTools{ResumeContractSHA256: hash, OnTool: func(context.Context, ToolCall) (ToolResult, error) {
					t.Error("unexpected tool")
					return ToolResult{}, nil
				}}
			}})
			if same && result.err != nil || !same && !errors.Is(result.err, ErrInput) {
				t.Fatalf("%v", result.err)
			}
		})
	}
	if HostToolContractSHA256(false) == HostToolContractSHA256(true) {
		t.Fatal("write authority not bound to contract")
	}
}
