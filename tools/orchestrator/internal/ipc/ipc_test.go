package ipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/iotest"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestFragmentedWireRejectsUnsupportedProtocolVersions(t *testing.T) {
	for _, version := range []int{0, 1, 2} {
		body := []byte(fmt.Sprintf(`{"version":%d,"kind":"event","request_id":"r","epoch":1,"payload":{"version":1,"kind":"failed"}}`, version))
		var wire bytes.Buffer
		if err := binary.Write(&wire, binary.BigEndian, uint32(len(body))); err != nil {
			t.Fatal(err)
		}
		wire.Write(body)
		got, err := Read(iotest.OneByteReader(&wire))
		if version == 1 {
			if err != nil || got.Kind != KindEvent {
				t.Fatalf("got=%#v err=%v", got, err)
			}
		} else if !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("version=%d err=%v", version, err)
		}
	}
}

func TestCodecRoundTripsTypedEnvelope(t *testing.T) {
	want := Envelope{
		Version:   Version,
		Kind:      KindHostHello,
		RequestID: "request-1",
		Epoch:     7,
		Payload: mustJSON(t, contract.HostHello{
			LaunchID:        "launch-1",
			LaunchToken:     "token-1",
			OriginContextID: "origin-1",
			HostGeneration:  "generation-1",
			PID:             42,
			Birth:           "birth-1",
			Executable:      "/private/tmp/orchestrator",
		}),
	}
	var wire bytes.Buffer
	if err := Write(&wire, want); err != nil {
		t.Fatal(err)
	}
	got, err := Read(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Kind != KindHostHello || got.RequestID != "request-1" || got.Epoch != 7 {
		t.Fatalf("envelope=%#v", got)
	}
	var hello contract.HostHello
	if err = json.Unmarshal(got.Payload, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.LaunchToken != "token-1" || hello.PID != 42 || hello.Birth != "birth-1" {
		t.Fatalf("hello=%#v", hello)
	}
}

func TestLaunchGrantCarriesFrozenActiveBudget(t *testing.T) {
	want := contract.LaunchCommand{
		CommandID:       "command-1",
		ReservationID:   "reservation-1",
		BudgetGroupID:   "budget-1",
		GrantedActiveMS: 900_000,
		DeadlineUnixMS:  2_000_000,
	}
	var wire bytes.Buffer
	if err := Write(&wire, Envelope{Version: Version, Kind: KindLaunch, RequestID: "request-2", Epoch: 8, Payload: mustJSON(t, want)}); err != nil {
		t.Fatal(err)
	}
	message, err := Read(&wire)
	if err != nil {
		t.Fatal(err)
	}
	var got contract.LaunchCommand
	if err = json.Unmarshal(message.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.ReservationID != "reservation-1" || got.BudgetGroupID != "budget-1" || got.GrantedActiveMS != 900_000 {
		t.Fatalf("grant=%#v", got)
	}
}

func TestCodecRejectsOversizeBeforeAllocation(t *testing.T) {
	var wire bytes.Buffer
	wire.Write([]byte{0, 1, 0, 1}) // 65,537 bytes, above the 64 KiB contract.
	if _, err := Read(&wire); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
