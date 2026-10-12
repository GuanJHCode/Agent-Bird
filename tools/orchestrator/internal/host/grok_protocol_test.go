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

func TestGrokNativeStructuredReviewField(t *testing.T) {
	object := `{"decision":"reject","summary":"return a - b returns -1, expected 5"}`
	for _, tc := range []struct {
		name, tail          string
		structured, invalid bool
	}{
		{"native-object", `,"structuredOutput":` + object, true, false},
		{"absent", ``, false, false},
		{"prose-not-authoritative", `,"text":` + `"{\"decision\":\"approve\",\"summary\":\"prose\"}"`, false, false},
		{"string", `,"structuredOutput":"not-an-object"`, false, true},
		{"array", `,"structuredOutput":[]`, false, true},
		{"null", `,"structuredOutput":null`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newProtocolCollector("grok-build")
			_, _ = p.Write([]byte(`{"type":"text","data":"ordinary commentary"}` + "\n" + `{"type":"end","stopReason":"end_turn","sessionId":"s1"` + tc.tail + "}\n"))
			result, _, err := p.Finish()
			if (err != nil) != tc.invalid {
				t.Fatalf("unexpected protocol error: %v", err)
			}
			if tc.invalid {
				return
			}
			if tc.structured && result.StructuredText != object {
				t.Fatalf("missing authoritative object: %+v", result)
			}
			if !tc.structured && result.StructuredText != "" {
				t.Fatal("promoted prose to review")
			}
		})
	}
}

// Real Grok 1.0.34 transcripts interleave tool_started/tool_completed events
// between assistant messages; only text after the last tool boundary is the
// final answer. mvp-grok-readonly delivered the full concatenation instead.
func TestGrokToolBoundaryKeepsFinalAnswerOnly(t *testing.T) {
	p, err := newProtocolCollector("grok-build")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.Write([]byte(`{"type":"text","data":"I'll read INPUT.txt and return it."}` + "\n" +
		`{"type":"tool_started","tool_name":"read_file"}` + "\n" +
		`{"type":"thought","data":"PRIVATE"}` + "\n" +
		`{"type":"tool_completed","tool_name":"read_file","outcome":"success"}` + "\n" +
		`{"type":"text","data":"GROK_READ_final"}` + "\n" +
		"{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"grok-1\"}\n"))
	event, session, err := p.Finish()
	if err != nil || session != "grok-1" || event.Text != "GROK_READ_final" {
		t.Fatalf("unexpected result: %+v %q %v", event, session, err)
	}
}

func TestGrokKeepsTextAfterLastToolBoundary(t *testing.T) {
	p, _ := newProtocolCollector("grok-build")
	_, _ = p.Write([]byte(`{"type":"text","data":"first "}` + "\n" +
		`{"type":"tool_started","tool_name":"list_dir"}` + "\n" +
		`{"type":"tool_completed","tool_name":"list_dir","outcome":"success"}` + "\n" +
		`{"type":"text","data":"middle"}` + "\n" +
		`{"type":"tool_started","tool_name":"read_file"}` + "\n" +
		`{"type":"tool_completed","tool_name":"read_file","outcome":"success"}` + "\n" +
		`{"type":"text","data":"final"}` + "\n" +
		"{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"grok-1\"}\n"))
	event, session, err := p.Finish()
	if err != nil || session != "grok-1" || event.Text != "final" {
		t.Fatalf("unexpected result: %+v %q %v", event, session, err)
	}
}

func TestGrokRejectsToolEventAfterTerminal(t *testing.T) {
	p, _ := newProtocolCollector("grok-build")
	_, _ = p.Write([]byte("{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"grok-1\"}\n" +
		`{"type":"tool_started","tool_name":"read_file"}` + "\n"))
	if _, _, err := p.Finish(); err == nil || err.Error() != "provider_event_after_terminal" {
		t.Fatalf("accepted late tool event: %v", err)
	}
}

// Boundary events now carry their session field, so a granted-session mismatch
// on a tool event is rejected instead of silently discarded.
func TestGrokRejectsToolEventWithDifferentGrantedSession(t *testing.T) {
	p, _ := newProtocolCollector("grok-build")
	p.expectedSessionID = "expected-session"
	_, _ = p.Write([]byte(`{"type":"tool_started","sessionId":"different-session","tool_name":"read_file"}` + "\n" +
		`{"type":"text","data":"final"}` + "\n" +
		"{\"type\":\"end\",\"stopReason\":\"end_turn\",\"sessionId\":\"expected-session\"}\n"))
	if _, _, err := p.Finish(); err == nil || err.Error() != "provider_session_changed" {
		t.Fatalf("mismatched tool session: %v", err)
	}
}
