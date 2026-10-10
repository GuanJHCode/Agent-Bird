package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// gracefulStopWindow bounds the protocol §6 cancellation wait after the budget
// context itself has already expired.
const gracefulStopWindow = 10 * time.Second

type Host struct {
	db                *store.DB
	spoolRoot         string
	mu                sync.Mutex
	active            map[string]*process.Handle
	activeBySeg       map[string]*process.Handle
	segmentCancels    map[string]context.CancelFunc
	producerID        string
	sequenceMu        sync.Mutex
	sequence          int64
	sequenceLoaded    bool
	publishedPath     string
	publishMu         publicationGate
	ledgerMu          sync.Mutex
	grantMu           sync.Mutex
	reservations      map[string]time.Time
	stopMu            sync.Mutex
	stopRequested     map[string]bool
	stopIntents       map[string]*segmentStopIntent
	stopAllIntent     *segmentStopIntent
	naturalGeneration uint64
	stopping          bool
	eventReady        chan struct{}
	progressSink      func(context.Context, contract.Event) error
	continuationOwner string
	codexLeases       map[string]*codexHandleLease           // CommandID; under stopMu + mu
	failedStarts      map[*process.Handle]*hostStopOperation // original ownership after Start error; under stopMu
	// Private observation boundary, fixed before execution starts. Production
	// leaves it nil; tests can distinguish durable append from caller return.
	afterSpoolAppend func(contract.Event)
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
	return &Host{db: db, spoolRoot: spoolRoot, active: map[string]*process.Handle{}, activeBySeg: map[string]*process.Handle{}, segmentCancels: map[string]context.CancelFunc{}, producerID: producerID, publishedPath: filepath.Join(spoolRoot, ".producer-published"), reservations: map[string]time.Time{}, stopRequested: map[string]bool{}, stopIntents: map[string]*segmentStopIntent{}, eventReady: make(chan struct{}, 1)}, nil
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
	return h.executeLaunch(ctx, grant, inv, codexExecuteControl{})
}

// The public entry always supplies zero control. Private per-call actor
// coordination has no environment/request/wire selector or alternate launcher.
func (h *Host) executeLaunch(ctx context.Context, grant contract.LaunchCommand, inv contract.InvocationView, control codexExecuteControl) (contract.Result, error) {
	if err := contract.RequireHostArtifacts(grant); err != nil {
		return contract.Result{}, err
	}
	if h.producerID != grant.HostID {
		return contract.Result{}, contract.ErrHostArtifactGrant
	}
	var filtered *filterRuntime
	var filterPayload adapter.InvocationPayload
	manifest, filterErr := contract.GitFilterManifestFromPayload(grant.AdapterPayload)
	if filterErr != nil {
		return contract.Result{}, filterErr
	}
	if grant.GitFilter != nil || manifest != nil {
		var err error
		filtered, err = newFilterRuntime(grant)
		if err != nil {
			return contract.Result{}, err
		}
		grant = filtered.grant
		filterPayload, err = admitFilterLaunch(grant)
		if err != nil {
			return contract.Result{}, err
		}
		inv, err = freezeFilterInvocation(inv, filterPayload)
		if err != nil {
			return contract.Result{}, err
		}
	}
	meta := metadataForLaunch(grant)
	meta.filter = filtered
	if err := contract.ValidateDependencyInputs(grant); err != nil {
		return contract.Result{}, err
	}
	filterObservation := filtered != nil && adapter.IsFilterTargetPreflight(filterPayload)
	if grant.ReservationID == "" || grant.AttemptID == "" || grant.SegmentID == "" || grant.RunID == "" || grant.TaskID == "" || grant.GrantedActiveMS <= 0 || inv == nil || !filterObservation && (len(inv.Args()) == 0 || inv.Args()[0] == "") {
		return contract.Result{}, errors.New("invalid_launch_grant")
	}
	managedInv, privateCodex := inv.(adapter.Invocation)
	if privateCodex {
		_, privateCodex = adapter.ManagedCodexDescriptorFor(managedInv)
	}
	if provider, ok := inv.(interface{ OutputProvider() string }); ok && provider.OutputProvider() == string(adapter.ProviderCodex) {
		if mode, ok := inv.(interface {
			TaskCompletionMode() adapter.TaskCompletionMode
		}); ok && mode.TaskCompletionMode() == adapter.TaskCompletionV1 && !privateCodex {
			return contract.Result{}, errors.New("codex_managed_invocation_required")
		}
	}
	if control.validationStarter != nil {
		actionInv, ok := inv.(interface {
			CandidateAction() *adapter.CandidateAction
		})
		payload, err := adapter.ValidationRetryPayload(grant.AdapterPayload)
		if privateCodex || !ok || actionInv.CandidateAction() == nil || actionInv.CandidateAction().Operation != "validate" || err != nil || contract.RequireValidationTool(grant, payload.CandidateAction.CommandSHA256) != nil {
			return contract.Result{}, errors.New("validation_start_control_invalid")
		}
	}
	if privateCodex {
		if grant.GrantedActiveMS > int64((1<<63-1)/time.Millisecond) {
			return contract.Result{}, errors.New("invalid_launch_grant")
		}
	} else {
		if control.beforeStart != nil || control.beforeSealFinalVerify != nil || control.actor != (codexActorCommand{}) {
			return contract.Result{}, errors.New("codex_actor_control_invalid")
		}
		if err := h.validateLaunchSession(grant); err != nil {
			return contract.Result{}, err
		}
		if err := h.recordContinuationOrigin(grant, inv); err != nil {
			return contract.Result{}, err
		}
	}
	now := time.Now()
	budgetDeadline := now.Add(time.Duration(grant.GrantedActiveMS) * time.Millisecond)
	deadlineReason := "active_budget_exhausted"
	var profile *adapter.ExecutionProfile
	if profiled, ok := inv.(interface {
		ExecutionProfile() *adapter.ExecutionProfile
	}); ok {
		profile = profiled.ExecutionProfile()
	}
	if profile != nil {
		if profile.TimeoutMS < 1 || profile.TimeoutMS > 3_600_000 {
			return contract.Result{}, errors.New("profile_timeout_invalid")
		}
		if deadline := now.Add(time.Duration(profile.TimeoutMS) * time.Millisecond); deadline.Before(budgetDeadline) {
			budgetDeadline = deadline
			deadlineReason = "profile_timeout"
		}
	}
	if grant.DeadlineUnixMS > 0 {
		deadline := time.UnixMilli(grant.DeadlineUnixMS)
		if deadline.Before(budgetDeadline) {
			budgetDeadline = deadline
			deadlineReason = "task_deadline_exceeded"
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
	runCtx, cancel := context.WithDeadlineCause(ctx, budgetDeadline, deadlineCause(deadlineReason))
	defer cancel()
	if structured, ok := inv.(interface{ OutputProvider() string }); ok {
		meta.outputProvider = structured.OutputProvider()
	}
	if completion, ok := inv.(interface {
		TaskCompletionMode() adapter.TaskCompletionMode
	}); ok {
		meta.completionMode = completion.TaskCompletionMode()
		if meta.completionMode != adapter.TaskCompletionNone && meta.completionMode != adapter.TaskCompletionV1 {
			return contract.Result{}, errors.New("task_completion_mode_invalid")
		}
		if meta.completionMode == adapter.TaskCompletionV1 {
			if profile == nil || (!privateCodex && meta.outputProvider != "claude-code" && meta.outputProvider != "antigravity-cli") {
				return contract.Result{}, errors.New("task_completion_mode_invalid")
			}
			meta.requireSession = true
		}
	}
	if privateCodex {
		return h.executeManagedCodex(runCtx, grant, managedInv, meta, control)
	}
	if grant.Continuation != nil && (meta.outputProvider == "claude-code" || meta.outputProvider == "antigravity-cli") {
		meta.expectedSessionID = grant.SessionID
		meta.requireSession = true
	}
	if meta.outputProvider == "antigravity-cli" && meta.completionMode == adapter.TaskCompletionV1 {
		p, decodeErr := adapter.DecodeInvocationPayload(grant.AdapterPayload)
		if decodeErr == nil && p.ProviderLock != nil && p.ProviderLock.Binary.Version == agyHistoryVersion && p.ProviderLock.Binary.SHA256 == agyHistorySHA256 {
			meta.agyHistoryGrant = &grant
		}
	}
	cmd := process.Command{}
	if !filterObservation {
		cmd = invocationCommand(inv)
	}
	if filterObservation {
		meta.runFilterObservation = func(ctx context.Context) ([]byte, error) {
			return h.observeFilteredTarget(ctx, filtered, filterPayload)
		}
		return h.execute(runCtx, store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID, Status: "running"}, grant.RunID, grant.TaskID, cmd, false, meta)
	}
	if meta.completionMode == adapter.TaskCompletionV1 {
		// Clear both inherited and wrapped helper authority before sandbox
		// preparation. The empty overrides also win in process mergeEnv.
		env := cmd.Env[:0]
		for _, entry := range cmd.Env {
			if !strings.HasPrefix(entry, "ORCHESTRATOR_REPORT_EXECUTABLE=") && !strings.HasPrefix(entry, "ORCHESTRATOR_REPORT_CAPABILITY=") {
				env = append(env, entry)
			}
		}
		cmd.Env = append(env, "ORCHESTRATOR_REPORT_EXECUTABLE=", "ORCHESTRATOR_REPORT_CAPABILITY=")
	}
	if value, ok := inv.(interface {
		CandidateAction() *adapter.CandidateAction
	}); ok && value.CandidateAction() != nil {
		action := value.CandidateAction()
		meta.integrationAction = action.Operation == "integrate"
		if _, err := adapter.ValidationRetryPayload(grant.AdapterPayload); action.Operation == "validate" && err == nil {
			meta.validationGrant = &grant
			if control.validationStarter != nil {
				meta.startProcess = func(ctx context.Context, cmd process.Command) (*process.Handle, error) {
					return process.StartWithCommandStarter(ctx, cmd, control.validationStarter)
				}
			}
		}
		meta.structuredReview = action.Operation == "review" && (meta.outputProvider == string(adapter.ProviderClaude) || meta.outputProvider == string(adapter.ProviderAGY) || meta.outputProvider == string(adapter.ProviderGrok))
		if action.Operation == "review" && meta.outputProvider == string(adapter.ProviderGrok) {
			meta.expectedSessionID = grokSessionID(grant)
		}
		meta.prepareAction = func(ctx context.Context) (process.Command, actionFinalizer, func(), error) {
			if filtered != nil {
				if filterPayload.CandidateAction.Operation == "review" {
					return h.prepareFilteredReview(ctx, filtered, inv, filterPayload)
				}
				return h.prepareFilteredValidation(ctx, filtered, inv, filterPayload)
			}
			return h.prepareCandidateAction(ctx, grant, inv, action, profile)
		}
		return h.execute(runCtx, store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID, Status: "running"}, grant.RunID, grant.TaskID, cmd, false, meta)
	}
	if managed, ok := inv.(interface {
		CandidateWorkspace() *adapter.CandidateWorkspace
	}); ok && managed.CandidateWorkspace() != nil {
		cmd.Dir = candidateDirectory(cmd.Dir, grant, managed.CandidateWorkspace().AutoDirectory)
		if profile != nil && profile.GrokSessionWrite {
			if meta.outputProvider != string(adapter.ProviderGrok) {
				return contract.Result{}, errors.New("profile_provider_state_mismatch")
			}
			meta.expectedSessionID = grokSessionID(grant)
		}
		meta.prepareCandidate = func(ctx context.Context) (process.Command, func(context.Context, string) ([]byte, error), func(), error) {
			var freeze func(context.Context, string) ([]byte, error)
			var closeCandidate func()
			var prepared *gitops.PreparedWorkspace
			var err error
			if filtered != nil {
				freeze, closeCandidate, prepared, err = h.prepareFilteredCandidate(ctx, filtered, filterPayload)
			} else {
				freeze, closeCandidate, prepared, err = h.prepareCandidate(ctx, grant, inv, profile)
			}
			if err != nil {
				return cmd, nil, closeCandidate, err
			}
			scratch := filepath.Join(h.spoolRoot, grant.AttemptID, grant.SegmentID, "scratch")
			var wrapped process.Command
			cmd.Dir = prepared.Receipt.Worktree
			if profile != nil && profile.GrokSessionWrite {
				cmd.Dir = prepared.Receipt.Worktree
				cmd, err = authorizeGrokEdits(cmd, profile, prepared)
				if err == nil {
					wrapped, err = prepareAuthenticatedGrokCommand(ctx, cmd, profile, grant, scratch)
				}
			} else {
				wrapped, err = h.sandboxProfileCommand(ctx, cmd, profile, scratch, grant)
			}
			return wrapped, freeze, closeCandidate, err
		}
	} else if profile != nil && profile.Permission == adapter.WorkspaceWrite {
		return contract.Result{}, errors.New("managed_workspace_required")
	} else if profile != nil && profile.GrokSessionWrite {
		if meta.outputProvider != string(adapter.ProviderGrok) {
			return contract.Result{}, errors.New("profile_provider_state_mismatch")
		}
		meta.expectedSessionID = grokSessionID(grant)
		meta.prepareProvider = func(ctx context.Context) (process.Command, error) {
			return prepareAuthenticatedGrokCommand(ctx, cmd, profile, grant, filepath.Join(h.spoolRoot, grant.AttemptID, grant.SegmentID, "scratch"))
		}
	} else if profile != nil {
		meta.prepareProvider = func(ctx context.Context) (process.Command, error) {
			return h.sandboxProfileCommand(ctx, cmd, profile, filepath.Join(h.spoolRoot, grant.AttemptID, grant.SegmentID, "scratch"), grant)
		}
	}
	return h.execute(runCtx, store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID, Status: "running"}, grant.RunID, grant.TaskID, cmd, false, meta)
}

func invocationCommand(inv contract.InvocationView) process.Command {
	args := inv.Args()
	envMap := inv.Environment()
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}
	var pinnedPath, pinnedSHA string
	if pinned, ok := inv.(interface{ ExecutablePin() (string, string) }); ok {
		pinnedPath, pinnedSHA = pinned.ExecutablePin()
	}
	return process.Command{PinnedPath: pinnedPath, PinnedSHA256: pinnedSHA, Path: args[0], Args: append([]string(nil), args[1:]...), Dir: inv.WorkingDirectory(), Env: env, Stdin: append([]byte(nil), inv.Stdin()...)}
}

func (h *Host) Execute(ctx context.Context, a store.Attempt, runID, taskID string, cmd process.Command) (contract.Result, error) {
	return contract.Result{}, errors.New("managed_launch_grant_required")
}

type launchMetadata struct {
	runFilterObservation     func(context.Context) ([]byte, error)
	afterObservationArtifact func(*contract.ArtifactRef) // original publication boundary; test seam
	filter                   *filterRuntime
	hasGitFilter             bool
	validationGrant          *contract.LaunchCommand
	artifactGrant            *contract.LaunchCommand
	diagnosticsGrant         *contract.LaunchCommand
	startProcess             func(context.Context, process.Command) (*process.Handle, error)
	agyHistoryGrant          *contract.LaunchCommand
	completionMode           adapter.TaskCompletionMode
	prepareProvider          func(context.Context) (process.Command, error)
	afterWait                func(*process.Handle, context.Context, error)
	afterNoStop              func(*process.Handle, context.Context)
	beforeStopJoin           func(*hostStopOperation)
	expectedSessionID        string
	requireSession           bool
	integrationAction        bool
	structuredReview         bool
	prepareAction            func(context.Context) (process.Command, actionFinalizer, func(), error)
	finalizeAction           actionFinalizer
	prepareCandidate         func(context.Context) (process.Command, func(context.Context, string) ([]byte, error), func(), error)
	freezeCandidate          func(context.Context, string) ([]byte, error)
	commandID                string
	workRevision             int
	executionEpoch           uint64
	outputProvider           string
	questionRevision         int
}

func (h *Host) execute(ctx context.Context, a store.Attempt, runID, taskID string, cmd process.Command, persistDB bool, meta launchMetadata) (result contract.Result, returnedErr error) {
	defer h.clearStop(a.SegmentID)
	ctx, cancelCause := context.WithCancelCause(ctx)
	cancelSegment := func() { cancelCause(context.Canceled) }
	h.mu.Lock()
	h.segmentCancels[a.SegmentID] = cancelSegment
	h.mu.Unlock()
	defer func() { cancelSegment(); h.mu.Lock(); delete(h.segmentCancels, a.SegmentID); h.mu.Unlock() }()
	// Preparatory Git/auth processes belong to this same Host, even when their
	// post-Start observations fail before the model worker can be launched.
	ctx = process.WithFailedStartOwner(ctx, func(p *process.Handle) error {
		return h.stopFailedStart(a.SegmentID, p, meta.beforeStopJoin)
	})

	spool, err := h.spool(a)
	if err != nil {
		return contract.Result{}, err
	}
	// Active time is measured between successful spawn and the moment the
	// exit is observed or the stop is confirmed. Both endpoints are monotonic
	// readings of the same clock, and the value is only reported for a
	// confirmed exited event; anything else stays unmeasured so the store
	// settles the grant conservatively.
	var spawnedAt time.Time
	var activeMS int64
	var diagnosticState executionDiagnostics
	var protocol *protocolCollector
	if meta.outputProvider != "" {
		protocol, err = newProtocolCollector(meta.outputProvider)
		if err != nil {
			return contract.Result{}, err
		}
		cmd.Stdout = protocol
		protocol.expectedSessionID = meta.expectedSessionID
		protocol.requireSession = meta.requireSession
		protocol.completionMode = meta.completionMode
	}
	eventCount := 0
	validationToolTreeConfirmed := false
	lockActive := func() {
		if activeMS == 0 && !spawnedAt.IsZero() {
			activeMS = time.Since(spawnedAt).Milliseconds()
		}
	}
	appendPhase := func(kind, payloadHash string, identity *process.Identity, exitCode *int, artifact *contract.ArtifactRef) error {
		eventErr := func() error {
			fields := structuredEventFields{}
			if kind == contract.EventExited {
				if meta.filter != nil {
					var cleanupErr error
					activeMS, fields.GitFilterCleanup, cleanupErr = meta.filter.settle()
					if cleanupErr != nil {
						unknown, appendErr := h.appendStructuredEvent(spool, runID, taskID, a, meta, contract.EventUnknown, hashText("unknown:"+a.SegmentID), identity, nil, nil, structuredEventFields{})
						if appendErr == nil && unknown.Sequence > 0 {
							eventCount++
						}
						return errors.Join(cleanupErr, appendErr)
					}
				}
				fields.ActiveMS = activeMS
				fields.DiagnosticsFailure = diagnosticState.failure()
				if meta.artifactGrant != nil && meta.artifactGrant.ValidationTool != nil {
					if !validationToolTreeConfirmed {
						return errors.New("validation_tool_original_wait_required")
					}
					var cleanupErr error
					fields.ValidationToolCleanup, cleanupErr = h.removeValidationTool(*meta.artifactGrant)
					if cleanupErr != nil {
						return cleanupErr
					}
				}
			}
			event, appendErr := h.appendStructuredEvent(spool, runID, taskID, a, meta, kind, payloadHash, identity, exitCode, artifact, fields)
			if appendErr == nil && event.Sequence > 0 {
				eventCount++
			}
			return appendErr
		}()
		return eventErr
	}
	finalizeCandidateUnknown := func(cause error, identity *process.Identity, exit int) (contract.Result, error) {
		body := candidateFailure(cause)
		ref, artifactErr := h.writeHostArtifact(meta, "terminal", 0, "terminal", "result-output.bin", body)
		path := ""
		if ref != nil {
			path = ref.Path
		}
		eventErr := appendPhase(contract.EventUnknown, hashBytes(body), identity, nil, ref)
		phaseErr := publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, identity, cause)
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount, ArtifactPath: path, OutputHash: hashBytes(body)}, errors.Join(cause, artifactErr, eventErr, phaseErr)
	}
	finalizePrelaunchRejected := func(cause error) (contract.Result, error) {
		if meta.filter != nil {
			span, cleanup, cleanupErr := meta.filter.settle()
			if cleanupErr != nil {
				return finalizeCandidateUnknown(errors.Join(cause, cleanupErr), nil, -1)
			}
			// Any known filter Start makes this an executed segment, even when
			// preparation prevented a Provider Start. Never invent a Provider PID
			// or exit code for the filter-only terminal outcome.
			if span > 0 {
				validationToolTreeConfirmed = true // Provider Start was never attempted.
				body := candidateFailure(cause)
				artifact, artifactErr := h.writeHostArtifact(meta, "terminal", 0, "terminal", "result-output.bin", body)
				if artifactErr != nil {
					return finalizeCandidateUnknown(errors.Join(cause, artifactErr), nil, -1)
				}
				if phaseErr := appendPhase(contract.EventFailed, artifact.SHA256, nil, nil, artifact); phaseErr != nil {
					return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, errors.Join(cause, phaseErr)
				}
				if phaseErr := publishLaunchPhase(spool, "exited", a, runID, taskID, cmd, nil, cause); phaseErr != nil {
					return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, errors.Join(cause, phaseErr)
				}
				if phaseErr := appendPhase(contract.EventExited, hashText("exited:"+a.SegmentID), nil, nil, artifact); phaseErr != nil {
					return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, errors.Join(cause, phaseErr)
				}
				h.finishAttempt(a.ID, "failed", persistDB)
				return contract.Result{Status: "failed", ExitCode: -1, EventCount: eventCount, ArtifactPath: artifact.Path, OutputHash: artifact.SHA256}, cause
			}
			var toolCleanup *contract.ValidationToolCleanup
			if meta.artifactGrant != nil && meta.artifactGrant.ValidationTool != nil {
				toolCleanup, cleanupErr = h.removeValidationTool(*meta.artifactGrant)
				if cleanupErr != nil {
					return finalizeCandidateUnknown(errors.Join(cause, cleanupErr), nil, -1)
				}
			}
			safeCause := safeLaunchFailureCause(cause)
			body, marshalErr := json.Marshal(launchFailureDiagnostic{1, "launch_failure", "prelaunch", string(safeCause)})
			if marshalErr != nil {
				return finalizeCandidateUnknown(errors.Join(cause, marshalErr), nil, -1)
			}
			artifact, artifactErr := h.writeHostArtifact(meta, "terminal", 0, "prelaunch", "launch-failure", body)
			if artifactErr != nil {
				return finalizeCandidateUnknown(errors.Join(cause, artifactErr), nil, -1)
			}
			if phaseErr := publishLaunchPhase(spool, "not_started", a, runID, taskID, process.Command{}, nil, safeCause); phaseErr != nil {
				return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, errors.Join(cause, phaseErr)
			}
			if _, appendErr := h.appendStructuredEvent(spool, runID, taskID, a, meta, contract.EventLaunchRejected, artifact.SHA256, nil, nil, artifact, structuredEventFields{GitFilterCleanup: cleanup, ValidationToolCleanup: toolCleanup}); appendErr != nil {
				return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, errors.Join(cause, appendErr)
			}
			eventCount++
			h.finishAttempt(a.ID, "failed", persistDB)
			return contract.Result{Status: "failed", ExitCode: -1, EventCount: eventCount, ArtifactPath: artifact.Path, OutputHash: artifact.SHA256}, cause
		}
		// Only these preparation branches positively know Start was never
		// entered. Preparation uncertainty and every starting/spawned path
		// retain their existing conservative evidence.
		if recordErr := h.recordLaunchFailed(a, runID, taskID, meta, cause); recordErr != nil {
			if meta.artifactGrant != nil && meta.artifactGrant.ValidationTool != nil {
				phaseErr := publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, nil, recordErr)
				eventErr := appendPhase(contract.EventUnknown, hashText("unknown:"+a.SegmentID), nil, nil, nil)
				recordErr = errors.Join(recordErr, phaseErr, eventErr)
			}
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, errors.Join(cause, recordErr)
		}
		eventCount++
		h.finishAttempt(a.ID, "failed", persistDB)
		return contract.Result{Status: "failed", ExitCode: -1, EventCount: eventCount}, cause
	}
	finalizeStopped := func(identity *process.Identity, exit int, cause error) (contract.Result, error) {
		if meta.agyHistoryGrant != nil {
			// This closure is reached only after the existing original-Handle
			// Stop/natural proof. Missing history cannot erase confirmed process
			// facts: no successful seal means future continuation is refused.
			if sealErr := h.captureAGYHistorySeal(*meta.agyHistoryGrant, identity, contract.EventStopped); sealErr != nil {
				_ = spool.WriteMetadata("agy-history-seal-error.json", struct {
					Code string `json:"code"`
				}{"agy_history_seal_unavailable"})
			}
		}
		if err = publishLaunchPhase(spool, "stopped", a, runID, taskID, cmd, identity, cause); err != nil {
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount}, err
		}
		_, err = h.appendStructuredEvent(spool, runID, taskID, a, meta, contract.EventStopped, hashText("stopped:"+a.SegmentID), identity, &exit, nil, structuredEventFields{StopReason: interruptionReason(ctx, h.stopWasRequested(a.SegmentID), exit)})
		if err != nil {
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
	if meta.filter != nil {
		defer func() { returnedErr = errors.Join(returnedErr, meta.filter.close()) }()
		if !meta.filter.matches(meta, h.producerID, runID, taskID, a.ID, a.SegmentID) {
			return finalizeCandidateUnknown(contract.ErrGitFilterContract, nil, -1)
		}
		if err = meta.filter.open(ctx, spool); err != nil {
			return finalizeCandidateUnknown(err, nil, -1)
		}
	}
	if h.stopWasRequested(a.SegmentID) {
		return finalizePrelaunchRejected(context.Canceled)
	}
	if meta.runFilterObservation != nil {
		if meta.filter == nil || !adapter.IsFilterTargetPreflight(filterPayloadForMetadata(meta)) {
			return finalizeCandidateUnknown(contract.ErrGitFilterContract, nil, -1)
		}
		if err = publishLaunchPhase(spool, "starting", a, runID, taskID, cmd, nil, nil); err != nil {
			return finalizeCandidateUnknown(err, nil, -1)
		}
		if err = meta.filter.observationStarting(); err != nil {
			return finalizeCandidateUnknown(err, nil, -1)
		}
		body, observeErr := meta.runFilterObservation(ctx)
		if errors.Is(observeErr, process.ErrProcessTreeUnknown) || errors.Is(observeErr, gitops.ErrIntegrationUncertain) {
			return finalizeCandidateUnknown(observeErr, nil, -1)
		}
		if err = meta.filter.observationTerminal(); err != nil {
			return finalizeCandidateUnknown(errors.Join(observeErr, err), nil, -1)
		}
		// Prove tool termination and cleanup before publishing any success.
		if _, _, err = meta.filter.settle(); err != nil {
			return finalizeCandidateUnknown(errors.Join(observeErr, err), nil, -1)
		}
		finalizeObservationStopped := func(artifact *contract.ArtifactRef) (contract.Result, error) {
			cause := context.Cause(ctx)
			if cause == nil {
				cause = context.Canceled
			}
			if err = publishLaunchPhase(spool, "stopped", a, runID, taskID, cmd, nil, cause); err != nil {
				return finalizeCandidateUnknown(err, nil, -1)
			}
			stoppedHash := hashText("stopped:" + a.SegmentID)
			if artifact != nil {
				stoppedHash = artifact.SHA256
			}
			if _, err = h.appendStructuredEvent(spool, runID, taskID, a, meta, contract.EventStopped, stoppedHash, nil, nil, artifact, structuredEventFields{StopReason: interruptionReason(ctx, h.stopWasRequested(a.SegmentID), -1)}); err != nil {
				return finalizeCandidateUnknown(err, nil, -1)
			}
			eventCount++
			if err = publishLaunchPhase(spool, "exited", a, runID, taskID, cmd, nil, cause); err != nil {
				return finalizeCandidateUnknown(err, nil, -1)
			}
			if err = appendPhase(contract.EventExited, hashText("exited:"+a.SegmentID), nil, nil, artifact); err != nil {
				return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, err
			}
			h.finishAttempt(a.ID, "interrupted", persistDB)
			result := contract.Result{Status: "interrupted", ExitCode: -1, EventCount: eventCount}
			if artifact != nil {
				result.ArtifactPath, result.OutputHash = artifact.Path, artifact.SHA256
			}
			return result, cause
		}
		if ctx.Err() != nil || h.stopWasRequested(a.SegmentID) {
			return finalizeObservationStopped(nil)
		}
		kind, status := contract.EventResult, "result_ready"
		if observeErr != nil {
			kind, status = contract.EventFailed, "failed"
			body = candidateFailure(observeErr)
		}
		artifact, artifactErr := h.writeHostArtifact(meta, "terminal", 0, "terminal", "result-output.bin", body)
		if artifactErr != nil {
			return finalizeCandidateUnknown(errors.Join(observeErr, artifactErr), nil, -1)
		}
		if meta.afterObservationArtifact != nil {
			meta.afterObservationArtifact(artifact)
		}
		// The bounded artifact writer uses its own context. A Stop arriving
		// there suppresses Result and retains this same slot on Stopped/Exited.
		if ctx.Err() != nil || h.stopWasRequested(a.SegmentID) {
			return finalizeObservationStopped(artifact)
		}
		if err = appendPhase(kind, artifact.SHA256, nil, nil, artifact); err != nil {
			return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, err
		}
		if err = publishLaunchPhase(spool, "exited", a, runID, taskID, cmd, nil, observeErr); err != nil {
			return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, err
		}
		if err = appendPhase(contract.EventExited, hashText("exited:"+a.SegmentID), nil, nil, artifact); err != nil {
			return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, err
		}
		h.finishAttempt(a.ID, status, persistDB)
		return contract.Result{Status: status, ExitCode: -1, EventCount: eventCount, ArtifactPath: artifact.Path, OutputHash: artifact.SHA256}, observeErr
	}
	if meta.prepareAction != nil {
		meta.prepareCandidate = func(ctx context.Context) (process.Command, func(context.Context, string) ([]byte, error), func(), error) {
			cmd, finalize, close, err := meta.prepareAction(ctx)
			meta.finalizeAction = finalize
			return cmd, nil, close, err
		}
	}
	if meta.prepareCandidate != nil {
		prepared, freeze, closeCandidate, prepareErr := meta.prepareCandidate(ctx)
		defer closeCandidate()
		if prepareErr != nil {
			if errors.Is(prepareErr, process.ErrProcessTreeUnknown) || errors.Is(prepareErr, gitops.ErrIntegrationUncertain) {
				return finalizeCandidateUnknown(prepareErr, nil, -1)
			}
			if meta.filter != nil {
				return finalizePrelaunchRejected(prepareErr)
			}
			if ctx.Err() != nil || h.stopWasRequested(a.SegmentID) {
				return finalizePrelaunchRejected(context.Canceled)
			}
			if meta.artifactGrant != nil && meta.artifactGrant.ValidationTool != nil {
				return finalizePrelaunchRejected(prepareErr)
			}
			return contract.Result{Status: "failed", ExitCode: -1, EventCount: eventCount}, prepareErr
		}
		// Provider protocols keep priority; a local action can supply its own
		// bounded stdout sink (compose must exclude stderr from its envelope).
		if cmd.Stdout != nil {
			prepared.Stdout = cmd.Stdout
		}
		cmd, meta.freezeCandidate = prepared, freeze
		if ctx.Err() != nil || h.stopWasRequested(a.SegmentID) {
			return finalizePrelaunchRejected(context.Canceled)
		}
	}
	if meta.prepareProvider != nil {
		prepared, prepareErr := meta.prepareProvider(ctx)
		if prepareErr != nil {
			if errors.Is(prepareErr, process.ErrProcessTreeUnknown) {
				return finalizeCandidateUnknown(prepareErr, nil, -1)
			}
			if meta.filter != nil {
				return finalizePrelaunchRejected(prepareErr)
			}
			if ctx.Err() != nil || h.stopWasRequested(a.SegmentID) {
				return finalizePrelaunchRejected(context.Canceled)
			}
			if meta.artifactGrant != nil && meta.artifactGrant.ValidationTool != nil {
				return finalizePrelaunchRejected(prepareErr)
			}
			return contract.Result{Status: "failed", ExitCode: -1, EventCount: eventCount}, prepareErr
		}
		prepared.Stdout = cmd.Stdout
		cmd = prepared
		if ctx.Err() != nil || h.stopWasRequested(a.SegmentID) {
			return finalizePrelaunchRejected(context.Canceled)
		}
	}
	if meta.diagnosticsGrant != nil {
		g := *meta.diagnosticsGrant
		if g.RunID != runID || g.TaskID != taskID || g.AttemptID != a.ID || g.SegmentID != a.SegmentID {
			return finalizePrelaunchRejected(errDiagnosticAuthority)
		}
		diagnosticState.driver, err = h.openDetailedDiagnostics(ctx, g, diagnosticState.wakeExecution(cancelCause))
		if err != nil {
			return finalizePrelaunchRejected(err)
		}
		cmd.ObserveOutput = diagnosticState.driver.Observe
	}
	defer func() { returnedErr = errors.Join(returnedErr, diagnosticState.end(false, returnedErr)) }()
	// Persist the uncertainty boundary before entering Start. A crash or any
	// later failure cannot be reclassified from the older prepared phase as a
	// never-started rejection, even if no spawned event became durable.
	if err = publishLaunchPhase(spool, "starting", a, runID, taskID, cmd, nil, nil); err != nil {
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: -1, EventCount: eventCount}, err
	}
	start := meta.startProcess
	if start == nil {
		start = process.Start
	}
	if meta.filter != nil {
		meta.filter.providerStarting()
	}
	p, err := start(ctx, cmd)
	if err != nil {
		if meta.filter == nil && p == nil && meta.validationGrant != nil && process.NeverStartedCode(ctx, err, cmd) != "" {
			if rejected := h.recordValidationStartRejected(ctx, *meta.validationGrant, cmd, err); rejected == nil {
				eventCount++
				h.finishAttempt(a.ID, "failed", persistDB)
				return contract.Result{Status: "failed", ExitCode: -1, EventCount: eventCount}, err
			} else {
				err = errors.Join(err, rejected)
			}
		}
		var identity *process.Identity
		exit := -1
		if p != nil {
			original := p.Identity()
			identity = &original
			err = errors.Join(err, h.stopFailedStart(a.SegmentID, p, meta.beforeStopJoin))
			exit = p.ExitCode()
		}
		phaseErr := publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, identity, err)
		_ = appendPhase(contract.EventUnknown, hashText("unknown:"+a.SegmentID), identity, nil, nil)
		h.finishAttempt(a.ID, "unknown", persistDB)
		if phaseErr != nil {
			return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount}, phaseErr
		}
		return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount}, err
	}
	identity := p.Identity()
	if meta.filter != nil {
		if startErr := meta.filter.providerStarted(p); startErr != nil {
			return finalizeCandidateUnknown(errors.Join(startErr, h.stopFailedStart(a.SegmentID, p, meta.beforeStopJoin)), &identity, p.ExitCode())
		}
	}
	spawnedAt = time.Now()
	if err = h.registerOwnedHandle(a.ID, a.SegmentID, p); err != nil {
		err = errors.Join(err, h.stopFailedStart(a.SegmentID, p, meta.beforeStopJoin))
		return finalizeCandidateUnknown(err, &identity, p.ExitCode())
	}
	defer h.unregisterOwnedHandle(a.ID, a.SegmentID, p)
	if err = publishLaunchPhase(spool, "spawned", a, runID, taskID, cmd, &identity, nil); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = h.stopForExecution(a.SegmentID, p, stopCtx, true, nil)
		stopCancel()
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount}, err
	}
	if err = appendPhase(contract.EventSpawned, hashText("spawned:"+a.SegmentID), &identity, nil, nil); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = h.stopForExecution(a.SegmentID, p, stopCtx, true, nil)
		stopCancel()
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount}, err
	}
	if err = publishLaunchPhase(spool, "running", a, runID, taskID, cmd, &identity, nil); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = h.stopForExecution(a.SegmentID, p, stopCtx, true, nil)
		stopCancel()
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount}, err
	}
	if err = appendPhase(contract.EventRunning, hashText("running:"+a.SegmentID), &identity, nil, nil); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = h.stopForExecution(a.SegmentID, p, stopCtx, true, nil)
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
			_, _ = h.stopForExecution(a.SegmentID, p, stopCtx, true, nil)
			stopCancel()
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount}, err
		}
	}
	// Progress is observational and bounded independently of the model budget.
	// Stop the observer before publishing terminal events, so late snapshots
	// cannot be mistaken for new work after exit.
	started := time.Now()
	progressDone, progressStopped := make(chan struct{}), make(chan struct{})
	if protocol != nil {
		go func() {
			defer close(progressStopped)
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for index := 0; index < 20; index++ {
				select {
				case <-progressDone:
					return
				case <-ticker.C:
					_ = h.recordProgress(a, runID, taskID, meta, protocol, &identity, index, started, "")
				}
			}
			<-progressDone
		}()
	} else {
		close(progressStopped)
	}
	waitErr := p.Wait(ctx)
	if meta.afterWait != nil {
		meta.afterWait(p, ctx, waitErr)
	}
	close(progressDone)
	var protocolSnapshots *protocolDiagnostics
	if protocol != nil {
		snapshot := protocol.Snapshot()
		snapshot.WaitComplete = waitErr == nil
		protocolSnapshots = &protocolDiagnostics{AfterWait: snapshot}
	}
	writeDiagnostics := func() {
		if protocolSnapshots == nil {
			return
		}
		output, captureTruncated := p.OutputSnapshot()
		diagnostics := providerDiagnostics(output)
		diagnostics.Truncated = diagnostics.Truncated || captureTruncated
		diagnostics.ProtocolSnapshots = protocolSnapshots
		if writeErr := spool.WriteMetadata("provider-diagnostics.json", diagnostics); writeErr != nil && diagnosticState.summaryErr == nil {
			diagnosticState.summaryErr = writeErr
		}
	}
	finishJoinedStop := func(stopErr error) (contract.Result, error) {
		if stopErr == nil {
			if meta.filter != nil {
				meta.filter.providerTerminal(p)
			}
			validationToolTreeConfirmed = true
			lockActive()
		}
		<-progressStopped
		if protocol != nil {
			snapshot := protocol.Snapshot()
			// Successful original Stop includes the original Wait and ordinary
			// stdout/stderr copy completion; no second Wait or reader is needed.
			snapshot.WaitComplete = stopErr == nil
			snapshot.TreeConfirmed = stopErr == nil
			protocolSnapshots.AfterStop = &snapshot
			writeDiagnostics()
		}
		if stopErr != nil {
			if phaseErr := publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, &identity, stopErr); phaseErr != nil {
				stopErr = phaseErr
			}
			_ = appendPhase(contract.EventUnknown, hashText("unknown:"+a.SegmentID), &identity, nil, nil)
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount, OutputHash: hashText(p.Output())}, stopErr
		}
		_ = diagnosticState.end(true, nil)
		exit := p.ExitCode()
		if protocol != nil {
			_ = h.recordProgress(a, runID, taskID, meta, protocol, &identity, 20, started, interruptionReason(ctx, h.stopWasRequested(a.SegmentID), exit))
		}
		stopCause := waitErr
		if stopCause == nil {
			stopCause = context.Canceled
		}
		result, stoppedErr := finalizeStopped(&identity, exit, stopCause)
		result.OutputHash = hashText(p.Output())
		return result, stoppedErr
	}
	stopped, stopErr := h.stopForExecution(a.SegmentID, p, nil, waitErr != nil, meta.beforeStopJoin)
	if stopped {
		return finishJoinedStop(stopErr)
	}
	if meta.afterNoStop != nil {
		meta.afterNoStop(p, ctx)
	}
	<-progressStopped
	writeDiagnostics()
	proofErr := p.ConfirmTreeExited()
	if proofErr == nil && waitErr == nil && meta.filter != nil {
		meta.filter.providerTerminal(p)
	}
	if stopped, stopErr = h.stopAfterNaturalProof(a.SegmentID, p, proofErr, meta.beforeStopJoin); stopped {
		return finishJoinedStop(stopErr)
	}
	if err = proofErr; err != nil {
		if phaseErr := publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, &identity, err); phaseErr != nil {
			err = phaseErr
		}
		if phaseErr := appendPhase(contract.EventUnknown, hashText("unknown:"+a.SegmentID), &identity, nil, nil); phaseErr != nil {
			err = phaseErr
		}
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount, OutputHash: hashText(p.Output())}, err
	}
	validationToolTreeConfirmed = true
	lockActive()
	_ = diagnosticState.end(true, nil)
	if h.stopWasRequested(a.SegmentID) || p.ExitCode() < 0 {
		// A stop can arrive after the natural tree observation. Its original
		// result still governs the stop path; the earlier observation must not
		// bypass a canceled or otherwise unproved stop scope.
		if _, stopErr := h.stopForExecution(a.SegmentID, p, nil, false, meta.beforeStopJoin); stopErr != nil {
			_ = publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, &identity, stopErr)
			_ = appendPhase(contract.EventUnknown, hashText("unknown:"+a.SegmentID), &identity, nil, nil)
			h.finishAttempt(a.ID, "unknown", persistDB)
			return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: eventCount, OutputHash: hashText(p.Output())}, stopErr
		}
		if protocol != nil {
			_ = h.recordProgress(a, runID, taskID, meta, protocol, &identity, 20, started, interruptionReason(ctx, h.stopWasRequested(a.SegmentID), p.ExitCode()))
		}
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
		if meta.completionMode == adapter.TaskCompletionV1 {
			kind, status, terminal.Text, protocolErr = managedTerminalOutcome(protocol.provider, terminal, exit, protocolErr)
		}
		if meta.structuredReview {
			// A schema request must return the native structured channel. Never
			// silently accept fenced prose or fall back to prompt-only JSON.
			terminal.Text = terminal.StructuredText
		}
		content := []byte(terminal.Text)
		if meta.finalizeAction != nil || status == "result_ready" && meta.freezeCandidate != nil {
			if meta.finalizeAction != nil {
				content, err = meta.finalizeAction(ctx, terminal.Text, exit, status == "result_ready")
			} else {
				content, err = meta.freezeCandidate(ctx, terminal.Text)
			}
			if errors.Is(err, process.ErrProcessTreeUnknown) || errors.Is(err, gitops.ErrIntegrationUncertain) {
				return finalizeCandidateUnknown(err, &identity, exit)
			}
			if ctx.Err() != nil || h.stopWasRequested(a.SegmentID) {
				if meta.integrationAction {
					if err != nil {
						content = candidateFailure(err)
					}
					return finalizeCandidateUnknown(&actionUncertain{content, context.Canceled}, &identity, exit)
				}
				return finalizeStopped(&identity, exit, context.Canceled)
			}
			if err != nil {
				kind, status = contract.EventFailed, "failed"
				content = candidateFailure(err)
			}
		}
		if protocolErr != nil && protocolErr.Error() == "provider_critical_event_too_large" {
			// Keep an actionable, bounded failure artifact in the durable event
			// path. Never publish an earlier message as a partial final answer.
			content = []byte(`{"status":"incomplete","reason":"provider_critical_event_too_large"}`)
		}
		if kind == contract.EventQuestion && meta.agyHistoryGrant != nil {
			// Preserve the actual exit/settlement, but never publish an
			// answerable Question without its proved segment's native state.
			if sealErr := h.captureAGYHistorySeal(*meta.agyHistoryGrant, &identity, contract.EventQuestion); sealErr != nil {
				kind, status, protocolErr = contract.EventFailed, "failed", sealErr
				content = []byte(`{"status":"failed","reason":"agy_history_seal_unavailable"}`)
			}
		}
		var artifact *contract.ArtifactRef
		artifactPath := ""
		if len(content) > 0 {
			artifact, err = h.writeHostArtifact(meta, "terminal", 0, "terminal", "result-output.bin", content)
			if err != nil {
				h.finishAttempt(a.ID, "unknown", persistDB)
				return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount}, err
			}
			artifactPath = artifact.Path
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
	content := []byte(p.Output())
	if meta.finalizeAction != nil || status == "result_ready" && meta.freezeCandidate != nil {
		if meta.finalizeAction != nil {
			content, err = meta.finalizeAction(ctx, p.Output(), exit, status == "result_ready")
		} else {
			content, err = meta.freezeCandidate(ctx, p.Output())
		}
		if errors.Is(err, process.ErrProcessTreeUnknown) || errors.Is(err, gitops.ErrIntegrationUncertain) {
			return finalizeCandidateUnknown(err, &identity, exit)
		}
		if ctx.Err() != nil || h.stopWasRequested(a.SegmentID) {
			if meta.integrationAction {
				if err != nil {
					content = candidateFailure(err)
				}
				return finalizeCandidateUnknown(&actionUncertain{content, context.Canceled}, &identity, exit)
			}
			return finalizeStopped(&identity, exit, context.Canceled)
		}
		if err != nil {
			status = "failed"
			content = candidateFailure(err)
		}
	}
	outHash := hashBytes(content)
	artifact, err := h.writeHostArtifact(meta, "terminal", 0, "terminal", "result-output.bin", content)
	if err != nil {
		h.finishAttempt(a.ID, "unknown", persistDB)
		return contract.Result{Status: "unknown", ExitCode: exit, EventCount: eventCount, OutputHash: outHash}, err
	}
	artifactPath := artifact.Path
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
	if grant.Continuation != nil {
		return h.validateTrustedContinuation(grant)
	}
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
	GitFilterCleanup      *contract.GitFilterCleanup
	DiagnosticsFailure    *contract.DiagnosticsFailure
	ValidationToolCleanup *contract.ValidationToolCleanup
	Failure               *contract.FailureFacts
	StopReason            string
	QuestionRevision      int
	SessionKind           string
	SessionID             string
	ActiveMS              int64
}

func (h *Host) appendStructuredEvent(spool *events.Spool, runID, taskID string, a store.Attempt, meta launchMetadata, kind, payloadHash string, identity *process.Identity, exitCode *int, artifact *contract.ArtifactRef, fields structuredEventFields) (contract.Event, error) {
	h.sequenceMu.Lock()
	defer h.sequenceMu.Unlock()
	if err := h.loadEventSequenceLocked(); err != nil {
		return contract.Event{}, err
	}
	sequence := h.sequence + 1
	event := contract.Event{Version: 1, ProducerID: h.producerID, RunID: runID, TaskID: taskID, AttemptID: a.ID, SegmentID: a.SegmentID, WorkRevision: meta.workRevision, ExecutionEpoch: meta.executionEpoch, CommandID: meta.commandID, Sequence: sequence, Kind: kind, PayloadHash: payloadHash, ExitCode: exitCode, ActiveMS: fields.ActiveMS, Artifact: artifact, QuestionRevision: fields.QuestionRevision, SessionKind: fields.SessionKind, SessionID: fields.SessionID}
	event.StopReason = fields.StopReason
	event.Failure = fields.Failure
	event.DiagnosticsFailure = fields.DiagnosticsFailure
	event.ValidationToolCleanup = fields.ValidationToolCleanup
	event.GitFilterCleanup = fields.GitFilterCleanup
	if identity != nil {
		event.Process = &contract.ProcessIdentity{PID: identity.PID, Birth: identity.Birth, PGID: identity.PGID}
	}
	if h.producerID != "" {
		event.EventID = h.producerID + ":" + fmt.Sprint(sequence)
	}
	if kind == contract.EventQuestion {
		event.QuestionID = event.EventID + ":question"
		event.QuestionKind = "technical"
	}
	if err := spool.Append(event); err != nil {
		return contract.Event{}, err
	}
	if h.afterSpoolAppend != nil {
		h.afterSpoolAppend(event)
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
	defer h.clearStop(a.SegmentID)
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
	for _, event := range existing {
		if event.Kind == contract.EventExited || event.Kind == contract.EventLaunchRejected {
			return nil
		}
	}
	return h.recordLaunchUnknown(a, runID, taskID, meta, cause)
}

func (h *Host) recordLaunchUnknown(a store.Attempt, runID, taskID string, meta launchMetadata, cause error) error {
	defer h.clearStop(a.SegmentID)
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

// recordLaunchFailed is called only by this command's pre-Start refusal path.
// It proves not_started without fabricating process exit. Cold-restart callers
// use UNKNOWN unless the original terminal event is already durable.
func (h *Host) recordLaunchFailed(a store.Attempt, runID, taskID string, meta launchMetadata, cause error) error {
	defer h.clearStop(a.SegmentID)
	spool, err := h.spool(a)
	if err != nil {
		return err
	}
	existing, err := spool.Read()
	if err != nil {
		return err
	}
	// ExecuteLaunch can already have published a terminal outcome before its
	// caller reaches this fallback. Never replace or append to that evidence.
	for _, event := range existing {
		if event.Kind == contract.EventUnknown {
			return errors.New("launch_failure_state_conflict")
		}
	}
	for _, event := range existing {
		if event.Kind == contract.EventExited || event.Kind == contract.EventLaunchRejected {
			return nil
		}
	}
	if meta.hasGitFilter {
		return errors.New("git_filter_original_runtime_required")
	}
	for _, event := range existing {
		if event.Kind != contract.EventPrepared || event.Process != nil || event.Artifact != nil {
			return errors.New("launch_failure_state_conflict")
		}
	}
	segmentRoot := filepath.Join(h.spoolRoot, a.ID, a.SegmentID)
	if _, err = os.Lstat(filepath.Join(segmentRoot, "result-output.bin")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("launch_failure_state_conflict")
	}
	// Read into a map because launch.json carries the existing identity fields.
	var phase map[string]any
	if err = spool.ReadMetadata("launch.json", &phase); err == nil {
		previousPhase, _ := phase["phase"].(string)
		if phase["process"] != nil || previousPhase != "prepared" {
			return errors.New("launch_failure_state_conflict")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("launch_failure_state_conflict")
	}

	var toolCleanup *contract.ValidationToolCleanup
	if meta.artifactGrant != nil && meta.artifactGrant.ValidationTool != nil {
		toolCleanup, err = h.removeValidationTool(*meta.artifactGrant)
		if err != nil {
			return err
		}
	}
	safeCause := safeLaunchFailureCause(cause)
	body, err := json.Marshal(launchFailureDiagnostic{1, "launch_failure", "prelaunch", string(safeCause)})
	if err != nil {
		return err
	}
	// A prelaunch refusal and a later terminal result compete for the same
	// reserved physical slot; neither may create an alternative namespace.
	artifact, err := h.writeHostArtifact(meta, "terminal", 0, "prelaunch", "launch-failure", body)
	if err != nil {
		return err
	}
	if err = publishLaunchPhase(spool, "not_started", a, runID, taskID, process.Command{}, nil, safeCause); err != nil {
		return err
	}
	_, err = h.appendStructuredEvent(spool, runID, taskID, a, meta, contract.EventLaunchRejected, artifact.SHA256, nil, nil, artifact, structuredEventFields{Failure: launchFailureFacts(safeCause), ValidationToolCleanup: toolCleanup})
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
	if err := contract.RequireHostArtifacts(grant); err != nil {
		return false, err
	}
	if err := contract.ValidateDependencyInputs(grant); err != nil {
		return false, err
	}
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
		if record.Kind == contract.EventExited || record.Kind == contract.EventLaunchRejected || record.Kind == contract.EventUnknown {
			return nil
		}
	}
	meta := metadataForLaunch(grant)
	meta.executionEpoch = epoch
	{
		// An original descriptor-bound artifact can be retained as evidence,
		// but it never proves that this restarted Host did not enter Start.
		binding, bindErr := h.artifactSlot(meta, "terminal", 0)
		if bindErr != nil {
			return bindErr
		}
		loadCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		desc, proof, body, readErr := artifacts.LoadHostSlot(loadCtx, h.spoolRoot, binding)
		cancel()
		if readErr == nil {
			id := "result-output.bin"
			var cause error = errors.New("recovered_incomplete_grant")
			if desc.Kind == "prelaunch" {
				id = "launch-failure"
				if validValidationStartDiagnostic(body) {
					// Authenticated diagnostic preserves uncertainty, never live proof.
					cause = errors.New("recovered_validation_start_incomplete")
				} else {
					safeCause, decodeErr := decodeRecoveredLaunchFailure(body)
					if decodeErr != nil {
						return decodeErr
					}
					cause = safeCause
				}
			}
			if err = publishLaunchPhase(spool, "unknown", a, grant.RunID, grant.TaskID, process.Command{}, nil, cause); err != nil {
				return err
			}
			ref, refErr := hostArtifactReference(id, desc, proof)
			if refErr != nil {
				return refErr
			}
			_, err = h.appendPhaseEvent(spool, grant.RunID, grant.TaskID, a, meta, contract.EventUnknown, ref.SHA256, nil, nil, ref)
			return err
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
	}
	if err = h.ensureLaunchUnknown(a, grant.RunID, grant.TaskID, meta, errors.New("recovered_incomplete_grant")); err != nil {
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
	if safe, ok := err.(launchFailureCause); ok {
		return string(safe)
	}
	if err == nil {
		return ""
	}
	if errors.Is(err, gitops.ErrIntegrationUncertain) {
		return "integration_uncertain"
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
	if provider, ok := inv.(interface{ OutputProvider() string }); ok && provider.OutputProvider() == string(adapter.ProviderCodex) {
		if mode, ok := inv.(interface {
			TaskCompletionMode() adapter.TaskCompletionMode
		}); ok && mode.TaskCompletionMode() == adapter.TaskCompletionV1 {
			return contract.Result{}, errors.New("codex_managed_launch_grant_required")
		}
	}
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
	return h.stopSegment(ctx, segmentID, nil, nil)
}

func (h *Host) stopSegment(ctx context.Context, segmentID string, afterRead func(), afterRegister func(*segmentStopIntent)) error {
	if segmentID == "" {
		return errors.New("segment_required")
	}
	capturedNow := time.Now()
	var lookup *segmentStopIntent
	transferred := false
	defer func() {
		if lookup != nil && lookup.cancel != nil && !transferred {
			lookup.cancel()
		}
	}()
	var terminal bool
	var err error
	observed := false
	for {
		h.stopMu.Lock()
		if i := h.stopIntents[segmentID]; i != nil {
			if i.terminal {
				h.stopMu.Unlock()
				return nil
			}
			if i.op != nil {
				ops := h.codexSegmentStopOperationsLocked(segmentID, i)
				h.stopMu.Unlock()
				return h.joinHostStopOperations(ops, nil)
			}
		}
		generation := h.naturalGeneration
		// Saturate rather than allowing a generation ABA through wraparound.
		if generation == ^uint64(0) {
			h.stopMu.Unlock()
			return process.ErrProcessTreeUnknown
		}
		h.stopMu.Unlock()
		if lookup == nil {
			lookup = stopIntentAt(ctx, capturedNow)
		}
		if observed && lookup.ctx.Err() != nil {
			return process.ErrProcessTreeUnknown
		}
		terminal, err = h.segmentStopComplete(segmentID)
		observed = true
		if afterRead != nil {
			afterRead()
		}
		h.stopMu.Lock()
		if h.naturalGeneration != generation {
			h.stopMu.Unlock()
			continue // same lookup deadline; consume current state before reread
		}
		if i := h.stopIntents[segmentID]; i != nil {
			if i.terminal {
				h.stopMu.Unlock()
				return nil
			}
			if i.op != nil {
				ops := h.codexSegmentStopOperationsLocked(segmentID, i)
				h.stopMu.Unlock()
				return h.joinHostStopOperations(ops, nil)
			}
		}
		if err != nil || terminal {
			h.stopMu.Unlock()
			return err
		}
		// A stable first observation consumes its original intent even if its
		// context already expired: bind UNKNOWN and cancel execution, rather
		// than forgetting it and permitting a follower to create new grace.
		break // stopMu remains held; lifecycle observation is generation-exact
	}
	h.mu.Lock()
	p := h.activeBySeg[segmentID]
	cancel := h.segmentCancels[segmentID]
	i := h.stopIntents[segmentID]
	if i == nil {
		if h.stopping {
			i = &segmentStopIntent{ctx: h.stopAllIntent.ctx, deadline: h.stopAllIntent.deadline, expires: h.stopAllIntent.expires, bounded: h.stopAllIntent.bounded}
		} else {
			i = lookup
			transferred = true
		}
		h.stopIntents[segmentID] = i
	}
	h.stopRequested[segmentID] = true
	if p != nil {
		h.bindStopLocked(segmentID, i, p, false)
	}
	ops := h.codexSegmentStopOperationsLocked(segmentID, i)
	h.mu.Unlock()
	h.stopMu.Unlock()
	if afterRegister != nil {
		afterRegister(i)
	}
	for _, operation := range ops {
		h.startStop(operation)
	}
	if cancel != nil {
		cancel()
	}
	if len(ops) == 0 {
		// No original handle is an intent, never process-exit evidence.
		if cancel == nil {
			terminal, err = h.segmentStopComplete(segmentID)
			if terminal || err != nil {
				if terminal {
					h.clearStop(segmentID)
				}
				return err
			}
		}
		return nil
	}
	return h.joinHostStopOperations(ops, nil)
}

func (h *Host) stopWasRequested(segmentID string) bool {
	h.stopMu.Lock()
	defer h.stopMu.Unlock()
	return h.stopping || h.stopRequested[segmentID]
}

func (h *Host) clearStop(segmentID string) {
	h.stopMu.Lock()
	i := h.stopIntents[segmentID]
	if i != nil && i.terminal {
		h.mu.Lock()
		occupied := h.activeBySeg[segmentID] != nil || h.segmentCancels[segmentID] != nil
		h.mu.Unlock()
		if occupied {
			h.stopMu.Unlock()
			return
		}
		h.advanceNaturalGenerationLocked()
	}
	if i != nil && i.op != nil {
		select {
		case <-i.op.done:
		default:
			i.op.retire = true
			h.stopMu.Unlock()
			return
		}
	}
	delete(h.stopIntents, segmentID)
	delete(h.stopRequested, segmentID)
	h.stopMu.Unlock()
	if i != nil && i.cancel != nil {
		i.cancel()
	}
}

// BeginStopAll registers the first global stop window and each original handle
// before cancellation can wake workers. It starts operations without waiting.
func (h *Host) BeginStopAll(ctx context.Context) {
	h.stopMu.Lock()
	h.stopping = true
	if h.stopAllIntent == nil {
		h.stopAllIntent = stopIntent(ctx)
	}
	h.mu.Lock()
	ops := make([]*hostStopOperation, 0, len(h.activeBySeg))
	cancels := make([]context.CancelFunc, 0, len(h.segmentCancels))
	for segment, cancel := range h.segmentCancels {
		if i := h.stopIntents[segment]; i != nil && i.terminal {
			continue
		}
		cancels = append(cancels, cancel)
		if h.stopIntents[segment] == nil {
			h.stopIntents[segment] = &segmentStopIntent{ctx: h.stopAllIntent.ctx, deadline: h.stopAllIntent.deadline, expires: h.stopAllIntent.expires, bounded: h.stopAllIntent.bounded}
		}
		h.stopRequested[segment] = true
	}
	for segment, p := range h.activeBySeg {
		i := h.stopIntents[segment]
		if i != nil && i.terminal {
			continue
		}
		if i == nil {
			i = &segmentStopIntent{ctx: h.stopAllIntent.ctx, deadline: h.stopAllIntent.deadline, expires: h.stopAllIntent.expires, bounded: h.stopAllIntent.bounded}
			h.stopIntents[segment] = i
		}
		h.stopRequested[segment] = true
		ops = append(ops, h.bindStopLocked(segment, i, p, false))
	}
	for _, lease := range h.codexLeases {
		if lease.cancel != nil {
			cancels = append(cancels, lease.cancel)
		}
		ops = append(ops, h.codexSegmentStopOperationsLocked(lease.segment, h.stopIntents[lease.segment])...)
	}
	for _, op := range h.failedStarts {
		ops = append(ops, op)
	}
	h.mu.Unlock()
	h.stopMu.Unlock()
	for _, op := range ops {
		h.startStop(op)
	}
	for _, cancel := range cancels {
		cancel()
	}
}

// StopAll joins the operations registered by BeginStopAll, including their
// existing KILL-confirmation stage. Reentry never creates another TERM window.
func (h *Host) StopAll(ctx context.Context) {
	h.stopAll(ctx, nil)
}

// Private per-call observation is used only by bounded join tests.
func (h *Host) stopAll(ctx context.Context, observe func(*hostStopOperation)) error {
	h.BeginStopAll(ctx)
	h.stopMu.Lock()
	ops := make([]*hostStopOperation, 0, len(h.stopIntents)+len(h.codexLeases))
	seen := map[*hostStopOperation]bool{}
	for segment, i := range h.stopIntents {
		for _, op := range h.codexSegmentStopOperationsLocked(segment, i) {
			if !seen[op] {
				seen[op] = true
				ops = append(ops, op)
			}
		}
	}
	for _, op := range h.failedStarts {
		if !seen[op] {
			seen[op] = true
			ops = append(ops, op)
		}
	}
	h.stopMu.Unlock()
	return h.joinHostStopOperations(ops, observe)
}
