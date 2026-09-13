package host

import (
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
)

func TestCodexProtocolRequiresOneSessionBeforeOneTerminal(t *testing.T) {
	collector, err := newProtocolCollector(string(adapter.ProviderCodex))
	if err != nil {
		t.Fatal(err)
	}
	var sessionKind, sessionID string
	if err := collector.SetSessionObserver(func(kind, id string) error {
		sessionKind, sessionID = kind, id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"codex-thread-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item-1","type":"reasoning","text":"discard"}}`,
		`{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"intermediate commentary"}}`,
		`{"type":"error","message":"retrying transport"}`,
		`{"type":"item.completed","item":{"id":"item-3","type":"agent_message","text":"final answer"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":7,"cached_input_tokens":3,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}`,
	}, "\n") + "\n"
	if _, err := collector.Write([]byte(stream)); err != nil {
		t.Fatal(err)
	}
	terminal, gotSession, err := collector.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if sessionKind != "session-id" || sessionID != "codex-thread-1" || gotSession != sessionID {
		t.Fatalf("observed=%q/%q returned=%q", sessionKind, sessionID, gotSession)
	}
	if terminal.Kind != adapter.EventResult || terminal.Status != "completed" || terminal.Text != "final answer" {
		t.Fatalf("terminal=%#v", terminal)
	}
	if !providerSucceeded(adapter.ProviderCodex, terminal) {
		t.Fatal("completed Codex turn was not successful")
	}
}

func TestCodexProtocolRejectsSequenceAndTerminalViolations(t *testing.T) {
	completed := `{"type":"turn.completed","usage":{"input_tokens":7,"cached_input_tokens":3,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}`
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"terminal before session", []string{completed}, "provider_event_before_session"},
		{"message before session", []string{`{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"answer"}}`}, "provider_event_before_session"},
		{"duplicate session", []string{`{"type":"thread.started","thread_id":"s"}`, `{"type":"thread.started","thread_id":"s"}`, completed}, "provider_session_duplicate"},
		{"changed session", []string{`{"type":"thread.started","thread_id":"s"}`, `{"type":"thread.started","thread_id":"other"}`, completed}, "provider_session_changed"},
		{"duplicate terminal", []string{`{"type":"thread.started","thread_id":"s"}`, `{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"answer"}}`, completed, `{"type":"turn.failed","error":{"message":"late"}}`}, "provider_multiple_terminal_events"},
		{"message after terminal", []string{`{"type":"thread.started","thread_id":"s"}`, `{"type":"item.completed","item":{"id":"item-1","type":"agent_message","text":"answer"}}`, completed, `{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"late"}}`}, "provider_event_after_terminal"},
		{"empty completed", []string{`{"type":"thread.started","thread_id":"s"}`, completed}, "provider_result_missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector, err := newProtocolCollector(string(adapter.ProviderCodex))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := collector.Write([]byte(strings.Join(tc.lines, "\n") + "\n")); err != nil {
				t.Fatal(err)
			}
			if _, _, err := collector.Finish(); err == nil || err.Error() != tc.want {
				t.Fatalf("err=%v want=%q", err, tc.want)
			}
		})
	}
}

func TestProtocolCollectorDrainsRawDiagnosticsUntilStructuredTerminal(t *testing.T) {
	collector, err := newProtocolCollector("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	line := "UNKNOWN:worker:1:" + strings.Repeat("x", 32*1024-len("UNKNOWN:worker:1:")-1) + "\n"
	if _, err = collector.Write([]byte(line + line)); err != nil {
		t.Fatal(err)
	}
	if err = collector.SetSessionObserver(func(string, string) error { return nil }); err != nil {
		t.Fatalf("raw diagnostics poisoned running collector: %v", err)
	}
	if _, err = collector.Write([]byte(`{"type":"result","subtype":"success","session_id":"session-a","result":"ok"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	terminal, session, err := collector.Finish()
	if err != nil || terminal.Text != "ok" || session != "session-a" {
		t.Fatalf("terminal=%#v session=%q err=%v", terminal, session, err)
	}
}

func TestProtocolCollectorDropsOversizedDiagnosticButRejectsOversizedResult(t *testing.T) {
	diagnostic, err := newProtocolCollector("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	largeDiagnostic := `{"type":"diagnostic","message":"` + strings.Repeat("x", 4*1024*1024) + `"}` + "\n"
	if _, err = diagnostic.Write([]byte(largeDiagnostic)); err != nil {
		t.Fatal(err)
	}
	if _, err = diagnostic.Write([]byte(`{"type":"result","subtype":"success","session_id":"session-a","result":"ok"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	terminal, _, err := diagnostic.Finish()
	if err != nil || terminal.Text != "ok" || diagnostic.droppedOversize != 1 {
		t.Fatalf("terminal=%#v dropped=%d err=%v", terminal, diagnostic.droppedOversize, err)
	}

	critical, err := newProtocolCollector("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	largeResult := `{"type":"result","subtype":"success","session_id":"session-a","result":"` + strings.Repeat("x", 4*1024*1024) + `"}` + "\n"
	if _, err = critical.Write([]byte(largeResult)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = critical.Finish(); err == nil || err.Error() != "provider_critical_event_too_large" {
		t.Fatalf("critical err=%v", err)
	}
}
