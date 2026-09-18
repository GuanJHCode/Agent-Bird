package host

import (
	"strings"
	"testing"
)

// Grok 1.0.34 emits public text chunks without a session ID; only the
// terminal end event supplies the session. Thinking and tool data stay private.
func TestGrokTerminalRegistersSession(t *testing.T) {
	p, err := newProtocolCollector("grok-build")
	if err != nil {
		t.Fatal(err)
	}
	var observed []string
	if err := p.SetSessionObserver(func(kind, id string) error {
		observed = append(observed, kind+":"+id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = p.Write([]byte("{\"type\":\"thought\",\"data\":\"PRIVATE\"}\n" +
		"{\"type\":\"text\",\"data\":\"hello \"}\n" +
		"{\"type\":\"text\",\"data\":\"world\"}\n" +
		"{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"grok-session-1\"}\n"))
	event, session, err := p.Finish()
	if err != nil || session != "grok-session-1" || event.Text != "hello world" {
		t.Fatalf("unexpected result: %+v %q %v", event, session, err)
	}
	if len(observed) != 1 || observed[0] != "session-id:grok-session-1" {
		t.Fatalf("terminal session not persisted: %v", observed)
	}
}

func TestGrokRejectsTextAfterTerminal(t *testing.T) {
	p, _ := newProtocolCollector("grok-build")
	_, _ = p.Write([]byte("{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"grok-1\"}\n" +
		"{\"type\":\"text\",\"data\":\"late\"}\n"))
	if _, _, err := p.Finish(); err == nil || err.Error() != "provider_event_after_terminal" {
		t.Fatalf("accepted late text: %v", err)
	}
}

func TestGrokRejectsOversizedPublicEvent(t *testing.T) {
	p, _ := newProtocolCollector("grok-build")
	_, _ = p.Write([]byte(`{"data":"` + strings.Repeat("a", providerEventLimit) + `","type":"text"}` + "\n" +
		"{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"grok-1\"}\n"))
	if _, _, err := p.Finish(); err == nil || err.Error() != "provider_critical_event_too_large" {
		t.Fatalf("accepted truncated result: %v", err)
	}
}

func TestGrokRejectsDifferentGrantedSession(t *testing.T) {
	p, _ := newProtocolCollector("grok-build")
	p.expectedSessionID = "expected-session"
	_, _ = p.Write([]byte("{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"different-session\"}\n"))
	if _, _, err := p.Finish(); err == nil || err.Error() != "provider_session_changed" {
		t.Fatalf("mismatched session: %v", err)
	}
}
