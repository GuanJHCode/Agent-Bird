package ipc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
)

const (
	Version        = 1
	MaxMessageSize = 64 * 1024
)

type Kind string

const (
	KindHostHello        Kind = "host_hello"
	KindHostReady        Kind = "host_ready"
	KindHostStatus       Kind = "host_status"
	KindHostReconciled   Kind = "host_reconciled"
	KindLaunch           Kind = "launch"
	KindStop             Kind = "stop"
	KindEvent            Kind = "event"
	KindHostProgress     Kind = "host_progress"
	KindReportCapability Kind = "report_capability"
	KindReportEvent      Kind = "report_event"
	KindDurableAck       Kind = "durable_ack"
	KindReady            Kind = "ready"
	KindOwnerBind        Kind = "owner_bind"
	KindProviderControl  Kind = "provider_control"
	KindSubmit           Kind = "submit"
	KindStatus           Kind = "status"
	KindSummary          Kind = "summary"
	KindCollect          Kind = "collect"
	KindWaitEvents       Kind = "wait_events"
	KindAck              Kind = "ack"
	KindResume           Kind = "resume"
	KindRetry            Kind = "retry"
	KindRework           Kind = "rework"
	KindAccept           Kind = "accept"
	KindAnswer           Kind = "answer"
	KindRebindOwner      Kind = "rebind_owner"
	KindRecoverHost      Kind = "recover_host"
	KindStopTask         Kind = "stop_task"
	KindResponse         Kind = "response"
	KindError            Kind = "error"
)

var (
	ErrMessageTooLarge = errors.New("ipc_message_too_large")
	ErrInvalidMessage  = errors.New("invalid_ipc_message")
)

type Envelope struct {
	Version   int             `json:"version"`
	Kind      Kind            `json:"kind"`
	RequestID string          `json:"request_id"`
	Epoch     uint64          `json:"epoch"`
	Payload   json.RawMessage `json:"payload"`
}

func Call(ctx context.Context, socketPath string, request Envelope) (Envelope, error) {
	if !filepath.IsAbs(socketPath) {
		return Envelope{}, ErrInvalidMessage
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return Envelope{}, err
	}
	defer conn.Close()
	// DialContext only governs connection establishment. Closing the owned
	// connection also interrupts a blocked write or an incomplete response frame.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err = Write(conn, request); err != nil {
		if ctx.Err() != nil {
			return Envelope{}, ctx.Err()
		}
		return Envelope{}, err
	}
	response, err := Read(conn)
	if ctx.Err() != nil {
		return Envelope{}, ctx.Err()
	}
	return response, err
}

func Write(w io.Writer, message Envelope) error {
	if message.Version != Version || message.Kind == "" || message.RequestID == "" {
		return ErrInvalidMessage
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageSize {
		return ErrMessageTooLarge
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err = w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func Read(r io.Reader) (Envelope, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Envelope{}, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return Envelope{}, ErrInvalidMessage
	}
	if size > MaxMessageSize {
		return Envelope{}, ErrMessageTooLarge
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return Envelope{}, err
	}
	var message Envelope
	if err := json.Unmarshal(data, &message); err != nil {
		return Envelope{}, ErrInvalidMessage
	}
	if message.Version != Version || message.Kind == "" || message.RequestID == "" {
		return Envelope{}, ErrInvalidMessage
	}
	return message, nil
}
