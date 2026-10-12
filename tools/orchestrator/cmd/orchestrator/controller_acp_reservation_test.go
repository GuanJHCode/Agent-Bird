package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestACPPromptReservationReservesBeforeReceiptAndScrubsToken(t *testing.T) {
	state, req := acpCompletionFixture(t)
	begin := req.ACPBeginRequest
	begin.RequestID = contract.ACPBeginRequestID(begin)
	path, err := reserveACPPrompt(state, begin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reserveACPPrompt(state, begin); err == nil || err.Error() != "controller_acp_prompt_unknown" {
		t.Fatalf("unknown reservation authorized replay: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), req.ClaimToken) || strings.Contains(string(raw), "prompt_text") {
		t.Fatalf("reservation leaked protected prompt data: %v", err)
	}
	if _, err = persistACPCompletion(state, req); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reservation survived durable receipt: %v", err)
	}
}

func TestACPPromptReservationCapacityRefusesBeforePrompt(t *testing.T) {
	state, req := acpCompletionFixture(t)
	for i := int64(1); i <= acpCompletionMaximumBytes/int64(bridgeMaxRequestFile); i++ {
		begin := req.ACPBeginRequest
		begin.RequestID = contract.ACPBeginRequestID(begin)
		begin.TurnOrdinal = i
		begin.AttemptID = fmt.Sprintf("%s-%d", req.AttemptID, i)
		begin.RequestID = contract.ACPBeginRequestID(begin)
		if _, err := reserveACPPrompt(state, begin); err != nil {
			t.Fatalf("reservation %d: %v", i, err)
		}
	}
	begin := req.ACPBeginRequest
	begin.TurnOrdinal = 999
	begin.AttemptID += "-full"
	begin.RequestID = contract.ACPBeginRequestID(begin)
	if _, err := reserveACPPrompt(state, begin); err == nil || err.Error() != "controller_acp_completion_capacity_exhausted" {
		t.Fatalf("full reservation admitted remote prompt: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(state, acpCompletionDirectory))
	if err != nil || len(entries) != int(acpCompletionMaximumBytes/int64(bridgeMaxRequestFile))+1 {
		t.Fatalf("capacity refusal wrote a reservation: entries=%d err=%v", len(entries), err)
	}
}
