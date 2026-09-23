package execbridge

import (
	"encoding/json"
	"testing"
)

func TestBridgeRejectsNetworkAndPolicyDrift(t *testing.T) {
	for _, raw := range []string{
		`{"id":1,"method":"http/request","params":{}}`,
		`{"id":1,"method":"environmentConfig/read","params":{}}`,
		`{"id":1,"method":"process/start","params":{"sandbox":{"permissions":{"type":"disabled"}}}}`,
		`{"id":1,"method":"process/start","params":{"enforceManagedNetwork":true}}`,
		`{"id":1,"method":"process/start","params":{"networkProxy":{}}}`,
		`{"id":1,"method":"fs/writeFile","params":{"sandbox":{"permissions":{"type":"external","network":"enabled"}}}}`,
		`{"id":1,"method":"fs/writeFile","params":{"sandbox":null}}`,
	} {
		if err := validateRequest([]byte(raw), true); err == nil {
			t.Fatalf("unsafe executor request admitted: %s", raw)
		}
	}
}
func TestBridgeRejectsToolsBeforeTurn(t *testing.T) {
	for _, method := range []string{"process/start", "fs/writeFile", "fs/remove", "fs/copy", "fs/createDirectory"} {
		if err := validateRequest([]byte(`{"id":1,"method":"`+method+`","params":{}}`), false); err == nil {
			t.Fatalf("early mutation allowed: %s", method)
		}
	}
}
func TestBridgeAllowsExternallyConstrainedToolRequests(t *testing.T) {
	for _, raw := range []string{
		`{"id":1,"method":"fs/readFile","params":{"sandbox":{"permissions":{"type":"external","network":"restricted"}}}}`,
		`{"id":1,"method":"fs/writeFile","params":{"sandbox":{"permissions":{"type":"external","network":"restricted"}}}}`,
		`{"id":1,"method":"fs/close","params":{"handleId":"h"}}`,
	} {
		if err := validateRequest([]byte(raw), true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLightweightBridgeRejectsEveryProcessRequestAndEvent(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, method := range []string{"process/start", "process/read", "process/write", "process/signal", "process/terminate", "process/output", "process/exited", "process/closed"} {
			params := map[string]any{"processId": "p", "sandbox": nil, "enforceManagedNetwork": false}
			message := map[string]any{"id": float64(1), "method": method, "params": params}
			raw, _ := json.Marshal(message)
			b := &Broker{tools: enabled, ready: true, pending: map[string]requestInfo{}, seen: map[string]bool{}, processes: map[string]bool{"p": true}}
			if err := b.checkFrame(message, raw, true); err == nil {
				t.Fatalf("process request accepted: %s", method)
			}
			delete(message, "id")
			raw, _ = json.Marshal(message)
			if err := b.checkFrame(message, raw, false); err == nil {
				t.Fatalf("process event accepted: %s", method)
			}
		}
	}
}
