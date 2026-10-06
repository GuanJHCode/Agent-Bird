package codexsession

import (
	"errors"
	"reflect"
	"testing"
)

// Pure Run -> M1 -> net.Pipe consumer tests. runScenario joins the original
// peer and transport before returning; there is no Host, actor or TestMain.
// Fixed 6b9826 item.rs emits availableDecisions despite nonexperimental field
// filtering. These display choices never authorize a decision other than the
// existing decline response, including choices that offer approval/amendments.
func approvalCompatibilityParams() map[string]any {
	return map[string]any{
		"threadId": "thread-1", "turnId": "turn-1", "itemId": "permission",
		"startedAtMs": int64(2), "environmentId": nil,
		"command": "probe", "cwd": "/private/owned/project",
		"commandActions": []any{map[string]any{"type": "unknown", "command": "probe"}},
	}
}

func TestRunCommandApprovalAvailableDecisionsOnlyDeclines(t *testing.T) {
	execAmendment := map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{"probe"}}}
	networkAmendment := map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "example.invalid", "action": "allow"}}}
	cases := []struct {
		name    string
		present bool
		value   any
		kind    string
	}{
		{"absent", false, nil, ""},
		{"null", true, nil, ""},
		{"empty", true, []any{}, ""},
		{"accept", true, []any{"accept"}, ""},
		{"accept_for_session", true, []any{"acceptForSession"}, ""},
		{"decline", true, []any{"decline"}, ""},
		{"cancel", true, []any{"cancel"}, ""},
		{"execpolicy_amendment", true, []any{execAmendment}, ""},
		{"network_amendment", true, []any{networkAmendment}, ""},
		{"complete_union", true, []any{"accept", "acceptForSession", "decline", "cancel", execAmendment, networkAmendment}, ""},
		{"explicit_command", true, []any{"accept", "decline"}, "command"},
		{"write_stdin", true, []any{"accept", "decline"}, "writeStdin"},
	}
	for _, phase := range []string{"pending_turn_reply", "after_turn_reply_sent"} {
		t.Run(phase, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					params := approvalCompatibilityParams()
					if tc.present {
						params["availableDecisions"] = tc.value
					}
					if tc.kind != "" {
						params["kind"] = tc.kind
					}
					declined := false
					send := func(p *fixturePeer) error {
						err := p.approval("item/commandExecution/requestApproval", "original-approval", params, map[string]any{"decision": "decline"})
						declined = err == nil
						return err
					}
					scenario := fixtureScenario{after: send}
					if phase == "pending_turn_reply" {
						scenario.after = nil
						scenario.before = func(method string, p *fixturePeer) error {
							if method == "turn/start" {
								return send(p)
							}
							return nil
						}
					}
					result := runScenario(t, scenario)
					// Evaluate both before Fatal so RED distinguishes the refusal
					// category and the absent original denial, rather than a timeout.
					if !errors.Is(result.err, ErrPermission) || result.err.Error() != ErrPermission.Error() {
						t.Errorf("original decline must end in ErrPermission, got %v", result.err)
					}
					if !declined {
						t.Error("peer did not receive the exact original typed-ID decline response")
					}
					requireFailure(t, result, ErrPermission)
					if result.callbacks != 1 || result.out.Binding.ThreadID != "thread-1" || result.out.TurnID != "turn-1" {
						t.Fatal("permission refusal lost the original callback/thread/turn binding")
					}
					if !reflect.DeepEqual(result.methods, []string{"initialize", "initialized", "config/read", "thread/start", "turn/start"}) {
						t.Fatal("extra or changed task RPC")
					}
					if phase == "after_turn_reply_sent" {
						facts, ok := FailureFactsOf(result.err)
						// Sending the reply does not order its consumption before
						// the request: Run may drain the request while turn/start
						// is still pending, or after it has bound the turn.
						pending := facts.Phase == "turn/start" && facts.Boundary == "response" && facts.Branch == "turn_binding" && facts.Shape == FailurePassed && facts.Scope == FailureNotChecked
						running := facts.Phase == "running" && facts.Boundary == "request" && facts.Branch == "permission" && facts.Shape == FailurePassed && facts.Scope == FailurePassed
						if !ok || (!pending && !running) || !facts.ThreadBound || !facts.TurnBound || !facts.CallbackDone {
							t.Fatal("valid display metadata did not reach the existing permission refusal")
						}
					}
				})
			}
		})
	}
}

func TestRunCommandApprovalAvailableDecisionsRejectsInvalidAndForeign(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   error
	}{
		{"unknown_enum", func(p map[string]any) { p["availableDecisions"] = []any{"futureDecision"} }, ErrProtocol},
		{"string_container", func(p map[string]any) { p["availableDecisions"] = "decline" }, ErrProtocol},
		{"object_container", func(p map[string]any) { p["availableDecisions"] = map[string]any{"decision": "decline"} }, ErrProtocol},
		{"bool_container", func(p map[string]any) { p["availableDecisions"] = false }, ErrProtocol},
		{"null_member", func(p map[string]any) { p["availableDecisions"] = []any{nil} }, ErrProtocol},
		{"number_member", func(p map[string]any) { p["availableDecisions"] = []any{1} }, ErrProtocol},
		{"execpolicy_missing", func(p map[string]any) {
			p["availableDecisions"] = []any{map[string]any{"acceptWithExecpolicyAmendment": map[string]any{}}}
		}, ErrProtocol},
		{"execpolicy_wrong_container", func(p map[string]any) {
			p["availableDecisions"] = []any{map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": "probe"}}}
		}, ErrProtocol},
		{"execpolicy_wrong_member", func(p map[string]any) {
			p["availableDecisions"] = []any{map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{1}}}}
		}, ErrProtocol},
		{"execpolicy_unknown_inner", func(p map[string]any) {
			p["availableDecisions"] = []any{map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{"probe"}, "future": true}}}
		}, ErrProtocol},
		{"network_missing_host", func(p map[string]any) {
			p["availableDecisions"] = []any{map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"action": "allow"}}}}
		}, ErrProtocol},
		{"network_wrong_host", func(p map[string]any) {
			p["availableDecisions"] = []any{map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": 1, "action": "allow"}}}}
		}, ErrProtocol},
		{"network_unknown_action", func(p map[string]any) {
			p["availableDecisions"] = []any{map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": map[string]any{"host": "example.invalid", "action": "future"}}}}
		}, ErrProtocol},
		{"network_wrong_field_case", func(p map[string]any) {
			p["availableDecisions"] = []any{map[string]any{"applyNetworkPolicyAmendment": map[string]any{"networkPolicyAmendment": map[string]any{"host": "example.invalid", "action": "deny"}}}}
		}, ErrProtocol},
		{"union_unknown_outer", func(p map[string]any) {
			p["availableDecisions"] = []any{map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []any{}}, "future": true}}
		}, ErrProtocol},
		{"unknown_top_field", func(p map[string]any) { p["future"] = true }, ErrProtocol},
		{"additional_permissions_null", func(p map[string]any) { p["additionalPermissions"] = nil }, ErrProtocol},
		{"additional_permissions_empty", func(p map[string]any) { p["additionalPermissions"] = map[string]any{} }, ErrProtocol},
		{"additional_permissions_network", func(p map[string]any) {
			p["additionalPermissions"] = map[string]any{"network": map[string]any{"enabled": true}}
		}, ErrProtocol},
		{"unknown_kind", func(p map[string]any) { p["kind"] = "futureKind" }, ErrProtocol},
		{"foreign_thread", func(p map[string]any) { p["threadId"] = "foreign" }, ErrProtocol},
		{"foreign_turn", func(p map[string]any) { p["turnId"] = "foreign" }, ErrProtocol},
		{"foreign_cwd", func(p map[string]any) { p["cwd"] = "/private/foreign" }, ErrPolicy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := approvalCompatibilityParams()
			tc.mutate(params)
			declined := false
			result := runScenario(t, fixtureScenario{before: func(method string, p *fixturePeer) error {
				if method != "turn/start" {
					return nil
				}
				// The original reply remains unsent. A validated started event
				// fixes provisional scope without claiming out.TurnID is bound.
				if err := p.notify("turn/started", map[string]any{"threadId": "thread-1", "turn": fixtureStartedTurn()}); err != nil {
					return err
				}
				err := p.approval("item/commandExecution/requestApproval", "original-approval", params, map[string]any{"decision": "decline"})
				declined = err == nil
				return err
			}})
			requireFailure(t, result, tc.want)
			if declined {
				t.Fatalf("invalid or foreign request received decline: %t", declined)
			}
			if result.callbacks != 1 {
				t.Fatalf("callback count: got %d, want 1", result.callbacks)
			}
			if result.out.Binding.ThreadID != "thread-1" {
				t.Fatalf("original thread binding: got %q, want thread-1", result.out.Binding.ThreadID)
			}
			if result.out.TurnID != "" {
				t.Fatalf("turn bound before original reply: got %q, want empty", result.out.TurnID)
			}
			if tc.name == "foreign_thread" || tc.name == "foreign_turn" {
				facts, ok := FailureFactsOf(result.err)
				if !ok || facts.Shape != FailurePassed || facts.Scope != FailureFailed {
					t.Fatal("existing foreign scope rejection changed")
				}
			}
		})
	}
}
