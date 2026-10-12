package adapter

import (
	"bytes"
	"strings"
	"testing"
)

func TestManagedNativeInvocationSelectsFixedCompletionSchema(t *testing.T) {
	for _, provider := range []Provider{ProviderAGY, ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000}`)
			req.Provider = provider
			req.Lock.Provider, req.Lock.Protocol = provider, ProtocolID(provider)
			inv, err := BuildInvocation(req)
			if err != nil {
				t.Fatal(err)
			}
			args := inv.Args()
			found := false
			for index, arg := range args {
				if arg == "--json-schema" && index+1 < len(args) && strings.Contains(args[index+1], `"technical_question"`) && strings.Contains(args[index+1], `"additionalProperties":false`) {
					found = true
				}
			}
			if !found {
				t.Fatal("ordinary managed invocation lacks fixed native completion schema")
			}
			if !bytes.Contains(inv.Stdin(), []byte("Agent Bird managed completion v1")) {
				t.Fatal("missing fixed segment completion instructions")
			}
			help := "--print --input-format --output-format --mode --permission-mode --disallowed-tools"
			if err = CheckCapabilities(req, help); err == nil {
				t.Fatal("managed schema capability was not required")
			}
			if err = CheckCapabilities(req, help+" --json-schema"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedCompletionStrictNativeObject(t *testing.T) {
	const limit = 64 * 1024
	for _, tc := range []struct {
		name, raw, kind, text string
		ok                    bool
	}{
		{"question", `{"version":1,"kind":"technical_question","text":"Which value?"}`, "technical_question", "Which value?", true},
		{"result_exact", `{"version":1,"kind":"result","text":"  exact\n数据  "}`, "result", "  exact\n数据  ", true},
		{"unicode_pair", `{"version":1,"kind":"result","text":"\ud83d\ude00"}`, "result", "😀", true},
		{"literal_replacement", `{"version":1,"kind":"result","text":"�"}`, "result", "�", true},
		{"literal_escape", `{"version":1,"kind":"result","text":"\\ud800"}`, "result", `\ud800`, true},
		{"missing", `{"kind":"result","text":"value"}`, "", "", false},
		{"additional", `{"version":1,"kind":"result","text":"value","owner":"caller"}`, "", "", false},
		{"duplicate_version", `{"version":1,"version":1,"kind":"result","text":"value"}`, "", "", false},
		{"duplicate_kind", `{"version":1,"kind":"result","kind":"technical_question","text":"value"}`, "", "", false},
		{"escaped_duplicate", `{"version":1,"kind":"result","\u006bind":"result","text":"value"}`, "", "", false},
		{"duplicate_text", `{"version":1,"kind":"result","text":"a","text":"b"}`, "", "", false},
		{"unknown_version", `{"version":2,"kind":"result","text":"value"}`, "", "", false},
		{"decimal_version", `{"version":1.0,"kind":"result","text":"value"}`, "", "", false},
		{"string_version", `{"version":"1","kind":"result","text":"value"}`, "", "", false},
		{"permission_kind", `{"version":1,"kind":"permission","text":"Approve tool"}`, "", "", false},
		{"question_kind", `{"version":1,"kind":"question","text":"value"}`, "", "", false},
		{"null_version", `{"version":null,"kind":"result","text":"value"}`, "", "", false},
		{"null_kind", `{"version":1,"kind":null,"text":"value"}`, "", "", false},
		{"null_text", `{"version":1,"kind":"result","text":null}`, "", "", false},
		{"null_object", `null`, "", "", false},
		{"array", `[{"version":1,"kind":"result","text":"value"}]`, "", "", false},
		{"non_string_text", `{"version":1,"kind":"result","text":7}`, "", "", false},
		{"empty_text", `{"version":1,"kind":"result","text":""}`, "", "", false},
		{"blank_text", `{"version":1,"kind":"result","text":" \t\n"}`, "", "", false},
		{"trailing_object", `{"version":1,"kind":"result","text":"value"}{}`, "", "", false},
		{"fenced_json", "```json\n{\"version\":1,\"kind\":\"result\",\"text\":\"value\"}\n```", "", "", false},
		{"bad_json", `{"version":1,"kind":"result","text":"value"`, "", "", false},
		{"bad_utf8", "{\"version\":1,\"kind\":\"result\",\"text\":\"\xff\"}", "", "", false},
		{"unpaired_high", `{"version":1,"kind":"result","text":"\ud800"}`, "", "", false},
		{"unpaired_low", `{"version":1,"kind":"result","text":"\udc00"}`, "", "", false},
		{"wrong_pair", `{"version":1,"kind":"result","text":"\ud800\u0061"}`, "", "", false},
		{"oversized", `{"version":1,"kind":"result","text":"` + strings.Repeat("x", limit) + `"}`, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeTaskCompletion(tc.raw, limit)
			if (err == nil) != tc.ok {
				t.Fatalf("acceptance=%t want=%t err=%v", err == nil, tc.ok, err)
			}
			if tc.ok && (got.Version != 1 || got.Kind != tc.kind || got.Text != tc.text) {
				t.Fatal("decoder changed exact structured text")
			}
		})
	}
}

func TestManagedCompletionModeSnapshotAndExistingContracts(t *testing.T) {
	for _, provider := range []Provider{ProviderAGY, ProviderClaude} {
		t.Run(string(provider), func(t *testing.T) {
			req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000}`)
			req.Provider = provider
			req.Lock.Provider, req.Lock.Protocol = provider, ProtocolID(provider)
			inv, err := BuildInvocation(req)
			if err != nil {
				t.Fatal(err)
			}
			req.Profile.Role = Implementer
			req.Action = &CandidateAction{Version: 1, Operation: "review"}
			if inv.TaskCompletionMode() != TaskCompletionV1 {
				t.Fatal("caller changed immutable completion mode")
			}
			args := inv.Args()
			args[0] = "changed"
			if inv.TaskCompletionMode() != TaskCompletionV1 || inv.Args()[0] == "changed" {
				t.Fatal("caller changed invocation snapshot")
			}
			req.Profile.Role = Reviewer
			inv, err = BuildInvocation(req)
			if err != nil {
				t.Fatal(err)
			}
			if inv.TaskCompletionMode() != TaskCompletionNone || strings.Contains(strings.Join(inv.Args(), " "), `"technical_question"`) || !strings.Contains(strings.Join(inv.Args(), " "), `"approve","reject"`) {
				t.Fatal("candidate review contract changed")
			}
			req.Action = nil
			req.Profile = nil
			req.Lock = nil
			inv, err = BuildInvocation(req)
			if err != nil {
				t.Fatal(err)
			}
			if inv.TaskCompletionMode() != TaskCompletionNone || strings.Contains(strings.Join(inv.Args(), " "), "--json-schema") || bytes.Contains(inv.Stdin(), []byte("Agent Bird managed completion v1")) {
				t.Fatal("legacy invocation opted into task schema")
			}
		})
	}
}
