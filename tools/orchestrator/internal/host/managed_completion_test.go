package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// This is an actual pinned executable and original Host launch, not a new
// helper API/stub or a parser-only substitute for terminal routing.
func TestManagedNativeStructuredQuestionRoutesToQuestion(t *testing.T) {
	t.Run("antigravity-cli", func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		frame := `{"event":"init","conversation_id":"owned-session"}` + "\n" +
			`{"event":"result","result":{"conversation_id":"owned-session","status":"SUCCESS","response":"native aggregate commentary","structured_output":{"version":1,"kind":"technical_question","text":"Which business value should be used?"}}}` + "\n"
		binary := filepath.Join(root, "native-provider-fixture")
		// shell printf is finite, touches no account/config and emits the
		// same stream-json fields as the provider's structured channel.
		body := []byte("#!/bin/sh\ncase \"$1\" in\n--help) printf '%s\\n' '--input-format --output-format --mode --json-schema'; exit 0;;\n--version) printf '%s\\n' 'owned-fixture-v1'; exit 0;;\nesac\nprintf '%s' '" + frame + "'\n")
		if err = os.WriteFile(binary, body, 0700); err != nil {
			t.Fatal(err)
		}
		pin := adapter.BinaryPin{Path: binary, Version: "owned-fixture-v1", SHA256: hashBytes(body)}
		profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 3000}
		lock := &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderAGY, Protocol: adapter.ProtocolID(adapter.ProviderAGY), Binary: pin}
		payload := adapter.InvocationPayload{Provider: string(adapter.ProviderAGY), Directory: root, Prompt: "Inspect; request a missing business value before completing.", Profile: profile, ProviderLock: lock}
		raw, _ := json.Marshal(payload)
		inv, err := adapter.BuildInvocation(adapter.Request{Provider: adapter.ProviderAGY, Binary: pin, CWD: root, Prompt: payload.Prompt, Profile: profile, Lock: lock})
		if err != nil {
			t.Fatal(err)
		}
		h, err := NewIPC(filepath.Join(root, "spool"), "owned-producer")
		if err != nil {
			t.Fatal(err)
		}
		if err = h.BindContinuationOwner("owned-owner"); err != nil {
			t.Fatal(err)
		}
		grant := contract.LaunchCommand{CommandID: "owned-command", ReservationID: "owned-reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, ExecutionEpoch: 1, GrantedActiveMS: 3000, AdapterPayload: raw}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, runErr := h.ExecuteLaunch(ctx, grant, inv)
		sink := &durableSink{}
		if err = h.PublishAll(ctx, sink, 1); err != nil {
			t.Fatal(err)
		}
		var question, final, exited *contract.Event
		for index := range sink.events {
			event := &sink.events[index]
			switch event.Kind {
			case contract.EventQuestion:
				question = event
			case contract.EventResult:
				final = event
			case contract.EventExited:
				exited = event
			}
		}
		if exited == nil || exited.Process == nil || exited.ExitCode == nil || *exited.ExitCode != 0 {
			t.Fatalf("actual fixture lacks original exit receipt: status=%s err=%v", result.Status, runErr)
		}
		if err = process.ConfirmGroupExited(exited.Process.PGID); err != nil {
			t.Fatalf("fixture original group remains: %v", err)
		}
		t.Logf("original_host_receipt status=%s exit=%d pgid=%d group_absent=true active_ms=%d question=%t result=%t", result.Status, *exited.ExitCode, exited.Process.PGID, exited.ActiveMS, question != nil, final != nil)
		if runErr != nil {
			t.Fatal(runErr)
		}
		if result.Status != "waiting_user" || question == nil || final != nil {
			t.Fatalf("native structured technical_question must route to authenticated Question after original exit proof; status=%s question=%t result=%t", result.Status, question != nil, final != nil)
		}
		if question.QuestionRevision != 1 || question.SessionID != "owned-session" || question.Artifact == nil || question.Sequence >= exited.Sequence {
			t.Fatal("missing Host-owned question revision/session/exit sequence")
		}
		text, err := os.ReadFile(question.Artifact.Path)
		if err != nil || string(text) != "Which business value should be used?" {
			t.Fatalf("question artifact exposed envelope/commentary: length=%d err=%v", len(text), err)
		}
	})
}

// OS negative proof is queued separately; it preserves the same authenticated
// Failed+Exited settlement boundary and never turns permission/error metadata
// into a business output, even when the native structured object is valid.
func TestManagedNativeContradictionFailsAfterOriginalTreeExit(t *testing.T) {
	for _, tc := range []struct {
		name           string
		provider       adapter.Provider
		status, fields string
	}{
		{"claude_native_error", adapter.ProviderClaude, "success", `,"is_error":true`},
		{"claude_permission_denied", adapter.ProviderClaude, "success", `,"permission_denials":[{"tool_name":"Bash","tool_use_id":"owned-tool","tool_input":{}}]`},
		{"claude_null_metadata", adapter.ProviderClaude, "success", `,"is_error":null`},
		{"claude_duplicate_metadata", adapter.ProviderClaude, "success", `,"permission_denials":[],"permission_denials":[]`},
		{"agy_waiting", adapter.ProviderAGY, "WAITING", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			structured := `{"version":1,"kind":"technical_question","text":"Which business value?"}`
			frame := managedNativeInit(tc.provider, "owned-session") + managedNativeFrame(tc.provider, "owned-session", tc.status, structured, "native commentary", tc.fields)
			binary := filepath.Join(root, "owned-native-negative")
			body := []byte("#!/bin/sh\nprintf '%s' '" + frame + "'\n")
			if err = os.WriteFile(binary, body, 0700); err != nil {
				t.Fatal(err)
			}
			pin := adapter.BinaryPin{Path: binary, Version: "owned-fixture-v1", SHA256: hashBytes(body)}
			profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 3000}
			lock := &adapter.ProviderLock{Version: 1, Provider: tc.provider, Protocol: adapter.ProtocolID(tc.provider), Binary: pin}
			payload := adapter.InvocationPayload{Provider: string(tc.provider), Directory: root, Prompt: "Inspect", Profile: profile, ProviderLock: lock}
			raw, _ := json.Marshal(payload)
			inv, err := adapter.BuildInvocation(adapter.Request{Provider: tc.provider, Binary: pin, CWD: root, Prompt: payload.Prompt, Profile: profile, Lock: lock})
			if err != nil {
				t.Fatal(err)
			}
			h, err := NewIPC(filepath.Join(root, "spool"), "owned-negative-producer")
			if err != nil {
				t.Fatal(err)
			}
			if err = h.BindContinuationOwner("owned-owner"); err != nil {
				t.Fatal(err)
			}
			grant := contract.LaunchCommand{CommandID: "command", ReservationID: "reservation", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, ExecutionEpoch: 1, GrantedActiveMS: 3000, AdapterPayload: raw}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := h.ExecuteLaunch(ctx, grant, inv)
			if err != nil {
				t.Fatal(err)
			}
			sink := &durableSink{}
			if err = h.PublishAll(ctx, sink, 1); err != nil {
				t.Fatal(err)
			}
			var failed, exited *contract.Event
			for index := range sink.events {
				event := &sink.events[index]
				if event.Kind == contract.EventQuestion || event.Kind == contract.EventResult || event.Kind == contract.EventLaunchRejected || event.Kind == contract.EventUnknown {
					t.Fatalf("native contradiction crossed business/ownership boundary: %s", event.Kind)
				}
				if event.Kind == contract.EventFailed {
					failed = event
				}
				if event.Kind == contract.EventExited {
					exited = event
				}
			}
			if result.Status != "failed" || failed == nil || exited == nil || exited.Process == nil || exited.ExitCode == nil || *exited.ExitCode != 0 || exited.ActiveMS <= 0 || exited.ActiveMS > grant.GrantedActiveMS || failed.Sequence >= exited.Sequence {
				t.Fatal("failed native output lost original actual-exit accounting")
			}
			if err = process.ConfirmGroupExited(exited.Process.PGID); err != nil {
				t.Fatal(err)
			}
			t.Logf("native_negative_receipt status=%s pgid=%d exit=%d group_absent=true active_ms=%d question=false result=false failed=true exited=true", result.Status, exited.Process.PGID, *exited.ExitCode, exited.ActiveMS)
		})
	}
}
