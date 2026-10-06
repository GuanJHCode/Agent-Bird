package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrantTypedContinuationBindsOriginalPayloadBeforePromptSubstitution(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderClaude, adapter.ProviderAGY} {
		for _, mode := range []string{contract.ContinuationQuestion, contract.ContinuationInterrupted} {
			t.Run(string(provider)+"/"+mode, func(t *testing.T) {
				root := privateTaskTemp(t)
				bin := filepath.Join(root, "provider")
				script := []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo '2.1.285 (Claude Code)';;\n--help) echo '--input-format --output-format --permission-mode --disallowed-tools --mode --resume --conversation --json-schema';;\n*) exit 99;;\nesac\n")
				if err := os.WriteFile(bin, script, 0700); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(script)
				p := adapter.InvocationPayload{Provider: string(provider), Directory: root, Prompt: "ORIGINAL_DESTRUCTIVE_OPERATION", Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}, ProviderLock: &adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: adapter.BinaryPin{Path: bin, Version: "2.1.285 (Claude Code)", SHA256: hex.EncodeToString(digest[:])}}}
				raw, _ := json.Marshal(p)
				sum := sha256.Sum256(raw)
				kind := "session-id"
				flag := "--resume"
				if provider == adapter.ProviderAGY {
					kind = "conversation-id"
					flag = "--conversation"
				}
				g := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "new", WorkRevision: 1, SessionKind: kind, SessionID: "native-session", AdapterPayload: raw, Continuation: &contract.ContinuationRef{Version: 1, SourceKind: "host", Mode: mode, SourceSegmentID: "old", SourceEventID: "source", SourceEventRevision: 7, SourceEventSHA256: strings.Repeat("a", 64), SessionEventID: "init", AdapterPayloadSHA256: hex.EncodeToString(sum[:]), OwnerContextID: "owner"}}
				if mode == contract.ContinuationQuestion {
					g.QuestionID = "question"
					g.QuestionRevision = 1
					g.Answer = "OWNER_TECHNICAL_ANSWER"
				}
				inv, err := invocationForGrant(context.Background(), g)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(strings.Join(inv.Args(), " "), flag+" native-session") {
					t.Fatal("exact session missing even with empty answer")
				}
				input := string(inv.Stdin())
				if strings.Contains(input, p.Prompt) || (mode == contract.ContinuationQuestion && !strings.Contains(input, g.Answer)) || (mode == contract.ContinuationInterrupted && !strings.Contains(input, "interrupted")) {
					t.Fatalf("unsafe continuation prompt: %s", input)
				}
				g.Continuation.AdapterPayloadSHA256 = strings.Repeat("f", 64)
				if _, err = invocationForGrant(context.Background(), g); err == nil || err.Error() != "continuation_payload_mismatch" {
					t.Fatalf("payload tamper not rejected before substitution: %v", err)
				}
			})
		}
	}
}
