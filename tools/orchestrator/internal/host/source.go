package host

import (
	"bytes"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/ipc"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
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
	var workers sync.WaitGroup
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		h.StopAll(stopCtx)
		cancel()
		workers.Wait()
	}()
	for {
		envelope, readErr := session.nextEnvelope(ctx)
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
			workers.Add(1)
			go func() {
				defer workers.Done()
				inv, invokeErr := cfg.InvocationProvider(ctx, grant)
				if invokeErr != nil || inv == nil {
					return
				}
				_, _ = h.ExecuteLaunch(ctx, grant, inv)
				a := store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}
				_ = h.Publish(ctx, a, session, session.epoch)
			}()
		case ipc.KindStop:
			var stop contract.StopCommand
			if err = decodePayload(envelope.Payload, &stop); err != nil || stop.SegmentID == "" {
				return errors.New("invalid_stop_command")
			}
			stopCtx := ctx
			if stop.DeadlineUnixMS > 0 {
				var cancel context.CancelFunc
				stopCtx, cancel = context.WithDeadline(ctx, time.UnixMilli(stop.DeadlineUnixMS))
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
			if err == nil && envelope.Kind == ipc.KindDurableAck {
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
	if hello.LaunchID == "" || hello.LaunchToken == "" || hello.OriginContextID == "" || hello.HostGeneration == "" || hello.PID != os.Getpid() || hello.Birth == "" || hello.Executable == "" {
		return errors.New("invalid_host_hello")
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
