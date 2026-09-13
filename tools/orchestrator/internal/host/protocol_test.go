package host

import (
	"strings"
	"testing"
)

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
