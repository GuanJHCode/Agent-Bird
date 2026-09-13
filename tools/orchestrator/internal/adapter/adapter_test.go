package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func pinForTest() BinaryPin {
	return BinaryPin{Path: "/private/bin/agent", Version: "1.2.3", SHA256: strings.Repeat("a", 64)}
}

func TestBuildInvocationUsesExactResumeAndBoundedPermission(t *testing.T) {
	invocation, err := BuildInvocation(Request{
		Provider: ProviderClaude,
		Binary:   pinForTest(),
		CWD:      "/private/workspace",
		Prompt:   "continue the bounded task",
		Session:  SessionRef{Kind: SessionID, ID: "sess_123"},
		Permission: Permission{
			Mode: "plan",
			Deny: []string{"Bash"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/private/bin/agent", "-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose", "--include-partial-messages", "--resume", "sess_123", "--permission-mode", "plan", "--disallowed-tools", "Bash"}
	if got := invocation.Args(); !equalStrings(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	args := invocation.Args()
	args[0] = "/tampered"
	if invocation.Args()[0] == "/tampered" {
		t.Fatal("invocation argv was mutable")
	}
	if invocation.Environment()["HOME"] != "" || invocation.Environment()["ANTHROPIC_API_KEY"] != "" || invocation.Environment()["XAI_API_KEY"] != "" {
		t.Fatalf("credential environment leaked: %#v", invocation.Environment())
	}
}

func TestBuildInvocationRejectsPermissionExpansionAndAuthFlags(t *testing.T) {
	cases := []Request{
		{Provider: ProviderClaude, Binary: pinForTest(), Permission: Permission{Mode: "bypassPermissions"}},
		{Provider: ProviderAGY, Binary: pinForTest(), Permission: Permission{Mode: "dangerously-skip-permissions"}},
		{Provider: ProviderGrok, Binary: pinForTest(), ExtraArgs: []string{"--oauth"}},
		{Provider: ProviderGrok, Binary: pinForTest(), ExtraArgs: []string{"login"}},
		{Provider: ProviderGrok, Binary: pinForTest(), ExtraArgs: []string{"--model", "other"}},
		{Provider: ProviderGrok, Binary: pinForTest(), ExtraArgs: []string{"--settings", "foreign.json"}},
		{Provider: ProviderGrok, Binary: pinForTest(), ExtraArgs: []string{"--resume", "other"}},
		{Provider: ProviderAGY, Binary: pinForTest(), Permission: Permission{Allow: []string{"Read"}}},
	}
	for i, tc := range cases {
		if _, err := BuildInvocation(tc); err == nil {
			t.Errorf("case %d: expected rejection", i)
		}
	}
}

func TestDefaultPermissionDoesNotAddApprovalFlags(t *testing.T) {
	invocation, err := BuildInvocation(Request{Provider: ProviderAGY, Binary: pinForTest(), CWD: "/private/workspace", Prompt: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range invocation.Args() {
		if arg == "--always-approve" || arg == "--yolo" || arg == "bypassPermissions" || arg == "--permission-mode" {
			t.Fatalf("default permission expanded argv: %#v", invocation.Args())
		}
	}
}

func TestVerifyExecutableReadsCurrentPinnedFile(t *testing.T) {
	path := t.TempDir() + "/agent"
	content := []byte("pinned executable fixture")
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	path, _ = filepath.EvalSymlinks(path)
	digest := sha256.Sum256(content)
	pin := BinaryPin{Path: path, Version: "fixture-1", SHA256: hex.EncodeToString(digest[:])}
	if err := VerifyExecutable(pin, "fixture-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyExecutable(pin, "fixture-1"); err == nil {
		t.Fatal("accepted changed executable")
	}
}

func TestParseEventsProjectsOnlyBoundedFields(t *testing.T) {
	cases := []struct {
		provider Provider
		line     string
		want     Event
	}{
		{ProviderClaude, `{"type":"system","subtype":"init","session_id":"claude-1","cwd":"PRIVATE","secret":"DROP"}`, Event{Kind: EventInit, SessionID: "claude-1"}},
		{ProviderClaude, `{"type":"result","subtype":"success","session_id":"claude-1","result":"done","usage":{"input_tokens":7}}`, Event{Kind: EventResult, SessionID: "claude-1", Status: "success", Text: "done"}},
		{ProviderAGY, `{"event":"init","conversation_id":"agy-1","init":{"cwd":"PRIVATE","tools":["read"]}}`, Event{Kind: EventInit, SessionID: "agy-1"}},
		{ProviderAGY, `{"event":"result","result":{"conversation_id":"agy-1","status":"SUCCESS","response":"done","secret":"DROP"}}`, Event{Kind: EventResult, SessionID: "agy-1", Status: "SUCCESS", Text: "done"}},
		{ProviderGrok, `{"type":"text","sessionId":"grok-1","data":"chunk","secret":"DROP"}`, Event{Kind: EventMessage, SessionID: "grok-1", Text: "chunk"}},
		{ProviderGrok, `{"type":"end","sessionId":"grok-1","stopReason":"end_turn","usage":{"output_tokens":2}}`, Event{Kind: EventResult, SessionID: "grok-1", Status: "end_turn"}},
		{ProviderGrok, `{"type":"result","sessionId":"grok-1","status":"success","text":"done","requestId":"req-1","secret":"DROP"}`, Event{Kind: EventResult, SessionID: "grok-1", Status: "success", Text: "done"}},
	}
	for _, tc := range cases {
		got, err := ParseEvent(tc.provider, tc.line, 4096)
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if got != tc.want {
			t.Errorf("%s: event = %#v, want %#v", tc.provider, got, tc.want)
		}
	}
}

func TestParseEventDoesNotPersistUnknownRawAndBoundsLines(t *testing.T) {
	got, err := ParseEvent(ProviderAGY, `{"event":"future_event","secret":"DROP"}`, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Event{Kind: EventUnknown}) {
		t.Fatalf("unknown event retained data: %#v", got)
	}
	if _, err := ParseEvent(ProviderClaude, strings.Repeat("x", 4097), 4096); err == nil {
		t.Fatal("oversized event accepted")
	}
	if _, err := ParseEvent(ProviderClaude, `{"type":"result","subtype":"success","session_id":7,"result":"bad"}`, 4096); err == nil {
		t.Fatal("malformed identity accepted")
	}
	if _, err := ParseEvent(ProviderAGY, `{"event":"result","result":{"conversation_id":7,"status":"SUCCESS","response":"bad"}}`, 4096); err == nil {
		t.Fatal("malformed nested identity accepted")
	}
	if _, err := ParseEvent(ProviderClaude, `{"type":"result"} trailing`, 4096); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if _, err := ParseEvent(ProviderClaude, `{"type":"result","subtype":"not-a-terminal-status","session_id":"s","result":"bad"}`, 4096); err == nil {
		t.Fatal("invalid Claude semantic status accepted")
	}
	if _, err := ParseEvent(ProviderAGY, `{"event":"result","result":{"conversation_id":"s","status":"NOT_A_STATUS","response":"bad"}}`, 4096); err == nil {
		t.Fatal("invalid AGY semantic status accepted")
	}
}

func TestStreamParserBoundsAggregateOutput(t *testing.T) {
	parser := NewStreamParser(ProviderClaude, 128, 80)
	if _, err := parser.Parse(`{"type":"system","subtype":"init","session_id":"s"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse(strings.Repeat("x", 41)); err == nil {
		t.Fatal("aggregate output limit was not enforced")
	}
}

func TestStreamParserConsumesEventsIncrementally(t *testing.T) {
	parser := NewStreamParser(ProviderClaude, 256, 1024)
	var got []Event
	if err := parser.Consume(strings.NewReader("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\n{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s\",\"result\":\"ok\"}\n"), func(event Event) error {
		got = append(got, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Kind != EventResult || got[1].Text != "ok" {
		t.Fatalf("events = %#v", got)
	}
}

func TestValidatePinRequiresVersionAndDigest(t *testing.T) {
	if err := ValidatePin(pinForTest(), "1.2.3", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"1.2.4", strings.Repeat("a", 64)}, {"1.2.3", strings.Repeat("b", 64)}} {
		if err := ValidatePin(pinForTest(), pair[0], pair[1]); err == nil {
			t.Fatalf("accepted drift: %#v", pair)
		}
	}
}
