package codexrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

type frameKind uint8

const (
	requestFrame frameKind = iota + 1
	notificationFrame
	resultFrame
	errorFrame
)

type frame struct {
	kind           frameKind
	id             ID
	method         string
	params, result json.RawMessage
	err            *RPCError
}

// objectFields rejects duplicate decoded keys, including escaped aliases.
func objectFields(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrProtocol
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, ErrProtocol
		}
		if _, exists := fields[key]; exists {
			return nil, ErrProtocol
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, ErrProtocol
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrProtocol
	}
	var extra any
	if err = d.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrProtocol
	}
	return fields, nil
}

func allowed(fields map[string]json.RawMessage, names ...string) bool {
	for key := range fields {
		known := false
		for _, name := range names {
			if key == name {
				known = true
				break
			}
		}
		if !known {
			return false
		}
	}
	return true
}

func parseID(raw json.RawMessage) (ID, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ID{}, ErrProtocol
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return ID{}, ErrProtocol
		}
		if len(text) > MaxScalarBytes {
			return ID{}, ErrIDLimit
		}
		return ID{valid: true, isString: true, text: text}, nil
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return ID{}, ErrProtocol
	}
	return ID{valid: true, integer: value}, nil
}

func methodValue(raw json.RawMessage) (string, error) {
	var method string
	if json.Unmarshal(raw, &method) != nil || method == "" {
		return "", ErrProtocol
	}
	if len(method) > MaxScalarBytes {
		return "", ErrMethodLimit
	}
	return method, nil
}

func parseRPCError(raw json.RawMessage) (*RPCError, error) {
	fields, err := objectFields(raw)
	if err != nil || !allowed(fields, "code", "message", "data") || fields["code"] == nil || fields["message"] == nil {
		return nil, ErrProtocol
	}
	code, err := strconv.ParseInt(string(bytes.TrimSpace(fields["code"])), 10, 64)
	if err != nil {
		return nil, ErrProtocol
	}
	var message string
	if bytes.Equal(bytes.TrimSpace(fields["message"]), []byte("null")) || json.Unmarshal(fields["message"], &message) != nil {
		return nil, ErrProtocol
	}
	return &RPCError{Code: code, Message: message, Data: fields["data"]}, nil
}

func validateTrace(raw json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	fields, err := objectFields(raw)
	if err != nil || !allowed(fields, "traceparent", "tracestate") {
		return ErrProtocol
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			return ErrProtocol
		}
	}
	return nil
}

func decodeFrame(raw []byte) (frame, error) {
	if !utf8.Valid(raw) || !validUnicode(raw) {
		return frame{}, ErrProtocol
	}
	fields, err := objectFields(raw)
	if err != nil || !allowed(fields, "id", "method", "params", "result", "error", "trace", "emittedAtMs") {
		return frame{}, ErrProtocol
	}
	_, hasID := fields["id"]
	_, hasMethod := fields["method"]
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	// Pinned 0.154 ServerNotificationEnvelope adds an optional i64 timestamp.
	// Validate then discard it; it provides no ordering or request authority.
	if timestamp, exists := fields["emittedAtMs"]; exists {
		if !hasMethod || hasID || hasResult || hasError {
			return frame{}, ErrProtocol
		}
		if !bytes.Equal(bytes.TrimSpace(timestamp), []byte("null")) {
			if _, err := strconv.ParseInt(string(bytes.TrimSpace(timestamp)), 10, 64); err != nil {
				return frame{}, ErrProtocol
			}
		}
	}
	if hasMethod {
		if hasResult || hasError {
			return frame{}, ErrProtocol
		}
		method, err := methodValue(fields["method"])
		if err != nil {
			return frame{}, err
		}
		f := frame{kind: notificationFrame, method: method, params: fields["params"]}
		if hasID {
			f.id, err = parseID(fields["id"])
			if err != nil {
				return frame{}, err
			}
			f.kind = requestFrame
			if trace, ok := fields["trace"]; ok && validateTrace(trace) != nil {
				return frame{}, ErrProtocol
			}
		} else if _, ok := fields["trace"]; ok {
			return frame{}, ErrProtocol
		}
		return f, nil
	}
	if !hasID || hasResult == hasError || fields["params"] != nil || fields["trace"] != nil {
		return frame{}, ErrProtocol
	}
	id, err := parseID(fields["id"])
	if err != nil {
		return frame{}, err
	}
	if hasResult {
		return frame{kind: resultFrame, id: id, result: fields["result"]}, nil
	}
	rpcErr, err := parseRPCError(fields["error"])
	if err != nil {
		return frame{}, err
	}
	return frame{kind: errorFrame, id: id, err: rpcErr}, nil
}

// encoding/json replaces lone UTF-16 surrogates. Reject them rather than
// silently changing a remote string ID, method, or opaque payload.
func validUnicode(raw []byte) bool {
	quoted := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if value >= 0xd800 && value <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		} else if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
	}
	return true
}

func outboundValid(method string, raw json.RawMessage, max int) error {
	if method == "" || !utf8.ValidString(method) {
		return ErrPayload
	}
	if len(method) > MaxScalarBytes {
		return ErrMethodLimit
	}
	return rawValid(raw, max)
}
func rawValid(raw json.RawMessage, max int) error {
	if len(raw) > max {
		return ErrFrameLimit
	}
	if raw != nil && (!json.Valid(raw) || !utf8.Valid(raw) || !validUnicode(raw)) {
		return ErrPayload
	}
	return nil
}
func encodedFrame(value any, max int) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, ErrPayload
	}
	if len(body) > max {
		return nil, ErrFrameLimit
	}
	return append(body, '\n'), nil
}
