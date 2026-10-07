package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type sourceTerminalDisposition string

func (e sourceTerminalDisposition) Error() string { return string(e) }

type sourceConnections struct {
	mu      sync.Mutex
	current *sourceSession
	changed chan struct{}
}

func (c *sourceConnections) set(session *sourceSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = session
	if c.changed != nil {
		close(c.changed)
	}
	c.changed = make(chan struct{})
}

func (c *sourceConnections) clear(session *sourceSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != session {
		return
	}
	c.current = nil
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *sourceConnections) available(ctx context.Context) (*sourceSession, error) {
	for {
		c.mu.Lock()
		if c.changed == nil {
			c.changed = make(chan struct{})
		}
		session, changed := c.current, c.changed
		c.mu.Unlock()
		if session != nil {
			select {
			case <-session.done:
			default:
				return session, nil
			}
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c *sourceConnections) snapshot() *sourceSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

type livingSource struct {
	cfg           SourceConfig
	host          *Host
	ctx           context.Context
	cancel        context.CancelCauseFunc
	connections   sourceConnections
	workers       sync.WaitGroup
	workingMu     sync.Mutex
	working       map[string]bool
	publisherDone <-chan struct{}
	reports       sourceReportRegistry
	prepareReport func(context.Context, contract.LaunchCommand) (string, error)
	// Original-owner runtime must register its shared TERM window before
	// cancellation. Legacy source behavior continues to use cancel directly.
	failure func(error)
}

func runLivingSource(parent context.Context, cfg SourceConfig, h *Host) error {
	scope, err := sourceRecoveryScope(cfg.Hello)
	if err != nil {
		return err
	}
	recovery, err := openSourceRecovery(parent, cfg, scope)
	if err != nil {
		return err
	}
	defer recovery.close()
	ctx, cancel := context.WithCancelCause(parent)
	r := &livingSource{cfg: cfg, host: h, ctx: ctx, cancel: cancel, working: make(map[string]bool)}
	h.progressSink = r.progress
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				birth, err := process.Birth(cfg.Hello.OriginPID)
				if err != nil || birth != cfg.Hello.OriginBirth {
					cancel(errors.New("origin_owner_lost"))
					return
				}
			}
		}
	}()
	defer func() {
		// Register shared original operations before cancellation wakes execute.
		// TERM keeps the existing10s; its primitive KILL confirmation has the
		// existing1s. Both deadlines derive from this single cleanup start.
		started := time.Now()
		termCtx, stopTerm := context.WithDeadline(context.Background(), started.Add(gracefulStopWindow))
		defer stopTerm()
		stopCtx, stop := context.WithDeadline(context.Background(), started.Add(gracefulStopWindow+time.Second))
		defer stop()
		h.BeginStopAll(termCtx)
		cancel(context.Canceled)
		workersDone := make(chan struct{})
		go func() { r.workers.Wait(); close(workersDone) }()
		select {
		case <-workersDone:
		case <-stopCtx.Done():
			// Join the same operations; never create a second TERM window.
			h.StopAll(termCtx)
		}
		if session := r.connections.snapshot(); session != nil {
			flushCtx, stopFlush := context.WithTimeout(context.Background(), sourceOperationTimeout)
			_ = h.PublishAll(flushCtx, session, session.epoch)
			stopFlush()
			session.fail(net.ErrClosed)
			if session.readerDone != nil {
				<-session.readerDone
			}
			if r.publisherDone != nil {
				<-r.publisherDone
			}
		}
		cancel(context.Canceled)
		<-watcherDone
	}()
	needRecovery := recovery.record.Attempts > 0 || recovery.record.Unavailable
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if needRecovery {
			now := time.Now()
			when, reserveErr := recovery.reserve(now)
			if reserveErr != nil {
				return reserveErr
			}
			// Convert the validated wall timestamp with that same captured now.
			// Since uses now's monotonic reading, so a later wall-clock rollback
			// cannot lengthen this reservation while persistence consumes time.
			timer := time.NewTimer(recoveryBackoffDelay(when, now, time.Since(now)))
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return context.Cause(ctx)
			}
			// Consume the durable reservation before the callback/connection can
			// perform side effects. A later restart must reserve its next retry.
			if err := recovery.attemptStarted(); err != nil {
				return err
			}
			if cfg.EnsureCoordinator != nil {
				ensureCtx, stopEnsure := context.WithTimeout(ctx, 5*time.Second)
				ensureErr := cfg.EnsureCoordinator(ensureCtx)
				stopEnsure()
				if ensureErr != nil {
					if ctx.Err() != nil {
						return context.Cause(ctx)
					}
					if ensureErr.Error() == "coordinator_shutdown" {
						return nil
					}
					if !transientSourceError(ensureErr) && ensureErr.Error() != "coordinator_busy" && ensureErr.Error() != "coordinator_start_timeout" {
						return ensureErr
					}
					continue
				}
			}
		}
		session, connectErr := r.connect()
		if connectErr != nil {
			var terminal sourceTerminalDisposition
			if errors.As(connectErr, &terminal) {
				return nil
			}
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			if !transientSourceError(connectErr) {
				return connectErr
			}
			needRecovery = true
			continue
		}
		if err := recovery.authenticatedHost(cfg.Hello.PID, cfg.Hello.Birth); err != nil {
			session.fail(err)
			return err
		}
		r.connections.set(session)
		publisherDone := r.publish(session)
		r.publisherDone = publisherDone
		sessionErr := r.receive(session)
		var terminal sourceTerminalDisposition
		if errors.As(sessionErr, &terminal) {
			return nil
		}
		if ctx.Err() != nil {
			// Keep the current transport for the bounded final stop/exit flush.
			// Owner cancellation ends workers, not their ACK reader prematurely.
			return context.Cause(ctx)
		}
		session.fail(sessionErr)
		r.connections.clear(session)
		<-publisherDone
		r.publisherDone = nil
		<-session.readerDone
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if !transientSourceError(sessionErr) {
			return sessionErr
		}
		needRecovery = true
	}
}

func sourceRecoveryScope(hello contract.HostHello) (string, error) {
	executableBytes, err := os.ReadFile(hello.Executable)
	if err != nil {
		return "", err
	}
	return contract.HostRecoveryScopeSHA256(hello, hashBytes(executableBytes)), nil
}

func transientSourceError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func (r *livingSource) disposition(envelope ipc.Envelope, expectedEpoch uint64) error {
	var value contract.HostDisposition
	if decodePayload(envelope.Payload, &value) != nil || !value.AuthenticatedBy(r.cfg.Hello) || envelope.Epoch != value.CoordinatorEpoch || (expectedEpoch != 0 && envelope.Epoch != expectedEpoch) {
		return errors.New("host_disposition_invalid")
	}
	return sourceTerminalDisposition(value.Reason)
}

func (r *livingSource) connect() (*sourceSession, error) {
	dialCtx, stopDial := context.WithTimeout(r.ctx, sourceOperationTimeout)
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", r.cfg.SocketPath)
	stopDial()
	if err != nil {
		return nil, err
	}
	s := &sourceSession{conn: conn}
	s.initialize()
	accepted := false
	defer func() {
		if !accepted {
			s.fail(net.ErrClosed)
			if s.readerDone != nil {
				<-s.readerDone
			}
		}
	}()
	payload, _ := json.Marshal(r.cfg.Hello)
	if err := s.writeEnvelope(r.ctx, ipc.KindHostHello, r.cfg.Hello.LaunchID, payload); err != nil {
		return nil, err
	}
	ready, err := s.readEnvelope(r.ctx)
	if err != nil {
		return nil, err
	}
	if ready.Kind == ipc.KindHostDisposition {
		return nil, r.disposition(ready, 0)
	}
	if ready.Kind == ipc.KindError {
		return nil, errors.New("host_authentication_rejected")
	}
	var value contract.HostReady
	if ready.Kind != ipc.KindHostReady || ready.RequestID != r.cfg.Hello.LaunchID || decodePayload(ready.Payload, &value) != nil || value.HostID != r.host.producerID || ready.Epoch == 0 || value.CoordinatorEpoch != ready.Epoch {
		return nil, errors.New("invalid_host_ready")
	}
	s.epoch = ready.Epoch
	s.startReader()
	if err := r.host.PublishAll(r.ctx, s, s.epoch); err != nil {
		return nil, err
	}
	if err := r.reports.replay(r.ctx, s); err != nil {
		return nil, err
	}
	published, err := r.host.readPublished()
	if err != nil {
		return nil, err
	}
	payload, _ = json.Marshal(contract.HostReconciled{ProducerID: r.host.producerID, PublishedThrough: published})
	if err := s.writeEnvelope(r.ctx, ipc.KindHostReconciled, r.host.producerID+":reconciled", payload); err != nil {
		return nil, err
	}
	accepted = true
	return s, nil
}

func (r *livingSource) publish(s *sourceSession) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if err := r.host.PublishAll(context.Background(), s, s.epoch); err != nil {
				s.fail(err)
				return
			}
			select {
			case <-r.host.eventReady:
			case <-s.done:
				return
			case <-r.ctx.Done():
				return
			}
		}
	}()
	return done
}

func (r *livingSource) receive(s *sourceSession) error {
	for {
		envelope, err := s.nextEnvelope(r.ctx)
		if err != nil {
			return err
		}
		if envelope.Epoch != s.epoch {
			return errors.New("host_message_epoch_mismatch")
		}
		switch envelope.Kind {
		case ipc.KindHostDisposition:
			return r.disposition(envelope, s.epoch)
		case ipc.KindLaunch:
			var grant contract.LaunchCommand
			if decodePayload(envelope.Payload, &grant) != nil || grant.CommandID == "" {
				return errors.New("invalid_launch_command")
			}
			grant.ExecutionEpoch = s.epoch
			if err := r.dispatchLaunch(grant); err != nil {
				return err
			}
		case ipc.KindStop:
			var stop contract.StopCommand
			if decodePayload(envelope.Payload, &stop) != nil || stop.SegmentID == "" {
				return errors.New("invalid_stop_command")
			}
			r.dispatchStop(stop)
		default:
			return errors.New("unsupported_host_message")
		}
	}
}

// Authentication belongs to the input transport. This shared execution path
// preserves the caller's verified grant, including its signed execution epoch.
func (r *livingSource) dispatchLaunch(grant contract.LaunchCommand) error {
	duplicate, err := r.host.acceptGrant(grant)
	if err != nil {
		return err
	}
	return r.dispatchAcceptedLaunch(grant, duplicate)
}

func (r *livingSource) dispatchAcceptedLaunch(grant contract.LaunchCommand, duplicate bool) error {
	r.workingMu.Lock()
	if r.working[grant.CommandID] {
		r.workingMu.Unlock()
		return nil
	}
	r.working[grant.CommandID] = true
	r.workingMu.Unlock()
	r.workers.Add(1)
	go r.execute(grant, duplicate)
	return nil
}

// Only a caller that has not dispatched execution may make this no-Start
// claim. A duplicate ledger record is not such proof and must not call it.
func (r *livingSource) recordUnstartedGrant(grant contract.LaunchCommand, cause error) error {
	a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
	meta := metadataForLaunch(grant)
	return r.host.recordLaunchFailed(a, grant.RunID, grant.TaskID, meta, cause)
}

func (r *livingSource) dispatchStop(stop contract.StopCommand) {
	stopCtx := r.ctx
	cancel := func() {}
	if stop.DeadlineUnixMS > 0 {
		stopCtx, cancel = context.WithDeadline(r.ctx, time.UnixMilli(stop.DeadlineUnixMS))
	}
	_ = r.host.StopSegment(stopCtx, stop.SegmentID)
	cancel()
}

func (r *livingSource) prepareExecutionReport(ctx context.Context, grant contract.LaunchCommand) (string, error) {
	if r.prepareReport != nil {
		return r.prepareReport(ctx, grant)
	}
	for {
		s, err := r.connections.available(ctx)
		if err != nil {
			return "", err
		}
		path, err := r.host.prepareReportCapability(ctx, r.cfg, grant, s, r.reports.remember)
		if err == nil || !transientSourceError(err) {
			return path, err
		}
		s.fail(err)
	}
}

func (r *livingSource) execute(grant contract.LaunchCommand, duplicate bool) {
	defer r.workers.Done()
	defer func() { r.workingMu.Lock(); delete(r.working, grant.CommandID); r.workingMu.Unlock() }()
	reportError := func(err error) {
		if err != nil {
			if r.failure != nil {
				r.failure(err)
			} else {
				r.cancel(err)
			}
		}
	}
	if duplicate {
		reportError(r.host.recoverDuplicateGrant(grant, grant.ExecutionEpoch))
		return
	}
	prepareCtx := r.ctx
	stopPrepare := func() {}
	if grant.DeadlineUnixMS > 0 {
		prepareCtx, stopPrepare = context.WithDeadline(r.ctx, time.UnixMilli(grant.DeadlineUnixMS))
	}
	defer stopPrepare()
	meta := metadataForLaunch(grant)
	inv, err := r.cfg.InvocationProvider(prepareCtx, grant)
	a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
	// These failures precede this execution's call to ExecuteLaunch/Start.
	// An uncertain preparation must still retain UNKNOWN, never zero-settle
	// an ambiguous process/integration outcome as a proven no-start rejection.
	recordPreparationFailure := func(cause error) error {
		if errors.Is(cause, process.ErrProcessTreeUnknown) || errors.Is(cause, gitops.ErrIntegrationUncertain) {
			return r.host.ensureLaunchUnknown(a, grant.RunID, grant.TaskID, meta, cause)
		}
		return r.host.recordLaunchFailed(a, grant.RunID, grant.TaskID, meta, cause)
	}
	if err != nil || inv == nil {
		if err == nil {
			err = errors.New("invocation_unavailable")
		}
		reportError(recordPreparationFailure(err))
		return
	}
	if grant.ReportCapabilityID != "" && reportCapabilityEnabled(inv) {
		path, err := r.prepareExecutionReport(prepareCtx, grant)
		if err != nil {
			reportError(recordPreparationFailure(err))
			return
		}
		inv = reportInvocation{base: inv, reportPath: path, reportExecutable: r.cfg.Hello.Executable}
	}
	result, executeErr := r.host.ExecuteLaunch(r.ctx, grant, inv)
	if executeErr == nil {
		return
	}
	switch result.Status {
	case "interrupted":
	case "unknown":
		reportError(r.host.ensureLaunchUnknown(a, grant.RunID, grant.TaskID, meta, executeErr))
	default:
		reportError(r.host.recordLaunchFailed(a, grant.RunID, grant.TaskID, meta, executeErr))
	}
}

func (r *livingSource) progress(_ context.Context, event contract.Event) error {
	session := r.connections.snapshot()
	if session == nil {
		return errCoordinatorUnavailable
	}
	ctx, cancel := context.WithTimeout(r.ctx, time.Second)
	defer cancel()
	event.ExecutionEpoch = session.epoch
	payload, err := json.Marshal(struct {
		LaunchID    string         `json:"launch_id"`
		LaunchToken string         `json:"launch_token"`
		Event       contract.Event `json:"event"`
	}{r.cfg.Hello.LaunchID, r.cfg.Hello.LaunchToken, event})
	if err != nil {
		return err
	}
	response, err := ipc.Call(ctx, r.cfg.SocketPath, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindHostProgress, RequestID: event.EventID, Payload: payload})
	if err != nil {
		return err
	}
	if response.Kind != ipc.KindResponse {
		return errors.New("progress_not_recorded")
	}
	return nil
}
