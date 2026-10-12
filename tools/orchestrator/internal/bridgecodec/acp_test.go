package bridgecodec

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestACPPromptPreservesWholeBatchAndSeparatesHookTransport(t *testing.T) {
	raw, err := os.ReadFile("testdata/bridge-v1/golden/batch.json")
	if err != nil {
		t.Fatal(err)
	}
	var batch contract.BridgeBatch
	if err := json.Unmarshal(raw, &batch); err != nil {
		t.Fatal(err)
	}
	hook := batch
	batch.TransportMode = contract.BridgeTransportGrokACP
	// Add another task and its independent delivery proof to the same batch.
	event := batch.Events[0]
	event.EventID = "event-second"
	event.TaskID = "task-second"
	event.ActionSlot = "slot-second"
	batch.Events = append(batch.Events, event)
	batch.DeliveryReceipts = append(batch.DeliveryReceipts, contract.BridgeDeliveryReceipt{TaskID: event.TaskID, DeliveryID: "delivery-second", ProofMode: "collect", ProofSHA256: strings.Repeat("d", 64), EventIDs: []string{event.EventID}})
	batch.RenderSHA256, err = RenderHash(batch)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := ACPPromptBytes(batch)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(prompt, []byte(additionalContextPrefix)) || len(prompt) > 8192 {
		t.Fatalf("prompt envelope: %q", prompt)
	}
	var got contract.BridgeBatch
	if err := json.Unmarshal(bytes.TrimPrefix(prompt, []byte(additionalContextPrefix)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.DeliveryReceipts) != len(batch.DeliveryReceipts) || got.AttemptID != batch.AttemptID || got.RenderSHA256 != batch.RenderSHA256 || got.DeliveryReceipts[len(got.DeliveryReceipts)-1].DeliveryID != "delivery-second" {
		t.Fatal("batch/attempt/receipt lost")
	}
	if _, err := WireBytes(batch, batch.AttemptID, "claude", "SessionStart"); err == nil {
		t.Fatal("hook renderer accepted ACP transport")
	}
	if _, err := ACPPromptBytes(hook); err == nil {
		t.Fatal("ACP renderer accepted hook transport")
	}
	batch.RenderSHA256 = strings.Repeat("f", 64)
	if _, err := ACPPromptBytes(batch); err == nil {
		t.Fatal("wrong render hash accepted")
	}
}

func TestACPPromptBoundsFinalEncodedText(t *testing.T) {
	batch := contract.BridgeBatch{SchemaVersion: 1, BatchID: "batch", BindingID: "binding", AttemptID: "attempt", OwnerEpoch: 1, TransportMode: contract.BridgeTransportGrokACP}
	for i := 0; i < 8; i++ {
		batch.Events = append(batch.Events, contract.BridgeBatchEvent{EventID: string(rune('a' + i)), EventRevision: 1, EventHash: strings.Repeat("a", 64), TaskID: "task", WorkRevision: 1, Kind: "progress", Summary: strings.Repeat("<", 512)})
	}
	var err error
	batch.RenderSHA256, err = RenderHash(batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ACPPromptBytes(batch); err == nil {
		t.Fatal("encoded text over 8192 admitted")
	}
}
