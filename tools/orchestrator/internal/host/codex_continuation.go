package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const codexSealChainLimit = 64

type codexSealedSegment struct {
	Root                      codexStateRoot
	File                      codexStateFile
	Record                    codexSealRecord
	Session, Question, Exited contract.Event
}

// Copied observations retained through the final pre-Start barrier. Ancestors
// are immutable evidence; runtime bytes are captured only once for the source.
type codexContinuationState struct {
	Segments    []codexSealedSegment // newest source first, exact initial last
	Thread      codexThreadRecord
	ThreadFile  codexStateFile
	Observation codexSealableObservation
}

func (h *Host) restoreManagedCodexState(ctx context.Context, g contract.LaunchCommand, inv adapter.Invocation, env []string, control codexPreparationControl) (codexPreparedState, error) {
	var empty codexPreparedState
	if err := codexValidateSourceEnvironment(env); err != nil {
		return empty, err
	}
	c := g.Continuation
	d, ok := adapter.ManagedCodexDescriptorFor(inv)
	if ctx.Err() != nil || !ok || adapter.ValidateContinuationReference(c) != nil || c.Mode != contract.ContinuationQuestion || c.SourceKind != "host" || c.WorkspaceSourceSegmentID != "" || g.SessionKind != "session-id" || !codexCanonicalUUID.MatchString(g.SessionID) || d.ResumeThreadID != g.SessionID || g.QuestionID == "" || g.QuestionRevision < 1 || g.QuestionRevision > codexSealChainLimit || g.Answer == "" || d.Prompt != g.Answer+"\n\n"+adapter.ManagedTaskInstruction() || c.SourceSegmentID == g.SegmentID || !codexSafeScopeID(c.SourceSegmentID) {
		return empty, errCodexStateFile
	}
	h.mu.Lock()
	owner := h.continuationOwner
	h.mu.Unlock()
	scope, err := codexScopeForGrant(g, owner)
	if err != nil || owner != c.OwnerContextID || scope.AdapterPayloadSHA256 != c.AdapterPayloadSHA256 {
		return empty, errCodexStateFile
	}
	sourceRoot, err := codexAnchorStateRoot(ctx, filepath.Join(h.spoolRoot, g.AttemptID, c.SourceSegmentID))
	if err != nil {
		return empty, errCodexStateFile
	}
	var first codexSealRecord
	sourceFile, err := codexReadProtectedRecord(ctx, sourceRoot, "codex-session-seal.json", &first)
	if err != nil || !codexSafeScopeID(first.InitialSegmentID) || first.InitialSegmentID == g.SegmentID {
		return empty, errCodexStateFile
	}
	initialPath := filepath.Join(h.spoolRoot, g.AttemptID, first.InitialSegmentID)
	initialRoot, err := codexAnchorStateRoot(ctx, initialPath)
	if err != nil {
		return empty, errCodexStateFile
	}
	var origin codexOriginRecord
	originFile, err := codexReadProtectedRecord(ctx, initialRoot, "codex-origin.json", &origin)
	scope.InitialSegmentID = first.InitialSegmentID
	profile := inv.ExecutionProfile()
	payload, payloadErr := adapter.DecodeInvocationPayload(g.AdapterPayload)
	if err != nil || payloadErr != nil || origin.Scope != scope || origin.ProtectedPath != initialPath || origin.StatePath != filepath.Join(h.spoolRoot, g.AttemptID, "codex-state") || origin.Lock != d.Lock || origin.Backend != d.Backend || profile == nil || origin.Profile != *profile || origin.Workspace != inv.WorkingDirectory() || payload.Provider != string(adapter.ProviderCodex) || payload.Kind != "provider" || payload.Profile == nil || *payload.Profile != origin.Profile || payload.ProviderLock == nil || *payload.ProviderLock != origin.Lock || payload.CandidateAction != nil || payload.CandidateWorkspace != nil || payload.SessionID != "" || payload.SessionKind != "" || payload.BinaryPath != "" || payload.BinaryVersion != "" || payload.BinarySHA256 != "" {
		return empty, errCodexStateFile
	}
	sourceHome, err := codexSourceHome(env)
	if err != nil || sourceHome != origin.Source.Home {
		return empty, errCodexStateFile
	}
	layout, source, protected, err := codexLayoutFromOrigin(ctx, origin)
	if err != nil || !reflect.DeepEqual(protected, initialRoot) {
		return empty, errCodexStateFile
	}
	// Recreate the admitted scalar projection from the already generated owned
	// config. Source config/auth bytes are never reread on continuation.
	chain, err := codexOpenAnchoredStateRoot(ctx, layout.Home)
	if err != nil {
		return empty, errCodexStateFile
	}
	config, body, err := codexObserveSourceFile(ctx, chain, "config.toml", true, codexSourceControl{})
	chain.close()
	if err != nil || config != origin.Owned.Config {
		return empty, errCodexStateFile
	}
	projection, err := codexconfig.Project(body, codexconfig.ReadOnly)
	if err != nil || codexDigest(projection.ConfigTOML()) != source.ProjectedSHA256 {
		return empty, errCodexStateFile
	}
	overrides, err := json.Marshal(projection.ManagedOverrides())
	if err != nil || codexDigest(overrides) != source.OverridesSHA256 {
		return empty, errCodexStateFile
	}
	projection, err = projection.BindRuntimePaths(layout.SQLite.Path, layout.Auxiliary.Runtime[3].Path)
	if err != nil {
		return empty, errCodexStateFile
	}
	var thread codexThreadRecord
	threadFile, err := codexReadProtectedRecord(ctx, protected, "codex-thread.json", &thread)
	p := codexPreparedState{Origin: origin, OriginFile: originFile, Protected: protected, Layout: layout, Source: source, Projection: projection, Descriptor: d}
	if err != nil || thread.Version != 1 || thread.Origin != originFile || thread.First.SegmentID != origin.Scope.InitialSegmentID || thread.First.ProducerID != h.producerID || !codexValidLaunchProvenance(thread.First) || thread.Binding.ThreadID != g.SessionID || codexCheckManagedBinding(p, thread.Binding.native()) != nil {
		return empty, errCodexStateFile
	}
	p.Launch = codexLaunchExpectation{Path: thread.First.Executable, SHA256: thread.First.ExecutableSHA256}
	state := &codexContinuationState{Thread: thread, ThreadFile: threadFile}
	p.Previous = state
	root, file, record := sourceRoot, sourceFile, first
	seen := map[string]bool{}
	remainingRecords := int64(1 << 20)
	for {
		segment := filepath.Base(root.Path)
		if ctx.Err() != nil || seen[segment] || len(state.Segments) >= codexSealChainLimit || file.Identity.Size > remainingRecords {
			return empty, errCodexStateFile
		}
		seen[segment] = true
		remainingRecords -= file.Identity.Size
		if h.codexValidateSealedRecord(ctx, p, root, record) != nil {
			return empty, errCodexStateFile
		}
		session, question, exited, err := h.codexSealedSegmentEvents(p, record)
		if err != nil {
			return empty, errCodexStateFile
		}
		item := codexSealedSegment{Root: root, File: file, Record: record, Session: session, Question: question, Exited: exited}
		if len(state.Segments) == 0 {
			if record.QuestionRevision != g.QuestionRevision || question.QuestionID != g.QuestionID || question.EventID != c.SourceEventID || question.Sequence != c.SourceEventRevision || question.PayloadHash != c.SourceEventSHA256 || record.InitialSession.EventID != c.SessionEventID || record.Source.Epoch > g.ExecutionEpoch {
				return empty, errCodexStateFile
			}
		} else {
			newer := state.Segments[len(state.Segments)-1]
			if newer.Record.PreviousSeal != file || newer.Record.PreviousSHA256 != file.SHA256 || newer.Record.QuestionRevision != record.QuestionRevision+1 || newer.Record.Source.Epoch < record.Source.Epoch || newer.Session.Sequence <= exited.Sequence {
				return empty, errCodexStateFile
			}
		}
		state.Segments = append(state.Segments, item)
		if segment == origin.Scope.InitialSegmentID {
			if record.QuestionRevision != 1 || record.PreviousSegmentID != "" || record.PreviousSHA256 != "" || record.PreviousSeal != (codexStateFile{}) || record.Source != thread.First || record.InitialSession != record.CurrentSession {
				return empty, errCodexStateFile
			}
			break
		}
		// Refuse before opening/reading a 65th record, not after consuming it.
		if len(state.Segments) >= codexSealChainLimit {
			return empty, errCodexStateFile
		}
		if !codexSafeScopeID(record.PreviousSegmentID) || record.PreviousSegmentID == g.SegmentID || !codexValidStateFile(record.PreviousSeal, true) || record.PreviousSeal.Identity.Mode != 0600 || record.PreviousSeal.Identity.Size > 16<<10 || record.PreviousSHA256 != record.PreviousSeal.SHA256 {
			return empty, errCodexStateFile
		}
		root, err = codexAnchorStateRoot(ctx, filepath.Join(h.spoolRoot, g.AttemptID, record.PreviousSegmentID))
		if err != nil {
			return empty, errCodexStateFile
		}
		// Decode into a fresh value. json.Unmarshal may reuse slice backing
		// arrays; reusing record would overwrite the already retained source's
		// slots/parents when an older immutable seal is read.
		var previous codexSealRecord
		file, err = codexReadProtectedRecord(ctx, root, "codex-session-seal.json", &previous)
		if err != nil {
			return empty, errCodexStateFile
		}
		record = previous
	}
	initialStamp := codexSessionEvidence(state.Segments[len(state.Segments)-1].Session)
	for _, segment := range state.Segments {
		if segment.Record.InitialSession != initialStamp {
			return empty, errCodexStateFile
		}
	}
	remainingBytes := codexStateTotalLimit
	state.Observation, err = codexCaptureSealableObservation(ctx, layout, thread.Binding.native(), origin.InstallationID, source, origin.Owned, codexStateReadControl{Remaining: &remainingBytes})
	if err != nil || !reflect.DeepEqual(first.State, state.Observation.Physical.State) {
		return empty, errCodexStateFile
	}
	if control.BeforePublish != nil {
		control.BeforePublish()
	}
	if h.codexVerifyRestoredState(ctx, p) != nil {
		return empty, errCodexStateFile
	}
	return p, nil
}

func (h *Host) codexValidateSealedRecord(ctx context.Context, p codexPreparedState, root codexStateRoot, r codexSealRecord) error {
	if r.Version != 1 || r.InitialSegmentID != p.Origin.Scope.InitialSegmentID || r.Origin != p.OriginFile || r.Thread != p.Previous.ThreadFile || r.Source.SegmentID != filepath.Base(root.Path) || root.Path != filepath.Join(h.spoolRoot, p.Origin.Scope.AttemptID, r.Source.SegmentID) || !reflect.DeepEqual(r.ProtectedParents, root.Parents) || !codexValidLaunchProvenance(r.Source) || r.Source.ProducerID != h.producerID || r.Source.Executable != p.Launch.Path || r.Source.ExecutableSHA256 != p.Launch.SHA256 || r.QuestionRevision < 1 || r.QuestionRevision > codexSealChainLimit || !codexHexDigest(r.QuestionPayloadSHA256) || len(r.State.Slots) != 20 || r.State.Bytes < 0 || r.State.Bytes > codexStateTotalLimit || !reflect.DeepEqual(r.State.RequiredAbsences, codexRequiredAbsences()) || ctx.Err() != nil {
		return errCodexStateFile
	}
	all, err := codexOriginParentMap(p.Origin)
	if err != nil || len(root.Parents) > 32 || len(root.Parents) < 2 {
		return errCodexStateFile
	}
	for _, parent := range root.Parents[:len(root.Parents)-1] {
		if all[parent.Path] != parent {
			return errCodexStateFile
		}
	}
	return nil
}

// No ordinal/time matching: exact authenticated source command/process tuple,
// one Session before one Question before the original Exited event, no unsafe
// terminal history. These rows are not a new kernel exit capability.
func (h *Host) codexSealedSegmentEvents(p codexPreparedState, r codexSealRecord) (contract.Event, contract.Event, contract.Event, error) {
	var session, question, exited contract.Event
	spool, err := h.spool(store.Attempt{ID: p.Origin.Scope.AttemptID, SegmentID: r.Source.SegmentID})
	if err != nil {
		return session, question, exited, errCodexStateFile
	}
	records, err := spool.Read()
	if err != nil {
		return session, question, exited, errCodexStateFile
	}
	var last int64
	beforeSession := []contract.Event{}
	for _, event := range records {
		if event.Sequence <= last {
			return session, question, exited, errCodexStateFile
		}
		last = event.Sequence
		if event.Kind == contract.EventPrepared {
			if session.EventID != "" || event.Process != nil || event.RunID != p.Origin.Scope.RunID || event.TaskID != p.Origin.Scope.TaskID || event.AttemptID != p.Origin.Scope.AttemptID || event.SegmentID != r.Source.SegmentID || event.ProducerID != r.Source.ProducerID || event.CommandID != r.Source.CommandID || event.ExecutionEpoch != r.Source.Epoch || event.WorkRevision != p.Origin.Scope.WorkRevision {
				return session, question, exited, errCodexStateFile
			}
		} else if !codexEventMatchesLaunch(event, p.Origin.Scope, r.Source) {
			return session, question, exited, errCodexStateFile
		}
		switch event.Kind {
		case contract.EventPrepared, contract.EventSpawned, contract.EventRunning:
			if session.EventID != "" {
				return session, question, exited, errCodexStateFile
			}
			beforeSession = append(beforeSession, event)
		case contract.EventSession:
			if session.EventID != "" || codexCheckRunningProvenance(beforeSession, p.Origin.Scope, r.Source) != nil || event.SessionKind != "session-id" || event.SessionID != p.Previous.Thread.Binding.ThreadID || event.PayloadHash != hashText("session:session-id:"+event.SessionID) || codexSessionEvidence(event) != r.CurrentSession {
				return session, question, exited, errCodexStateFile
			}
			session = event
		case contract.EventQuestion:
			if session.EventID == "" || question.EventID != "" || exited.EventID != "" || event.QuestionKind != "technical" || event.QuestionRevision != r.QuestionRevision || event.PayloadHash != r.QuestionPayloadSHA256 || event.SessionKind != "session-id" || event.SessionID != p.Previous.Thread.Binding.ThreadID || event.ExitCode == nil || *event.ExitCode != 0 || event.QuestionID == "" {
				return session, question, exited, errCodexStateFile
			}
			question = event
		case contract.EventExited:
			if question.EventID == "" || exited.EventID != "" || event.ExitCode == nil || *event.ExitCode != 0 || event.ActiveMS < 0 {
				return session, question, exited, errCodexStateFile
			}
			exited = event
		default:
			return session, question, exited, errCodexStateFile
		}
	}
	if session.EventID == "" || question.EventID == "" || exited.EventID == "" {
		return session, question, exited, errCodexStateFile
	}
	return session, question, exited, nil
}

func (h *Host) codexVerifyRestoredState(ctx context.Context, p codexPreparedState) error {
	if p.Previous == nil || len(p.Previous.Segments) < 1 || len(p.Previous.Segments) > codexSealChainLimit || codexVerifyManagedReferences(ctx, p) != nil || codexVerifySealableObservation(ctx, p.Previous.Observation) != nil {
		return errCodexStateFile
	}
	if h.codexVerifyContinuationRecords(ctx, p) != nil {
		return errCodexStateFile
	}
	return codexVerifySealableObservation(ctx, p.Previous.Observation)
}

// A callback/terminal consumer rechecks the same grant decision against the
// retained source proof. No new source lookup, path selector or answer is
// accepted merely because the original restore succeeded earlier.
func (h *Host) codexCheckContinuationGrant(p codexPreparedState, g contract.LaunchCommand) error {
	if p.Previous == nil || len(p.Previous.Segments) == 0 || g.Continuation == nil {
		return errCodexStateBinding
	}
	h.mu.Lock()
	owner := h.continuationOwner
	h.mu.Unlock()
	scope, err := codexScopeForGrant(g, owner)
	scope.InitialSegmentID = p.Origin.Scope.InitialSegmentID
	c, source := g.Continuation, p.Previous.Segments[0]
	if err != nil || scope != p.Origin.Scope || adapter.ValidateContinuationReference(c) != nil || c.Mode != contract.ContinuationQuestion || c.SourceKind != "host" || c.OwnerContextID != owner || c.AdapterPayloadSHA256 != scope.AdapterPayloadSHA256 || c.WorkspaceSourceSegmentID != "" || c.SourceSegmentID != source.Record.Source.SegmentID || c.SourceEventID != source.Question.EventID || c.SourceEventRevision != source.Question.Sequence || c.SourceEventSHA256 != source.Question.PayloadHash || c.SessionEventID != source.Record.InitialSession.EventID || g.SessionKind != "session-id" || g.SessionID != p.Previous.Thread.Binding.ThreadID || p.Descriptor.ResumeThreadID != g.SessionID || g.QuestionID != source.Question.QuestionID || g.QuestionRevision != source.Record.QuestionRevision || g.ExecutionEpoch < source.Record.Source.Epoch || g.Answer == "" || p.Descriptor.Prompt != g.Answer+"\n\n"+adapter.ManagedTaskInstruction() {
		return errCodexStateBinding
	}
	for _, ancestor := range p.Previous.Segments {
		if ancestor.Record.Source.SegmentID == g.SegmentID || ancestor.Record.Source.CommandID == g.CommandID {
			return errCodexStateBinding
		}
	}
	return nil
}

// Protected ancestors remain immutable while native startup may legitimately
// update runtime state. OnThread never pretends to revalidate the pre-Start
// physical snapshot after that startup has already happened.
func (h *Host) codexVerifyContinuationRecords(ctx context.Context, p codexPreparedState) error {
	if p.Previous == nil || len(p.Previous.Segments) < 1 || len(p.Previous.Segments) > codexSealChainLimit || codexVerifyManagedReferences(ctx, p) != nil {
		return errCodexStateFile
	}
	var thread codexThreadRecord
	file, err := codexReadProtectedRecord(ctx, p.Protected, "codex-thread.json", &thread)
	if err != nil || file != p.Previous.ThreadFile || thread != p.Previous.Thread || thread.First.Executable != p.Launch.Path || thread.First.ExecutableSHA256 != p.Launch.SHA256 {
		return errCodexStateFile
	}
	for _, segment := range p.Previous.Segments {
		parents := map[string]codexStateDirectory{}
		for _, parent := range segment.Root.Parents {
			parents[parent.Path] = parent
		}
		if codexCheckPersistentSlotMetadata(ctx, segment.Root, "codex-session-seal.json", segment.File, parents) != nil {
			return errCodexStateFile
		}
		session, question, exited, err := h.codexSealedSegmentEvents(p, segment.Record)
		if err != nil || !reflect.DeepEqual(session, segment.Session) || !reflect.DeepEqual(question, segment.Question) || !reflect.DeepEqual(exited, segment.Exited) {
			return errCodexStateFile
		}
	}
	return codexVerifyManagedReferences(ctx, p)
}
