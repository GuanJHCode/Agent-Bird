package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// The runtime supplies ipc.OwnerClient.Call, which authenticates the kernel
// channel and verifies every full signed reply. This private boundary also
// permits isolated ACK-loss tests without pretending they are native RPC proof.
type ownerExecutionRPC func(context.Context, uint64, string, string, any) (contract.OwnerRPCReply, error)

type ownerExecutionConfig struct {
	host           *Host
	binding        contract.OriginalOwnerBinding
	privateKey     ed25519.PrivateKey
	coordinatorKey ed25519.PublicKey
	reportSocket   string
	invocation     func(context.Context, contract.LaunchCommand) (contract.InvocationView, error)
	rpc            ownerExecutionRPC
}

type ownerExecution struct {
	admissionFailure  error // guarded by mu; cleanup cannot hide a late durable failure
	admissions        sync.WaitGroup
	acceptGrant       func(contract.LaunchCommand) (bool, error)
	run               *livingSource
	cfg               ownerExecutionConfig
	scopeHash         string
	identityHash      string
	workflow          sync.Mutex // serial permit/reconciliation and immutable receipt
	applied           *contract.OwnerPermitApplied
	mu                sync.Mutex // admission/worker Add precedes shutdown Wait
	session           *ownerExecutionSession
	last              *ownerExecutionSession
	changed           chan struct{}
	recovery          chan error
	fatal             chan error // cannot be dropped behind a recoverable notification
	active            bool
	admitSignedLaunch bool
	observedEpoch     uint64
	stopping          bool
}

func newOwnerExecution(ctx context.Context, cfg ownerExecutionConfig) (*ownerExecution, error) {
	if cfg.host == nil || cfg.rpc == nil || cfg.invocation == nil || !filepath.IsAbs(cfg.reportSocket) || cfg.host.producerID != cfg.binding.Scope.ProducerID || len(cfg.privateKey) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(cfg.privateKey.Seed()), cfg.privateKey) || contract.VerifyOriginalOwnerBinding(cfg.binding, cfg.coordinatorKey) != nil || hex.EncodeToString(cfg.privateKey.Public().(ed25519.PublicKey)) != cfg.binding.Owner.PublicKey {
		return nil, errors.New("owner_execution_identity_rejected")
	}
	if err := cfg.host.BindContinuationOwner(cfg.binding.Scope.OriginalOriginContextID); err != nil {
		return nil, err
	}
	cfg.privateKey = append(ed25519.PrivateKey(nil), cfg.privateKey...)
	cfg.coordinatorKey = append(ed25519.PublicKey(nil), cfg.coordinatorKey...)
	scopeHash, _ := contract.OriginalOwnerScopeSHA256(cfg.binding.Scope)
	identityHash, _ := contract.OriginalOwnerIdentitySHA256(cfg.binding.Owner)
	e := &ownerExecution{acceptGrant: cfg.host.acceptGrant, cfg: cfg, scopeHash: scopeHash, identityHash: identityHash, changed: make(chan struct{}), recovery: make(chan error, 1), fatal: make(chan error, 1)}
	// The runtime registers the first stop window before canceling execution.
	// Transport/context loss is observed by that runtime, never inherited here.
	runCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	e.run = &livingSource{host: cfg.host, ctx: runCtx, cancel: cancel, working: make(map[string]bool), cfg: SourceConfig{SocketPath: cfg.reportSocket, SpoolRoot: cfg.host.spoolRoot, ProducerID: cfg.host.producerID, InvocationProvider: cfg.invocation, Hello: contract.HostHello{Executable: cfg.binding.Scope.Executable}}}
	e.run.prepareReport = e.prepareReport
	e.run.failure = func(cause error) { _ = e.failAdmission(cause) }
	cfg.host.progressSink = e.progress
	return e, nil
}

func (e *ownerExecution) reconcile(ctx context.Context, permit contract.OwnerControlPermit) error {
	e.workflow.Lock()
	defer e.workflow.Unlock()
	if contract.VerifyOwnerControlPermit(permit, e.cfg.coordinatorKey) != nil || permit.OwnerID != e.cfg.binding.Scope.OwnerID || permit.ScopeSHA256 != e.scopeHash || permit.OwnerIdentitySHA256 != e.identityHash {
		return errors.New("owner_execution_permit_rejected")
	}
	if permit.OldHost != nil {
		old := *permit.OldHost
		permit.OldHost = &old
	}
	e.mu.Lock()
	if e.stopping {
		e.mu.Unlock()
		return errors.New("owner_execution_stopping")
	}
	if permit.CoordinatorEpoch < e.observedEpoch {
		e.mu.Unlock()
		return errors.New("owner_execution_permit_rejected")
	}
	same := false
	if e.last != nil {
		prior := e.last.permit
		a, _ := json.Marshal(prior)
		b, _ := json.Marshal(permit)
		same = string(a) == string(b)
		if !same && (permit.ExpectedControlRevision != prior.ControlRevision || permit.CoordinatorEpoch < prior.CoordinatorEpoch || permit.OldHost == nil || *permit.OldHost != prior.NewHost || (permit.NewHost == prior.NewHost && permit.CoordinatorEpoch == prior.CoordinatorEpoch)) {
			e.mu.Unlock()
			return errors.New("owner_execution_permit_rejected")
		}
	} else if permit.ControlRevision != 1 || permit.NewHost != e.cfg.binding.FirstHost {
		e.mu.Unlock()
		return errors.New("owner_execution_permit_rejected")
	}
	s := &ownerExecutionSession{owner: e, permit: permit}
	e.session, e.last, e.active = s, s, false
	e.observedEpoch = permit.CoordinatorEpoch
	if !same {
		e.admitSignedLaunch = false
	}
	close(e.changed)
	e.changed = make(chan struct{})
	e.mu.Unlock()
	if !same {
		e.applied = nil
	}
	if err := e.run.host.PublishAll(ctx, s, permit.CoordinatorEpoch); err != nil {
		e.offline(s, err)
		return err
	}
	if err := e.run.reports.replay(ctx, s); err != nil {
		return err
	}
	if e.applied == nil {
		cursor, err := e.run.host.readPublished()
		if err != nil {
			return err
		}
		applied, err := contract.SignOwnerPermitApplied(contract.OwnerPermitApplied{Permit: permit, Reconciled: contract.HostReconciled{ProducerID: e.run.host.producerID, PublishedThrough: cursor}}, e.cfg.privateKey)
		if err != nil {
			return err
		}
		e.applied = &applied
	}
	// ACK loss must retain these exact signed bytes even if later worker events
	// advance the local publication cursor before the next reconciliation.
	if err := s.status(ctx, "applied", permit.PermitID+":applied", *e.applied, "applied"); err != nil {
		return err
	}
	e.mu.Lock()
	if e.stopping || e.session != s {
		e.mu.Unlock()
		return errCoordinatorUnavailable
	}
	// Coordinator publishes command signing authority to dispatch only after
	// Activate commits. A full signed launch for this exact permit therefore
	// proves activation even when its separate RPC reply arrives later or is
	// lost. Keep accepting those durable claims across transient transport loss;
	// a new permit or shutdown revokes this admission, not reply arrival order.
	e.admitSignedLaunch = true
	e.mu.Unlock()
	if err := s.status(ctx, "activate", permit.PermitID+":activate", *e.applied, "active"); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopping || e.session != s {
		return errCoordinatorUnavailable
	}
	e.active = true
	return nil
}

func (e *ownerExecution) command(raw []byte) error {
	command, err := contract.DecodeOwnerCommand(raw)
	if err != nil {
		return rejectOwnerCommand(ownerCommandInvalid, err)
	}
	// A stale frame is harmless only after its full signature and this owner's
	// immutable scope have been verified. Untrusted claimed epochs never revoke.
	if command.Permit.OwnerID != e.cfg.binding.Scope.OwnerID || command.Permit.ScopeSHA256 != e.scopeHash || command.Permit.OwnerIdentitySHA256 != e.identityHash || contract.VerifyOwnerCommand(command, e.cfg.coordinatorKey, command.Permit) != nil {
		return rejectOwnerCommand(ownerCommandInvalid, errors.New("owner_execution_command_rejected"))
	}
	e.mu.Lock()
	s := e.last
	if s != nil && command.Permit.CoordinatorEpoch > e.observedEpoch {
		if e.stopping {
			e.mu.Unlock()
			return rejectOwnerCommand(ownerCommandStopped, errors.New("owner_execution_stopping"))
		}
		recovery := &ownerExecutionRecoveryError{cause: errors.New("owner_command_newer_epoch")}
		e.observeEpochLocked(command.Permit.CoordinatorEpoch, recovery)
		e.mu.Unlock()
		return recovery
	}
	if s != nil && (command.Permit.ControlRevision < s.permit.ControlRevision || (command.Kind != "stop" && command.Permit.CoordinatorEpoch < e.observedEpoch)) {
		e.mu.Unlock()
		return rejectOwnerCommand(ownerCommandStale, errors.New("owner_execution_stale_command"))
	}
	if s == nil || contract.VerifyOwnerCommand(command, e.cfg.coordinatorKey, s.permit) != nil {
		e.mu.Unlock()
		return rejectOwnerCommand(ownerCommandInvalid, errors.New("owner_execution_command_rejected"))
	}
	if command.Kind == "terminal" {
		var terminal contract.OwnerTerminal
		if err = contract.DecodeOwnerRuntimeStrict(command.Body, &terminal); err != nil {
			e.mu.Unlock()
			return rejectOwnerCommand(ownerCommandInvalid, err)
		}
		e.stopping, e.active, e.admitSignedLaunch = true, false, false
		e.mu.Unlock()
		return sourceTerminalDisposition(terminal.Reason)
	}
	// A transport outage cannot erase stop authority for the latest verified
	// permit or the original Handle.
	if command.Kind == "stop" {
		e.mu.Unlock()
		var stop contract.StopCommand
		if err = contract.DecodeOwnerRuntimeStrict(command.Body, &stop); err != nil {
			return rejectOwnerCommand(ownerCommandInvalid, err)
		}
		e.run.dispatchStop(stop)
		return nil
	}
	if e.stopping {
		e.mu.Unlock()
		return rejectOwnerCommand(ownerCommandStopped, errors.New("owner_execution_stopping"))
	}
	if !e.admitSignedLaunch {
		e.mu.Unlock()
		return rejectOwnerCommand(ownerCommandInvalid, errors.New("owner_execution_not_active"))
	}
	if command.Kind == "launch" {
		var grant contract.LaunchCommand
		if err = contract.DecodeOwnerRuntimeStrict(command.Body, &grant); err == nil && (grant.RunID != e.cfg.binding.Scope.RunID || grant.HostID != e.cfg.binding.Scope.ProducerID) {
			err = errors.New("owner_execution_command_rejected")
		}
		if err != nil {
			e.mu.Unlock()
			return rejectOwnerCommand(ownerCommandInvalid, err)
		}
		// Authentication is the admission linearization point. A later permit
		// renewal cannot erase this already received durable claim. Slow disk I/O
		// must not hold the stop/renewal lock or prevent registering original TERM.
		e.admissions.Add(1)
		e.mu.Unlock()
		defer e.admissions.Done()
		duplicate, err := e.acceptGrant(grant)
		if err != nil {
			return e.failAdmission(err)
		}
		e.mu.Lock()
		if e.stopping {
			e.mu.Unlock()
			if !duplicate {
				if err = e.run.recordUnstartedGrant(grant, context.Canceled); err != nil {
					return e.failAdmission(err)
				}
			}
			return rejectOwnerCommand(ownerCommandStopped, errors.New("owner_execution_stopping"))
		}
		defer e.mu.Unlock()
		// workers.Add remains under the stop fence. Once beginStop acquires mu,
		// pending admissions can finish persistence but cannot create any worker.
		return e.run.dispatchAcceptedLaunch(grant, duplicate)
	}

	e.mu.Unlock()
	return rejectOwnerCommand(ownerCommandInvalid, errors.New("owner_execution_command_rejected"))
}

func (e *ownerExecution) failAdmission(cause error) error {
	e.mu.Lock()
	if e.admissionFailure == nil {
		e.admissionFailure = cause
	}
	// Seal admission immediately, but leave original worker cancellation to the
	// runtime's beginStop so its first shared TERM deadline is registered first.
	e.stopping, e.active, e.admitSignedLaunch = true, false, false
	select {
	case e.fatal <- cause:
	default:
	}
	e.mu.Unlock()
	return rejectOwnerCommand(ownerCommandLocalFailure, cause)
}

func (e *ownerExecution) offline(s *ownerExecutionSession, cause error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// A late authentication/local failure is still fatal even when a newer
	// session or an earlier recoverable notification already cleared this one.
	if cause != nil && !ownerExecutionNeedsRecovery(cause) {
		select {
		case e.fatal <- cause:
		default:
		}
	}
	if e.session != s {
		return
	}
	e.session, e.active = nil, false
	close(e.changed)
	e.changed = make(chan struct{})
	select {
	case e.recovery <- cause:
	default:
	}
}

func (e *ownerExecution) revokeAdmission(s *ownerExecutionSession) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.last == s {
		e.admitSignedLaunch, e.active = false, false
	}
}

// A newer signed epoch is already a revocation proof; obtaining its new permit
// may require reconnecting first. Never reopen an older permit in that gap,
// even if the proof came from a call on a concurrently replaced session.
func (e *ownerExecution) observeEpoch(epoch uint64, cause error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.observeEpochLocked(epoch, cause)
}

// Caller holds admission mu, including when the proof is a signed command.
func (e *ownerExecution) observeEpochLocked(epoch uint64, cause error) {
	if epoch <= e.observedEpoch {
		return
	}
	e.observedEpoch = epoch
	if e.last != nil && e.last.permit.CoordinatorEpoch < epoch {
		e.admitSignedLaunch, e.active, e.session = false, false, nil
		close(e.changed)
		e.changed = make(chan struct{})
		select {
		case e.recovery <- cause:
		default:
		}
	}
}

func (e *ownerExecution) available(ctx context.Context) (*ownerExecutionSession, error) {
	for {
		e.mu.Lock()
		s, changed, stopping := e.session, e.changed, e.stopping
		e.mu.Unlock()
		if stopping {
			return nil, errors.New("owner_execution_stopping")
		}
		if s != nil {
			return s, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// The serial runtime workflow calls this BEFORE Attach (or reconnect), not
// after receiving its reply. An in-flight old event can then be safely replayed
// if the Coordinator commits a replacement before local permit installation.
// Original signed claims/stop authority remain governed by the last permit.
func (e *ownerExecution) pausePublication() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.session, e.active = nil, false
	close(e.changed)
	e.changed = make(chan struct{})
}

func (e *ownerExecution) obsoleteRejection(session *ownerExecutionSession, err error) bool {
	var semantic *ownerExecutionSemanticError
	if !errors.As(err, &semantic) {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.session != session
}

func (e *ownerExecution) prepareReport(ctx context.Context, grant contract.LaunchCommand) (string, error) {
	for {
		s, err := e.available(ctx)
		if err != nil {
			return "", err
		}
		path, err := e.run.host.prepareReportCapability(ctx, e.run.cfg, grant, s, e.run.reports.remember)
		if err == nil {
			return path, nil
		}
		if e.obsoleteRejection(s, err) {
			continue // preserve the accepted claim and exact remembered capability
		}
		if !ownerExecutionNeedsRecovery(err) {
			return "", err // authenticated rejection or local failure, not an outage
		}
	}
}

// Progress stays observational: a missed best-effort update cannot consume a
// recovery attempt or revoke an otherwise working execution session. A verified
// newer epoch still revokes old authority, regardless of which RPC observed it.
func (e *ownerExecution) progress(_ context.Context, event contract.Event) error {
	e.mu.Lock()
	s := e.session
	e.mu.Unlock()
	if s == nil {
		return errCoordinatorUnavailable
	}
	ctx, cancel := context.WithTimeout(e.run.ctx, time.Second)
	defer cancel()
	event.ExecutionEpoch = s.permit.CoordinatorEpoch
	return s.status(ctx, "progress", event.EventID, event, "recorded")
}

func (e *ownerExecution) beginStop(ctx context.Context, cause error) {
	e.mu.Lock()
	e.stopping, e.active, e.admitSignedLaunch = true, false, false
	close(e.changed)
	e.changed = make(chan struct{})
	e.mu.Unlock()
	e.run.host.BeginStopAll(ctx)
	e.run.cancel(cause)
}

// join waits for both admitted writes and workers after beginStop sealed Add.
// A timeout reports incomplete ownership instead of inventing an original Wait.
func (e *ownerExecution) join(ctx context.Context) error {
	e.mu.Lock()
	stopping := e.stopping
	e.mu.Unlock()
	if !stopping {
		return errors.New("owner_execution_not_stopping")
	}
	done := make(chan struct{})
	go func() { e.admissions.Wait(); e.run.workers.Wait(); close(done) }()
	select {
	case <-done:
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.admissionFailure
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Final flush is allowed over the last verified permit after transport EOF.
// Only the living owner's private RPC can submit it; the runtime supplies the
// original shutdown deadline, including its final at-most-two-second window.
func (e *ownerExecution) flush(ctx context.Context) error {
	e.mu.Lock()
	s := e.last
	e.mu.Unlock()
	if s == nil {
		return nil
	}
	if err := e.run.host.PublishAll(ctx, s, s.permit.CoordinatorEpoch); err != nil {
		return err
	}
	return e.run.reports.replay(ctx, s)
}

type ownerExecutionSession struct {
	owner  *ownerExecution
	permit contract.OwnerControlPermit
}

// Only transport uncertainty and a verified newer Coordinator epoch can enter
// the reconnect path. In particular, a signed application error is never
// classified by its text or by a concurrent session replacement.
type ownerExecutionRecoveryError struct{ cause error }

// Only a fully verified, same-epoch Coordinator rejection gets this marker.
// Authentication/ACK corruption and local I/O must never be discarded merely
// because their original session was replaced while the call was in flight.
type ownerExecutionSemanticError struct{ message string }

func (e *ownerExecutionSemanticError) Error() string { return e.message }

func (e *ownerExecutionRecoveryError) Error() string { return e.cause.Error() }
func (e *ownerExecutionRecoveryError) Unwrap() error { return e.cause }
func ownerExecutionNeedsRecovery(err error) bool {
	var recovery *ownerExecutionRecoveryError
	return errors.As(err, &recovery)
}

func (s *ownerExecutionSession) reportEpoch() uint64 { return s.permit.CoordinatorEpoch }

func (s *ownerExecutionSession) verifyReply(reply contract.OwnerRPCReply, id string) error {
	if reply.Version != 1 || reply.OwnerID != s.permit.OwnerID || reply.CoordinatorEpoch != s.permit.CoordinatorEpoch || reply.RequestID != id || reply.Error != "" {
		return errors.New("owner_execution_reply_rejected")
	}
	return nil
}

func (s *ownerExecutionSession) call(ctx context.Context, kind, id string, body any) (contract.OwnerRPCReply, error) {
	op, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	reply, err := s.owner.cfg.rpc(op, s.permit.ControlRevision, kind, id, body)
	// A completed authenticated response remains authoritative even when the
	// caller has concurrently canceled. Cancellation can explain a missing
	// transport reply, never erase a signed revocation or a protocol failure.
	if reply.Version == 1 && reply.OwnerID == s.permit.OwnerID && reply.RequestID == id {
		if reply.CoordinatorEpoch > s.permit.CoordinatorEpoch {
			err = &ownerExecutionRecoveryError{cause: errCoordinatorUnavailable}
			s.owner.observeEpoch(reply.CoordinatorEpoch, err)
		} else if reply.CoordinatorEpoch == s.permit.CoordinatorEpoch && reply.Error != "" {
			// OwnerClient returns this reply only after full signature and
			// request-hash verification. Preserve the Store rejection without
			// clearing the session or requesting another recovery reservation.
			if kind == "activate" {
				s.owner.revokeAdmission(s)
			}
			return reply, &ownerExecutionSemanticError{message: reply.Error}
		} else if err == nil {
			err = s.verifyReply(reply, id)
		}
	} else if err != nil && reply.Version == 0 && (transientSourceError(err) || errors.Is(err, context.Canceled)) {
		if ctx.Err() != nil {
			return reply, ctx.Err()
		}
		if kind == "progress" {
			return reply, err
		}
		err = &ownerExecutionRecoveryError{cause: err}
	} else if err == nil {
		err = errors.New("owner_execution_reply_rejected")
	}
	if err != nil {
		if !ownerExecutionNeedsRecovery(err) {
			s.owner.revokeAdmission(s)
		}
		s.owner.offline(s, err)
	}
	return reply, err
}

func (s *ownerExecutionSession) verifyStatus(reply contract.OwnerRPCReply, id, status string) error {
	if err := s.verifyReply(reply, id); err != nil {
		return err
	}
	var result struct {
		Status string `json:"status"`
	}
	if contract.DecodeOwnerRuntimeStrict(reply.Body, &result) != nil || result.Status != status {
		return errors.New("owner_execution_reply_rejected")
	}
	return nil
}

func (s *ownerExecutionSession) status(ctx context.Context, kind, id string, body any, expected string) error {
	reply, err := s.call(ctx, kind, id, body)
	if err != nil {
		return err
	}
	err = s.verifyStatus(reply, id, expected)
	if err != nil {
		s.owner.revokeAdmission(s)
		s.owner.offline(s, err)
	}
	return err
}

func (s *ownerExecutionSession) SendEvent(ctx context.Context, epoch uint64, event contract.Event) (contract.DurableAck, error) {
	var ack contract.DurableAck
	if epoch != s.permit.CoordinatorEpoch || event.ExecutionEpoch != epoch || event.ProducerID != s.owner.cfg.binding.Scope.ProducerID {
		return ack, errors.New("owner_execution_event_rejected")
	}
	reply, err := s.call(ctx, "event", event.EventID, event)
	if err != nil {
		return ack, err
	}
	if err = contract.DecodeOwnerRuntimeStrict(reply.Body, &ack); err != nil {
		s.owner.revokeAdmission(s)
		s.owner.offline(s, err)
	}
	return ack, err
}

func (s *ownerExecutionSession) registerReportCapability(ctx context.Context, registration contract.ReportCapabilityRegistration) error {
	if registration.ExecutionEpoch != s.permit.CoordinatorEpoch || registration.ProducerID != s.owner.cfg.binding.Scope.ProducerID {
		return errors.New("owner_execution_report_rejected")
	}
	reply, err := s.call(ctx, "report_capability", registration.CapabilityID, registration)
	if err != nil {
		return err
	}
	var ack contract.DurableAck
	if contract.DecodeOwnerRuntimeStrict(reply.Body, &ack) != nil || ack.Status != "durable" || ack.EventID != registration.CapabilityID || ack.PayloadHash != registration.TokenHash {
		err = errors.New("report_capability_ack_mismatch")
		s.owner.revokeAdmission(s)
		s.owner.offline(s, err)
	}
	return err
}
