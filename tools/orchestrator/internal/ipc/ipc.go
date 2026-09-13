package ipc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const (
	Version        = 1
	MaxMessageSize = 64 * 1024
)

type Kind string

const (
	KindHostHello  Kind = "host_hello"
	KindHostReady  Kind = "host_ready"
	KindLaunch     Kind = "launch"
	KindStop       Kind = "stop"
	KindEvent      Kind = "event"
	KindDurableAck Kind = "durable_ack"
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
