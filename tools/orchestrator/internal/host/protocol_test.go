package host

import (
	"strings"
	"testing"
)

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
