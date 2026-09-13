package host

import (
	"bytes"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/events"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EventSink is the control-plane half of the source Host protocol. SendEvent
// must return only after the coordinator has durably committed the event.
type EventSink interface {
	SendEvent(context.Context, uint64, contract.Event) (contract.DurableAck, error)
}

// Publish sends every unacknowledged spool event through the control-plane
// durable-ack path. The spool cursor advances only after a matching durable
// acknowledgement, so a disconnected source can safely replay the event.
func (h *Host) Publish(ctx context.Context, a store.Attempt, sink EventSink, epoch uint64) error {
	if h.producerID == "" || sink == nil {
		return errors.New("ipc_sink_required")
	}
	s, err := h.spool(a)
	if err != nil {
		return err
	}
	acked, err := s.AckedThrough()
	if err != nil {
		return err
	}
	events, err := s.Read()
	if err != nil {
		return err
	}
	last := acked
	for _, event := range events {
		if event.Sequence <= acked {
			continue
		}
		if event.ProducerID != h.producerID || event.EventID == "" || event.PayloadHash == "" {
			return errors.New("event_identity_missing")
		}
		ack, err := sink.SendEvent(ctx, epoch, event)
		if err != nil {
			return err
		}
		if ack.Status != "durable" || ack.ProducerID != event.ProducerID || ack.EventID != event.EventID || ack.PayloadHash != event.PayloadHash || ack.AckedThrough < event.Sequence {
			return errors.New("invalid_durable_ack")
		}
		last = ack.AckedThrough
	}
	if last > acked {
		return s.AckThrough(last)
	}
	return nil
}

// PublishAll is the single producer publisher. It merges every segment spool,
// persists a producer-wide cursor, and refuses to send a later sequence while
// an earlier sequence is still absent. This prevents concurrent segments from
// creating durable DB gaps.
func (h *Host) PublishAll(ctx context.Context, sink EventSink, epoch uint64) error {
	if h.producerID == "" || sink == nil {
		return errors.New("ipc_sink_required")
	}
	h.publishMu.Lock()
	defer h.publishMu.Unlock()
	entries, err := h.spoolEntries()
	if err != nil {
		return err
	}
	cursor, err := h.readPublished()
	if err != nil {
		return err
	}
	originalCursor := cursor
	type pendingEvent struct {
		spool *events.Spool
		event contract.Event
		acked int64
	}
	pending := make([]pendingEvent, 0)
	for _, entry := range entries {
		acked, readErr := entry.spool.AckedThrough()
		if readErr != nil {
			return readErr
		}
		all, readErr := entry.spool.Read()
		if readErr != nil {
			return readErr
		}
		for _, event := range all {
			if event.ProducerID != h.producerID || event.EventID == "" {
				return errors.New("event_identity_missing")
			}
			if event.Sequence > acked {
				pending = append(pending, pendingEvent{spool: entry.spool, event: event, acked: acked})
			}
		}
		// Already durably acknowledged events can advance the recovery cursor.
		if acked > cursor {
			cursor = acked
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].event.Sequence < pending[j].event.Sequence })
	last := cursor
	for _, item := range pending {
		if item.event.Sequence <= last {
			continue
		}
		if item.event.Sequence != last+1 {
			return errors.New("event_sequence_gap")
		}
		sendEvent := item.event
		sendEvent.ExecutionEpoch = epoch
		ack, sendErr := sink.SendEvent(ctx, epoch, sendEvent)
		if sendErr != nil {
			return sendErr
		}
		if ack.Status != "durable" || ack.ProducerID != item.event.ProducerID || ack.EventID != item.event.EventID || ack.PayloadHash != item.event.PayloadHash || ack.AckedThrough < item.event.Sequence {
			return errors.New("invalid_durable_ack")
		}
		last = item.event.Sequence
	}
	if last == originalCursor {
		return nil
	}
	// Advance each affected spool only after all corresponding control-plane
	// durable ACKs succeeded. The cursor is written last; a crash before it is
	// safe because the coordinator ACK is idempotent and replayable.
	for _, item := range pending {
		if item.event.Sequence <= last && item.event.Sequence > item.acked {
			if err = item.spool.AckThrough(item.event.Sequence); err != nil {
				return err
			}
		}
	}
	return h.writePublished(last)
}

type spoolEntry struct {
	spool *events.Spool
}

func (h *Host) spoolEntries() ([]spoolEntry, error) {
	attemptDirs, err := os.ReadDir(h.spoolRoot)
	if err != nil {
		return nil, err
	}
	entries := make([]spoolEntry, 0)
	for _, attempt := range attemptDirs {
		if !attempt.IsDir() || strings.HasPrefix(attempt.Name(), ".") {
			continue
		}
		segments, readErr := os.ReadDir(filepath.Join(h.spoolRoot, attempt.Name()))
		if readErr != nil {
			return nil, readErr
		}
		for _, segment := range segments {
			if !segment.IsDir() || strings.HasPrefix(segment.Name(), ".") {
				continue
			}
			spool, openErr := events.Open(filepath.Join(h.spoolRoot, attempt.Name(), segment.Name()))
			if openErr != nil {
				return nil, openErr
			}
			entries = append(entries, spoolEntry{spool: spool})
		}
	}
	return entries, nil
}

func (h *Host) readPublished() (int64, error) {
	data, err := os.ReadFile(h.publishedPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var value int64
	if _, err = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err != nil {
		return 0, errors.New("invalid_published_sequence")
	}
	value, _ = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if value < 0 {
		return 0, errors.New("invalid_published_sequence")
	}
	return value, nil
}

func (h *Host) writePublished(value int64) error {
	stage := h.publishedPath + ".tmp"
	_ = os.Remove(stage)
	fd, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = fd.WriteString(strconv.FormatInt(value, 10) + "\n")
	if err == nil {
		err = fd.Sync()
	}
	if closeErr := fd.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(stage, h.publishedPath)
	}
	if err != nil {
		_ = os.Remove(stage)
	}
	return err
}

// SourceConfig contains only source-owned identity and a local invocation
// provider. Authentication/environment remain inside the provider and are
// never serialized in the Host IPC envelope.
type SourceConfig struct {
	SocketPath         string
	SpoolRoot          string
	ProducerID         string
	Hello              contract.HostHello
	InvocationProvider func(context.Context, contract.LaunchCommand) (contract.InvocationView, error)
}

// RunSource owns the source Host IPC loop. It does not open the coordinator
// DB and cannot execute a launch until prepared metadata is durable.
func RunSource(ctx context.Context, cfg SourceConfig) error {
	if !filepath.IsAbs(cfg.SocketPath) || !filepath.IsAbs(cfg.SpoolRoot) || cfg.InvocationProvider == nil {
		return errors.New("invalid_source_config")
	}
	if err := validateHello(cfg.Hello); err != nil {
		return err
	}
	producerID := cfg.ProducerID
	if producerID == "" {
		producerID = cfg.Hello.LaunchID
	}
	if producerID != cfg.Hello.LaunchID {
		return errors.New("producer_launch_mismatch")
	}
	h, err := NewIPC(cfg.SpoolRoot, producerID)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", cfg.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	session := &sourceSession{conn: conn, epoch: 0}
	payload, _ := json.Marshal(cfg.Hello)
	if err = session.writeEnvelope(ctx, ipc.KindHostHello, cfg.Hello.LaunchID, payload); err != nil {
		return err
	}
	ready, err := session.readEnvelope(ctx)
	if err != nil {
		return err
	}
	if ready.Kind != ipc.KindHostReady || ready.RequestID != cfg.Hello.LaunchID {
		return errors.New("host_ready_mismatch")
	}
	var hostReady contract.HostReady
	if err = decodePayload(ready.Payload, &hostReady); err != nil || hostReady.HostID == "" || hostReady.HostID != producerID {
		return errors.New("invalid_host_ready")
	}
	session.epoch = ready.Epoch
	session.startReader()
	runCtx, cancelRun := context.WithCancel(ctx)
	// Reconcile the durable source spool before accepting another grant. This
	// closes the disconnect window without automatically resuming work.
	if err = h.PublishAll(runCtx, session, session.epoch); err != nil {
		cancelRun()
		return err
	}
	publishedThrough, err := h.readPublished()
	if err != nil {
		cancelRun()
		return err
	}
	reconciledBody, _ := json.Marshal(contract.HostReconciled{ProducerID: producerID, PublishedThrough: publishedThrough})
	if err = session.writeEnvelope(runCtx, ipc.KindHostReconciled, producerID+":reconciled", reconciledBody); err != nil {
		cancelRun()
		return err
	}
	var workers sync.WaitGroup
	workerErrors := make(chan error, 2)
	publisherErrors := make(chan error, 1)
	publisherDone := make(chan struct{})
	go func() {
		defer close(publisherDone)
		for {
			select {
			case <-h.eventReady:
				if publishErr := h.PublishAll(runCtx, session, session.epoch); publishErr != nil {
					publisherErrors <- publishErr
					return
				}
			case <-runCtx.Done():
				return
			}
		}
	}()
	defer func() {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 2*time.Second)
		h.StopAll(stopCtx)
		cancelStop()
		done := make(chan struct{})
		go func() { workers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		// Workers append stopped/exited only after their process groups are
		// settled. Flush those records before tearing down the sole publisher.
		_ = h.PublishAll(runCtx, session, session.epoch)
		cancelRun()
		<-publisherDone
	}()
	originTicker := time.NewTicker(200 * time.Millisecond)
	defer originTicker.Stop()
	for {
		var envelope ipc.Envelope
		var readErr error
		select {
		case frame := <-session.frames:
			envelope, readErr = frame.envelope, frame.err
		case workerErr := <-workerErrors:
			return workerErr
		case publisherErr := <-publisherErrors:
			return publisherErr
		case <-originTicker.C:
			birth, ownerErr := process.Birth(cfg.Hello.OriginPID)
			if ownerErr != nil || birth != cfg.Hello.OriginBirth {
				return errors.New("origin_owner_lost")
			}
			continue
		case <-runCtx.Done():
			return runCtx.Err()
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return readErr
		}
		switch envelope.Kind {
		case ipc.KindLaunch:
			var grant contract.LaunchCommand
			if err = decodePayload(envelope.Payload, &grant); err != nil || grant.CommandID == "" {
				return errors.New("invalid_launch_command")
			}
			grant.ExecutionEpoch = session.epoch
			duplicate, acceptErr := h.acceptGrant(grant)
			if acceptErr != nil {
				return acceptErr
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				reportError := func(err error) {
					if err != nil {
						select {
						case workerErrors <- err:
						case <-runCtx.Done():
						}
					}
				}
				if duplicate {
					reportError(h.recoverDuplicateGrant(grant, session.epoch))
					return
				}
				inv, invokeErr := cfg.InvocationProvider(runCtx, grant)
				a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
				if invokeErr != nil || inv == nil {
					if invokeErr == nil {
						invokeErr = errors.New("invocation_unavailable")
					}
					if recordErr := h.recordLaunchFailed(a, grant.RunID, grant.TaskID, launchMetadata{commandID: grant.CommandID, workRevision: grant.WorkRevision, executionEpoch: grant.ExecutionEpoch}, invokeErr); recordErr != nil {
						reportError(recordErr)
						return
					}
					return
				}
				if grant.ReportCapabilityID != "" {
					reportPath, capabilityErr := h.prepareReportCapability(runCtx, cfg, grant, session)
					if capabilityErr != nil {
						reportError(capabilityErr)
						return
					}
					inv = reportInvocation{base: inv, reportPath: reportPath, reportExecutable: cfg.Hello.Executable}
				}
				result, executeErr := h.ExecuteLaunch(runCtx, grant, inv)
				if executeErr != nil {
					meta := launchMetadata{commandID: grant.CommandID, workRevision: grant.WorkRevision, executionEpoch: grant.ExecutionEpoch}
					var terminalErr error
					switch result.Status {
					case "interrupted":
						// ExecuteLaunch already wrote stopped followed by exited.
					case "unknown":
						terminalErr = h.ensureLaunchUnknown(a, grant.RunID, grant.TaskID, meta, executeErr)
					default:
						terminalErr = h.recordLaunchFailed(a, grant.RunID, grant.TaskID, meta, executeErr)
					}
					if terminalErr != nil {
						reportError(terminalErr)
						return
					}
				}
			}()
		case ipc.KindStop:
			var stop contract.StopCommand
			if err = decodePayload(envelope.Payload, &stop); err != nil || stop.SegmentID == "" {
				return errors.New("invalid_stop_command")
			}
			stopCtx := runCtx
			if stop.DeadlineUnixMS > 0 {
				var cancel context.CancelFunc
				stopCtx, cancel = context.WithDeadline(runCtx, time.UnixMilli(stop.DeadlineUnixMS))
				defer cancel()
			}
			_ = h.StopSegment(stopCtx, stop.SegmentID)
		default:
			return errors.New("unsupported_host_message")
		}
	}
}

type sourceSession struct {
	conn   net.Conn
	mu     sync.Mutex
	epoch  uint64
	frames chan frameResult
	ackMu  sync.Mutex
	acks   map[string]chan frameResult
}

type frameResult struct {
	envelope ipc.Envelope
	err      error
}

func (s *sourceSession) writeEnvelope(ctx context.Context, kind ipc.Kind, requestID string, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := setConnDeadline(s.conn, ctx); err != nil {
		return err
	}
	return ipc.Write(s.conn, ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: requestID, Epoch: s.epoch, Payload: payload})
}

func (s *sourceSession) startReader() {
	s.frames = make(chan frameResult, 8)
	s.acks = make(map[string]chan frameResult)
	go func() {
		for {
			envelope, err := ipc.Read(s.conn)
			frame := frameResult{envelope: envelope, err: err}
			if err == nil {
				s.ackMu.Lock()
				ackChannel := s.acks[envelope.RequestID]
				if ackChannel != nil {
					ackChannel <- frame
					s.ackMu.Unlock()
					continue
				}
				s.ackMu.Unlock()
			}
			s.frames <- frame
			if err != nil {
				return
			}
		}
	}()
}

func (s *sourceSession) nextEnvelope(ctx context.Context) (ipc.Envelope, error) {
	select {
	case frame := <-s.frames:
		return frame.envelope, frame.err
	case <-ctx.Done():
		return ipc.Envelope{}, ctx.Err()
	}
}

func (s *sourceSession) readEnvelope(ctx context.Context) (ipc.Envelope, error) {
	if err := setConnDeadline(s.conn, ctx); err != nil {
		return ipc.Envelope{}, err
	}
	return ipc.Read(s.conn)
}

func (s *sourceSession) SendEvent(ctx context.Context, epoch uint64, event contract.Event) (contract.DurableAck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	requestID := event.EventID
	payload, err := json.Marshal(event)
	if err != nil {
		return contract.DurableAck{}, err
	}
	if err = setConnDeadline(s.conn, ctx); err != nil {
		return contract.DurableAck{}, err
	}
	ackChannel := make(chan frameResult, 1)
	s.ackMu.Lock()
	s.acks[requestID] = ackChannel
	s.ackMu.Unlock()
	defer func() { s.ackMu.Lock(); delete(s.acks, requestID); s.ackMu.Unlock() }()
	if err = ipc.Write(s.conn, ipc.Envelope{Version: ipc.Version, Kind: ipc.KindEvent, RequestID: requestID, Epoch: epoch, Payload: payload}); err != nil {
		return contract.DurableAck{}, err
	}
	var frame frameResult
	select {
	case frame = <-ackChannel:
	case <-ctx.Done():
		return contract.DurableAck{}, ctx.Err()
	}
	ackEnvelope, err := frame.envelope, frame.err
	if err != nil {
		return contract.DurableAck{}, err
	}
	if ackEnvelope.Kind != ipc.KindDurableAck || ackEnvelope.RequestID != requestID {
		return contract.DurableAck{}, errors.New("durable_ack_mismatch")
	}
	var ack contract.DurableAck
	if err = decodePayload(ackEnvelope.Payload, &ack); err != nil {
		return contract.DurableAck{}, err
	}
	return ack, nil
}

func setConnDeadline(conn net.Conn, ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		return conn.SetDeadline(deadline)
	}
	return conn.SetDeadline(time.Time{})
}

func decodePayload(data []byte, out any) error {
	dec := json.NewDecoder(bytesReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

func validateHello(hello contract.HostHello) error {
	if hello.LaunchID == "" || hello.LaunchToken == "" || hello.OriginContextID == "" || hello.OriginPID <= 0 || hello.OriginBirth == "" || hello.HostGeneration == "" || hello.PID != os.Getpid() || hello.Birth == "" || hello.Executable == "" {
		return errors.New("invalid_host_hello")
	}
	originBirth, err := process.Birth(hello.OriginPID)
	if err != nil || originBirth != hello.OriginBirth {
		return errors.New("origin_birth_mismatch")
	}
	birth, err := process.Birth(hello.PID)
	if err != nil || birth != hello.Birth {
		return errors.New("host_birth_mismatch")
	}
	executable, err := os.Executable()
	if err != nil {
		return errors.New("host_executable_unknown")
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil || executable != hello.Executable {
		return errors.New("host_executable_mismatch")
	}
	return nil
}

// bytesReader keeps source.go independent of a mutable bytes.Buffer.
func bytesReader(data []byte) *bytes.Reader { return bytes.NewReader(data) }

var _ EventSink = (*sourceSession)(nil)
