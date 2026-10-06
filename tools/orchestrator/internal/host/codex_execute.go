package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// The managed backend never attaches a legacy stdout collector. The original
// handle is leased before Start; every returned handle, including h+err, is
// attached to that lease before any phase/RPC/binding operation can fail.
func (h *Host) executeManagedCodex(ctx context.Context, g contract.LaunchCommand, inv adapter.Invocation, meta launchMetadata, control codexExecuteControl) (contract.Result, error) {
	ctx, cancel := context.WithCancel(ctx)
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
	appendEvent := func(kind string, id *process.Identity, exit *int, artifact *contract.ArtifactRef, fields structuredEventFields) error {
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
	// Session selectors alone never authorize a legacy continuation. The typed
	// decision below must restore the exact original protected/sealed chain.
	if g.Continuation == nil && (g.SessionKind != "" || g.SessionID != "" || g.QuestionID != "" || g.QuestionRevision != 0 || g.Answer != "") {
		return reject(errors.New("codex_managed_continuation_unverified"))
	}
	sourceEnv := os.Environ()
	prepared, err := h.prepareManagedCodexState(ctx, g, inv, sourceEnv, codexPreparationControl{})
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
	ownerStopped := ctx.Err() != nil || h.stopWasRequested(g.SegmentID)
	if !ownerStopped && runErr == nil && h.codexVerifyBoundThread(ctx, prepared, bound) != nil {
		runErr = errCodexStateFile
	}
	kind, status := contract.EventResult, "result_ready"
	content := []byte(out.Completion.Text)
	if ownerStopped {
		kind, status, content = contract.EventStopped, "interrupted", nil
	} else if runErr != nil || exit.exitCode != 0 || out.Status != "completed" {
		kind, status, content = contract.EventFailed, "failed", []byte(`{"status":"failed","reason":"codex_managed_protocol_failed"}`)
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
		path, err = spool.WriteArtifact(content)
		if err != nil {
			return unknown(err, &id)
		}
		artifact = &contract.ArtifactRef{ID: filepath.Base(path), Path: path, Size: int64(len(content)), SHA256: hashBytes(content)}
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
	if err = h.retireCodexHandleLease(lease, exit); err != nil {
		return unknown(err, &id)
	}
	return contract.Result{Status: status, ExitCode: exit.exitCode, EventCount: count, ArtifactPath: path, OutputHash: hashBytes(content)}, runErr
}

// Actual post-spawn consumer shared by ExecuteLaunch and bounded net.Pipe
// tests. Native phase validation and durable Host binding both precede turn.
func (h *Host) runManagedCodexTurn(ctx context.Context, client *codexrpc.Client, p codexPreparedState, g contract.LaunchCommand, id process.Identity) (codexsession.Outcome, codexBoundThread, error) {
	var bound codexBoundThread
	prompt := strings.TrimSuffix(p.Descriptor.Prompt, "\n\n"+adapter.ManagedTaskInstruction())
	out, err := codexsession.Run(ctx, client, p.Projection, codexsession.Options{
		CWD: p.Origin.Workspace, OwnedHome: p.Layout.Home.Path,
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
