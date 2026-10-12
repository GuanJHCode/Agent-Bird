// Package codexrpc implements the pinned Codex 0.154 app-server wire boundary.
// It never starts a process, supplies configuration, or grants a permission.
package codexrpc

import (
	"encoding/json"
	"fmt"
)

const (
	MaxFrameBytes       = 1 << 20
	MaxLocalCalls       = 16
	MaxRemoteRequests   = 16
	MaxEvents           = 128
	MaxConsumedRequests = 1024
	MaxScalarBytes      = 256
	writerQueueSize     = 32
)

// Limits may lower the fixed maxima; zero selects the corresponding maximum.
type Limits struct {
	FrameBytes, LocalCalls, RemoteRequests, Events, ConsumedRequests int
}

func (l Limits) normalized() (Limits, error) {
	fields := []*int{&l.FrameBytes, &l.LocalCalls, &l.RemoteRequests, &l.Events, &l.ConsumedRequests}
	maxima := []int{MaxFrameBytes, MaxLocalCalls, MaxRemoteRequests, MaxEvents, MaxConsumedRequests}
	for i, p := range fields {
		if *p == 0 {
			*p = maxima[i]
		}
		if *p < 1 || *p > maxima[i] {
			return Limits{}, ErrLimits
		}
	}
	return l, nil
}

// TransportError's code contains no remote content or trace information.
type TransportError struct{ code string }

func (e *TransportError) Error() string { return "codexrpc: " + e.code }
func (e *TransportError) Code() string  { return e.code }
func (e *TransportError) Is(target error) bool {
	t, ok := target.(*TransportError)
	return ok && e.code == t.code
}

var (
	ErrProtocol        = &TransportError{"protocol"}
	ErrFrameLimit      = &TransportError{"frame_limit"}
	ErrPendingLimit    = &TransportError{"local_call_limit"}
	ErrRemoteLimit     = &TransportError{"remote_request_limit"}
	ErrEventLimit      = &TransportError{"event_queue_limit"}
	ErrConsumedLimit   = &TransportError{"consumed_request_limit"}
	ErrWriterLimit     = &TransportError{"writer_queue_limit"}
	ErrPending         = &TransportError{"pending_write"}
	ErrIDLimit         = &TransportError{"id_limit"}
	ErrMethodLimit     = &TransportError{"method_limit"}
	ErrIncompleteFrame = &TransportError{"incomplete_frame"}
	ErrIO              = &TransportError{"stream_io"}
	ErrClosed          = &TransportError{"closed"}
	ErrAuthority       = &TransportError{"request_authority"}
	ErrResponded       = &TransportError{"request_already_responded"}
	ErrLimits          = &TransportError{"invalid_limits"}
	ErrPayload         = &TransportError{"invalid_outbound_payload"}
	ErrConstructor     = &TransportError{"invalid_constructor"}
)

// ID preserves the original string/int64 type. There is no float64 conversion.
type ID struct {
	valid, isString bool
	text            string
	integer         int64
}

func (id ID) StringValue() (string, bool) { return id.text, id.valid && id.isString }
func (id ID) Int64Value() (int64, bool)   { return id.integer, id.valid && !id.isString }
func (id ID) MarshalJSON() ([]byte, error) {
	if !id.valid {
		return nil, ErrProtocol
	}
	if id.isString {
		return json.Marshal(id.text)
	}
	return json.Marshal(id.integer)
}

// RPCError is bounded by the frame limit; Error never prints its remote message.
type RPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("codexrpc: remote error (%d)", e.Code) }

// Exactly one of Result (including raw null) or Error must be supplied.
type Reply struct {
	Result json.RawMessage
	Error  *RPCError
}

// Request is an opaque, session-owned authority, not an arbitrary response ID.
// Its getter values are snapshots; only the exact emitted pointer may Respond.
type Request struct {
	owner      *Client
	id         ID
	method     string
	params     json.RawMessage
	responding bool
}

func (r *Request) ID() ID                  { return r.id }
func (r *Request) Method() string          { return r.method }
func (r *Request) Params() json.RawMessage { return append(json.RawMessage(nil), r.params...) }

type Notification struct {
	Method string
	Params json.RawMessage
}
type Event struct {
	Notification *Notification
	Request      *Request
}
