package host

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"github.com/pelletier/go-toml/v2"
)

type codexRecordedBinding struct {
	ThreadID    string `json:"thread_id"`
	Model       string `json:"model"`
	Provider    string `json:"provider"`
	RolloutPath string `json:"rollout_path"`
	HistoryMode string `json:"history_mode"`
}

type codexLaunchProvenance struct {
	SegmentID        string                   `json:"segment_id"`
	CommandID        string                   `json:"command_id"`
	ProducerID       string                   `json:"producer_id"`
	Epoch            uint64                   `json:"execution_epoch"`
	Process          contract.ProcessIdentity `json:"process"`
	Executable       string                   `json:"executable"`
	ExecutableSHA256 string                   `json:"executable_sha256"`
}

// The immutable record precedes the first Session append. It binds that
// command/producer/process provenance, without inventing an event ID before the
// real durable append. A seal separately records the returned Session event.
type codexThreadRecord struct {
	Version int                   `json:"version"`
	Origin  codexStateFile        `json:"origin"`
	Binding codexRecordedBinding  `json:"binding"`
	First   codexLaunchProvenance `json:"first"`
}

type codexBoundThread struct {
	Record         codexThreadRecord
	File           codexStateFile
	Session        contract.Event
	InitialSession contract.Event
	Current        codexLaunchProvenance
}

func (h *Host) bindManagedCodexThread(ctx context.Context, p codexPreparedState, g contract.LaunchCommand, identity process.Identity, binding codexsession.ThreadBinding) (codexBoundThread, error) {
	if g.Continuation != nil {
		return h.bindResumedCodexThread(ctx, p, g, identity, binding)
	}
	var empty codexBoundThread
	if g.Continuation != nil || g.SessionID != "" || codexVerifyManagedReferences(ctx, p) != nil {
		return empty, errCodexStateBinding
	}
	h.mu.Lock()
	owner := h.continuationOwner
	h.mu.Unlock()
	scope, err := codexScopeForGrant(g, owner)
	if err != nil || scope != p.Origin.Scope || !identity.BirthKnown || p.Launch.Path != "/usr/bin/sandbox-exec" || !codexHexDigest(p.Launch.SHA256) || identity.Executable != p.Launch.Path || identity.ExecutableSHA256 != p.Launch.SHA256 {
		return empty, errCodexStateBinding
	}
	first := codexLaunchProvenance{SegmentID: g.SegmentID, CommandID: g.CommandID, ProducerID: h.producerID, Epoch: g.ExecutionEpoch, Process: contract.ProcessIdentity{PID: identity.PID, PGID: identity.PGID, Birth: identity.Birth}, Executable: identity.Executable, ExecutableSHA256: identity.ExecutableSHA256}
	if !codexValidLaunchProvenance(first) || codexCheckManagedBinding(p, binding) != nil {
		return empty, errCodexStateBinding
	}
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		return empty, errCodexStateBinding
	}
	records, err := spool.Read()
	if err != nil || codexCheckRunningProvenance(records, p.Origin.Scope, first) != nil {
		return empty, errCodexStateBinding
	}
	record := codexThreadRecord{Version: 1, Origin: p.OriginFile, Binding: codexBindingRecord(binding), First: first}
	body, err := json.Marshal(record)
	if err != nil || len(body) > 8<<10 {
		return empty, errCodexStateBinding
	}
	file, err := codexWriteProtectedRecord(ctx, p.Protected, "codex-thread.json", body)
	if err != nil {
		return empty, errCodexStateBinding
	}
	// Join the protected and source reference observations before Session/turn;
	// failed/partial publication remains evidence and is never adopted.
	var actual codexThreadRecord
	checked, err := codexReadProtectedRecord(ctx, p.Protected, "codex-thread.json", &actual)
	if err != nil || checked != file || actual != record || codexVerifyManagedReferences(ctx, p) != nil || ctx.Err() != nil {
		return empty, errCodexStateBinding
	}
	a := store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}
	meta := launchMetadata{workRevision: g.WorkRevision, executionEpoch: g.ExecutionEpoch, commandID: g.CommandID}
	session, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, a, meta, contract.EventSession, hashText("session:session-id:"+binding.ThreadID), &identity, nil, nil, structuredEventFields{SessionKind: "session-id", SessionID: binding.ThreadID})
	if err != nil || ctx.Err() != nil {
		return empty, errCodexStateBinding
	}
	return codexBoundThread{Record: record, File: file, Session: session, InitialSession: session, Current: first}, nil
}

func (h *Host) bindResumedCodexThread(ctx context.Context, p codexPreparedState, g contract.LaunchCommand, identity process.Identity, binding codexsession.ThreadBinding) (codexBoundThread, error) {
	var empty codexBoundThread
	if h.codexCheckContinuationGrant(p, g) != nil || h.codexVerifyContinuationRecords(ctx, p) != nil || binding != p.Previous.Thread.Binding.native() || !identity.BirthKnown || identity.Executable != p.Launch.Path || identity.ExecutableSHA256 != p.Launch.SHA256 || codexCheckManagedBinding(p, binding) != nil {
		return empty, errCodexStateBinding
	}
	current := codexLaunchProvenance{SegmentID: g.SegmentID, CommandID: g.CommandID, ProducerID: h.producerID, Epoch: g.ExecutionEpoch, Process: contract.ProcessIdentity{PID: identity.PID, PGID: identity.PGID, Birth: identity.Birth}, Executable: identity.Executable, ExecutableSHA256: identity.ExecutableSHA256}
	if !codexValidLaunchProvenance(current) {
		return empty, errCodexStateBinding
	}
	spool, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID})
	if err != nil {
		return empty, errCodexStateBinding
	}
	records, err := spool.Read()
	if err != nil || codexCheckRunningProvenance(records, p.Origin.Scope, current) != nil {
		return empty, errCodexStateBinding
	}
	// The original protected binding is verified, never overwritten or copied
	// into a new scope. Only this segment's actual native Session is appended.
	meta := launchMetadata{workRevision: g.WorkRevision, executionEpoch: g.ExecutionEpoch, commandID: g.CommandID}
	session, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}, meta, contract.EventSession, hashText("session:session-id:"+binding.ThreadID), &identity, nil, nil, structuredEventFields{SessionKind: "session-id", SessionID: binding.ThreadID})
	if err != nil || ctx.Err() != nil {
		return empty, errCodexStateBinding
	}
	initial := p.Previous.Segments[len(p.Previous.Segments)-1].Session
	return codexBoundThread{Record: p.Previous.Thread, File: p.Previous.ThreadFile, Session: session, InitialSession: initial, Current: current}, nil
}

func codexBindingRecord(b codexsession.ThreadBinding) codexRecordedBinding {
	return codexRecordedBinding{ThreadID: b.ThreadID, Model: b.Model, Provider: b.ModelProvider, RolloutPath: b.RolloutPath, HistoryMode: b.HistoryMode}
}
func (b codexRecordedBinding) native() codexsession.ThreadBinding {
	return codexsession.ThreadBinding{ThreadID: b.ThreadID, Model: b.Model, ModelProvider: b.Provider, RolloutPath: b.RolloutPath, HistoryMode: b.HistoryMode}
}

func codexCheckManagedBinding(p codexPreparedState, b codexsession.ThreadBinding) error {
	if _, err := codexManagedRolloutRelative(p.Layout.Home.Path, b); err != nil {
		return errCodexStateBinding
	}
	var config map[string]any
	if p.Projection == nil || toml.Unmarshal(p.Projection.ConfigTOML(), &config) != nil || config["sandbox_mode"] != "read-only" {
		return errCodexStateBinding
	}
	if model, present := config["model"]; present && model != b.Model {
		return errCodexStateBinding
	}
	return nil
}

func codexVerifyManagedReferences(ctx context.Context, p codexPreparedState) error {
	if ctx == nil || ctx.Err() != nil {
		return errCodexStateFile
	}
	sqlite, log, bound := p.Projection.RuntimePaths()
	if !bound || sqlite != p.Layout.SQLite.Path || log != p.Layout.Auxiliary.Runtime[3].Path || codexDigest(p.Projection.ConfigTOML()) != p.Source.ProjectedSHA256 {
		return errCodexStateFile
	}
	layout, source, protected, err := codexLayoutFromOrigin(ctx, p.Origin)
	if err != nil || !reflect.DeepEqual(layout, p.Layout) || !reflect.DeepEqual(source, p.Source) || !reflect.DeepEqual(protected, p.Protected) {
		return errCodexStateFile
	}
	var origin codexOriginRecord
	file, err := codexReadProtectedRecord(ctx, protected, "codex-origin.json", &origin)
	if err != nil || file != p.OriginFile || !reflect.DeepEqual(origin, p.Origin) {
		return errCodexStateFile
	}
	// Native runtime state can change during the admitted turn. Only the
	// original immutable/reference scope is checked here; post-exit seals own
	// the bounded complete runtime inventory and content hashes.
	if codexVerifySourceReferences(ctx, source) != nil || codexVerifyOwnedReferences(ctx, layout.Home, source, p.Origin.Owned) != nil || ctx.Err() != nil {
		return errCodexStateFile
	}
	return nil
}

func codexValidLaunchProvenance(p codexLaunchProvenance) bool {
	return codexSafeScopeID(p.SegmentID) && codexSafeScopeID(p.CommandID) && codexBoundedIdentity(p.ProducerID) && p.Epoch > 0 && p.Process.PID > 0 && p.Process.PGID == p.Process.PID && codexBoundedIdentity(p.Process.Birth) && p.Executable == "/usr/bin/sandbox-exec" && codexHexDigest(p.ExecutableSHA256)
}

func codexEventMatchesLaunch(e contract.Event, s codexOriginScope, p codexLaunchProvenance) bool {
	return e.Version == 1 && e.EventID != "" && e.Sequence > 0 && e.RunID == s.RunID && e.TaskID == s.TaskID && e.AttemptID == s.AttemptID && e.SegmentID == p.SegmentID && e.CommandID == p.CommandID && e.ProducerID == p.ProducerID && e.ExecutionEpoch == p.Epoch && e.WorkRevision == s.WorkRevision && e.Process != nil && *e.Process == p.Process
}

func codexCheckRunningProvenance(records []contract.Event, s codexOriginScope, p codexLaunchProvenance) error {
	var spawned, running int64
	for _, e := range records {
		if e.AttemptID != s.AttemptID || e.SegmentID != p.SegmentID {
			return errCodexStateBinding
		}
		switch e.Kind {
		case contract.EventSpawned:
			if spawned != 0 || !codexEventMatchesLaunch(e, s, p) {
				return errCodexStateBinding
			}
			spawned = e.Sequence
		case contract.EventRunning:
			if running != 0 || !codexEventMatchesLaunch(e, s, p) {
				return errCodexStateBinding
			}
			running = e.Sequence
		case contract.EventSession, contract.EventQuestion, contract.EventResult, contract.EventFailed, contract.EventStopped, contract.EventUnknown, contract.EventExited, contract.EventLaunchRejected:
			return errCodexStateBinding
		}
	}
	if spawned == 0 || running <= spawned {
		return errCodexStateBinding
	}
	return nil
}
