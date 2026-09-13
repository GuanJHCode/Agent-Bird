package host

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/events"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Host struct {
	db             *store.DB
	spoolRoot      string
	mu             sync.Mutex
	active         map[string]*process.Handle
	activeBySeg    map[string]*process.Handle
	producerID     string
	sequenceMu     sync.Mutex
	sequence       int64
	sequenceLoaded bool
	publishedPath  string
	publishMu      sync.Mutex
	ledgerMu       sync.Mutex
	grantMu        sync.Mutex
	reservations   map[string]time.Time
	stopMu         sync.Mutex
	stopRequested  map[string]bool
	eventReady     chan struct{}
}

func New(db *store.DB, spoolRoot string) (*Host, error) {
	if db == nil {
		return nil, errors.New("store_required")
	}
	return newHost(db, spoolRoot, "")
}

// NewIPC creates a source Host which owns only process trees and a durable
// spool. It never opens or writes the coordinator database; Publish must
// receive a durable ACK before the spool cursor advances.
func NewIPC(spoolRoot, producerID string) (*Host, error) {
	if producerID == "" {
		return nil, errors.New("producer_id_required")
	}
	return newHost(nil, spoolRoot, producerID)
}

func newHost(db *store.DB, spoolRoot, producerID string) (*Host, error) {
	if !filepath.IsAbs(spoolRoot) {
		return nil, errors.New("spool_path_not_absolute")
	}
	if err := os.MkdirAll(spoolRoot, 0700); err != nil {
		return nil, err
	}
	return &Host{db: db, spoolRoot: spoolRoot, active: map[string]*process.Handle{}, activeBySeg: map[string]*process.Handle{}, producerID: producerID, publishedPath: filepath.Join(spoolRoot, ".producer-published"), reservations: map[string]time.Time{}, stopRequested: map[string]bool{}, eventReady: make(chan struct{}, 1)}, nil
}
func (h *Host) spool(a store.Attempt) (*events.Spool, error) {
	return events.Open(filepath.Join(h.spoolRoot, a.ID, a.SegmentID))
}

// ExecuteLaunch is the only Host entrypoint for a coordinator launch grant.
// Invocation remains local to the source Host; the grant carries ownership and
// budget metadata but never authentication or environment data. A reservation
// is consumed once and is retained until process restart so reconnects cannot
// reset its active budget.
func (h *Host) ExecuteLaunch(ctx context.Context, grant contract.LaunchCommand, inv contract.InvocationView) (contract.Result, error) {
	if grant.ReservationID == "" || grant.AttemptID == "" || grant.SegmentID == "" || grant.RunID == "" || grant.TaskID == "" || grant.GrantedActiveMS <= 0 || inv == nil || len(inv.Args()) == 0 || inv.Args()[0] == "" {
		return contract.Result{}, errors.New("invalid_launch_grant")
	}
	if err := h.validateLaunchSession(grant); err != nil {
		return contract.Result{}, err
	}
	now := time.Now()
	budgetDeadline := now.Add(time.Duration(grant.GrantedActiveMS) * time.Millisecond)
	if grant.DeadlineUnixMS > 0 {
		deadline := time.UnixMilli(grant.DeadlineUnixMS)
		if deadline.Before(budgetDeadline) {
			budgetDeadline = deadline
		}
	}
	if !budgetDeadline.After(now) {
		return contract.Result{}, errors.New("launch_deadline_expired")
	}
	h.grantMu.Lock()
	if _, exists := h.reservations[grant.ReservationID]; exists {
		h.grantMu.Unlock()
		return contract.Result{}, errors.New("reservation_reused")
	}
	h.reservations[grant.ReservationID] = budgetDeadline
	h.grantMu.Unlock()
	runCtx, cancel := context.WithDeadline(ctx, budgetDeadline)
	defer cancel()
	meta := launchMetadata{commandID: grant.CommandID, workRevision: grant.WorkRevision, executionEpoch: grant.ExecutionEpoch, questionRevision: grant.QuestionRevision}
	if structured, ok := inv.(interface{ OutputProvider() string }); ok {
		meta.outputProvider = structured.OutputProvider()
	}
	return h.execute(runCtx, store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID, Status: "running"}, grant.RunID, grant.TaskID, invocationCommand(inv), false, meta)
}

func invocationCommand(inv contract.InvocationView) process.Command {
	args := inv.Args()
	envMap := inv.Environment()
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}
	return process.Command{Path: args[0], Args: append([]string(nil), args[1:]...), Dir: inv.WorkingDirectory(), Env: env, Stdin: append([]byte(nil), inv.Stdin()...)}
}

func (h *Host) Execute(ctx context.Context, a store.Attempt, runID, taskID string, cmd process.Command) (contract.Result, error) {
	return h.execute(ctx, a, runID, taskID, cmd, true, launchMetadata{})
}

type launchMetadata struct {
	commandID        string
	workRevision     int
	executionEpoch   uint64
	outputProvider   string
	questionRevision int
}

func (h *Host) execute(ctx context.Context, a store.Attempt, runID, taskID string, cmd process.Command, persistDB bool, meta launchMetadata) (contract.Result, error) {
	spool, err := h.spool(a)
	if err != nil {
		return contract.Result{}, err
	}
	var protocol *protocolCollector
	if meta.outputProvider != "" {
		protocol, err = newProtocolCollector(meta.outputProvider)
		if err != nil {
			return contract.Result{}, err
		}
		cmd.Stdout = protocol
	}
	eventCount := 0
	appendPhase := func(kind, payloadHash string, identity *process.Identity, exitCode *int, artifact *contract.ArtifactRef) error {
		eventErr := func() error {
			event, appendErr := h.appendPhaseEvent(spool, runID, taskID, a, meta, kind, payloadHash, identity, exitCode, artifact)
			if appendErr == nil && event.Sequence > 0 {
				eventCount++
			}
			return appendErr
		}()
		return eventErr
	}
	finalizeStopped := func(identity *process.Identity, exit int, cause error) (contract.Result, error) {
		if err = publishLaunchPhase(spool, "stopped", a, runID, taskID, cmd, identity, cause); err != nil {
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount}, err
		}
		if err = appendPhase(contract.EventStopped, hashText("stopped:"+a.SegmentID), identity, &exit, nil); err != nil {
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount}, err
		}
		if err = publishLaunchPhase(spool, "exited", a, runID, taskID, cmd, identity, nil); err != nil {
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount}, err
		}
		if err = appendPhase(contract.EventExited, hashText("exited:"+a.SegmentID), identity, &exit, nil); err != nil {
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount}, err
		}
		h.finishAttempt(a.ID, "interrupted", persistDB)
		return contract.Result{Status: "interrupted", ExitCode: exit, EventCount: eventCount}, cause
	}

	startHash := hashText("segment_started:" + a.SegmentID)
	if err = publishLaunchPhase(spool, "prepared", a, runID, taskID, cmd, nil, nil); err != nil {
		h.finishAttempt(a.ID, "failed", persistDB)
		return contract.Result{Status: "failed", ExitCode: -1, EventCount: eventCount}, err
	}
	if err = appendPhase(contract.EventPrepared, startHash, nil, nil, nil); err != nil {
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, err
	}
	if h.stopWasRequested(a.SegmentID) {
		return finalizeStopped(nil, -1, context.Canceled)
	}
	p, err := process.Start(ctx, cmd)
	if err != nil {
		phaseErr := publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, nil, err)
		_ = appendPhase(contract.EventUnknown, hashText("unknown:"+a.SegmentID), nil, nil, nil)
		h.finishAttempt(a.ID, "unknown", persistDB)
		if phaseErr != nil {
			return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, phaseErr
		}
		return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, err
	}
	identity := p.Identity()
	if err = publishLaunchPhase(spool, "spawned", a, runID, taskID, cmd, &identity, nil); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.Stop(stopCtx)
		stopCancel()
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount}, err
	}
	if err = appendPhase(contract.EventSpawned, hashText("spawned:"+a.SegmentID), &identity, nil, nil); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.Stop(stopCtx)
		stopCancel()
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount}, err
	}
	if err = publishLaunchPhase(spool, "running", a, runID, taskID, cmd, &identity, nil); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.Stop(stopCtx)
		stopCancel()
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount}, err
	}
	if err = appendPhase(contract.EventRunning, hashText("running:"+a.SegmentID), &identity, nil, nil); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.Stop(stopCtx)
		stopCancel()
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount}, err
	}
	if protocol != nil {
		if err = protocol.SetSessionObserver(func(kind, id string) error {
			_, appendErr := h.appendStructuredEvent(spool, runID, taskID, a, meta, contract.EventSession, hashText("session:"+kind+":"+id), &identity, nil, nil, structuredEventFields{SessionKind: kind, SessionID: id})
			return appendErr
		}); err != nil {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = p.Stop(stopCtx)
			stopCancel()
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount}, err
		}
	}
	h.mu.Lock()
	h.active[a.ID] = p
	h.activeBySeg[a.SegmentID] = p
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.active, a.ID)
		delete(h.activeBySeg, a.SegmentID)
		h.mu.Unlock()
		h.clearStop(a.SegmentID)
	}()
	if h.stopWasRequested(a.SegmentID) {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.Stop(stopCtx)
		stopCancel()
	}
	waitErr := p.Wait(ctx)
	if waitErr != nil {
		stopErr := p.Stop(ctx)
		if stopErr != nil {
			if phaseErr := publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, &identity, stopErr); phaseErr != nil {
				stopErr = phaseErr
			}
			_ = appendPhase(contract.EventUnknown, hashText("unknown:"+a.SegmentID), &identity, nil, nil)
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount, OutputHash: hashText(p.Output())}, stopErr
		}
		exit := p.ExitCode()
		result, stoppedErr := finalizeStopped(&identity, exit, waitErr)
		result.OutputHash = hashText(p.Output())
		return result, stoppedErr
	}
	if err = p.ConfirmTreeExited(); err != nil {
		if phaseErr := publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, &identity, err); phaseErr != nil {
			err = phaseErr
		}
		if phaseErr := appendPhase(contract.EventUnknown, hashText("unknown:"+a.SegmentID), &identity, nil, nil); phaseErr != nil {
			err = phaseErr
		}
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount, OutputHash: hashText(p.Output())}, err
	}
	if h.stopWasRequested(a.SegmentID) || p.ExitCode() < 0 {
		return finalizeStopped(&identity, p.ExitCode(), context.Canceled)
	}
	if protocol != nil {
		exit := p.ExitCode()
		terminal, sessionID, protocolErr := protocol.Finish()
		kind, status := contract.EventResult, "result_ready"
		if protocolErr != nil || exit != 0 || (terminal.Kind != "question" && !providerSucceeded(protocol.provider, terminal)) {
			kind, status = contract.EventFailed, "failed"
		} else if terminal.Kind == "question" {
			kind, status = contract.EventQuestion, "waiting_user"
		}
		content := []byte(terminal.Text)
		var artifact *contract.ArtifactRef
		artifactPath := ""
		if len(content) > 0 {
			artifactPath, err = spool.WriteArtifact(content)
			if err != nil {
				h.finishAttempt(a.ID, "unknown", persistDB)
				return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount}, err
			}
			artifact = &contract.ArtifactRef{ID: filepath.Base(artifactPath), Path: artifactPath, Size: int64(len(content)), SHA256: hashBytes(content)}
		}
		payloadHash := hashText(kind + ":" + a.SegmentID)
		if artifact != nil {
			payloadHash = artifact.SHA256
		}
		fields := structuredEventFields{SessionKind: providerSessionKind(protocol.provider), SessionID: sessionID}
		if kind == contract.EventQuestion {
			fields.QuestionRevision = meta.questionRevision + 1
		}
		if _, err = h.appendStructuredEvent(spool, runID, taskID, a, meta, kind, payloadHash, &identity, &exit, artifact, fields); err != nil {
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount, OutputHash: payloadHash, ArtifactPath: artifactPath}, err
		}
		eventCount++
		if err = publishLaunchPhase(spool, "exited", a, runID, taskID, cmd, &identity, protocolErr); err != nil {
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount, OutputHash: payloadHash, ArtifactPath: artifactPath}, err
		}
		if err = appendPhase(contract.EventExited, hashText("exited:"+a.SegmentID), &identity, &exit, artifact); err != nil {
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount, OutputHash: payloadHash, ArtifactPath: artifactPath}, err
		}
		h.finishAttempt(a.ID, status, persistDB)
		return contract.Result{Status: status, ExitCode: exit, EventCount: eventCount, OutputHash: payloadHash, ArtifactPath: artifactPath}, nil
	}
	status := "result_ready"
	if p.ExitCode() < 0 {
		status = "interrupted"
	} else if p.ExitCode() != 0 {
		status = "failed"
	}
	exit := p.ExitCode()
	outHash := hashText(p.Output())
	artifactPath, err := spool.WriteArtifact([]byte(p.Output()))
	if err != nil {
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount, OutputHash: outHash}, err
	}
	artifact := &contract.ArtifactRef{ID: filepath.Base(artifactPath), Path: artifactPath, Size: int64(len(p.Output())), SHA256: outHash}
	terminalKind := contract.EventResult
	if status == "failed" {
		terminalKind = contract.EventFailed
	}
	if err = appendPhase(terminalKind, outHash, &identity, &exit, artifact); err != nil {
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount, OutputHash: outHash, ArtifactPath: artifactPath}, err
	}
	phase := "exited"
	if status == "interrupted" {
		phase = "stopped"
	}
	if err = publishLaunchPhase(spool, phase, a, runID, taskID, cmd, &identity, nil); err != nil {
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount, OutputHash: outHash, ArtifactPath: artifactPath}, err
	}
	if err = appendPhase(contract.EventExited, hashText("exited:"+a.SegmentID), &identity, &exit, artifact); err != nil {
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount, OutputHash: outHash, ArtifactPath: artifactPath}, err
	}
	if persistDB {
		if err = h.db.FinishAttempt(context.Background(), a.ID, status); err != nil {
			return contract.Result{}, err
		}
	}
	return contract.Result{Status: status, ExitCode: exit, EventCount: eventCount, OutputHash: outHash, ArtifactPath: artifactPath}, nil
}

func (h *Host) validateLaunchSession(grant contract.LaunchCommand) error {
	hasSession := grant.SessionKind != "" || grant.SessionID != ""
	needsSession := grant.QuestionID != "" || grant.QuestionRevision != 0 || grant.Answer != ""
	if needsSession && !hasSession {
		return errors.New("resume_session_required")
	}
	if !hasSession {
		return nil
	}
	if (grant.SessionKind != "session-id" && grant.SessionKind != "conversation-id") || grant.SessionID == "" {
		return errors.New("resume_session_invalid")
	}
	entries, err := h.spoolEntries()
	if err != nil {
		return err
	}
	foundKind, foundID := "", ""
	for _, entry := range entries {
		records, readErr := entry.spool.Read()
		if readErr != nil {
			return readErr
		}
		for _, record := range records {
			if record.AttemptID != grant.AttemptID || record.Kind != contract.EventSession {
				continue
			}
			if foundID != "" && (foundKind != record.SessionKind || foundID != record.SessionID) {
				return errors.New("resume_session_history_conflict")
			}
			foundKind, foundID = record.SessionKind, record.SessionID
		}
	}
	if foundID == "" {
		return errors.New("resume_session_unbound")
	}
	if foundKind != grant.SessionKind || foundID != grant.SessionID {
		return errors.New("resume_session_mismatch")
	}
	return nil
}

func (h *Host) appendPhaseEvent(spool *events.Spool, runID, taskID string, a store.Attempt, meta launchMetadata, kind, payloadHash string, identity *process.Identity, exitCode *int, artifact *contract.ArtifactRef) (contract.Event, error) {
	return h.appendStructuredEvent(spool, runID, taskID, a, meta, kind, payloadHash, identity, exitCode, artifact, structuredEventFields{})
}

type structuredEventFields struct {
	QuestionRevision int
	SessionKind      string
	SessionID        string
}

func (h *Host) appendStructuredEvent(spool *events.Spool, runID, taskID string, a store.Attempt, meta launchMetadata, kind, payloadHash string, identity *process.Identity, exitCode *int, artifact *contract.ArtifactRef, fields structuredEventFields) (contract.Event, error) {
	h.sequenceMu.Lock()
	defer h.sequenceMu.Unlock()
	if err := h.loadEventSequenceLocked(); err != nil {
		return contract.Event{}, err
	}
	sequence := h.sequence + 1
	event := contract.Event{Version: 1, ProducerID: h.producerID, RunID: runID, TaskID: taskID, AttemptID: a.ID, SegmentID: a.SegmentID, WorkRevision: meta.workRevision, ExecutionEpoch: meta.executionEpoch, CommandID: meta.commandID, Sequence: sequence, Kind: kind, PayloadHash: payloadHash, ExitCode: exitCode, Artifact: artifact, QuestionRevision: fields.QuestionRevision, SessionKind: fields.SessionKind, SessionID: fields.SessionID}
	if identity != nil {
		event.Process = &contract.ProcessIdentity{PID: identity.PID, Birth: identity.Birth, PGID: identity.PGID}
	}
	if h.producerID != "" {
		event.EventID = h.producerID + ":" + fmt.Sprint(sequence)
	}
	if kind == contract.EventQuestion {
		event.QuestionID = event.EventID + ":question"
	}
	if err := spool.Append(event); err != nil {
		return contract.Event{}, err
	}
	// The sequence becomes visible only after the event file and its directory
	// entry are durable. A crash can replay an event, but cannot reserve a hole.
	h.sequence = sequence
	h.notifyEventReady()
	return event, nil
}

func (h *Host) notifyEventReady() {
	select {
	case h.eventReady <- struct{}{}:
	default:
	}
}

func (h *Host) loadEventSequenceLocked() error {
	if h.sequenceLoaded {
		return nil
	}
	maximum := int64(0)
	entries, err := h.spoolEntries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		records, readErr := entry.spool.Read()
		if readErr != nil {
			return readErr
		}
		for _, record := range records {
			if (h.producerID != "" && record.ProducerID != h.producerID) || record.Sequence < 1 {
				return errors.New("event_identity_missing")
			}
			if record.Sequence > maximum {
				maximum = record.Sequence
			}
		}
	}
	if published, readErr := h.readPublished(); readErr != nil {
		return readErr
	} else if published > maximum {
		maximum = published
	}
	h.sequence, h.sequenceLoaded = maximum, true
	return nil
}

func (h *Host) ensureLaunchUnknown(a store.Attempt, runID, taskID string, meta launchMetadata, cause error) error {
	spool, err := h.spool(a)
	if err != nil {
		return err
	}
	existing, err := spool.Read()
	if err != nil {
		return err
	}
	for _, event := range existing {
		if event.Kind == contract.EventUnknown {
			return nil
		}
	}
	return h.recordLaunchUnknown(a, runID, taskID, meta, cause)
}

func (h *Host) recordLaunchUnknown(a store.Attempt, runID, taskID string, meta launchMetadata, cause error) error {
	spool, err := h.spool(a)
	if err != nil {
		return err
	}
	if err = publishLaunchPhase(spool, "unknown", a, runID, taskID, process.Command{}, nil, cause); err != nil {
		return err
	}
	_, err = h.appendPhaseEvent(spool, runID, taskID, a, meta, contract.EventUnknown, hashText("unknown:"+a.SegmentID), nil, nil, nil)
	return err
}

// recordLaunchFailed closes a grant rejected before its worker process was
// spawned. UNKNOWN remains reserved for uncertain process ownership.
func (h *Host) recordLaunchFailed(a store.Attempt, runID, taskID string, meta launchMetadata, cause error) error {
	spool, err := h.spool(a)
	if err != nil {
		return err
	}
	if err = publishLaunchPhase(spool, "failed", a, runID, taskID, process.Command{}, nil, cause); err != nil {
		return err
	}
	exit := -1
	if _, err = h.appendPhaseEvent(spool, runID, taskID, a, meta, contract.EventFailed, hashText("failed:"+a.SegmentID), nil, &exit, nil); err != nil {
		return err
	}
	if err = publishLaunchPhase(spool, "exited", a, runID, taskID, process.Command{}, nil, nil); err != nil {
		return err
	}
	_, err = h.appendPhaseEvent(spool, runID, taskID, a, meta, contract.EventExited, hashText("exited:"+a.SegmentID), nil, &exit, nil)
	return err
}

type grantLedgerRecord struct {
	Version       int    `json:"version"`
	CommandID     string `json:"command_id"`
	ReservationID string `json:"reservation_id"`
	AttemptID     string `json:"attempt_id"`
	SegmentID     string `json:"segment_id"`
	GrantSHA256   string `json:"grant_sha256"`
}

func (h *Host) acceptGrant(grant contract.LaunchCommand) (bool, error) {
	if grant.CommandID == "" || grant.ReservationID == "" || grant.AttemptID == "" || grant.SegmentID == "" {
		return false, errors.New("invalid_launch_grant")
	}
	identityGrant := grant
	identityGrant.ExecutionEpoch = 0
	body, err := json.Marshal(identityGrant)
	if err != nil {
		return false, err
	}
	record := grantLedgerRecord{Version: 1, CommandID: grant.CommandID, ReservationID: grant.ReservationID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, GrantSHA256: hashBytes(body)}
	recordBody, _ := json.Marshal(record)
	recordBody = append(recordBody, '\n')

	h.ledgerMu.Lock()
	defer h.ledgerMu.Unlock()
	dir := filepath.Join(h.spoolRoot, ".grants")
	if err = os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return false, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return false, errors.New("grant_ledger_unsafe")
	}
	path := filepath.Join(dir, hashText(grant.CommandID)+".json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil || string(existing) != string(recordBody) {
			return false, errors.New("grant_identity_conflict")
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = file.Write(recordBody); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	err = directory.Sync()
	_ = directory.Close()
	return false, err
}

func (h *Host) recoverDuplicateGrant(grant contract.LaunchCommand, epoch uint64) error {
	a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
	spool, err := h.spool(a)
	if err != nil {
		return err
	}
	records, err := spool.Read()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.CommandID != grant.CommandID {
			return errors.New("grant_event_identity_conflict")
		}
		if record.Kind == contract.EventExited {
			return nil
		}
	}
	meta := launchMetadata{commandID: grant.CommandID, workRevision: grant.WorkRevision, executionEpoch: epoch}
	if len(records) == 0 {
		if err = h.recordLaunchFailed(a, grant.RunID, grant.TaskID, meta, errors.New("recovered_unspawned_grant")); err != nil {
			return err
		}
	} else if err = h.ensureLaunchUnknown(a, grant.RunID, grant.TaskID, meta, errors.New("recovered_incomplete_grant")); err != nil {
		return err
	}
	return nil
}

func (h *Host) finishAttempt(id, status string, persistDB bool) {
	if persistDB && h.db != nil {
		_ = h.db.FinishAttempt(context.Background(), id, status)
	}
}

func publishLaunchPhase(spool *events.Spool, phase string, a store.Attempt, runID, taskID string, cmd process.Command, identity *process.Identity, cause error) error {
	argsHash := hashBytes([]byte(strings.Join(cmd.Args, "\x00")))
	record := map[string]any{
		"version": 1, "phase": phase, "run_id": runID, "task_id": taskID,
		"attempt_id": a.ID, "segment_id": a.SegmentID, "executable": cmd.Path,
		"args_sha256": argsHash, "started_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if identity != nil {
		record["process"] = map[string]any{"pid": identity.PID, "pgid": identity.PGID, "birth": identity.Birth, "birth_known": identity.BirthKnown, "executable": identity.Executable, "executable_sha256": identity.ExecutableSHA256, "started_at": identity.StartedAt}
	}
	if cause != nil {
		record["cause_code"] = stableCause(cause)
	}
	return spool.WriteMetadata("launch.json", record)
}

func hashBytes(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func stableCause(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, process.ErrProcessTreeUnknown) {
		return "process_tree_unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "process_failed"
}
func (h *Host) Collect(ctx context.Context, a store.Attempt) ([]contract.Event, error) {
	_ = ctx
	s, err := h.spool(a)
	if err != nil {
		return nil, err
	}
	return s.Read()
}
func (h *Host) Ack(ctx context.Context, a store.Attempt) error {
	s, err := h.spool(a)
	if err != nil {
		return err
	}
	ev, err := s.Read()
	if err != nil {
		return err
	}
	if len(ev) == 0 {
		return errors.New("no_events")
	}
	if err = h.db.AcknowledgeAttempt(ctx, a.ID); err != nil {
		return err
	}
	return s.AckThrough(ev[len(ev)-1].Sequence)
}
func hashText(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func (h *Host) ExecuteInvocation(ctx context.Context, a store.Attempt, runID, taskID string, inv contract.InvocationView) (contract.Result, error) {
	args := inv.Args()
	if len(args) == 0 || args[0] == "" {
		return contract.Result{}, errors.New("invocation_args_invalid")
	}
	return h.Execute(ctx, a, runID, taskID, invocationCommand(inv))
}

func (h *Host) Cancel(ctx context.Context, a store.Attempt) error {
	h.mu.Lock()
	p := h.active[a.ID]
	h.mu.Unlock()
	if p == nil {
		return h.db.FinishAttempt(ctx, a.ID, "cancelled")
	}
	if err := p.Stop(ctx); err != nil {
		_ = h.db.FinishAttempt(context.Background(), a.ID, "unknown")
		return err
	}
	return nil
}

// StopSegment stops only the process group owned by segment. It never signals
// a caller-selected PID or a process outside the Host registry.
func (h *Host) StopSegment(ctx context.Context, segmentID string) error {
	if segmentID == "" {
		return errors.New("segment_required")
	}
	h.stopMu.Lock()
	h.stopRequested[segmentID] = true
	h.stopMu.Unlock()
	h.mu.Lock()
	p := h.activeBySeg[segmentID]
	h.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.Stop(ctx)
}

func (h *Host) stopWasRequested(segmentID string) bool {
	h.stopMu.Lock()
	defer h.stopMu.Unlock()
	return h.stopRequested[segmentID]
}

func (h *Host) clearStop(segmentID string) {
	h.stopMu.Lock()
	delete(h.stopRequested, segmentID)
	h.stopMu.Unlock()
}

// StopAll is used when the source IPC connection ends. Each registered handle
// is bounded by the caller's context; an unprovable tree stop remains unknown.
func (h *Host) StopAll(ctx context.Context) {
	h.mu.Lock()
	processes := make([]*process.Handle, 0, len(h.activeBySeg))
	segments := make([]string, 0, len(h.activeBySeg))
	seen := make(map[*process.Handle]struct{}, len(h.activeBySeg))
	for segmentID, p := range h.activeBySeg {
		segments = append(segments, segmentID)
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			processes = append(processes, p)
		}
	}
	h.mu.Unlock()
	h.stopMu.Lock()
	for _, segmentID := range segments {
		h.stopRequested[segmentID] = true
	}
	h.stopMu.Unlock()
	for _, p := range processes {
		_ = p.Stop(ctx)
	}
}
