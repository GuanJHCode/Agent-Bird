//go:build darwin

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Durable spool appends + protected FS records with synthetic process/exit
// tuples. No Handle/Wait capability, launcher, pipe actor or publisher is used.
// Initial candidate/state comes from the existing bounded capture fixture.
func codexLimitAppendSealedSegment(t *testing.T, ctx context.Context, h *Host, p codexPreparedState, g contract.LaunchCommand, previous codexSealRecord, previousFile codexStateFile, initialSession contract.Event, revision int) (codexSealRecord, codexStateFile, contract.Event) {
	t.Helper()
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		t.Fatal(err)
	}
	root, err := codexAnchorStateRoot(ctx, filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID))
	if err != nil {
		t.Fatal(err)
	}
	id := process.Identity{PID: 4000 + revision, PGID: 4000 + revision, Birth: fmt.Sprintf("synthetic-boundary-%d", revision), BirthKnown: true, Executable: p.Launch.Path, ExecutableSHA256: p.Launch.SHA256}
	meta := launchMetadata{commandID: g.CommandID, executionEpoch: g.ExecutionEpoch, workRevision: g.WorkRevision}
	for _, kind := range []string{contract.EventSpawned, contract.EventRunning} {
		if _, err := h.appendPhaseEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, kind, hashText(kind), &id, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	session, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventSession, hashText("session:session-id:"+g.SessionID), &id, nil, nil, structuredEventFields{SessionKind: "session-id", SessionID: g.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	record := previous
	record.ProtectedParents = append([]codexStateDirectory(nil), root.Parents...)
	record.Source = codexLaunchProvenance{SegmentID: g.SegmentID, CommandID: g.CommandID, ProducerID: h.producerID, Epoch: g.ExecutionEpoch, Process: contract.ProcessIdentity{PID: id.PID, PGID: id.PGID, Birth: id.Birth}, Executable: id.Executable, ExecutableSHA256: id.ExecutableSHA256}
	record.InitialSession = codexSessionEvidence(initialSession)
	record.CurrentSession = codexSessionEvidence(session)
	record.PreviousSegmentID = previous.Source.SegmentID
	record.PreviousSHA256 = previousFile.SHA256
	record.PreviousSeal = previousFile
	record.QuestionRevision = revision
	record.QuestionPayloadSHA256 = hashText(fmt.Sprintf("Which authorized file %d?", revision))
	body, err := codexEncodeSeal(record)
	if err != nil || len(body) > 16<<10 {
		t.Fatal("fixture exceeded original single-record cap", err)
	}
	file, err := codexWriteProtectedRecord(ctx, root, "codex-session-seal.json", body)
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	question, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventQuestion, record.QuestionPayloadSHA256, &id, &zero, nil, structuredEventFields{SessionKind: "session-id", SessionID: g.SessionID, QuestionRevision: revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventExited, hashText("exited"), &id, &zero, nil, structuredEventFields{ActiveMS: 10}); err != nil {
		t.Fatal(err)
	}
	return record, file, question
}
func codexLimitNextGrant(t *testing.T, p codexPreparedState, g contract.LaunchCommand, question, initialSession contract.Event, revision int) (contract.LaunchCommand, adapter.Invocation) {
	t.Helper()
	ref := &contract.ContinuationRef{Version: 1, Mode: contract.ContinuationQuestion, SourceKind: "host", SourceSegmentID: g.SegmentID, SourceEventID: question.EventID, SourceEventRevision: question.Sequence, SourceEventSHA256: question.PayloadHash, SessionEventID: initialSession.EventID, AdapterPayloadSHA256: codexDigest(g.AdapterPayload), OwnerContextID: p.Origin.Scope.OwnerContextID}
	g.SegmentID = fmt.Sprintf("limit-%02d", revision+1)
	g.CommandID = fmt.Sprintf("limit-command-%02d", revision+1)
	g.ExecutionEpoch++
	g.Continuation = ref
	g.QuestionID = question.QuestionID
	g.QuestionRevision = revision
	g.Answer = "Read the next authorized file."
	inv, err := adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: p.Origin.Lock.Binary, Lock: &p.Origin.Lock, Profile: &p.Origin.Profile, CWD: p.Origin.Workspace, Prompt: g.Answer, Session: adapter.SessionRef{Kind: adapter.SessionID, ID: g.SessionID}, Continuation: ref})
	if err != nil {
		t.Fatal(err)
	}
	return g, inv
}

// Bounded exact fixture inventory; no broad source/home traversal. It includes
// the synthetic auth reference and every retained seal/event, while stable
// directory identities deliberately omit mtime changed by legitimate entries.
func codexLimitSnapshot(t *testing.T, base string) map[string]codexSandboxFingerprint {
	t.Helper()
	paths := []string{}
	err := filepath.WalkDir(base, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		if len(paths) > 2048 {
			return errCodexStateFile
		}
		return nil
	})
	if err != nil {
		t.Fatal("bounded fixture inventory", err)
	}
	snap, err := codexSandboxSnapshot(paths)
	if err != nil {
		t.Fatal("owned fixture snapshot", err)
	}
	return snap
}

// The short name keeps the same original path/record/parent caps; it does not
// change any production constant. 64 counts seals including initial+source,
// permitting a 65th execution but not a 65th resumable technical Question.
func TestCodexChain64(t *testing.T) {
	if codexSealChainLimit != 64 {
		t.Fatal("explicit contract changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h, original, g, inv, env := codexContinuationFixture(t)
	var last codexSealRecord
	lastFile, err := codexReadProtectedRecord(ctx, original.Protected, "codex-session-seal.json", &last)
	if err != nil {
		t.Fatal(err)
	}
	initialSpool, _ := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: original.Origin.Scope.InitialSegmentID})
	rows, err := initialSpool.Read()
	if err != nil {
		t.Fatal(err)
	}
	var initialSession contract.Event
	for _, e := range rows {
		if e.Kind == contract.EventSession {
			initialSession = e
		}
	}
	if initialSession.EventID == "" {
		t.Fatal("actual durable initial Session missing")
	}
	for revision := 2; revision <= 64; revision++ {
		var question contract.Event
		last, lastFile, question = codexLimitAppendSealedSegment(t, ctx, h, original, g, last, lastFile, initialSession, revision)
		g, inv = codexLimitNextGrant(t, original, g, question, initialSession, revision)

	}
	// No cold Host sequence inference from ordinal: restore authenticates the
	// exact retained source/ref and every original Session->Question->Exited.
	cold, err := NewIPC(h.spoolRoot, h.producerID)
	if err != nil || cold.BindContinuationOwner("owner") != nil {
		t.Fatal("cold owner")
	}
	h = cold
	base := filepath.Dir(original.Source.Home)
	before := codexLimitSnapshot(t, base)
	var prepared codexPreparedState
	t.Run("exact_64_restore_validate", func(t *testing.T) {
		prepared, err = h.prepareManagedCodexState(ctx, g, inv, env, codexPreparationControl{})
		if err != nil {
			t.Fatal("complete 64-seal chain rejected", err)
		}
		if prepared.Previous == nil || len(prepared.Previous.Segments) != 64 || prepared.OriginFile != original.OriginFile || prepared.Previous.Segments[0].Record.QuestionRevision != 64 || prepared.Previous.Segments[63].Record.QuestionRevision != 1 || prepared.Previous.Segments[63].Record.Source.SegmentID != original.Origin.Scope.InitialSegmentID {
			t.Fatal("initial/source counting or original origin lost")
		}
		var total int64
		for i, s := range prepared.Previous.Segments {
			if s.Record.QuestionRevision != 64-i || s.File.Identity.Size > 16<<10 || s.File.Identity.Size <= 0 || s.Session.Sequence >= s.Question.Sequence || s.Question.Sequence >= s.Exited.Sequence {
				t.Fatal("invalid actual record/Session/Question/Exited tuple")
			}
			total += s.File.Identity.Size
		}
		if total > 1<<20 || prepared.Previous.Observation.Physical.State.Bytes > codexStateTotalLimit {
			t.Fatal("chain or shared physical read cap bypassed")
		}
		if h.codexVerifyRestoredState(ctx, prepared) != nil || h.codexCheckContinuationGrant(prepared, g) != nil {
			t.Fatal("real restored validation/grant consumer refused boundary")
		}
		if !reflect.DeepEqual(before, codexLimitSnapshot(t, base)) || len(h.active) != 0 || len(h.codexLeases) != 0 {
			t.Fatal("restore changed retained files or fabricated process ownership")
		}
		t.Logf("seals=%d source_revision=%d seal_bytes=%d single_record_cap=%d chain_cap=%d physical_bytes=%d", len(prepared.Previous.Segments), g.QuestionRevision, total, 16<<10, 1<<20, prepared.Previous.Observation.Physical.State.Bytes)
	})
	if t.Failed() {
		return
	} // No overlimit fixture conceals a failed positive.
	var bound65 codexBoundThread
	t.Run("next_question_capture_refused", func(t *testing.T) {
		// Real binding consumer with synthetic Running identity, then exact
		// next candidate request. The refusal must consume no supplied bytes.
		spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
		if err != nil {
			t.Fatal(err)
		}
		id := process.Identity{PID: 4065, PGID: 4065, Birth: "synthetic-boundary-65", BirthKnown: true, Executable: prepared.Launch.Path, ExecutableSHA256: prepared.Launch.SHA256}
		meta := launchMetadata{commandID: g.CommandID, executionEpoch: g.ExecutionEpoch, workRevision: g.WorkRevision}
		for _, kind := range []string{contract.EventSpawned, contract.EventRunning} {
			if _, err := h.appendPhaseEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, kind, hashText(kind), &id, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		bound, err := h.bindManagedCodexThread(ctx, prepared, g, id, prepared.Previous.Thread.Binding.native())
		bound65 = bound
		if err != nil {
			t.Fatal("65th execution binding was incorrectly blocked", err)
		}
		prior := codexLimitSnapshot(t, base)
		remaining := codexStateTotalLimit
		_, err = h.captureManagedCodexQuestionSeal(ctx, prepared, g, bound, 65, hashText("next"), codexStateReadControl{Remaining: &remaining}, codexSealControl{})
		if !errors.Is(err, errCodexStateFile) || remaining != codexStateTotalLimit || !reflect.DeepEqual(prior, codexLimitSnapshot(t, base)) {
			t.Fatal("65th Question consumed budget or changed immutable state", err)
		}
		if _, err := os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "codex-session-seal.json")); !os.IsNotExist(err) {
			t.Fatal("overlimit candidate published a seal")
		}
	})
	if t.Failed() {
		return
	}
	t.Run("overlimit_serialized_chain_restore_refused", func(t *testing.T) {
		// Adversarial retained input only: the approved capture above refused
		// to create it. Build all65 linked FS/spool tuples to test the reader's
		// revision bound, without claiming its 65th loop branch was reachable.
		root, err := codexAnchorStateRoot(ctx, filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID))
		if err != nil {
			t.Fatal(err)
		}
		previous := prepared.Previous.Segments[0]
		record := previous.Record
		record.ProtectedParents = append([]codexStateDirectory(nil), root.Parents...)
		record.Source = bound65.Current
		record.InitialSession = codexSessionEvidence(bound65.InitialSession)
		record.CurrentSession = codexSessionEvidence(bound65.Session)
		record.PreviousSegmentID = previous.Record.Source.SegmentID
		record.PreviousSHA256 = previous.File.SHA256
		record.PreviousSeal = previous.File
		record.QuestionRevision = 65
		record.QuestionPayloadSHA256 = hashText("overlimit fixture question")
		body, err := codexEncodeSeal(record)
		if err != nil || len(body) > 16<<10 {
			t.Fatal("original record cap", err)
		}
		if _, err := codexWriteProtectedRecord(ctx, root, "codex-session-seal.json", body); err != nil {
			t.Fatal(err)
		}
		spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
		if err != nil {
			t.Fatal(err)
		}
		id := process.Identity{PID: record.Source.Process.PID, PGID: record.Source.Process.PGID, Birth: record.Source.Process.Birth, BirthKnown: true, Executable: record.Source.Executable, ExecutableSHA256: record.Source.ExecutableSHA256}
		zero := 0
		meta := launchMetadata{commandID: g.CommandID, executionEpoch: g.ExecutionEpoch, workRevision: g.WorkRevision}
		question, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventQuestion, record.QuestionPayloadSHA256, &id, &zero, nil, structuredEventFields{QuestionRevision: 65, SessionKind: "session-id", SessionID: g.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventExited, hashText("exited"), &id, &zero, nil, structuredEventFields{ActiveMS: 10}); err != nil {
			t.Fatal(err)
		}
		over, overInv := codexLimitNextGrant(t, original, g, question, initialSession, 65)
		prior := codexLimitSnapshot(t, base)
		if _, err := h.prepareManagedCodexState(ctx, over, overInv, env, codexPreparationControl{}); !errors.Is(err, errCodexStateFile) {
			t.Fatal("65-seal/revision input was admitted", err)
		}
		if !reflect.DeepEqual(prior, codexLimitSnapshot(t, base)) || len(h.active) != 0 || len(h.codexLeases) != 0 {
			t.Fatal("refused overlimit restore changed retained state or created ownership")
		}
	})

}
