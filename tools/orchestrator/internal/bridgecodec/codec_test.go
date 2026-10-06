package bridgecodec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func fixtureBatch() contract.BridgeBatch {
	return contract.BridgeBatch{
		SchemaVersion: 1, BatchID: "batch-1", AttemptID: "attempt-1", BindingID: "binding-1", OwnerEpoch: 4,
		TransportMode: "hook_pull", RenderSHA256: "266d8aa0cc95ba9f4ead95f222df6ef782fddf1e3af0c9b617177a0314878f35",
		Events:           []contract.BridgeBatchEvent{{EventID: "event-1", EventRevision: 2, EventHash: strings.Repeat("b", 64), ActionSlot: "slot-1", TaskID: "task-1", WorkRevision: 3, Kind: "result", Summary: "你好 \"review\"\n", ArtifactRef: &contract.BridgeArtifactRef{ID: "artifact-1", SHA256: strings.Repeat("a", 64)}}},
		DeliveryReceipts: []contract.BridgeDeliveryReceipt{{TaskID: "task-1", DeliveryID: "delivery-1", ProofMode: "collect", ProofSHA256: strings.Repeat("c", 64), EventIDs: []string{"event-1"}}},
	}
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("testdata", "bridge-v1", "golden", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixed codec fixture missing: %v", err)
	}
	return p
}

func TestBridgeCodecGolden(t *testing.T) {
	b := fixtureBatch()
	content, err := ContentBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	wantContent, err := os.ReadFile(fixturePath(t, "content.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, wantContent) {
		t.Fatalf("content bytes differ\ngot %s\nwant %s", content, wantContent)
	}
	h, err := RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if h != "266d8aa0cc95ba9f4ead95f222df6ef782fddf1e3af0c9b617177a0314878f35" {
		t.Fatalf("render hash=%s", h)
	}
	wire, err := WireBytes(b, "attempt-1", "claude", "SessionStart")
	if err != nil {
		t.Fatal(err)
	}
	wantWire, err := os.ReadFile(fixturePath(t, "claude-session-start.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire, wantWire) {
		t.Fatalf("wire bytes differ\ngot %s\nwant %s", wire, wantWire)
	}
	s := sha256.Sum256(wire)
	if hex.EncodeToString(s[:]) != "0a1feef340902585c7295f1ebdbca2bc2ad05c4f199187b30efdc3fbfcb53a38" {
		t.Fatal("wire sha changed")
	}
}

func TestBridgeHashExcludesAttemptAndSelf(t *testing.T) {
	a := fixtureBatch()
	b := a
	b.AttemptID = "attempt-2"
	b.RenderSHA256 = "different self hash"
	ha, err := RenderHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatal("attempt or self hash changed immutable content")
	}
	b.RenderSHA256 = ha
	wa, err := WireBytes(a, "attempt-1", "claude", "SessionStart")
	if err != nil {
		t.Fatal(err)
	}
	wb, err := WireBytes(b, "attempt-2", "claude", "SessionStart")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(wa, wb) {
		t.Fatal("attempt did not change wire")
	}
	b = a
	b.Events = append([]contract.BridgeBatchEvent(nil), a.Events...)
	b.Events[0].Summary += "changed"
	hc, err := RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if hc == ha {
		t.Fatal("content change did not change render hash")
	}
	b = fixtureBatch()
	b.Events[0].EventHash = "not-a-sha256"
	if _, err := ContentBytes(b); err == nil {
		t.Fatal("invalid event digest accepted")
	}
	b = fixtureBatch()
	b.DeliveryReceipts[0].ProofSHA256 = "not-a-sha256"
	if _, err := ContentBytes(b); err == nil {
		t.Fatal("invalid task proof digest accepted")
	}
}

func TestBridgeCodecUTF8AndWireLimits(t *testing.T) {
	b := fixtureBatch()
	b.Events[0].Summary = strings.Repeat("界", 170) + "a" // 511 UTF-8 bytes.
	if _, err := ContentBytes(b); err != nil {
		t.Fatalf("511 bytes rejected: %v", err)
	}
	b.Events[0].Summary += "b" // 512 bytes.
	if _, err := ContentBytes(b); err != nil {
		t.Fatalf("512 bytes rejected: %v", err)
	}
	b.Events[0].Summary += "c" // 513 bytes.
	if _, err := ContentBytes(b); err == nil {
		t.Fatal("513-byte summary accepted")
	}
	b = fixtureBatch()
	b.Events[0].Summary = string([]byte{0xff})
	if _, err := ContentBytes(b); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	b = fixtureBatch()
	for len(b.Events) < 8 {
		e := b.Events[0]
		e.EventID = "event-" + string(rune('0'+len(b.Events)+1))
		b.Events = append(b.Events, e)
		b.DeliveryReceipts[0].EventIDs = append(b.DeliveryReceipts[0].EventIDs, e.EventID)
	}
	if _, err := ContentBytes(b); err != nil {
		t.Fatalf("8 events rejected: %v", err)
	}
	b.Events = append(b.Events, b.Events[0])
	if _, err := ContentBytes(b); err == nil {
		t.Fatal("9 events accepted")
	}
	// Escaping is charged against the complete native wrapper, not inner text.
	b = fixtureBatch()
	b.Events[0].Summary = strings.Repeat("\"", 512)
	h, err := RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	b.RenderSHA256 = h
	wire, err := WireBytes(b, "attempt-1", "claude", "SessionStart")
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(wire) || len(wire) <= 1500 {
		t.Fatalf("escaped wrapper invalid or unexpectedly small: %d", len(wire))
	}
	b.Events = append(b.Events, b.Events[0], b.Events[0], b.Events[0], b.Events[0], b.Events[0], b.Events[0], b.Events[0])
	for i := 1; i < len(b.Events); i++ {
		b.Events[i].EventID = "event-" + string(rune('0'+i+1))
		b.DeliveryReceipts[0].EventIDs = append(b.DeliveryReceipts[0].EventIDs, b.Events[i].EventID)
	}
	h, err = RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	b.RenderSHA256 = h
	if _, err := WireBytes(b, "attempt-1", "claude", "SessionStart"); err == nil {
		t.Fatal("wire larger than 8192 accepted")
	}
	// These independently calculated fixtures hit the final encoded boundary.
	b = fixtureBatch()
	b.Events = nil
	b.DeliveryReceipts[0].EventIDs = nil
	for i := 1; i <= 8; i++ {
		e := fixtureBatch().Events[0]
		e.EventID = "event-" + string(rune('0'+i))
		e.Summary = strings.Repeat("\"", 133)
		if i == 8 {
			e.Summary += strings.Repeat("a", 369)
		}
		b.Events = append(b.Events, e)
		b.DeliveryReceipts[0].EventIDs = append(b.DeliveryReceipts[0].EventIDs, e.EventID)
	}
	h, err = RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	b.RenderSHA256 = h
	wire, err = WireBytes(b, "attempt-1", "claude", "SessionStart")
	if err != nil || len(wire) != 8192 {
		t.Fatalf("8192-byte wrapper: len=%d err=%v", len(wire), err)
	}
	b.Events[7].Summary += "a"
	h, err = RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	b.RenderSHA256 = h
	if _, err := WireBytes(b, "attempt-1", "claude", "SessionStart"); err == nil {
		t.Fatal("8193-byte wrapper accepted")
	}
}

func TestBridgeCodecNeverRendersCapabilities(t *testing.T) {
	private := contract.BridgeClaimReply{Batch: fixtureBatch(), ClaimToken: "claim-canary-secret"}
	for _, hook := range []string{"SessionStart", "UserPromptSubmit"} {
		wire, err := WireBytes(private.PublicBatch(), "attempt-1", "claude", hook)
		if err != nil {
			t.Fatal(err)
		}
		for _, canary := range []string{"claim-canary-secret", "claim_token", "hook_capability", "control_token"} {
			if bytes.Contains(wire, []byte(canary)) {
				t.Fatalf("%s leaked", canary)
			}
		}
	}
}

func TestBridgeObservationBatchUsesEmptyReceiptArray(t *testing.T) {
	b := fixtureBatch()
	b.Events[0].ActionSlot = ""
	b.DeliveryReceipts = nil
	content, err := ContentBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte(`"delivery_receipts":[]`)) {
		t.Fatalf("content rendered null receipts: %s", content)
	}
	h, err := RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	b.RenderSHA256 = h
	wire, err := WireBytes(b, "attempt-1", "claude", "SessionStart")
	if err != nil {
		t.Fatal(err)
	}
	var wrapper struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(wire, &wrapper); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrapper.HookSpecificOutput.AdditionalContext, `"delivery_receipts":[]`) {
		t.Fatal("native wrapper rendered null receipts")
	}
}

func TestBridgePrivateClaimFieldsCannotEnterPublicBatch(t *testing.T) {
	reply := contract.BridgeClaimReply{Batch: fixtureBatch(), ClaimToken: "claim-canary-secret"}
	public := reply.PublicBatch()
	publicJSON, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"claim-canary-secret", "claim_token", "hook_capability", "owner_capability_file", "control_file", "report_token"} {
		if bytes.Contains(publicJSON, []byte(secret)) {
			t.Fatalf("private field %q entered public batch", secret)
		}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(publicJSON, &object); err != nil {
		t.Fatal(err)
	}
	want := []string{"schema_version", "batch_id", "attempt_id", "binding_id", "owner_epoch", "transport_mode", "render_sha256", "events", "delivery_receipts"}
	if len(object) != len(want) {
		t.Fatalf("public keys=%v", object)
	}
	for _, key := range want {
		if _, exists := object[key]; !exists {
			t.Fatalf("missing public key %s", key)
		}
	}
	for _, private := range []string{"claim_token", "hook_capability", "owner_capability_file", "control_file", "report_token"} {
		object[private] = json.RawMessage(`"canary-secret"`)
		bad, _ := json.Marshal(object)
		var decoded contract.BridgeBatch
		if err := contract.DecodeBridgeStrict(bad, &decoded); err == nil {
			t.Fatalf("private key %s accepted by public batch decoder", private)
		}
		delete(object, private)
	}
	wire, err := WireBytes(public, "attempt-1", "claude", "SessionStart")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire, []byte(reply.ClaimToken)) {
		t.Fatal("private Claim token entered native wrapper")
	}
}

func TestBridgeCodecNonASCIIControlWireBoundary(t *testing.T) {
	b := fixtureBatch()
	b.Events = nil
	b.DeliveryReceipts[0].EventIDs = nil
	for i := 1; i <= 8; i++ {
		e := fixtureBatch().Events[0]
		e.EventID = "event-" + string(rune('0'+i))
		e.Summary = strings.Repeat("\"", 133)
		if i == 1 {
			e.Summary = "界\x01\n" + e.Summary
		}
		if i == 8 {
			e.Summary += strings.Repeat("a", 356)
		}
		b.Events = append(b.Events, e)
		b.DeliveryReceipts[0].EventIDs = append(b.DeliveryReceipts[0].EventIDs, e.EventID)
	}
	h, err := RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	b.RenderSHA256 = h
	wire, err := WireBytes(b, "attempt-1", "claude", "SessionStart")
	if err != nil || len(wire) != 8192 {
		t.Fatalf("Unicode/control exact wrapper len=%d err=%v", len(wire), err)
	}
	if !utf8.Valid(wire) {
		t.Fatal("final wrapper invalid UTF-8")
	}
	var wrapper struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(wire, &wrapper); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrapper.HookSpecificOutput.AdditionalContext, "界") || !strings.Contains(wrapper.HookSpecificOutput.AdditionalContext, `\u0001`) {
		t.Fatal("Unicode/control content lost before wrapper")
	}
	b.Events[7].Summary += "a"
	h, err = RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	b.RenderSHA256 = h
	if _, err := WireBytes(b, "attempt-1", "claude", "SessionStart"); err == nil {
		t.Fatal("8193-byte Unicode/control wrapper accepted")
	}
}
