package host

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Opaque owned bytes and actual durable spool appends, with synthetic process
// provenance. The fixture writes a private seal directly; it creates no kernel
// Wait/tree capability and does not validate the original-exit publisher.
func codexContinuationFixture(t *testing.T) (*Host, codexPreparedState, contract.LaunchCommand, adapter.Invocation, []string) {
	t.Helper()
	h, p, g, bound := codexSealFixture(t)
	t.Log("fixture-native-state-ready")
	candidate, err := h.captureManagedCodexQuestionSeal(context.Background(), p, g, bound, 1, hashText("Which file?"), codexStateReadControl{}, codexSealControl{})
	if err != nil {
		t.Fatal("fixture capture", err)
	}
	body, err := codexEncodeSeal(candidate.Record)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("fixture-seal-bytes", len(body))
	if _, err := codexWriteProtectedRecord(context.Background(), p.Protected, "codex-session-seal.json", body); err != nil {
		t.Fatal("fixture write seal", err)
	}
	spool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	first := bound.Record.First
	id := process.Identity{PID: first.Process.PID, PGID: first.Process.PGID, Birth: first.Process.Birth, BirthKnown: true, Executable: first.Executable, ExecutableSHA256: first.ExecutableSHA256}
	meta := launchMetadata{commandID: g.CommandID, executionEpoch: g.ExecutionEpoch, workRevision: g.WorkRevision}
	zero := 0
	question, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventQuestion, hashText("Which file?"), &id, &zero, nil, structuredEventFields{SessionKind: "session-id", SessionID: bound.Record.Binding.ThreadID, QuestionRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventExited, hashText("exited"), &id, &zero, nil, structuredEventFields{ActiveMS: 10}); err != nil {
		t.Fatal(err)
	}
	ref := &contract.ContinuationRef{Version: 1, Mode: contract.ContinuationQuestion, SourceKind: "host", SourceSegmentID: g.SegmentID, SourceEventID: question.EventID, SourceEventRevision: question.Sequence, SourceEventSHA256: question.PayloadHash, SessionEventID: bound.Session.EventID, AdapterPayloadSHA256: codexDigest(g.AdapterPayload), OwnerContextID: "owner"}
	g.SegmentID, g.CommandID, g.ExecutionEpoch = "second", "second-command", 2
	g.SessionKind, g.SessionID = "session-id", bound.Record.Binding.ThreadID
	g.QuestionID, g.QuestionRevision, g.Answer, g.Continuation = question.QuestionID, 1, "Read the authorized file.", ref
	inv, err := adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: p.Origin.Lock.Binary, Lock: &p.Origin.Lock, Profile: &p.Origin.Profile, CWD: p.Origin.Workspace, Prompt: g.Answer, Session: adapter.SessionRef{Kind: adapter.SessionID, ID: g.SessionID}, Continuation: ref})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}); err != nil {
		t.Fatal(err)
	}
	return h, p, g, inv, []string{"HOME=" + filepath.Dir(p.Source.Home), "CODEX_HOME=" + p.Source.Home}
}

func TestCodexContinuationRestoresExactOriginalOwnedStateBeforeStart(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_host", true: "cold_host"}[cold], func(t *testing.T) {
			h, original, g, inv, env := codexContinuationFixture(t)
			if cold {
				var err error
				h, err = NewIPC(h.spoolRoot, h.producerID)
				if err != nil || h.BindContinuationOwner("owner") != nil {
					t.Fatal("cold Host did not bind original owner")
				}
			}
			prepared, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
			if err != nil {
				t.Fatal("trusted sealed continuation refused", err)
			}
			if prepared.OriginFile != original.OriginFile || !reflect.DeepEqual(prepared.Origin, original.Origin) || !reflect.DeepEqual(prepared.Layout, original.Layout) || !reflect.DeepEqual(prepared.Source, original.Source) || prepared.Descriptor.ResumeThreadID != g.SessionID || string(prepared.Projection.ConfigTOML()) != string(original.Projection.ConfigTOML()) {
				t.Fatal("continuation allocated fresh state/defaults or lost exact native thread")
			}
			if len(h.codexLeases) != 0 || len(h.active) != 0 {
				t.Fatal("pure preparation fabricated process ownership")
			}
		})
	}
}

func TestCodexContinuationRefusesDriftAndNonquestionSourcesBeforeStart(t *testing.T) {
	for _, kind := range []string{"owner", "session_event", "question_event", "question_revision", "source_hash", "interrupted", "report", "source_config", "earlier_main", "new_wal"} {
		t.Run(kind, func(t *testing.T) {
			h, original, g, inv, env := codexContinuationFixture(t)
			switch kind {
			case "owner":
				g.Continuation.OwnerContextID = "other"
			case "session_event":
				g.Continuation.SessionEventID = "other"
			case "question_event":
				g.Continuation.SourceEventID = "other"
			case "question_revision":
				g.QuestionRevision++
			case "source_hash":
				g.Continuation.SourceEventSHA256 = hashText("other")
			case "interrupted":
				g.Continuation.Mode = contract.ContinuationInterrupted
			case "report":
				g.Continuation.SourceKind = "report"
			case "source_config":
				if err := os.WriteFile(filepath.Join(original.Source.Home, "config.toml"), []byte("model=\"drift\"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "earlier_main":
				if err := os.WriteFile(filepath.Join(original.Layout.SQLite.Path, "state_5.sqlite"), []byte("changed opaque bytes"), 0644); err != nil {
					t.Fatal(err)
				}
			case "new_wal":
				if err := os.WriteFile(filepath.Join(original.Layout.SQLite.Path, "state_5.sqlite-wal"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{}); err == nil {
				t.Fatal("drift or unapproved source resumed")
			}
		})
	}
}
