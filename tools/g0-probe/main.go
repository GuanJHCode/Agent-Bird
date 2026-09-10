package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	deadline        = 10 * time.Second
	maxMessageBytes = 64 * 1024
	maxMessages     = 32
)

// Only these types are serialized. Unknown server fields never enter evidence.
type evidence struct {
	Evidence string          `json:"evidence"`
	Attached string          `json:"attached"`
	G0       string          `json:"g0"`
	Process  processMetadata `json:"process"`
	Thread   threadMetadata  `json:"thread"`
}

type processMetadata struct {
	ID uint32 `json:"id"`
}

type threadMetadata struct {
	ID                   string       `json:"id"`
	SessionID            string       `json:"sessionId"`
	ParentThreadID       *string      `json:"parentThreadId"`
	ForkedFromID         *string      `json:"forkedFromId"`
	CanAcceptDirectInput *bool        `json:"canAcceptDirectInput"`
	Status               threadStatus `json:"status"`
}

type threadStatus struct {
	Type        string    `json:"type"`
	ActiveFlags *[]string `json:"activeFlags,omitempty"`
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	socket, threadID, ok := arguments(args)
	if !ok {
		fmt.Fprintln(stderr, "g0-probe: invalid_arguments")
		return 2
	}
	result, category := probe(ctx, socket, threadID)
	if category != "" {
		fmt.Fprintln(stderr, "g0-probe: "+category)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "g0-probe: output_failed")
		return 1
	}
	return 0
}

func arguments(args []string) (socket, threadID string, ok bool) {
	if len(args) != 4 {
		return "", "", false
	}
	for i := 0; i < len(args); i += 2 {
		switch args[i] {
		case "--socket":
			if socket != "" {
				return "", "", false
			}
			socket = args[i+1]
		case "--thread":
			if threadID != "" {
				return "", "", false
			}
			threadID = args[i+1]
		default:
			return "", "", false
		}
	}
	return socket, threadID, len(socket) <= 4096 && filepath.IsAbs(socket) && !strings.ContainsAny(socket, "\x00\r\n") && validID(threadID)
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func probe(ctx context.Context, socket, threadID string) (evidence, string) {
	var result evidence
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// No environment proxy, redirect, TCP fallback, cookie jar, or default
	// transport: every dial targets exactly the supplied Unix socket.
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
			if err != nil {
				return nil, err
			}
			// An invalid HTTP 101 makes websocket.Dial read the upgraded body
			// during error cleanup. Bind the raw connection to the probe's
			// context; net/http's dial context may outlive request cancellation.
			if cutoff, ok := ctx.Deadline(); ok {
				if err := conn.SetDeadline(cutoff); err != nil {
					conn.Close()
					return nil, err
				}
			}
			context.AfterFunc(ctx, func() { conn.Close() })
			return conn, nil
		},
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 16 * 1024,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	conn, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return result, failure(ctx, err, "connect_failed")
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessageBytes)
	rpc := rpcReader{conn: conn}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"g0-probe","version":"0.1.0"},"capabilities":{"experimentalApi":true}}}`)); err != nil {
		return result, failure(ctx, err, "transport_error")
	}
	if _, category := rpc.response(ctx, "1"); category != "" {
		return result, category
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"method":"initialized"}`)); err != nil {
		return result, failure(ctx, err, "transport_error")
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"id":2,"method":"server/diagnostics","params":{}}`)); err != nil {
		return result, failure(ctx, err, "transport_error")
	}
	diagnostics, category := rpc.response(ctx, "2")
	if category != "" {
		return result, category
	}
	process, ok := diagnostics["process"].(map[string]any)
	if !ok {
		return result, "invalid_metadata"
	}
	number, ok := process["id"].(json.Number)
	if !ok {
		return result, "invalid_metadata"
	}
	pid, err := strconv.ParseUint(string(number), 10, 32)
	if err != nil || pid == 0 {
		return result, "invalid_metadata"
	}
	result.Process.ID = uint32(pid)
	// validID restricts this interpolation to ASCII identifier characters.
	request := `{"id":3,"method":"thread/read","params":{"threadId":"` + threadID + `","includeTurns":false}}`
	if err := conn.Write(ctx, websocket.MessageText, []byte(request)); err != nil {
		return result, failure(ctx, err, "transport_error")
	}
	metadata, category := rpc.response(ctx, "3")
	if category != "" {
		return result, category
	}
	result.Thread, category = readThread(metadata["thread"], threadID)
	if category != "" {
		return result, category
	}
	result.Evidence, result.Attached, result.G0 = "metadata_only", "unknown", "not_verified"
	return result, ""
}

func failure(ctx context.Context, err error, fallback string) string {
	if ctx.Err() != nil {
		return "timeout"
	}
	// A socket deadline can fire just before the context timer publishes Err.
	if cutoff, ok := ctx.Deadline(); ok && !time.Now().Before(cutoff) {
		return "timeout"
	}
	if errors.Is(err, websocket.ErrMessageTooBig) {
		return "message_limit"
	}
	return fallback
}

type rpcReader struct {
	conn     *websocket.Conn
	messages int
}

func (r *rpcReader) response(ctx context.Context, expectedID string) (map[string]any, string) {
	for {
		if r.messages >= maxMessages {
			return nil, "message_limit"
		}
		r.messages++
		kind, data, err := r.conn.Read(ctx)
		if err != nil {
			return nil, failure(ctx, err, "transport_error")
		}
		if kind != websocket.MessageText {
			return nil, "protocol_error"
		}
		message, err := decodeObject(data)
		if err != nil {
			return nil, "protocol_error"
		}
		id, hasID := message["id"]
		value, hasResult := message["result"]
		_, hasError := message["error"]
		if method, hasMethod := message["method"]; hasMethod {
			if hasID {
				return nil, "server_request"
			}
			name, valid := method.(string)
			if !valid || name == "" || hasResult || hasError {
				return nil, "protocol_error"
			}
			continue
		}
		if !hasID || hasResult == hasError {
			return nil, "protocol_error"
		}
		number, valid := id.(json.Number)
		if !valid || string(number) != expectedID {
			return nil, "response_id_mismatch"
		}
		if hasError {
			return nil, "rpc_error"
		}
		object, valid := value.(map[string]any)
		if !valid {
			return nil, "protocol_error"
		}
		return object, ""
	}
}

func readThread(value any, expectedID string) (threadMetadata, string) {
	var result threadMetadata
	thread, ok := value.(map[string]any)
	if !ok {
		return result, "invalid_metadata"
	}
	result.ID, ok = thread["id"].(string)
	if !ok || !validID(result.ID) {
		return result, "invalid_metadata"
	}
	if result.ID != expectedID {
		return result, "thread_identity_mismatch"
	}
	result.SessionID, ok = thread["sessionId"].(string)
	if !ok || !validID(result.SessionID) {
		return result, "invalid_metadata"
	}
	if result.ParentThreadID, ok = optionalID(thread["parentThreadId"]); !ok {
		return result, "invalid_metadata"
	}
	if result.ForkedFromID, ok = optionalID(thread["forkedFromId"]); !ok {
		return result, "invalid_metadata"
	}
	if value := thread["canAcceptDirectInput"]; value != nil {
		flag, ok := value.(bool)
		if !ok {
			return result, "invalid_metadata"
		}
		result.CanAcceptDirectInput = &flag
	}
	status, ok := thread["status"].(map[string]any)
	if !ok {
		return result, "invalid_metadata"
	}
	result.Status.Type, ok = status["type"].(string)
	if !ok {
		return result, "invalid_metadata"
	}
	switch result.Status.Type {
	case "notLoaded", "idle", "systemError":
	case "active":
		values, ok := status["activeFlags"].([]any)
		if !ok {
			return result, "invalid_metadata"
		}
		flags := make([]string, 0, len(values))
		for _, value := range values {
			flag, ok := value.(string)
			if !ok || (flag != "waitingOnApproval" && flag != "waitingOnUserInput") {
				return result, "invalid_metadata"
			}
			flags = append(flags, flag)
		}
		result.Status.ActiveFlags = &flags
	default:
		return result, "invalid_metadata"
	}
	return result, ""
}

func optionalID(value any) (*string, bool) {
	if value == nil {
		return nil, true
	}
	id, ok := value.(string)
	if !ok || !validID(id) {
		return nil, false
	}
	return &id, true
}

// encoding/json normally accepts duplicate keys. Reject them, including in
// nested metadata, so there is no ambiguous identity selected by key order.
func decodeObject(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("invalid envelope")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("invalid envelope")
	}
	return object, nil
}

func decodeValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, errors.New("JSON depth exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid key")
			}
			if _, exists := object[name]; exists {
				return nil, errors.New("duplicate key")
			}
			value, err := decodeValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New("invalid object")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New("invalid array")
		}
		return array, nil
	default:
		return nil, errors.New("invalid delimiter")
	}
}
