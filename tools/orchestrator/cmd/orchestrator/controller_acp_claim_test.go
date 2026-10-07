package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/bridgecodec"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func acpPublicClaimFixture(t *testing.T) (contract.BridgeOwnerRequest, contract.ACPClaimReply) {
	t.Helper()
	raw, err := os.ReadFile("../../internal/bridgecodec/testdata/bridge-v1/golden/batch.json")
	if err != nil {
		t.Fatal(err)
	}
	var b contract.BridgeBatch
	if json.Unmarshal(raw, &b) != nil {
		t.Fatal("invalid golden batch")
	}
	b.TransportMode = contract.BridgeTransportGrokACP
	b.AttemptID = contract.ACPAttemptID(b.BindingID, b.OwnerEpoch, "original-claim")
	b.RenderSHA256, err = bridgecodec.RenderHash(b)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := bridgecodec.ACPPromptBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(prompt)
	base := contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: b.BindingID, OwnerEpoch: b.OwnerEpoch}, OwnerMode: "local", OwnerCapabilityFile: "/private/owner.json", ControlFile: "/private/control.json"}
	return base, contract.ACPClaimReply{Batch: b, NativeSessionID: "original-session", TurnOrdinal: 1, ClaimToken: "private-original-claim-token", PromptText: string(prompt), PromptSHA256: hex.EncodeToString(sum[:])}
}

func TestControllerACPClaimValidationPreservesCompleteOriginalTuple(t *testing.T) {
	base, reply := acpPublicClaimFixture(t)
	begin, err := controllerACPBeginForClaim(base, "original-session", "original-claim", 1, reply)
	if err != nil {
		t.Fatal(err)
	}
	if begin.BatchID != reply.Batch.BatchID || begin.AttemptID != reply.Batch.AttemptID || begin.ClaimToken != reply.ClaimToken || begin.PromptSHA256 != reply.PromptSHA256 || begin.RenderSHA256 != reply.Batch.RenderSHA256 || begin.RequestID != contract.ACPBeginRequestID(begin) {
		t.Fatal("original claim tuple lost")
	}
	if strings.Contains(reply.PromptText, begin.ClaimToken) || !strings.Contains(reply.PromptText, "delivery-1") || !strings.Contains(reply.PromptText, "event-1") {
		t.Fatal("public prompt lost delivery proof or leaked private token")
	}
}

func TestControllerACPClaimValidationRejectsWrongOriginalTuple(t *testing.T) {
	for _, name := range []string{"binding", "epoch", "attempt", "transport", "schema", "session", "ordinal", "zero_ordinal", "overflow_ordinal", "claim_request", "prompt_text", "prompt_hash", "render_hash", "missing_token", "token_in_prompt", "mutated_event", "oversized_session"} {
		t.Run(name, func(t *testing.T) {
			base, reply := acpPublicClaimFixture(t)
			session, rid, ordinal := "original-session", "original-claim", int64(1)
			switch name {
			case "binding":
				reply.Batch.BindingID = "foreign"
			case "epoch":
				reply.Batch.OwnerEpoch++
			case "attempt":
				reply.Batch.AttemptID = "foreign"
			case "transport":
				reply.Batch.TransportMode = contract.BridgeTransportHookPull
			case "schema":
				reply.Batch.SchemaVersion++
			case "session":
				reply.NativeSessionID = "foreign"
			case "ordinal":
				reply.TurnOrdinal++
			case "zero_ordinal":
				ordinal = 0
			case "overflow_ordinal":
				ordinal = math.MaxInt64
			case "claim_request":
				rid = "foreign"
			case "prompt_text":
				reply.PromptText += " "
			case "prompt_hash":
				reply.PromptSHA256 = strings.Repeat("a", 64)
			case "render_hash":
				reply.Batch.RenderSHA256 = strings.Repeat("a", 64)
			case "missing_token":
				reply.ClaimToken = ""
			case "token_in_prompt":
				reply.ClaimToken = "delivery-1"
			case "mutated_event":
				reply.Batch.Events[0].Summary = "foreign content"
			case "oversized_session":
				session = strings.Repeat("x", 65536)
				reply.NativeSessionID = session
			}
			if _, err := controllerACPBeginForClaim(base, session, rid, ordinal, reply); err == nil || err.Error() != "controller_acp_claim_invalid" {
				t.Fatalf("foreign claim accepted: %v", err)
			}
		})
	}
}
