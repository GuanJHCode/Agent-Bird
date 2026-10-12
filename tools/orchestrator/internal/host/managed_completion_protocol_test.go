package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func managedNativeFrame(provider adapter.Provider, session, status, structured, text, nativeFields string) string {
	if provider == adapter.ProviderClaude {
		body := `{"type":"result","subtype":` + stringJSON(status) + `,"session_id":` + stringJSON(session) + `,"result":` + stringJSON(text)
		if structured != "" {
			body += `,"structured_output":` + structured
		}
		return body + nativeFields + "}\n"
	}
	if text == "" {
		text = "native aggregate commentary"
	}
	body := `{"event":"result","result":{"status":` + stringJSON(status) + `,"conversation_id":` + stringJSON(session) + `,"response":` + stringJSON(text)
	if structured != "" {
		body += `,"structured_output":` + structured
	}
	return body + "}}\n"
}
func stringJSON(text string) string { raw, _ := json.Marshal(text); return string(raw) }
func managedNativeInit(provider adapter.Provider, session string) string {
	if provider == adapter.ProviderClaude {
		return `{"type":"system","subtype":"init","session_id":` + stringJSON(session) + "}\n"
	}
	return `{"event":"init","conversation_id":` + stringJSON(session) + "}\n"
}

func TestManagedCompletionNativeTerminalProjection(t *testing.T) {
	const question = `{"version":1,"kind":"technical_question","text":"Which business value?"}`
	for _, provider := range []adapter.Provider{adapter.ProviderAGY, adapter.ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			success, failure := "SUCCESS", "ERROR"
			if provider == adapter.ProviderClaude {
				success, failure = "success", "error_during_execution"
			}
			for _, tc := range []struct {
				name, status, session, structured, text, native string
				omitInit                                        bool
				exit                                            int
				wantKind, wantText                              string
			}{
				{name: "question", status: success, session: "s1", structured: question, wantKind: contract.EventQuestion, wantText: "Which business value?"},
				{name: "result_exact", status: success, session: "s1", structured: `{"version":1,"kind":"result","text":"  exact\n数据  "}`, wantKind: contract.EventResult, wantText: "  exact\n数据  "},
				{name: "native_error", status: failure, session: "s1", structured: question},
				{name: "missing_structure", status: success, session: "s1", text: "plain result"},
				{name: "prose_json", status: success, session: "s1", text: question},
				{name: "foreign_session", status: success, session: "foreign", structured: question},
				{name: "missing_native_session", status: success, structured: question},
				{name: "missing_init", status: success, session: "s1", structured: question, omitInit: true},
				{name: "process_error", status: success, session: "s1", structured: question, exit: 1},
				{name: "permission_kind", status: success, session: "s1", structured: `{"version":1,"kind":"permission","text":"Approve a tool"}`},
				{name: "duplicate_kind", status: success, session: "s1", structured: `{"version":1,"kind":"result","kind":"technical_question","text":"Which value?"}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					p, err := newProtocolCollector(string(provider))
					if err != nil {
						t.Fatal(err)
					}
					p.requireSession = true
					p.completionMode = adapter.TaskCompletionV1
					if !tc.omitInit {
						_, _ = p.Write([]byte(managedNativeInit(provider, "s1")))
					}
					_, _ = p.Write([]byte(managedNativeFrame(provider, tc.session, tc.status, tc.structured, tc.text, tc.native)))
					terminal, _, protocolErr := p.Finish()
					kind, status, text, _ := managedTerminalOutcome(provider, terminal, tc.exit, protocolErr)
					if tc.wantKind == "" {
						if kind != contract.EventFailed || status != "failed" {
							t.Fatalf("invalid native input published %s/%s", kind, status)
						}
					} else if kind != tc.wantKind || text != tc.wantText {
						t.Fatalf("native outcome kind=%s text=%q", kind, text)
					}
				})
			}
			if provider == adapter.ProviderAGY {
				terminal, err := adapter.ParseEvent(provider, strings.TrimSpace(managedNativeFrame(provider, "s1", "WAITING", question, "ambiguous input/permission wait", "")), providerEventLimit)
				if err != nil {
					t.Fatal(err)
				}
				kind, status, _, _ := managedTerminalOutcome(provider, terminal, 0, nil)
				if kind != contract.EventFailed || status != "failed" {
					t.Fatal("native WAITING disguised as a technical question")
				}
			}
		})
	}
}

func TestManagedCompletionRejectsClaudeNativeErrorAndPermissionMetadata(t *testing.T) {
	const question = `{"version":1,"kind":"technical_question","text":"Which business value?"}`
	for _, fields := range []string{
		`,"is_error":true`, `,"is_error":null`, `,"is_error":"false"`, `,"is_error":false,"is_error":false`,
		`,"permission_denials":[{"tool_name":"Bash","tool_use_id":"owned-tool","tool_input":{}}]`,
		`,"permission_denials":null`, `,"permission_denials":{}`, `,"permission_denials":[],"permission_denials":[]`,
	} {
		t.Run(fields, func(t *testing.T) {
			p, _ := newProtocolCollector(string(adapter.ProviderClaude))
			p.requireSession = true
			p.completionMode = adapter.TaskCompletionV1
			_, _ = p.Write([]byte(managedNativeInit(adapter.ProviderClaude, "s1")))
			_, _ = p.Write([]byte(managedNativeFrame(adapter.ProviderClaude, "s1", "success", question, "native commentary", fields)))
			event, _, err := p.Finish()
			kind, status, _, _ := managedTerminalOutcome(adapter.ProviderClaude, event, 0, err)
			if kind != contract.EventFailed || status != "failed" {
				t.Fatal("native metadata contradiction became business output")
			}
			// Legacy/candidate routing still sees its existing successful native
			// event; only the admitted managed mode consumes these new facts.
			if err != nil || event.Kind != adapter.EventResult || !providerSucceeded(adapter.ProviderClaude, event) {
				t.Fatal("legacy native projection changed")
			}
		})
	}
	good, err := adapter.ParseEvent(adapter.ProviderClaude, strings.TrimSpace(managedNativeFrame(adapter.ProviderClaude, "s1", "success", question, "", `,"is_error":false,"permission_denials":[]`)), providerEventLimit)
	if err != nil {
		t.Fatal(err)
	}
	kind, _, _, err := managedTerminalOutcome(adapter.ProviderClaude, good, 0, nil)
	if err != nil || kind != contract.EventQuestion {
		t.Fatal("valid native metadata rejected")
	}
}
