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
	for _, s := range []string{`null`, `{}`, `{"version":2,"summary":true}`, `{"version":1,"summary":false,"detailed":false}`, `{"version":1,"summary":true,"detailed":null}`, `{"version":1}`, `{"version":1,"detailed":1}`, `{"version":1,"Detailed":true}`, `{"version":1,"detailed":true,"detailed":true}`, `{"version":1,"detailed":true,"x":true}`} {
		var x DiagnosticsFailure
		if json.Unmarshal([]byte(s), &x) == nil {
			t.Fatal(s)
		}
	}
	for _, kind := range []string{EventResult, EventQuestion, EventStopped, EventFailed, EventRunning, EventPrepared, EventSpawned, EventSession, EventUnknown, EventLaunchRejected, EventProgress} {
		if ValidateDiagnosticsFailure(Event{Kind: kind, DiagnosticsFailure: &good}) == nil {
			t.Fatal(kind)
		}
	}
	if ValidateDiagnosticsFailure(Event{Kind: EventExited, DiagnosticsFailure: &good}) != nil {
		t.Fatal("exited")
	}
	old := Event{Kind: EventExited, Version: 1}
	body, err := json.Marshal(old)
	const legacy = `{"version":1,"run_id":"","task_id":"","attempt_id":"","segment_id":"","sequence":0,"kind":"exited","payload_hash":""}`
	if err != nil || string(body) != legacy {
		t.Fatalf("nil wire drift: %s %v", body, err)
	}
}
