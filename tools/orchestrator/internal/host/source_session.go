package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

const sourceOperationTimeout = 2 * time.Second

// A connection owns its reader, write cancellation and ACK waiters. Nothing in
// its teardown refers to a mutable "current connection" or a worker lifetime.
type sourceSession struct {
	conn       net.Conn
	mu         sync.Mutex
	epoch      uint64
	frames     chan frameResult
	ackMu      sync.Mutex
	acks       map[string]chan frameResult
	initOnce   sync.Once
	readerOnce sync.Once
	failOnce   sync.Once
	done       chan struct{}
	readerDone chan struct{}
	failureMu  sync.Mutex
	failure    error
}

type frameResult struct {
	envelope ipc.Envelope
	err      error
}

func (s *sourceSession) initialize() {
	s.initOnce.Do(func() {
		s.frames = make(chan frameResult, 8)
		s.acks = make(map[string]chan frameResult)
		s.done = make(chan struct{})
	})
}

func (s *sourceSession) fail(err error) {
	s.initialize()
	s.failOnce.Do(func() {
		if err == nil {
			err = net.ErrClosed
		}
		s.failureMu.Lock()
		s.failure = err
		s.failureMu.Unlock()
		close(s.done)
		_ = s.conn.Close()
	})
}

func (s *sourceSession) connectionError() error {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	return s.failure
}

func (s *sourceSession) startReader() {
	s.initialize()
	s.readerOnce.Do(func() {
		s.readerDone = make(chan struct{})
		go func() {
			defer close(s.readerDone)
			for {
				envelope, err := ipc.Read(s.conn)
				if err != nil {
					s.fail(err)
					return
				}
				frame := frameResult{envelope: envelope}
				s.ackMu.Lock()
				ackChannel := s.acks[envelope.RequestID]
				if ackChannel != nil {
					select {
					case ackChannel <- frame:
					default:
					}
					s.ackMu.Unlock()
					continue
				}
				s.ackMu.Unlock()
				select {
				case s.frames <- frame:
				case <-s.done:
					return
				}
			}
		}()
	})
}

func (s *sourceSession) nextEnvelope(ctx context.Context) (ipc.Envelope, error) {
	s.initialize()
	if err := ctx.Err(); err != nil {
		return ipc.Envelope{}, err
	}
	// A terminal notice can have been queued just before EOF. Consume already
	// received frames before interpreting the connection's final error.
	select {
	case frame := <-s.frames:
		return frame.envelope, frame.err
	default:
	}
	select {
	case frame := <-s.frames:
		return frame.envelope, frame.err
	case <-s.done:
		select {
		case frame := <-s.frames:
			return frame.envelope, frame.err
		default:
			return ipc.Envelope{}, s.connectionError()
		}
	case <-ctx.Done():
		return ipc.Envelope{}, ctx.Err()
	}
}

func (s *sourceSession) writeEnvelope(ctx context.Context, kind ipc.Kind, requestID string, payload []byte) error {
	return s.writeEnvelopeAtEpoch(ctx, kind, requestID, payload, s.epoch)
}

func (s *sourceSession) writeEnvelopeAtEpoch(ctx context.Context, kind ipc.Kind, requestID string, payload []byte, epoch uint64) error {
	s.initialize()
	opCtx, cancel := context.WithTimeout(ctx, sourceOperationTimeout)
	defer cancel()
	stop := context.AfterFunc(opCtx, func() { s.fail(opCtx.Err()) })
	defer stop()
	if err := opCtx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return s.connectionError()
	default:
	}
	if deadline, ok := opCtx.Deadline(); ok {
		if err := s.conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
	}
	err := ipc.Write(s.conn, ipc.Envelope{Version: ipc.Version, Kind: kind, RequestID: requestID, Epoch: epoch, Payload: payload})
	if opCtx.Err() != nil {
		return opCtx.Err()
	}
	if err != nil {
		s.fail(err)
	}
	return err
}

func (s *sourceSession) readEnvelope(ctx context.Context) (ipc.Envelope, error) {
	opCtx, cancel := context.WithTimeout(ctx, sourceOperationTimeout)
	defer cancel()
	stop := context.AfterFunc(opCtx, func() { s.fail(opCtx.Err()) })
	defer stop()
	if err := setConnDeadline(s.conn, opCtx); err != nil {
		return ipc.Envelope{}, err
	}
	envelope, err := ipc.Read(s.conn)
	_ = s.conn.SetReadDeadline(time.Time{})
	if opCtx.Err() != nil {
		return ipc.Envelope{}, opCtx.Err()
	}
	return envelope, err
}

func (s *sourceSession) requestACK(ctx context.Context, kind ipc.Kind, requestID string, payload []byte, epoch uint64) (ipc.Envelope, error) {
	s.initialize()
	if epoch != s.epoch && s.epoch != 0 {
		return ipc.Envelope{}, errors.New("source_epoch_mismatch")
	}
	opCtx, cancel := context.WithTimeout(ctx, sourceOperationTimeout)
	defer cancel()
	stop := context.AfterFunc(opCtx, func() { s.fail(opCtx.Err()) })
	defer stop()
	ackChannel := make(chan frameResult, 1)
	s.ackMu.Lock()
	if s.acks[requestID] != nil {
		s.ackMu.Unlock()
		return ipc.Envelope{}, errors.New("ack_request_in_flight")
	}
	s.acks[requestID] = ackChannel
	s.ackMu.Unlock()
	defer func() { s.ackMu.Lock(); delete(s.acks, requestID); s.ackMu.Unlock() }()
	if err := s.writeEnvelopeAtEpoch(opCtx, kind, requestID, payload, epoch); err != nil {
		return ipc.Envelope{}, err
	}
	select {
	case frame := <-ackChannel:
		return frame.envelope, frame.err
	case <-opCtx.Done():
		return ipc.Envelope{}, opCtx.Err()
	case <-s.done:
		if opCtx.Err() != nil {
			return ipc.Envelope{}, opCtx.Err()
		}
		return ipc.Envelope{}, s.connectionError()
	}
}

func (s *sourceSession) SendEvent(ctx context.Context, epoch uint64, event contract.Event) (contract.DurableAck, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return contract.DurableAck{}, err
	}
	ackEnvelope, err := s.requestACK(ctx, ipc.KindEvent, event.EventID, payload, epoch)
	if err != nil {
		return contract.DurableAck{}, err
	}
	// Only an envelope bound to this exact event and epoch may influence the
	// returned error. In particular, never parse a peer error before this
	// check: a queued or forged frame must not disclose its payload through a
	// source failure record.
	if ackEnvelope.RequestID != event.EventID || ackEnvelope.Epoch != epoch {
		return contract.DurableAck{}, errors.New("durable_ack_mismatch")
	}
	if ackEnvelope.Kind == ipc.KindError {
		return contract.DurableAck{}, durableACKError(ackEnvelope.Payload)
	}
	if ackEnvelope.Kind != ipc.KindDurableAck {
		return contract.DurableAck{}, errors.New("durable_ack_mismatch")
	}
	var ack contract.DurableAck
	if err = decodePayload(ackEnvelope.Payload, &ack); err != nil {
		return contract.DurableAck{}, err
	}
	return ack, nil
}

// durableACKError preserves only the coordinator's compact protocol code.
// Malformed or non-code payloads are represented by a digest so a peer's
// arbitrary error text cannot enter Host diagnostics.
func durableACKError(raw []byte) error {
	var body struct {
		Error string `json:"error"`
	}
	if decodePayload(raw, &body) == nil && sourceErrorCode(body.Error) {
		return errors.New("durable_ack_error:" + body.Error)
	}
	digest := sha256.Sum256(raw)
	return errors.New("durable_ack_error_sha256:" + hex.EncodeToString(digest[:]))
}

func sourceErrorCode(code string) bool {
	if len(code) == 0 || len(code) > 128 {
		return false
	}
	for _, b := range []byte(code) {
		if (b < 'a' || b > 'z') && (b < '0' || b > '9') && b != '_' {
			return false
		}
	}
	return true
}
