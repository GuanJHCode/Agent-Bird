package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/bridgecodec"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/grokacp"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/grokorigin"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// An incomplete cleanup error retains the original lifecycle object for an
// in-process caller. Durable origin metadata preserves the unknown state for
// inspection; it never grants a restarted controller permission to resend.
type controllerACPCleanupError struct {
	prepared *grokorigin.Prepared
	cause    error
}

func (e *controllerACPCleanupError) Error() string { return "controller_acp_cleanup_incomplete" }
func (e *controllerACPCleanupError) Unwrap() error { return e.cause }

type controllerACPSession struct {
	ctx               context.Context
	state, handlePath string
	opTimeout         time.Duration
	metadata          controllerACPMetadata
	metadataWritten   bool
	handle            taskHandle
	prepared          *grokorigin.Prepared
	launch            *grokorigin.Launch
	updatesDone       chan struct{}
	uncertain         bool
}

func controllerACPError(err error) error {
	var transport *grokacp.TransportError
	if errors.As(err, &transport) {
		return codeError("controller_acp_" + transport.Code())
	}
	return bridgeCleanError(err)
}

func (s *controllerACPSession) metadataPath() string { return s.handlePath + ".acp-origin.json" }
func (s *controllerACPSession) save(next controllerACPMetadata) error {
	if err := replaceControllerACPMetadata(s.metadataPath(), s.metadata, next); err != nil {
		return err
	}
	s.metadata = next
	return nil
}
func (s *controllerACPSession) operation() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.ctx, s.opTimeout)
}
func (s *controllerACPSession) rpc(kind ipc.Kind, request, response any) error {
	ctx, cancel := s.operation()
	defer cancel()
	reply, err := call(ctx, s.state, kind, request)
	if err != nil {
		return err
	}
	return decodeControllerACPResponse(reply.Payload, response)
}
func (s *controllerACPSession) base(rid string) contract.BridgeOwnerRequest {
	return contract.BridgeOwnerRequest{BridgeRequestBase: contract.BridgeRequestBase{SchemaVersion: 1, BindingID: s.metadata.BindingID, OwnerEpoch: s.metadata.OwnerEpoch, RequestID: rid}, OwnerMode: "local", OwnerCapabilityFile: s.metadata.OwnerCapabilityFile, ControlFile: s.metadata.ControlFile}
}

func runControllerACP(ctx context.Context, state, handlePath string, plan taskPlan, owner controllerOwnerGrant, request grokorigin.Request) (retErr error) {
	stopPipeSignals := retainACPOutputFailures()
	defer stopPipeSignals()
	total, cancel := context.WithTimeout(ctx, time.Duration(request.TotalTimeoutMS)*time.Millisecond)
	defer cancel()
	s := &controllerACPSession{ctx: total, state: state, handlePath: handlePath, opTimeout: time.Duration(request.OperationTimeoutMS) * time.Millisecond}
	defer func() { retErr = s.finish(retErr, os.Stdout) }()
	var err error
	s.prepared, err = grokorigin.Prepare(total, state, request)
	if err != nil {
		return err
	}
	s.handle, err = submitControllerPlan(total, state, handlePath, plan, owner, func(h taskHandle) error {
		s.metadata = controllerACPMetadata{Version: 1, TransportMode: contract.BridgeTransportGrokACP, RunID: h.RunID, OwnerMode: "local", OwnerCapabilityFile: owner.OwnerCapability, OwnerContextID: owner.OriginContextID, OwnerPID: owner.OriginPID, OwnerBirth: owner.OriginBirth, ManifestSHA256: contract.GrokACPManifestSHA256(), Status: "submitting"}
		if err := writeControllerBridgeExclusive(s.metadataPath(), s.metadata); err != nil {
			return err
		}
		s.metadataWritten = true
		return nil
	}, func(h taskHandle) error { next := s.metadata; next.ControlFile = h.ControlFile; return s.save(next) })
	if err != nil {
		return err
	}
	s.launch, err = s.prepared.Start(total)
	if err != nil {
		return err
	}
	client := s.launch.Client()
	s.updatesDone = make(chan struct{})
	go func() {
		defer close(s.updatesDone)
		for range client.Updates() {
		}
	}()
	identity := s.launch.Identity()
	bindRID, err := randomValue("acp-bind")
	if err != nil {
		return err
	}
	next := s.metadata
	next.BindingRequestID = bindRID
	next.NativePID = identity.PID
	next.NativeBirth = identity.Birth
	next.NativeExecutableSHA256 = identity.ExecutableSHA256
	next.Status = "started"
	if err = s.save(next); err != nil {
		return err
	}
	bind := contract.ACPBindRequest{SchemaVersion: 1, RequestID: bindRID, RunID: s.handle.RunID, ProviderID: "grok", NativePID: identity.PID, NativeBirth: identity.Birth, NativeExecutableSHA256: identity.ExecutableSHA256, TransportMode: contract.BridgeTransportGrokACP, OwnerMode: "local", OwnerCapabilityFile: owner.OwnerCapability, ControlFile: s.handle.ControlFile, ManifestSHA256: contract.GrokACPManifestSHA256()}
	var view contract.BridgeBindingView
	if err = s.rpc(ipc.KindACPBridgeBind, bind, &view); err != nil {
		return err
	}
	if !controllerACPBindingMatches(view, s.metadata, identity, "PENDING_SESSION", "") || view.BindingID == "" || view.OwnerEpoch != 1 {
		return codeError("controller_acp_receipt_invalid")
	}
	next = s.metadata
	next.BindingID = view.BindingID
	next.OwnerEpoch = view.OwnerEpoch
	next.Status = "bound"
	if err = s.save(next); err != nil {
		return err
	}
	op, done := s.operation()
	err = client.Initialize(op)
	done()
	if err != nil {
		return err
	}
	op, done = s.operation()
	session, err := client.NewSession(op, request.Workspace)
	done()
	if err != nil {
		return err
	}
	activate := contract.ACPActivateRequest{BridgeOwnerRequest: s.base(""), NativeSessionID: session}
	activate.RequestID = contract.ACPActivateRequestID(activate)
	activationRaw, _ := json.Marshal(activate)
	if _, err = contract.DecodeACPActivateRequest(activationRaw); err != nil {
		return codeError("controller_acp_session_invalid")
	}
	op, done = s.operation()
	initial, err := client.Prompt(op, session, request.InitialPrompt)
	done()
	if err != nil {
		return err
	}
	if initial.SessionID != session || initial.RPCRequestID != 3 {
		return codeError("controller_acp_receipt_invalid")
	}
	var activeView contract.BridgeBindingView
	if err = s.rpc(ipc.KindACPBridgeActivate, activate, &activeView); err != nil {
		return err
	}
	if !controllerACPBindingMatches(activeView, s.metadata, identity, "ACTIVE", session) || activeView.BindingID != s.metadata.BindingID || activeView.OwnerEpoch != s.metadata.OwnerEpoch {
		return codeError("controller_acp_receipt_invalid")
	}
	next = s.metadata
	next.NativeSessionID = session
	next.Status = "active"
	if err = s.save(next); err != nil {
		return err
	}
	if err = json.NewEncoder(os.Stdout).Encode(struct {
		Version   int    `json:"version"`
		Status    string `json:"status"`
		BindingID string `json:"binding_id"`
		NativePID int    `json:"native_pid"`
	}{1, "controller_acp_active", view.BindingID, identity.PID}); err != nil {
		return codeError("controller_acp_status_write_failed")
	}
	return s.deliver(client)
}

func controllerACPBindingMatches(view contract.BridgeBindingView, m controllerACPMetadata, id process.Identity, status, session string) bool {
	return view.SchemaVersion == 1 && view.RunID == m.RunID && view.ProviderID == "grok" && view.TransportMode == contract.BridgeTransportGrokACP && view.ManifestSHA256 == contract.GrokACPManifestSHA256() && view.Role == "owner" && view.OwnerContextID == m.OwnerContextID && view.OwnerPID == m.OwnerPID && view.OwnerBirth == m.OwnerBirth && view.NativePID == id.PID && view.NativeBirth == id.Birth && view.NativeExecutableSHA256 == id.ExecutableSHA256 && view.NativeSessionID == session && view.Status == status && view.CapabilityFile == "" && view.CandidateObservationEventID == "" && view.CandidateObservationHash == ""
}

func (s *controllerACPSession) deliver(client *grokacp.Client) error {
	ordinal := int64(1)
	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-client.Done():
			return client.Err()
		default:
		}
		rid, err := randomValue("acp-claim")
		if err != nil {
			return err
		}
		claim := contract.ACPClaimRequest{BridgeOwnerRequest: s.base(rid)}
		var reply contract.ACPClaimReply
		if err = s.rpc(ipc.KindACPBridgeClaim, claim, &reply); err != nil {
			if err.Error() != "bridge_empty_batch" {
				return err
			}
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-s.ctx.Done():
				timer.Stop()
				return s.ctx.Err()
			case <-client.Done():
				timer.Stop()
				return client.Err()
			case <-timer.C:
			}
			continue
		}
		begin, err := controllerACPBeginForClaim(s.base(""), s.metadata.NativeSessionID, rid, ordinal, reply)
		if err != nil {
			return err
		}
		s.uncertain = true
		var committed controllerACPCommitReceipt
		if err = s.rpc(ipc.KindACPBridgeBeginEmit, begin, &committed); err != nil {
			return err
		}
		if committed.Status != "committed" {
			return codeError("controller_acp_receipt_invalid")
		}
		op, done := s.operation()
		turn, err := client.Prompt(op, s.metadata.NativeSessionID, reply.PromptText)
		done()
		if err != nil {
			return err
		}
		if turn.SessionID != begin.NativeSessionID || turn.PromptSHA256 != begin.PromptSHA256 || turn.RPCRequestID != uint64(ordinal+3) || turn.RPCRequestID > math.MaxInt64 {
			return codeError("controller_acp_receipt_invalid")
		}
		received := contract.ACPReceiveRequest{ACPBeginRequest: begin, RPCRequestID: int64(turn.RPCRequestID), ResponseSHA256: turn.ResponseSHA256, StopReason: turn.StopReason}
		received.RequestID = contract.ACPReceiveRequestID(received)
		if _, err = persistACPCompletion(s.state, received); err != nil {
			return err
		}
		var receipt contract.BridgeReceiveReceipt
		for attempt := 0; attempt < 2; attempt++ {
			err = s.rpc(ipc.KindACPBridgeReceived, received, &receipt)
			if err == nil {
				break
			}
			var remote remoteError
			var invalid codeError
			if errors.As(err, &remote) || errors.As(err, &invalid) || s.ctx.Err() != nil {
				break
			}
			// One data-only replay after transport ACK loss, with the same durable
			// request. Never repeat Begin or Prompt from this recovery branch.
		}
		if err != nil {
			return err
		}
		if receipt.ReceiptID == "" || receipt.BatchID != begin.BatchID || receipt.AttemptID != begin.AttemptID || receipt.RenderSHA256 != begin.RenderSHA256 {
			return codeError("controller_acp_receipt_invalid")
		}
		if _, err = time.Parse(time.RFC3339Nano, receipt.ReceivedAt); err != nil {
			return codeError("controller_acp_receipt_invalid")
		}
		s.uncertain = false
		if err = json.NewEncoder(os.Stdout).Encode(struct {
			Version   int    `json:"version"`
			Status    string `json:"status"`
			BatchID   string `json:"batch_id"`
			AttemptID string `json:"attempt_id"`
			ReceiptID string `json:"receipt_id"`
			Ordinal   int64  `json:"turn_ordinal"`
		}{1, "controller_acp_protocol_received", receipt.BatchID, receipt.AttemptID, receipt.ReceiptID, ordinal}); err != nil {
			return codeError("controller_acp_status_write_failed")
		}
		if ordinal >= math.MaxInt64-3 {
			return codeError("controller_acp_ordinal_exhausted")
		}
		ordinal++
	}
}

func controllerACPBeginForClaim(base contract.BridgeOwnerRequest, session, claimRID string, ordinal int64, reply contract.ACPClaimReply) (contract.ACPBeginRequest, error) {
	invalid := codeError("controller_acp_claim_invalid")
	b := reply.Batch
	if ordinal < 1 || ordinal > math.MaxInt64-3 || b.SchemaVersion != 1 || b.BindingID != base.BindingID || b.OwnerEpoch != base.OwnerEpoch || b.TransportMode != contract.BridgeTransportGrokACP || b.AttemptID != contract.ACPAttemptID(base.BindingID, base.OwnerEpoch, claimRID) || reply.NativeSessionID != session || reply.TurnOrdinal != ordinal {
		return contract.ACPBeginRequest{}, invalid
	}
	prompt, err := bridgecodec.ACPPromptBytes(b)
	sum := sha256.Sum256(prompt)
	if err != nil || !bytes.Equal(prompt, []byte(reply.PromptText)) || hex.EncodeToString(sum[:]) != reply.PromptSHA256 || reply.ClaimToken == "" || bytes.Contains(prompt, []byte(reply.ClaimToken)) {
		return contract.ACPBeginRequest{}, invalid
	}
	begin := contract.ACPBeginRequest{BridgeOwnerRequest: base, NativeSessionID: session, BatchID: b.BatchID, AttemptID: b.AttemptID, ClaimToken: reply.ClaimToken, RenderSHA256: b.RenderSHA256, PromptSHA256: reply.PromptSHA256, TurnOrdinal: ordinal}
	begin.RequestID = contract.ACPBeginRequestID(begin)
	raw, _ := json.Marshal(begin)
	if _, err = contract.DecodeACPBeginRequest(raw); err != nil {
		return contract.ACPBeginRequest{}, invalid
	}
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": ordinal + 3, "method": "session/prompt", "params": map[string]any{"sessionId": session, "prompt": []map[string]string{{"type": "text", "text": reply.PromptText}}}})
	if err != nil || len(frame)+1 > grokacp.MaxFrameBytes {
		return contract.ACPBeginRequest{}, invalid
	}
	return begin, nil
}

// Keep Go's standard-output EPIPE path from terminating the lifecycle owner.
// Notify/Stop adds only this subscription; it preserves other subscribers.
func retainACPOutputFailures() func() {
	if signal.Ignored(syscall.SIGPIPE) {
		return func() {}
	}
	pipeSignals := make(chan os.Signal, 1)
	signal.Notify(pipeSignals, syscall.SIGPIPE)
	return func() { signal.Stop(pipeSignals) }
}

func (s *controllerACPSession) terminalMetadata(status string) controllerACPMetadata {
	next := s.metadata
	next.Status = status
	if s.launch != nil {
		if exit, e := s.launch.ExitCode(); e == nil {
			next.NativeExitKnown = true
			next.NativeExitCode = exit
			next.NativeTreeExited = true
		}
	}
	return next
}

func (s *controllerACPSession) reportTerminal(next controllerACPMetadata, stdout io.Writer) error {
	persisted := true
	status, intended := next.Status, ""
	var result error
	if err := s.save(next); err != nil {
		persisted = false
		status = "controller_acp_metadata_persistence_failed"
		intended = next.Status
		result = errors.Join(codeError(status), controllerACPError(err))
	}
	if err := json.NewEncoder(stdout).Encode(struct {
		Version           int    `json:"version"`
		Status            string `json:"status"`
		IntendedStatus    string `json:"intended_status,omitempty"`
		MetadataPersisted bool   `json:"metadata_persisted"`
		NativePID         int    `json:"native_pid"`
		ExitKnown         bool   `json:"native_exit_known"`
		ExitCode          int    `json:"native_exit_code"`
		TreeExited        bool   `json:"native_tree_exited"`
	}{1, status, intended, persisted, next.NativePID, next.NativeExitKnown, next.NativeExitCode, next.NativeTreeExited}); err != nil {
		result = errors.Join(result, codeError("controller_acp_status_write_failed"))
	}
	return result
}

func (s *controllerACPSession) finish(cause error, stdout io.Writer) error {
	retErr := controllerACPError(cause)
	var cleanupErr, reportErr error
	if s.prepared != nil {
		cleanupErr = s.prepared.Close(context.Background())
		if cleanupErr != nil && s.prepared.ConfirmTreeExited() != nil {
			if s.metadataWritten {
				reportErr = s.reportTerminal(s.terminalMetadata("cleanup_pending"), stdout)
			}
			// One fresh bounded cleanup stage retains the very same original handles.
			cleanupErr = s.prepared.Close(context.Background())
			if cleanupErr != nil && s.prepared.ConfirmTreeExited() != nil {
				// Active-work timeout never authorizes dropping a live lifecycle owner.
				// Quarantine is passive: no RPC, Prompt, replacement PID or new session.
				ticker := time.NewTicker(time.Second)
				for s.prepared.ConfirmTreeExited() != nil {
					<-ticker.C
				}
				ticker.Stop()
				cleanupErr = s.prepared.Close(context.Background())
			}
		}
	}
	if s.updatesDone != nil {
		<-s.updatesDone
	}
	if s.metadataWritten {
		status := "stopped"
		if retErr != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
			status = "failed"
		}
		if s.uncertain {
			status = "uncertain"
		}
		if cleanupErr != nil {
			status = "cleanup_incomplete"
		}
		reportErr = errors.Join(reportErr, s.reportTerminal(s.terminalMetadata(status), stdout))
	}
	retErr = errors.Join(reportErr, retErr)
	if cleanupErr != nil {
		retErr = &controllerACPCleanupError{prepared: s.prepared, cause: errors.Join(retErr, cleanupErr)}
	}
	if retErr != nil {
		controllerBridgeRecoveryNotice(s.handlePath, s.handle)
	}
	return retErr
}
