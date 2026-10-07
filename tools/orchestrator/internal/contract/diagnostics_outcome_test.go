package contract

import (
	"encoding/json"
	"testing"
)

func TestDiagnosticsFailureWireAndEventBoundary(t *testing.T) {
	good := DiagnosticsFailure{Version: 1, Detailed: true}
	raw, _ := json.Marshal(good)
	var got DiagnosticsFailure
	if err := json.Unmarshal(raw, &got); err != nil || got != good {
		t.Fatal(err, got)
	}
	for _, s := range []string{`null`, `{}`, `{"version":1}`, `{"version":1,"detailed":1}`, `{"version":1,"Detailed":true}`, `{"version":1,"detailed":true,"detailed":true}`, `{"version":1,"detailed":true,"x":true}`} {
		var x DiagnosticsFailure
		if json.Unmarshal([]byte(s), &x) == nil {
			t.Fatal(s)
		}
	}
	for _, kind := range []string{EventResult, EventStopped, EventRunning} {
		if ValidateDiagnosticsFailure(Event{Kind: kind, DiagnosticsFailure: &good}) == nil {
			t.Fatal(kind)
		}
	}
	if ValidateDiagnosticsFailure(Event{Kind: EventExited, DiagnosticsFailure: &good}) != nil {
		t.Fatal("exited")
	}
	old := Event{Kind: EventExited, Version: 1}
	a, _ := json.Marshal(old)
	old.DiagnosticsFailure = nil
	b, _ := json.Marshal(old)
	if string(a) != string(b) {
		t.Fatal("nil wire drift")
	}
}
