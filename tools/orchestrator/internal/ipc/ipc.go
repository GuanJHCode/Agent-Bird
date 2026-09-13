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
	KindReportCapability Kind = "report_capability"
	KindReportEvent      Kind = "report_event"
	KindDurableAck       Kind = "durable_ack"
	KindReady            Kind = "ready"
	KindSubmit           Kind = "submit"
	KindStatus           Kind = "status"
	KindCollect          Kind = "collect"
	KindWaitEvents       Kind = "wait_events"
	KindAck              Kind = "ack"
	KindResume           Kind = "resume"
	KindRetry            Kind = "retry"
	KindAccept           Kind = "accept"
	KindAnswer           Kind = "answer"
	KindRebindOwner      Kind = "rebind_owner"
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
	if err = Write(conn, request); err != nil {
		return Envelope{}, err
	}
	return Read(conn)
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
