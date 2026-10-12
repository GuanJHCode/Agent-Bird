package host

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// The managed backend never attaches a legacy stdout collector. The original
// handle is leased before Start; every returned handle, including h+err, is
// attached to that lease before any phase/RPC/binding operation can fail.
func (h *Host) executeManagedCodex(ctx context.Context, g contract.LaunchCommand, inv adapter.Invocation, meta launchMetadata, control codexExecuteControl) (result contract.Result, returnedErr error) {
	ctx, cancelCause := context.WithCancelCause(ctx)
	cancel := func() { cancelCause(context.Canceled) }
	lease, err := h.reserveCodexHandleLease(g, cancel)
	if err != nil {
		cancel()
		return contract.Result{}, err
	}
	// Canceling a completed execution is safe; retiring ownership requires
	// independent positive original-Wait/tree proof, never this defer.
	defer cancel()
	a := store.Attempt{ID: g.AttemptID, TaskID: g.TaskID, SegmentID: g.SegmentID}
	spool, err := h.spool(a)
	if err != nil {
		_ = h.retireCodexHandleLease(lease, codexOriginalExit{})
		return contract.Result{}, err
	}
	cmd := process.Command{}
	count := 0
	var diagnosticState executionDiagnostics
	appendEvent := func(kind string, id *process.Identity, exit *int, artifact *contract.ArtifactRef, fields structuredEventFields) error {
		if kind == contract.EventExited {
			fields.DiagnosticsFailure = diagnosticState.failure()
		}
		hash := hashText(kind + ":" + g.SegmentID)
		if artifact != nil && kind != contract.EventExited {
			hash = artifact.SHA256
		}
		_, err := h.appendStructuredEvent(spool, g.RunID, g.TaskID, a, meta, kind, hash, id, exit, artifact, fields)
		if err == nil {
			count++
		}
		return err
	}
	unknown := func(cause error, id *process.Identity) (contract.Result, error) {
		phaseErr := publishLaunchPhase(spool, "unknown", a, g.RunID, g.TaskID, cmd, id, errors.New("codex_managed_state_unknown"))
		eventErr := appendEvent(contract.EventUnknown, id, nil, nil, structuredEventFields{})
		return contract.Result{Status: "unknown", ExitCode: -1, EventCount: count}, errors.Join(cause, phaseErr, eventErr)
	}
	reject := func(cause error) (contract.Result, error) {
		// This closure is unreachable once enterCodexLeaseStart succeeds.
		if err := h.recordLaunchFailed(a, g.RunID, g.TaskID, meta, cause); err != nil {
			return unknown(errors.Join(cause, err), nil)
		}
		if err := h.retireCodexHandleLease(lease, codexOriginalExit{}); err != nil {
			return unknown(errors.Join(cause, err), nil)
		}
		return contract.Result{Status: "failed", ExitCode: -1, EventCount: count + 1}, cause
	}
	if err = publishLaunchPhase(spool, "prepared", a, g.RunID, g.TaskID, cmd, nil, nil); err != nil {
		_ = h.retireCodexHandleLease(lease, codexOriginalExit{})
		return contract.Result{Status: "failed", ExitCode: -1}, err
	}
	if err = appendEvent(contract.EventPrepared, nil, nil, nil, structuredEventFields{}); err != nil {
		_ = h.retireCodexHandleLease(lease, codexOriginalExit{})
		return contract.Result{Status: "unknown", ExitCode: -1}, err
	}
	if ctx.Err() != nil || h.stopWasRequested(g.SegmentID) {
		return reject(context.Canceled)
	}
	if meta.diagnosticsGrant != nil {
		frozen := meta.diagnosticsGrant
		if frozen.Diagnostics == nil || frozen.RunID != g.RunID || frozen.TaskID != g.TaskID || frozen.AttemptID != g.AttemptID || frozen.SegmentID != g.SegmentID || frozen.HostID != g.HostID || frozen.CommandID != g.CommandID {
			return reject(errDiagnosticAuthority)
		}
		policy := *frozen.Diagnostics
		g.Diagnostics = &policy
	} else if g.Diagnostics != nil {
		return reject(errDiagnosticAuthority)
	}
	// Session selectors alone never authorize a legacy continuation. The typed
	// decision below must restore the exact original protected/sealed chain.
	if g.Continuation == nil && (g.SessionKind != "" || g.SessionID != "" || g.QuestionID != "" || g.QuestionRevision != 0 || g.Answer != "") {
		return reject(errors.New("codex_managed_continuation_unverified"))
	}
	var candidate *gitops.PreparedWorkspace
	var freeze func(context.Context, string) ([]byte, error)
	if descriptor, ok := adapter.ManagedCodexDescriptorFor(inv); ok && descriptor.Backend == adapter.CodexPluginWriteBackend {
		var closeCandidate func()
		freeze, closeCandidate, candidate, err = h.prepareCandidate(ctx, g, inv, inv.ExecutionProfile())
		if closeCandidate != nil {
			defer closeCandidate()
		}
		if err != nil || candidate == nil || freeze == nil {
			return reject(errors.Join(err, errors.New("codex_plugin_candidate_unavailable")))
		}
		meta.candidateResourceKind = "producer"
	}
	sourceEnv := os.Environ()
	prepared, err := h.prepareManagedCodexState(ctx, g, inv, sourceEnv, codexPreparationControl{Candidate: candidate})
	if err != nil {
		return reject(err)
	}
	cmd, prepared, err = codexManagedCommand(ctx, prepared, sourceEnv, h.spoolRoot)
	if err != nil || ctx.Err() != nil {
		return reject(errors.Join(err, ctx.Err()))
	}
	if prepared.Previous != nil && (h.codexCheckContinuationGrant(prepared, g) != nil || h.codexVerifyRestoredState(ctx, prepared) != nil) {
		return reject(errCodexStateFile)
	}
	// Always build/validate the real provider command first. Actor replacement
	// touches only that exact app-server argv tail; sandbox/env/pin stay intact.
	cmd, err = codexActorTail(ctx, cmd, prepared, control.actor)
	if err != nil {
		return reject(err)
	}
	if control.beforeStart != nil {
		control.beforeStart()
	}
	if ctx.Err() != nil || h.stopWasRequested(g.SegmentID) || codexVerifyActorCommand(ctx, control.actor) != nil || codexVerifyManagedReferences(ctx, prepared) != nil || prepared.Previous == nil && codexVerifyInitialPrepared(ctx, prepared) != nil || prepared.Previous != nil && h.codexVerifyRestoredState(ctx, prepared) != nil {
		return reject(errCodexStateFile)
	}
	diagnosticState.driver, err = h.openDetailedDiagnostics(ctx, g, diagnosticState.wakeExecution(cancelCause))
	if err != nil {
		return reject(err)
	}
	if diagnosticState.driver != nil {
		cmd.ObserveOutput = diagnosticState.driver.Observe
	}
	defer func() { returnedErr = errors.Join(returnedErr, diagnosticState.end(false, returnedErr)) }()
	if err = h.enterCodexLeaseStart(lease); err != nil {
		return reject(err)
	}
	if err = publishLaunchPhase(spool, "starting", a, g.RunID, g.TaskID, cmd, nil, nil); err != nil {
		// No Start was called, but the persisted boundary can be uncertain.
		return unknown(err, nil)
	}
	p, stream, startErr := process.StartDuplex(ctx, cmd)
	if p == nil {
		// Entered Start is not a positive never-started proof.
		return unknown(startErr, nil)
	}
	attachErr := h.attachCodexLease(lease, p)
	if stream != nil {
		defer stream.Close()
	}
	id := p.Identity()
	if startErr != nil || attachErr != nil {
		_, stopErr := h.stopCodexLease(lease, true)
		// A launcher receipt is immutable. A later physical observation does
		// not replace a failed startup Stop or create a refund/Exited event.
		return unknown(errors.Join(startErr, attachErr, stopErr), &id)
	}
	for _, phase := range []string{contract.EventSpawned, contract.EventRunning} {
		if err = publishLaunchPhase(spool, phase, a, g.RunID, g.TaskID, cmd, &id, nil); err == nil {
			err = appendEvent(phase, &id, nil, nil, structuredEventFields{})
		}
		if err != nil {
			_, stopErr := h.stopCodexLease(lease, true)
			return unknown(errors.Join(err, stopErr), &id)
		}
	}
	client, err := codexrpc.New(ctx, stream, codexrpc.Limits{})
	if err != nil {
		_, stopErr := h.stopCodexLease(lease, true)
		return unknown(errors.Join(err, stopErr), &id)
	}
	out, bound, runErr := h.runManagedCodexTurn(ctx, client, prepared, g, id)
	if bound.Session.EventID != "" {
		count++
	}
	stopped, stopErr := h.stopCodexLease(lease, runErr != nil)
	var exit codexOriginalExit
	if stopped {
		if stopErr != nil {
			return unknown(errors.Join(runErr, stopErr), &id)
		}
		h.stopMu.Lock()
		deadline := lease.op.deadline.Add(time.Second)
		h.stopMu.Unlock()
		proofCtx, proofCancel := context.WithDeadline(context.Background(), deadline)
		exit, err = codexProveOriginalExit(proofCtx, p)
		proofCancel()
	} else {
		waitErr := p.Wait(ctx)
		proofErr := waitErr
		if proofErr == nil {
			proofErr = p.ConfirmTreeExited()
		}
		// A canceled original Wait consumes the existing grace once; a
		// natural successful Wait must not invent a stop operation.
		joined, joinErr := false, error(nil)
		if waitErr != nil {
			joined, joinErr = h.stopCodexLease(lease, true)
		} else {
			joined, joinErr = h.stopAfterNaturalProof(g.SegmentID, p, proofErr, nil)
		}
		if joined {
			if joinErr != nil {
				return unknown(joinErr, &id)
			}
			stopped, stopErr = h.stopCodexLease(lease, false)
			if !stopped || stopErr != nil {
				return unknown(process.ErrProcessTreeUnknown, &id)
			}
			h.stopMu.Lock()
			deadline := lease.op.deadline.Add(time.Second)
			h.stopMu.Unlock()
			proofCtx, proofCancel := context.WithDeadline(context.Background(), deadline)
			exit, err = codexProveOriginalExit(proofCtx, p)
			proofCancel()
		} else if proofErr == nil {
			exit, err = codexProveOriginalExit(ctx, p)
		} else {
			err = proofErr
		}
	}
	if err != nil && ctx.Err() != nil {
		// Cancellation can arrive after the natural no-intent selection but
		// before the final original-Wait check. Join the same latched stop,
		// rather than dropping its still-running operation into UNKNOWN.
		if joined, joinErr := h.stopCodexLease(lease, true); joined && joinErr == nil {
			h.stopMu.Lock()
			deadline := lease.op.deadline.Add(time.Second)
			h.stopMu.Unlock()
			proofCtx, proofCancel := context.WithDeadline(context.Background(), deadline)
			exit, err = codexProveOriginalExit(proofCtx, p)
			proofCancel()
		} else {
			err = errors.Join(err, joinErr)
		}
	}
	if err != nil {
		return unknown(errors.Join(runErr, err), &id)
	}
	// Lock active time only after the original tree proof, including drain.
	activeMS := time.Since(lease.launch.SpawnedAt).Milliseconds()
	if activeMS < 0 {
		return unknown(process.ErrProcessTreeUnknown, &id)
	}
	// The original process is proved regardless of the RPC outcome. A late
	// logging abort cannot reclassify that outcome as caller cancellation.
	diagnosticState.proved.Store(true)
	if diagnosticState.driver != nil {
		if client.ReadEOF() {
			_ = diagnosticState.end(true, nil)
		} else {
			_ = diagnosticState.end(false, errors.New("codex_stdout_incomplete"))
		}
	}
	ownerStopped := ctx.Err() != nil || h.stopWasRequested(g.SegmentID)
	if !ownerStopped && runErr == nil && h.codexVerifyBoundThread(ctx, prepared, bound) != nil {
		runErr = errCodexStateFile
	}
	kind, status := contract.EventResult, "result_ready"
	content := []byte(out.Completion.Text)
	if ownerStopped {
		kind, status, content = contract.EventStopped, "interrupted", nil
	} else if runErr != nil || exit.exitCode != 0 || out.Status != "completed" {
		kind, status, content = contract.EventFailed, "failed", codexFailureContent(runErr)
	} else if out.Completion.Kind == "technical_question" {
		kind, status = contract.EventQuestion, "waiting_user"
		candidate, sealErr := h.captureManagedCodexQuestionSeal(ctx, prepared, g, bound, meta.questionRevision+1, hashBytes(content), codexStateReadControl{}, codexSealControl{BeforeFinalVerify: control.beforeSealFinalVerify})
		if sealErr == nil {
			_, sealErr = h.publishManagedCodexQuestionSeal(ctx, candidate, exit, out)
		}
		if sealErr != nil {
			kind, status, content, runErr = contract.EventFailed, "failed", []byte(`{"status":"failed","reason":"codex_history_seal_unavailable"}`), sealErr
		}
	} else if out.Completion.Version != 1 || out.Completion.Kind != "result" {
		kind, status, content, runErr = contract.EventFailed, "failed", []byte(`{"status":"failed","reason":"codex_managed_output_invalid"}`), codexsession.ErrOutput
	}
	if kind == contract.EventResult && freeze != nil {
		content, err = freeze(ctx, string(content))
		if err != nil {
			kind, status, content, runErr = contract.EventFailed, "failed", candidateFailure(err), err
		}
	}
	// A stop arriving during final reference/state capture still joins the
	// same original operation before terminal publication. It cannot turn a
	// canceled or UNKNOWN stop into a successful natural completion.
	if joined, joinErr := h.stopCodexLease(lease, false); joined {
		if joinErr != nil {
			return unknown(joinErr, &id)
		}
		if ctx.Err() != nil || h.stopWasRequested(g.SegmentID) {
			kind, status, content = contract.EventStopped, "interrupted", nil
		}
	}
	var artifact *contract.ArtifactRef
	path := ""
	if len(content) > 0 {
		artifact, err = h.writeHostArtifact(meta, "terminal", 0, "terminal", "result-output.bin", content)
		if err != nil {
			return unknown(err, &id)
		}
		path = artifact.Path
	}
	fields := structuredEventFields{SessionKind: "session-id", SessionID: out.Binding.ThreadID}
	if kind == contract.EventQuestion {
		fields.QuestionRevision = meta.questionRevision + 1
	} else if kind == contract.EventStopped {
		fields = structuredEventFields{StopReason: interruptionReason(ctx, h.stopWasRequested(g.SegmentID), exit.exitCode)}
	}
	if err = appendEvent(kind, &id, &exit.exitCode, artifact, fields); err != nil {
		return unknown(err, &id)
	}
	if err = publishLaunchPhase(spool, "exited", a, g.RunID, g.TaskID, cmd, &id, nil); err == nil {
		err = appendEvent(contract.EventExited, &id, &exit.exitCode, artifact, structuredEventFields{ActiveMS: activeMS})
	}
	if err != nil {
		return unknown(err, &id)
	}
	if status == "result_ready" && freeze != nil {
		count += h.registerCandidateResource(ctx, spool, g.RunID, g.TaskID, a, meta, content)
	}
	if err = h.retireCodexHandleLease(lease, exit); err != nil {
		return unknown(err, &id)
	}
	return contract.Result{Status: status, ExitCode: exit.exitCode, EventCount: count, ArtifactPath: path, OutputHash: hashBytes(content)}, runErr
}

// Actual post-spawn consumer shared by ExecuteLaunch and bounded net.Pipe
// tests. Native phase validation and durable Host binding both precede turn.
func (h *Host) runManagedCodexTurn(ctx context.Context, client *codexrpc.Client, p codexPreparedState, g contract.LaunchCommand, id process.Identity) (codexsession.Outcome, codexBoundThread, error) {
	var bound codexBoundThread
	var tools *codexsession.HostTools
	if p.Origin.Backend == adapter.CodexPluginBackend || p.Origin.Backend == adapter.CodexPluginWriteBackend {
		if p.Previous != nil || p.Origin.Profile.CodexHostToolsVersion != 1 || g.Continuation != nil {
			return codexsession.Outcome{}, bound, errCodexStateFile
		}
		var opened *codexsession.HostTools
		var closeTools func() error
		var err error
		if p.Origin.Backend == adapter.CodexPluginWriteBackend {
			if p.Candidate == nil {
				return codexsession.Outcome{}, bound, errCodexStateFile
			}
			opened, closeTools, err = openCodexCandidateTools(ctx, *p.Candidate, p.Protected)
		} else {
			opened, closeTools, err = openCodexReadTools(p.Origin.Workspace)
		}
		if err != nil {
			return codexsession.Outcome{}, bound, err
		}
		defer closeTools()
		tools = opened
	}
	prompt := strings.TrimSuffix(p.Descriptor.Prompt, "\n\n"+adapter.ManagedTaskInstruction())
	if tools != nil {
		prompt += "\nUse agent_bird.read_file and agent_bird.list_files with relative paths for all project inspection. If write_file is available, edit only declared paths using the SHA256 returned by read_file (empty expectedSHA256 only for new files). Read back edits using read_file. Shell execution is unavailable. Do not invoke other tools or delegate."
	}
	out, err := codexsession.Run(ctx, client, p.Projection, codexsession.Options{
		HostTools:        tools,
		ModelOverride:    string(p.Origin.Profile.Model),
		RequireStdoutEOF: g.Diagnostics != nil,
		CWD:              p.Origin.Workspace, OwnedHome: p.Layout.Home.Path,
		PlatformFamily: "unix", PlatformOS: "macos", Prompt: prompt, ResumeThreadID: p.Descriptor.ResumeThreadID,
		OnThread: func(callbackCtx context.Context, binding codexsession.ThreadBinding) error {
			var err error
			bound, err = h.bindManagedCodexThread(callbackCtx, p, g, id, binding)
			return err
		},
	}) // closes and joins M1 + every cooperative phase/callback job
	return out, bound, err
}

// A lease can retain a second original handle when the segment registry is
// occupied. Reuse its first operation without overwriting the foreign entry.
func (h *Host) stopCodexLease(lease *codexHandleLease, create bool) (bool, error) {
	h.stopMu.Lock()
	h.mu.Lock()
	if lease == nil || h.codexLeases[lease.command] != lease || lease.handle == nil {
		h.mu.Unlock()
		h.stopMu.Unlock()
		return true, process.ErrProcessTreeUnknown
	}
	i := h.stopIntents[lease.segment]
	if i == nil && h.stopping && h.stopAllIntent != nil {
		i = &segmentStopIntent{ctx: h.stopAllIntent.ctx, deadline: h.stopAllIntent.deadline, expires: h.stopAllIntent.expires, bounded: h.stopAllIntent.bounded}
		h.stopIntents[lease.segment] = i
	}
	if lease.op == nil && i != nil && !i.terminal {
		if i.op == nil || i.op.handle == lease.handle {
			lease.op = h.bindStopLocked(lease.segment, i, lease.handle, false)
		} else {
			lease.op = codexLeaseStopOperation(lease.segment, lease.handle, i)
		}
	}
	if lease.op == nil && i != nil && i.terminal {
		h.mu.Unlock()
		h.stopMu.Unlock()
		return true, process.ErrProcessTreeUnknown
	}
	if lease.op == nil && create {
		if i == nil {
			i = stopIntent(context.Background())
			h.stopIntents[lease.segment] = i
		}
		if lease.registryConflict || i.op != nil && i.op.handle != lease.handle {
			lease.op = codexLeaseStopOperation(lease.segment, lease.handle, i)
		} else {
			lease.op = h.bindStopLocked(lease.segment, i, lease.handle, false)
		}
	}
	op := lease.op
	h.mu.Unlock()
	h.stopMu.Unlock()
	if op == nil {
		return false, nil
	}
	h.startStop(op)
	return true, joinStop(op, nil)
}
