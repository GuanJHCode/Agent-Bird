package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
		all, readErr := entry.spool.ReadAfter(acked)
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
	maxima := map[*events.Spool]int64{}
	for _, item := range pending {
		if item.event.Sequence <= last && item.event.Sequence > item.acked && item.event.Sequence > maxima[item.spool] {
			maxima[item.spool] = item.event.Sequence
		}
	}
	for spool, sequence := range maxima {
		if err = spool.AckThrough(sequence); err != nil {
			return err
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
	// EnsureCoordinator may restore only the sanitized coordinator process.
	// It must honor ctx; nil relies on an externally restored socket.
	EnsureCoordinator func(context.Context) error
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
	if err = h.BindContinuationOwner(cfg.Hello.OriginContextID); err != nil {
		return err
	}
	return runLivingSource(ctx, cfg, h)
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
