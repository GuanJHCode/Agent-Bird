package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type codexSessionStamp struct {
	EventID       string `json:"event_id"`
	Sequence      int64  `json:"sequence"`
	PayloadSHA256 string `json:"payload_sha256"`
}

type codexSealRecord struct {
	Version               int                   `json:"version"`
	InitialSegmentID      string                `json:"initial_segment_id"`
	ProtectedParents      []codexStateDirectory `json:"protected_parents"`
	Origin                codexStateFile        `json:"origin"`
	Thread                codexStateFile        `json:"thread"`
	Source                codexLaunchProvenance `json:"source"`
	InitialSession        codexSessionStamp     `json:"initial_session"`
	CurrentSession        codexSessionStamp     `json:"current_session"`
	PreviousSHA256        string                `json:"previous_sha256"`
	PreviousSegmentID     string                `json:"previous_segment_id"`
	PreviousSeal          codexStateFile        `json:"previous_seal"`
	QuestionRevision      int                   `json:"question_revision"`
	QuestionPayloadSHA256 string                `json:"question_payload_sha256"`
	State                 codexPhysicalState    `json:"state"`
}

type codexSealControl struct{ BeforeFinalVerify func() }

// A candidate publishes nothing and provides no process-exit authority. The
// original retained observations must survive protected/source reads through
// the final publisher's verification barrier.
type codexQuestionSealCandidate struct {
	Record      codexSealRecord
	Prepared    codexPreparedState
	Bound       codexBoundThread
	Observation codexSealableObservation
	Protected   codexStateRoot
}

func (h *Host) captureManagedCodexQuestionSeal(ctx context.Context, p codexPreparedState, g contract.LaunchCommand, bound codexBoundThread, revision int, payloadSHA string, read codexStateReadControl, control codexSealControl) (codexQuestionSealCandidate, error) {
	var empty codexQuestionSealCandidate
	// The real Host consumer owns one fixed total budget. Explicit retained
	// budgets (including depleted/invalid ones) are never replaced or reset.
	remaining := codexStateTotalLimit
	if read.Remaining == nil {
		read.Remaining = &remaining
	}
	if !codexHexDigest(payloadSHA) || revision < 1 || revision > codexSealChainLimit || bound.Record.First.SegmentID != p.Origin.Scope.InitialSegmentID || bound.Current.SegmentID != g.SegmentID || bound.Current.CommandID != g.CommandID || bound.Current.Epoch != g.ExecutionEpoch || bound.Current.ProducerID != h.producerID || h.codexVerifyBoundThread(ctx, p, bound) != nil {
		return empty, errCodexStateFile
	}
	h.mu.Lock()
	owner := h.continuationOwner
	h.mu.Unlock()
	scope, err := codexScopeForGrant(g, owner)
	scope.InitialSegmentID = p.Origin.Scope.InitialSegmentID
	if err != nil || scope != p.Origin.Scope {
		return empty, errCodexStateFile
	}
	protected := p.Protected
	if p.Previous == nil {
		if g.Continuation != nil || g.SessionID != "" || revision != 1 || g.SegmentID != p.Origin.Scope.InitialSegmentID {
			return empty, errCodexStateFile
		}
	} else {
		if h.codexCheckContinuationGrant(p, g) != nil || revision != g.QuestionRevision+1 || len(p.Previous.Segments) >= codexSealChainLimit {
			return empty, errCodexStateFile
		}
		protected, err = codexAnchorStateRoot(ctx, filepath.Join(h.spoolRoot, scope.AttemptID, g.SegmentID))
		if err != nil {
			return empty, errCodexStateFile
		}
	}
	observed, err := codexCaptureSealableObservation(ctx, p.Layout, bound.Record.Binding.native(), p.Origin.InstallationID, p.Source, p.Origin.Owned, read)
	if err != nil {
		return empty, errCodexStateFile
	}
	record := codexSealRecord{Version: 1, InitialSegmentID: p.Origin.Scope.InitialSegmentID, ProtectedParents: append([]codexStateDirectory(nil), protected.Parents...), Origin: p.OriginFile, Thread: bound.File, Source: bound.Current, InitialSession: codexSessionEvidence(bound.InitialSession), CurrentSession: codexSessionEvidence(bound.Session), QuestionRevision: revision, QuestionPayloadSHA256: payloadSHA, State: observed.Physical.State}
	if p.Previous != nil {
		previous := p.Previous.Segments[0]
		record.PreviousSegmentID, record.PreviousSHA256, record.PreviousSeal = previous.Record.Source.SegmentID, previous.File.SHA256, previous.File
	}
	if _, err := codexMergeProtectedParents(p.Origin.Parents, record.State.Primary.Parents, protected.Parents); err != nil {
		return empty, errCodexStateFile
	}
	if _, err := codexEncodeSeal(record); err != nil {
		return empty, err
	}
	candidate := codexQuestionSealCandidate{Record: record, Prepared: p, Bound: bound, Observation: observed, Protected: protected}
	if control.BeforeFinalVerify != nil {
		control.BeforeFinalVerify()
	}
	if h.codexVerifyQuestionSealCandidate(ctx, candidate) != nil {
		return empty, errCodexStateFile
	}
	return candidate, nil
}

// Only this original Handle's actual Wait + group absence can create this
// capability. It is private, has no JSON/wire constructor and grants no refund
// or rewriting of a previous failed Stop result.
type codexOriginalExit struct {
	handle   *process.Handle
	identity process.Identity
	exitCode int
}

func codexProveOriginalExit(ctx context.Context, handle *process.Handle) (codexOriginalExit, error) {
	if ctx == nil || handle == nil {
		return codexOriginalExit{}, process.ErrProcessTreeUnknown
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return codexOriginalExit{}, process.ErrProcessTreeUnknown
	}
	if ctx.Err() != nil || handle.Wait(ctx) != nil || ctx.Err() != nil || handle.ConfirmTreeExited() != nil || ctx.Err() != nil {
		return codexOriginalExit{}, process.ErrProcessTreeUnknown
	}
	identity := handle.Identity()
	if identity.PID < 1 || identity.PGID != identity.PID {
		return codexOriginalExit{}, process.ErrProcessTreeUnknown
	}
	return codexOriginalExit{handle: handle, identity: identity, exitCode: handle.ExitCode()}, nil
}

func (h *Host) publishManagedCodexQuestionSeal(ctx context.Context, candidate codexQuestionSealCandidate, exit codexOriginalExit, out codexsession.Outcome) (codexStateFile, error) {
	if exit.handle == nil || exit.exitCode != 0 || out.Status != "completed" || out.Completion.Version != 1 || out.Completion.Kind != "technical_question" || out.Binding != candidate.Bound.Record.Binding.native() || !codexBoundedIdentity(out.TurnID) || !utf8.ValidString(out.Completion.Text) || strings.TrimSpace(out.Completion.Text) == "" || len(out.Completion.Text) > 64<<10 || hashText(out.Completion.Text) != candidate.Record.QuestionPayloadSHA256 || h.stopWasRequested(candidate.Record.Source.SegmentID) {
		return codexStateFile{}, errCodexStateFile
	}
	identity := exit.handle.Identity()
	expected := candidate.Record.Source.Process
	if identity != exit.identity || !identity.BirthKnown || identity.PID != expected.PID || identity.PGID != expected.PGID || identity.Birth != expected.Birth || identity.Executable != candidate.Prepared.Launch.Path || identity.ExecutableSHA256 != candidate.Prepared.Launch.SHA256 || ctx == nil || ctx.Err() != nil || h.codexVerifyQuestionSealCandidate(ctx, candidate) != nil {
		return codexStateFile{}, errCodexStateFile
	}
	body, err := codexEncodeSeal(candidate.Record)
	if err != nil {
		return codexStateFile{}, err
	}
	// The metadata observation is joined immediately before exclusive durable
	// publication. This does not create a new read/time budget or repair drift.
	return codexWriteProtectedRecord(ctx, candidate.Protected, "codex-session-seal.json", body)
}

func (h *Host) codexVerifyBoundThread(ctx context.Context, p codexPreparedState, bound codexBoundThread) error {
	if bound.Record.Version != 1 || bound.Record.Origin != p.OriginFile || !codexValidStateFile(bound.File, true) || bound.File.Identity.Mode != 0600 || bound.File.Identity.Size > 8<<10 || bound.Record.First.SegmentID != p.Origin.Scope.InitialSegmentID || !codexValidLaunchProvenance(bound.Record.First) || p.Launch.Path != "/usr/bin/sandbox-exec" || bound.Record.First.Executable != p.Launch.Path || bound.Record.First.ExecutableSHA256 != p.Launch.SHA256 || codexCheckManagedBinding(p, bound.Record.Binding.native()) != nil || codexVerifyManagedReferences(ctx, p) != nil {
		return errCodexStateFile
	}
	var actual codexThreadRecord
	file, err := codexReadProtectedRecord(ctx, p.Protected, "codex-thread.json", &actual)
	if err != nil || file != bound.File || actual != bound.Record {
		return errCodexStateFile
	}
	if !codexValidLaunchProvenance(bound.Current) || bound.Current.ProducerID != h.producerID || bound.Current.Executable != p.Launch.Path || bound.Current.ExecutableSHA256 != p.Launch.SHA256 {
		return errCodexStateFile
	}
	if p.Previous == nil {
		if bound.Current != bound.Record.First || !reflect.DeepEqual(bound.InitialSession, bound.Session) {
			return errCodexStateFile
		}
	} else {
		if h.codexVerifyContinuationRecords(ctx, p) != nil || bound.Record != p.Previous.Thread || bound.File != p.Previous.ThreadFile || !reflect.DeepEqual(bound.InitialSession, p.Previous.Segments[len(p.Previous.Segments)-1].Session) || bound.Current.SegmentID == bound.Record.First.SegmentID || bound.Current.Epoch < p.Previous.Segments[0].Record.Source.Epoch {
			return errCodexStateFile
		}
		for _, ancestor := range p.Previous.Segments {
			if ancestor.Record.Source.SegmentID == bound.Current.SegmentID || ancestor.Record.Source.CommandID == bound.Current.CommandID {
				return errCodexStateFile
			}
		}
	}
	spool, err := h.spool(store.Attempt{ID: p.Origin.Scope.AttemptID, SegmentID: bound.Current.SegmentID})
	if err != nil {
		return errCodexStateFile
	}
	records, err := spool.Read()
	if err != nil {
		return errCodexStateFile
	}
	var session *contract.Event
	phases := make([]contract.Event, 0, len(records))
	for i := range records {
		e := records[i]
		if e.Kind == contract.EventSession {
			if session != nil || !codexEventMatchesLaunch(e, p.Origin.Scope, bound.Current) || e.SessionKind != "session-id" || e.SessionID != bound.Record.Binding.ThreadID || e.PayloadHash != hashText("session:session-id:"+e.SessionID) {
				return errCodexStateFile
			}
			session = &records[i]
		} else {
			phases = append(phases, e)
		}
	}
	if session == nil || !reflect.DeepEqual(*session, bound.Session) || !codexBoundedIdentity(session.EventID) || !codexHexDigest(session.PayloadHash) || codexCheckRunningProvenance(phases, p.Origin.Scope, bound.Current) != nil {
		return errCodexStateFile
	}
	for _, e := range phases {
		if e.Kind == contract.EventRunning && e.Sequence >= session.Sequence {
			return errCodexStateFile
		}
	}
	return codexVerifyManagedReferences(ctx, p)
}

func (h *Host) codexVerifyQuestionSealCandidate(ctx context.Context, c codexQuestionSealCandidate) error {
	r, p, bound := c.Record, c.Prepared, c.Bound
	if r.Version != 1 || r.InitialSegmentID != p.Origin.Scope.InitialSegmentID || !reflect.DeepEqual(r.ProtectedParents, c.Protected.Parents) || c.Protected.Path != filepath.Join(h.spoolRoot, p.Origin.Scope.AttemptID, bound.Current.SegmentID) || r.Origin != p.OriginFile || r.Thread != bound.File || r.Source != bound.Current || r.InitialSession != codexSessionEvidence(bound.InitialSession) || r.CurrentSession != codexSessionEvidence(bound.Session) || !codexHexDigest(r.QuestionPayloadSHA256) || !reflect.DeepEqual(r.State, c.Observation.Physical.State) || h.codexVerifyBoundThread(ctx, p, bound) != nil || codexVerifySealableObservation(ctx, c.Observation) != nil {
		return errCodexStateFile
	}
	if p.Previous == nil {
		if r.PreviousSHA256 != "" || r.PreviousSegmentID != "" || r.PreviousSeal != (codexStateFile{}) || r.QuestionRevision != 1 || !reflect.DeepEqual(c.Protected, p.Protected) {
			return errCodexStateFile
		}
	} else {
		previous := p.Previous.Segments[0]
		if len(p.Previous.Segments) >= codexSealChainLimit || r.PreviousSHA256 != previous.File.SHA256 || r.PreviousSeal != previous.File || r.PreviousSegmentID != previous.Record.Source.SegmentID || r.QuestionRevision != previous.Record.QuestionRevision+1 {
			return errCodexStateFile
		}
	}
	chain, err := codexOpenAnchoredStateRoot(ctx, c.Protected)
	if err != nil {
		return errCodexStateFile
	}
	chain.close()
	// Protected/source reads above may happen after the last auxiliary FD read.
	// Retain and rejoin every original physical metadata/absence observation;
	// never recapture a changed tree with a reset content budget.
	if codexVerifyManagedReferences(ctx, p) != nil || codexVerifyPhysicalObservation(ctx, c.Observation.Physical) != nil || ctx.Err() != nil {
		return errCodexStateFile
	}
	parents := map[string]codexStateDirectory{}
	for _, parent := range p.Protected.Parents {
		parents[parent.Path] = parent
	}
	if codexCheckPersistentSlotMetadata(ctx, p.Protected, "codex-origin.json", p.OriginFile, parents) != nil || codexCheckPersistentSlotMetadata(ctx, p.Protected, "codex-thread.json", bound.File, parents) != nil || codexVerifySourceReferences(ctx, p.Source) != nil || codexVerifyOwnedReferences(ctx, p.Layout.Home, p.Source, p.Origin.Owned) != nil || ctx.Err() != nil {
		return errCodexStateFile
	}
	return nil
}

func codexSessionEvidence(e contract.Event) codexSessionStamp {
	return codexSessionStamp{EventID: e.EventID, Sequence: e.Sequence, PayloadSHA256: e.PayloadHash}
}

func codexEncodeSeal(record codexSealRecord) ([]byte, error) {
	body, err := json.Marshal(record)
	if err != nil || len(body) > 16<<10 {
		return nil, errCodexStateFile
	}
	return body, nil
}
