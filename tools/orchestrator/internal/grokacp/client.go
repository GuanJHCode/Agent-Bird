// Package grokacp implements the bounded ACP v1 wire boundary used by a
// controlled Grok agent stdio process. It does not start a process, select an
// account, or grant filesystem, terminal, or tool permissions.
package grokacp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"sync"
	"unicode/utf8"
)

const (
	MaxFrameBytes = 64 << 10
	MaxUpdates    = 64
)

type Limits struct {
	FrameBytes int
	Updates    int
}

func (l Limits) normalized() (Limits, error) {
	if l.FrameBytes == 0 {
		l.FrameBytes = MaxFrameBytes
	}
	if l.Updates == 0 {
		l.Updates = MaxUpdates
	}
	if l.FrameBytes < 1 || l.FrameBytes > MaxFrameBytes || l.Updates < 1 || l.Updates > MaxUpdates {
		return Limits{}, ErrLimits
	}
	return l, nil
}

type TransportError struct{ code string }

func (e *TransportError) Error() string { return "grokacp: " + e.code }
func (e *TransportError) Code() string  { return e.code }
func (e *TransportError) Is(target error) bool {
	t, ok := target.(*TransportError)
	return ok && t.code == e.code
}

var (
	ErrConstructor      = &TransportError{"invalid_constructor"}
	ErrLimits           = &TransportError{"invalid_limits"}
	ErrProtocol         = &TransportError{"protocol"}
	ErrFrameLimit       = &TransportError{"frame_limit"}
	ErrUpdateLimit      = &TransportError{"update_queue_limit"}
	ErrClosed           = &TransportError{"closed"}
	ErrUncertain        = &TransportError{"uncertain"}
	ErrTurnActive       = &TransportError{"turn_active"}
	ErrState            = &TransportError{"state"}
	ErrPayload          = &TransportError{"payload"}
	ErrPermissionDenied = &TransportError{"permission_denied"}
)

type Turn struct{ StopReason string }

type Update struct {
	SessionID string
	Kind      string
	Text      string
	Raw       json.RawMessage
}

type response struct {
	result json.RawMessage
	err    error
}

type pending struct {
	id uint64
	ch chan response
}

// Client owns exactly one reader. All outbound writes are serialized and all
// terminal transport failures close the supplied stream.
type Client struct {
	stream io.ReadWriteCloser
	limits Limits

	mu              sync.Mutex
	writeMu         sync.Mutex
	opMu            sync.Mutex
	err             error
	initialized     bool
	initializing    bool
	sessionID       string
	creatingSession bool
	turnActive      bool
	turnDenied      bool
	nextID          uint64
	pending         *pending
	updates         chan Update
	done            chan struct{}
	joined          chan struct{}
	failOnce        sync.Once
}

func New(ctx context.Context, stream io.ReadWriteCloser, limits Limits) (*Client, error) {
	if ctx == nil || stream == nil {
		return nil, ErrConstructor
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	c := &Client{stream: stream, limits: l, updates: make(chan Update, l.Updates), done: make(chan struct{}), joined: make(chan struct{})}
	go c.readPump()
	return c, nil
}

func (c *Client) Updates() <-chan Update { return c.updates }
func (c *Client) Done() <-chan struct{}  { return c.done }
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) Close() error {
	c.fail(ErrClosed)
	<-c.joined
	return nil
}

func (c *Client) Initialize(ctx context.Context) error {
	if ctx == nil {
		return ErrPayload
	}
	c.mu.Lock()
	if c.initialized || c.initializing {
		c.mu.Unlock()
		return ErrState
	}
	c.initializing = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.initializing = false; c.mu.Unlock() }()
	result, err := c.call(ctx, "initialize", json.RawMessage(`{"protocolVersion":1,"clientCapabilities":{}}`))
	if err != nil {
		return err
	}
	fields, err := object(result)
	if err != nil || !only(fields, "protocolVersion", "agentCapabilities", "agentInfo", "authMethods", "_meta") || !isInteger(fields["protocolVersion"], 1) {
		c.fail(ErrProtocol)
		return ErrProtocol
	}
	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
	return nil
}

func (c *Client) NewSession(ctx context.Context, cwd string) (string, error) {
	if ctx == nil || cwd == "" || !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd || !utf8.ValidString(cwd) {
		return "", ErrPayload
	}
	c.mu.Lock()
	ready := c.initialized
	alreadyBound := c.sessionID != "" || c.creatingSession
	if ready && !alreadyBound {
		c.creatingSession = true
	}
	c.mu.Unlock()
	if !ready || alreadyBound {
		return "", ErrState
	}
	defer func() {
		c.mu.Lock()
		c.creatingSession = false
		c.mu.Unlock()
	}()
	params, _ := json.Marshal(struct {
		CWD        string `json:"cwd"`
		MCPServers []any  `json:"mcpServers"`
	}{cwd, []any{}})
	result, err := c.call(ctx, "session/new", params)
	if err != nil {
		return "", err
	}
	fields, err := object(result)
	if err != nil || fields["sessionId"] == nil {
		c.fail(ErrProtocol)
		return "", ErrProtocol
	}
	var sessionID string
	if json.Unmarshal(fields["sessionId"], &sessionID) != nil || sessionID == "" || !utf8.ValidString(sessionID) {
		c.fail(ErrProtocol)
		return "", ErrProtocol
	}
	c.mu.Lock()
	c.sessionID = sessionID
	c.mu.Unlock()
	return sessionID, nil
}

func (c *Client) Prompt(ctx context.Context, sessionID, text string) (Turn, error) {
	if ctx == nil || sessionID == "" || text == "" || !utf8.ValidString(sessionID) || !utf8.ValidString(text) {
		return Turn{}, ErrPayload
	}
	c.mu.Lock()
	if !c.initialized || c.sessionID != sessionID {
		c.mu.Unlock()
		return Turn{}, ErrState
	}
	if c.turnActive {
		c.mu.Unlock()
		return Turn{}, ErrTurnActive
	}
	c.turnActive = true
	c.turnDenied = false
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.turnActive = false; c.mu.Unlock() }()
	params, _ := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}{sessionID, []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "text", Text: text}}})
	result, err := c.call(ctx, "session/prompt", params)
	if err != nil {
		return Turn{}, err
	}
	c.mu.Lock()
	denied := c.turnDenied
	c.mu.Unlock()
	if denied {
		return Turn{}, ErrPermissionDenied
	}
	fields, err := object(result)
	if err != nil || !only(fields, "stopReason", "_meta") || fields["stopReason"] == nil {
		c.fail(ErrProtocol)
		return Turn{}, ErrProtocol
	}
	var stopReason string
	if json.Unmarshal(fields["stopReason"], &stopReason) != nil || stopReason == "" || !utf8.ValidString(stopReason) {
		c.fail(ErrProtocol)
		return Turn{}, ErrProtocol
	}
	return Turn{StopReason: stopReason}, nil
}

func (c *Client) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.nextID++
	if c.nextID == 0 {
		c.mu.Unlock()
		c.fail(ErrProtocol)
		return nil, ErrProtocol
	}
	p := &pending{id: c.nextID, ch: make(chan response, 1)}
	c.pending = p
	c.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { c.fail(ErrUncertain) })
	defer stop()
	frame, err := marshalFrame(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      uint64          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", p.id, method, params}, c.limits.FrameBytes)
	if err != nil {
		c.clearPending(p)
		return nil, err
	}
	if err := c.write(frame); err != nil {
		c.clearPending(p)
		c.fail(ErrUncertain)
		return nil, ErrUncertain
	}
	select {
	case reply := <-p.ch:
		return reply.result, reply.err
	case <-ctx.Done():
		c.fail(ErrUncertain)
		return nil, ErrUncertain
	case <-c.done:
		return nil, c.Err()
	}
}

func (c *Client) clearPending(p *pending) {
	c.mu.Lock()
	if c.pending == p {
		c.pending = nil
	}
	c.mu.Unlock()
}

func (c *Client) write(frame []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	for len(frame) > 0 {
		n, err := c.stream.Write(frame)
		if n < 0 || n > len(frame) || (n == 0 && err == nil) || err != nil {
			return ErrUncertain
		}
		frame = frame[n:]
	}
	return nil
}

func (c *Client) readPump() {
	defer close(c.joined)
	defer close(c.updates)
	reader := bufio.NewReaderSize(c.stream, 4096)
	var body []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(body)+len(part) > c.limits.FrameBytes+1 {
			c.fail(ErrFrameLimit)
			return
		}
		body = append(body, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(body) != 0 {
				c.fail(ErrUncertain)
			} else {
				c.mu.Lock()
				active := c.turnActive
				c.mu.Unlock()
				if active {
					c.fail(ErrUncertain)
				} else {
					c.fail(io.EOF)
				}
			}
			return
		}
		body = body[:len(body)-1]
		if err := c.accept(body); err != nil {
			c.fail(err)
			return
		}
		body = nil
	}
}

func (c *Client) accept(raw []byte) error {
	fields, err := object(raw)
	if err != nil || !only(fields, "jsonrpc", "id", "method", "params", "result", "error") || !isString(fields["jsonrpc"], "2.0") {
		return ErrProtocol
	}
	_, hasID := fields["id"]
	_, hasMethod := fields["method"]
	_, hasResult := fields["result"]
	_, hasError := fields["error"]
	if hasMethod {
		method, ok := stringValue(fields["method"])
		if !ok || hasResult || hasError {
			return ErrProtocol
		}
		if hasID {
			return c.reverseRequest(fields["id"], method, fields["params"])
		}
		if method != "session/update" {
			return ErrProtocol
		}
		return c.update(fields["params"])
	}
	if !hasID || hasResult == hasError || fields["params"] != nil {
		return ErrProtocol
	}
	id, ok := uintID(fields["id"])
	if !ok {
		return ErrProtocol
	}
	c.mu.Lock()
	p := c.pending
	if p == nil || p.id != id {
		c.mu.Unlock()
		return ErrProtocol
	}
	c.pending = nil
	c.mu.Unlock()
	if hasError {
		c.fail(ErrUncertain)
		return ErrUncertain
	} else {
		p.ch <- response{result: append(json.RawMessage(nil), fields["result"]...)}
	}
	return nil
}

func (c *Client) reverseRequest(id json.RawMessage, method string, params json.RawMessage) error {
	if !validRPCID(id) {
		return ErrProtocol
	}
	fields, err := object(params)
	if err != nil {
		return ErrProtocol
	}
	sessionID, ok := stringValue(fields["sessionId"])
	c.mu.Lock()
	bound, active := c.sessionID, c.turnActive
	if method == "session/request_permission" && ok && sessionID == bound && active {
		c.turnDenied = true
	}
	denied := method == "session/request_permission" && ok && sessionID == bound && active
	c.mu.Unlock()
	if method == "session/request_permission" && !denied {
		return ErrProtocol
	}
	var body any
	if method == "session/request_permission" {
		body = struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  any             `json:"result"`
		}{"2.0", id, map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}}
	} else {
		body = struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Error   any             `json:"error"`
		}{"2.0", id, map[string]any{"code": -32601, "message": "method not found"}}
	}
	frame, err := marshalFrame(body, c.limits.FrameBytes)
	if err != nil {
		return err
	}
	if err := c.write(frame); err != nil {
		return ErrUncertain
	}
	return nil
}

func (c *Client) update(raw json.RawMessage) error {
	fields, err := object(raw)
	if err != nil || !only(fields, "sessionId", "update") {
		return ErrProtocol
	}
	sessionID, ok := stringValue(fields["sessionId"])
	if !ok || sessionID == "" {
		return ErrProtocol
	}
	c.mu.Lock()
	boundSessionID := c.sessionID
	c.mu.Unlock()
	if boundSessionID == "" || sessionID != boundSessionID {
		return ErrProtocol
	}
	updateFields, err := object(fields["update"])
	if err != nil || updateFields["sessionUpdate"] == nil {
		return ErrProtocol
	}
	kind, ok := stringValue(updateFields["sessionUpdate"])
	if !ok || kind == "" {
		return ErrProtocol
	}
	text := ""
	if content := updateFields["content"]; content != nil {
		contentFields, err := object(content)
		if err != nil || !only(contentFields, "type", "text") || !isString(contentFields["type"], "text") {
			return ErrProtocol
		}
		text, ok = stringValue(contentFields["text"])
		if !ok {
			return ErrProtocol
		}
	}
	item := Update{SessionID: sessionID, Kind: kind, Text: text, Raw: append(json.RawMessage(nil), raw...)}
	select {
	case c.updates <- item:
		return nil
	default:
		return ErrUpdateLimit
	}
}

func (c *Client) fail(err error) {
	if err == nil {
		err = ErrClosed
	}
	c.failOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		p := c.pending
		c.pending = nil
		close(c.done)
		c.mu.Unlock()
		if p != nil {
			p.ch <- response{err: err}
		}
		_ = c.stream.Close()
	})
}

func marshalFrame(value any, max int) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil || len(body)+1 > max || !utf8.Valid(body) {
		return nil, ErrPayload
	}
	return append(body, '\n'), nil
}

func object(raw []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) || !validUnicode(raw) || !validJSON(raw) {
		return nil, ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrProtocol
	}
	out := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || out[key] != nil {
			return nil, ErrProtocol
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrProtocol
		}
		out[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrProtocol
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrProtocol
	}
	return out, nil
}

// validUnicode rejects lone UTF-16 surrogate escapes. encoding/json otherwise
// replaces them with U+FFFD, silently changing a session ID or request ID.
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

func validJSON(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() bool
	walk = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{':
				seen := make(map[string]struct{})
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return false
					}
					name, ok := key.(string)
					if !ok {
						return false
					}
					if _, exists := seen[name]; exists {
						return false
					}
					seen[name] = struct{}{}
					if !walk() {
						return false
					}
				}
				end, err := decoder.Token()
				return err == nil && end == json.Delim('}')
			case '[':
				for decoder.More() {
					if !walk() {
						return false
					}
				}
				end, err := decoder.Token()
				return err == nil && end == json.Delim(']')
			}
		}
		return true
	}
	if !walk() {
		return false
	}
	var extra any
	return errors.Is(decoder.Decode(&extra), io.EOF)
}

func only(fields map[string]json.RawMessage, names ...string) bool {
	for key := range fields {
		found := false
		for _, name := range names {
			if key == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func stringValue(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) != nil || !utf8.ValidString(text) {
		return "", false
	}
	return text, true
}
func isString(raw json.RawMessage, want string) bool {
	got, ok := stringValue(raw)
	return ok && got == want
}
func isInteger(raw json.RawMessage, want int) bool {
	got, err := strconv.Atoi(string(raw))
	return err == nil && got == want
}
func uintID(raw json.RawMessage) (uint64, bool) {
	got, err := strconv.ParseUint(string(raw), 10, 64)
	return got, err == nil && got != 0
}

func validRPCID(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	if raw[0] == '"' {
		_, ok := stringValue(raw)
		return ok
	}
	_, err := strconv.ParseInt(string(raw), 10, 64)
	return err == nil
}
