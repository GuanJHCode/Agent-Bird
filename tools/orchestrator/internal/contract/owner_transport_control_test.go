package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOwnerTransportControlIsStrictNotificationOnly(t *testing.T) {
	for _, action := range []string{"connect", "offline"} {
		raw, _ := json.Marshal(OwnerTransportControl{Version: 1, Action: action})
		got, err := DecodeOwnerTransportControl(raw)
		if err != nil || got.Action != action || got.Ticket != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	for _, raw := range []string{
		`{}`, `{"version":1}`, `{"version":1,"action":"launch"}`, `{"version":1,"action":"connect","launch_token":"secret"}`,
		`{"version":1,"action":"connect","action":"connect"}`, `{"version":1,"action":"connect","ticket":null}`,
		`{"version":1,"action":"offline","ticket":{}}`, `{"version":1,"action":"connect"} {}`,
	} {
		if _, err := DecodeOwnerTransportControl([]byte(raw)); err == nil {
			t.Fatal("accepted control outside strict notification union", raw)
		}
	}
}

func TestOwnerHostPendingRequiresExactPublicIdentity(t *testing.T) {
	p := OwnerHostPending{Version: 1, HostID: "producer", CoordinatorEpoch: 7, CoordinatorPublicKey: strings.Repeat("a", 64), OwnerID: "owner"}
	raw, _ := json.Marshal(p)
	if _, err := DecodeOwnerHostPending(raw); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"version", "epoch", "host", "key", "owner"} {
		t.Run(kind, func(t *testing.T) {
			wrong := p
			switch kind {
			case "version":
				wrong.Version = 0
			case "epoch":
				wrong.CoordinatorEpoch = 0
			case "host":
				wrong.HostID = ""
			case "key":
				wrong.CoordinatorPublicKey = strings.Repeat("A", 64)
			case "owner":
				wrong.OwnerID = "\x00"
			}
			raw, _ := json.Marshal(wrong)
			if _, err := DecodeOwnerHostPending(raw); err == nil {
				t.Fatal("malformed pending accepted")
			}
		})
	}
	if _, err := DecodeOwnerHostPending([]byte(`{"version":1,"host_id":"producer","coordinator_epoch":7,"coordinator_public_key":"` + p.CoordinatorPublicKey + `","owner_id":"owner","ready":true}`)); err == nil {
		t.Fatal("extra unsigned readiness accepted")
	}
}
