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
	run          *livingSource
	cfg          ownerExecutionConfig
	scopeHash    string
	identityHash string
	workflow     sync.Mutex // serial permit/reconciliation and immutable receipt
	applied      *contract.OwnerPermitApplied
	mu           sync.Mutex // admission/worker Add precedes shutdown Wait
	session      *ownerExecutionSession
	last         *ownerExecutionSession
	changed      chan struct{}
	recovery     chan error
	active       bool
	stopping     bool
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
	e := &ownerExecution{cfg: cfg, scopeHash: scopeHash, identityHash: identityHash, changed: make(chan struct{}), recovery: make(chan error, 1)}
	// The runtime registers the first stop window before canceling execution.
	// Transport/context loss is observed by that runtime, never inherited here.
	runCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	e.run = &livingSource{host: cfg.host, ctx: runCtx, cancel: cancel, working: make(map[string]bool), cfg: SourceConfig{SocketPath: cfg.reportSocket, SpoolRoot: cfg.host.spoolRoot, ProducerID: cfg.host.producerID, InvocationProvider: cfg.invocation, Hello: contract.HostHello{Executable: cfg.binding.Scope.Executable}}}
	e.run.prepareReport = e.prepareReport
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
		return err
	}
	e.mu.Lock()
	s := e.last
	if s == nil || contract.VerifyOwnerCommand(command, e.cfg.coordinatorKey, s.permit) != nil {
		e.mu.Unlock()
		return errors.New("owner_execution_command_rejected")
	}
	if command.Kind == "terminal" {
		var terminal contract.OwnerTerminal
		if err = contract.DecodeOwnerRuntimeStrict(command.Body, &terminal); err != nil {
			e.mu.Unlock()
			return err
		}
		e.stopping, e.active = true, false
		e.mu.Unlock()
		return sourceTerminalDisposition(terminal.Reason)
	}
	// Loss of transport readiness revokes new launch admission. It does not
	// erase a stop signed for the latest verified permit and original Handle.
	if command.Kind == "stop" {
		e.mu.Unlock()
		var stop contract.StopCommand
		if err = contract.DecodeOwnerRuntimeStrict(command.Body, &stop); err != nil {
			return err
		}
		e.run.dispatchStop(stop)
		return nil
	}
	if e.stopping || !e.active || e.session != s {
		e.mu.Unlock()
		return errors.New("owner_execution_not_active")
	}
	if command.Kind == "launch" {
		var grant contract.LaunchCommand
		if err = contract.DecodeOwnerRuntimeStrict(command.Body, &grant); err == nil && (grant.RunID != e.cfg.binding.Scope.RunID || grant.HostID != e.cfg.binding.Scope.ProducerID) {
			err = errors.New("owner_execution_command_rejected")
		}
		if err == nil {
			err = e.run.dispatchLaunch(grant)
		}
		e.mu.Unlock()
		return err
	}
	e.mu.Unlock()
	return errors.New("owner_execution_command_rejected")
}

func (e *ownerExecution) offline(s *ownerExecutionSession, cause error) {
	e.mu.Lock()
	defer e.mu.Unlock()
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
		e.mu.Lock()
		current := e.session == s
		e.mu.Unlock()
		if current {
			return "", err // local capability failure, not connection recovery
		}
	}
}

// Progress stays observational: a missed best-effort update cannot consume a
// recovery attempt or revoke an otherwise working execution session.
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
	reply, err := e.cfg.rpc(ctx, s.permit.ControlRevision, "progress", event.EventID, event)
	if err != nil {
		return err
	}
	return s.verifyStatus(reply, event.EventID, "recorded")
}

func (e *ownerExecution) beginStop(ctx context.Context, cause error) {
	e.mu.Lock()
	e.stopping, e.active = true, false
	close(e.changed)
	e.changed = make(chan struct{})
	e.mu.Unlock()
	e.run.host.BeginStopAll(ctx)
	e.run.cancel(cause)
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
	if err == nil {
		err = s.verifyReply(reply, id)
	}
	if err != nil {
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
	if err == nil {
		err = s.verifyStatus(reply, id, expected)
	}
	if err != nil {
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
		s.owner.offline(s, err)
	}
	return err
}
