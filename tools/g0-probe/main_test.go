package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const canary = "CANARY_PRIVATE_TEXT_DO_NOT_OUTPUT"

var requests = []string{
	`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"g0-probe","version":"0.1.0"},"capabilities":{"experimentalApi":true}}}`,
	`{"method":"initialized"}`,
	`{"id":2,"method":"server/diagnostics","params":{}}`,
	`{"id":3,"method":"thread/read","params":{"threadId":"thread-explicit","includeTurns":false}}`,
}

var responses = []string{
	`{"id":1,"result":{"codexHome":"CANARY_PRIVATE_TEXT_DO_NOT_OUTPUT","platformFamily":"unix","platformOs":"macos","userAgent":"CANARY_PRIVATE_TEXT_DO_NOT_OUTPUT"}}`,
	"",
	`{"id":2,"result":{"process":{"id":1234,"residentMemoryBytes":9876},"gauges":[{"name":"CANARY_PRIVATE_TEXT_DO_NOT_OUTPUT","value":1}]}}`,
	`{"id":3,"result":{"thread":{"id":"thread-explicit","sessionId":"session-tree","parentThreadId":"parent-thread","forkedFromId":null,"canAcceptDirectInput":true,"status":{"type":"active","activeFlags":["waitingOnApproval"]},"cliVersion":"0.153.4","createdAt":1,"cwd":"CANARY_PRIVATE_TEXT_DO_NOT_OUTPUT","ephemeral":false,"modelProvider":"CANARY_PRIVATE_TEXT_DO_NOT_OUTPUT","preview":"CANARY_PRIVATE_TEXT_DO_NOT_OUTPUT","projectId":null,"source":"cli","turns":[],"updatedAt":2,"name":"CANARY_PRIVATE_TEXT_DO_NOT_OUTPUT","extra":{"secret":"CANARY_PRIVATE_TEXT_DO_NOT_OUTPUT"}}}}`,
}

// A real HTTP Upgrade and real Unix socket exercise the transport boundary.
// The fixture never opens a Codex session or calls a model.
func fixture(t *testing.T, script func(context.Context, *websocket.Conn) error) string {
	t.Helper()
	base := ""
	if runtime.GOOS == "darwin" {
		base = "/tmp" // macOS sockaddr_un cannot hold a long worktree path.
	}
	root, err := os.MkdirTemp(base, "g0-probe-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "tmp"), 0700); err != nil {
		os.RemoveAll(root)
		t.Fatal(err)
	}
	path := filepath.Join(root, "tmp", "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		os.RemoveAll(root)
		t.Fatal(err)
	}
	done := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			done <- fmt.Errorf("fixture upgrade failed")
			return
		}
		defer c.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		done <- script(ctx, c)
	})}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		<-served
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("fixture connection did not close")
		}
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("fixture cleanup failed: %v", err)
		}
	})
	return path
}

func expectRequest(ctx context.Context, c *websocket.Conn, want string) error {
	kind, payload, err := c.Read(ctx)
	if err != nil {
		return fmt.Errorf("expected allowlisted request, connection ended")
	}
	var gotValue, wantValue any
	if kind != websocket.MessageText || json.Unmarshal(payload, &gotValue) != nil || json.Unmarshal([]byte(want), &wantValue) != nil || !reflect.DeepEqual(gotValue, wantValue) {
		return fmt.Errorf("request differs from exact allowlist or order")
	}
	return nil
}

func expectClosed(ctx context.Context, c *websocket.Conn) error {
	_, _, err := c.Read(ctx)
	if err == nil {
		return errors.New("unexpected additional client message or server-request answer")
	}
	if ctx.Err() != nil {
		return errors.New("client did not release socket")
	}
	return nil
}

func transcript(replaceAt int, replacement string, kind websocket.MessageType, before []string) func(context.Context, *websocket.Conn) error {
	return func(ctx context.Context, c *websocket.Conn) error {
		for i, request := range requests {
			if err := expectRequest(ctx, c, request); err != nil {
				return err
			}
			if responses[i] == "" {
				continue
			}
			if i == replaceAt {
				for _, message := range before {
					if err := c.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
						return nil // Limits may close during a notification flood.
					}
				}
				if err := c.Write(ctx, kind, []byte(replacement)); err != nil {
					return nil // Oversized payload can close before write completes.
				}
				return expectClosed(ctx, c)
			}
			if err := c.Write(ctx, websocket.MessageText, []byte(responses[i])); err != nil {
				return errors.New("fixture response write failed")
			}
		}
		return expectClosed(ctx, c)
	}
}

func invoke(ctx context.Context, path string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"--socket", path, "--thread", "thread-explicit"}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// Catches extra RPCs, settings copied into params, JSONL over UDS, raw response
// logging, and claiming attachment or G0 validation from a metadata response.
func TestReadOnlyTranscriptAndSanitizedEvidence(t *testing.T) {
	path := fixture(t, transcript(-1, "", websocket.MessageText, nil))
	code, stdout, stderr := invoke(context.Background(), path)
	want := `{"evidence":"metadata_only","attached":"unknown","g0":"not_verified","process":{"id":1234},"thread":{"id":"thread-explicit","sessionId":"session-tree","parentThreadId":"parent-thread","forkedFromId":null,"canAcceptDirectInput":true,"status":{"type":"active","activeFlags":["waitingOnApproval"]}}}` + "\n"
	if code != 0 || stdout != want || stderr != "" {
		t.Errorf("code=%d; got sanitized evidence=%q; stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, canary) {
		t.Fatal("private canary leaked")
	}
}

// Catches treating unknown notification text as output or a response.
func TestUnknownNotificationIsDiscarded(t *testing.T) {
	path := fixture(t, func(ctx context.Context, c *websocket.Conn) error {
		for i, request := range requests {
			if err := expectRequest(ctx, c, request); err != nil {
				return err
			}
			if responses[i] == "" {
				continue
			}
			if err := c.Write(ctx, websocket.MessageText, []byte(`{"method":"future/notification","params":{"secret":"`+canary+`"}}`)); err != nil {
				return err
			}
			if err := c.Write(ctx, websocket.MessageText, []byte(responses[i])); err != nil {
				return err
			}
		}
		return expectClosed(ctx, c)
	})
	code, stdout, stderr := invoke(context.Background(), path)
	if code != 0 || stderr != "" || strings.Contains(stdout, canary) {
		t.Errorf("code=%d; output=%q; stderr=%q", code, stdout, stderr)
	}
}

// Each row catches a protocol ambiguity, unexpected server action, or unsafe
// shape that must terminate before any later allowlisted request is sent.
func TestProtocolFailuresDoNotLeakOrContinue(t *testing.T) {
	tests := []struct {
		name     string
		at       int
		payload  string
		kind     websocket.MessageType
		category string
	}{
		{"wrong_id", 0, `{"id":9,"result":{}}`, websocket.MessageText, "response_id_mismatch"},
		{"string_id", 0, `{"id":"1","result":{}}`, websocket.MessageText, "response_id_mismatch"},
		{"missing_id", 0, `{"result":{}}`, websocket.MessageText, "protocol_error"},
		{"null_id", 0, `{"id":null,"result":{}}`, websocket.MessageText, "response_id_mismatch"},
		{"error", 0, `{"id":1,"error":{"code":-1,"message":"` + canary + `","data":"` + canary + `"}}`, websocket.MessageText, "rpc_error"},
		{"error_and_result", 0, `{"id":1,"result":{},"error":{"message":"` + canary + `"}}`, websocket.MessageText, "protocol_error"},
		{"server_request", 0, `{"id":"server-1","method":"item/commandExecution/requestApproval","params":{"command":"` + canary + `"}}`, websocket.MessageText, "server_request"},
		{"invalid_json", 0, `{"secret":"` + canary, websocket.MessageText, "protocol_error"},
		{"json_batch", 0, `[{"id":1,"result":{}}]`, websocket.MessageText, "protocol_error"},
		{"jsonl_in_frame", 0, "{\"id\":1,\"result\":{}}\n{\"id\":2,\"result\":{}}", websocket.MessageText, "protocol_error"},
		{"duplicate_id", 0, `{"id":9,"id":1,"result":{}}`, websocket.MessageText, "protocol_error"},
		{"binary_frame", 0, responses[0], websocket.MessageBinary, "protocol_error"},
		{"wrong_thread", 3, strings.Replace(responses[3], `"id":"thread-explicit"`, `"id":"other-thread"`, 1), websocket.MessageText, "thread_identity_mismatch"},
		{"unknown_status", 3, strings.Replace(responses[3], `"type":"active"`, `"type":"`+canary+`"`, 1), websocket.MessageText, "invalid_metadata"},
		{"unknown_active_flag", 3, strings.Replace(responses[3], "waitingOnApproval", canary, 1), websocket.MessageText, "invalid_metadata"},
		{"bad_session_id", 3, strings.Replace(responses[3], "session-tree", "session\\nsecret", 1), websocket.MessageText, "invalid_metadata"},
		{"missing_thread", 3, `{"id":3,"result":{}}`, websocket.MessageText, "invalid_metadata"},
		{"missing_process", 2, `{"id":2,"result":{"gauges":[]}}`, websocket.MessageText, "invalid_metadata"},
		{"bad_process", 2, `{"id":2,"result":{"process":{"id":-1}}}`, websocket.MessageText, "invalid_metadata"},
		{"frame_limit", 0, `{"id":1,"result":{"secret":"` + strings.Repeat("x", 65536) + `"}}`, websocket.MessageText, "message_limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := fixture(t, transcript(tt.at, tt.payload, tt.kind, nil))
			code, stdout, stderr := invoke(context.Background(), path)
			if code != 1 || stdout != "" || stderr != "g0-probe: "+tt.category+"\n" {
				t.Errorf("code=%d; output=%q; stderr=%q", code, stdout, stderr)
			}
		})
	}
}

func TestNotificationFloodIsBounded(t *testing.T) {
	notifications := make([]string, 33)
	for i := range notifications {
		notifications[i] = `{"method":"future/notification","params":{"secret":"` + canary + `"}}`
	}
	path := fixture(t, transcript(0, responses[0], websocket.MessageText, notifications))
	code, stdout, stderr := invoke(context.Background(), path)
	if code != 1 || stdout != "" || stderr != "g0-probe: message_limit\n" {
		t.Errorf("code=%d; output=%q; stderr=%q", code, stdout, stderr)
	}
}

func TestDeadlineIncludesHandshake(t *testing.T) {
	base := ""
	if runtime.GOOS == "darwin" {
		base = "/tmp"
	}
	root, err := os.MkdirTemp(base, "g0-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	path := filepath.Join(root, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = bytes.NewBuffer(nil).ReadFrom(c)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	code, stdout, stderr := invoke(ctx, path)
	if code != 1 || stdout != "" || stderr != "g0-probe: timeout\n" {
		t.Errorf("code=%d; output=%q; stderr=%q", code, stdout, stderr)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("handshake connection not closed")
	}
}

// A malformed 101 reaches the WebSocket library's upgrade-validation failure
// and body cleanup path. Context cancellation must also close that raw socket.
func TestInvalidUpgradeCleanupHonorsCancellation(t *testing.T) {
	for _, mode := range []string{"deadline", "early_cancel"} {
		t.Run(mode, func(t *testing.T) {
			base := ""
			if runtime.GOOS == "darwin" {
				base = "/tmp"
			}
			root, err := os.MkdirTemp(base, "g0-probe-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(root) })
			path := filepath.Join(root, "s")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { listener.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			if mode == "early_cancel" {
				cancel()
				ctx, cancel = context.WithCancel(context.Background())
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				c, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				request, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil || request.Header.Get("Upgrade") != "websocket" {
					done <- errors.New("fixture did not receive WebSocket upgrade")
					return
				}
				_, err = fmt.Fprintf(c, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", canary)
				if err != nil {
					done <- err
					return
				}
				if mode == "early_cancel" {
					// Let Dial validate the received 101 and enter its body read.
					timer := time.AfterFunc(100*time.Millisecond, cancel)
					defer timer.Stop()
				}
				var one [1]byte
				n, err := c.Read(one[:])
				if n != 0 || err == nil {
					done <- errors.New("client sent data after invalid upgrade")
					return
				}
				if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
					done <- errors.New("client did not release invalid-upgrade socket")
					return
				}
				done <- nil
			}()
			start := time.Now()
			code, stdout, stderr := invoke(ctx, path)
			elapsed := time.Since(start)
			if code != 1 || stdout != "" || stderr != "g0-probe: timeout\n" {
				t.Errorf("code=%d; output=%q; stderr=%q", code, stdout, stderr)
			}
			if elapsed > time.Second {
				t.Errorf("invalid-upgrade cleanup exceeded cancellation bound: %s", elapsed)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(time.Second):
				t.Error("invalid-upgrade connection did not close")
			}
		})
	}
}

func TestOverallDeadlineIsTenSeconds(t *testing.T) {
	path := fixture(t, func(ctx context.Context, c *websocket.Conn) error {
		if err := expectRequest(ctx, c, requests[0]); err != nil {
			return err
		}
		return expectClosed(ctx, c)
	})
	start := time.Now()
	code, stdout, stderr := invoke(context.Background(), path)
	elapsed := time.Since(start)
	if code != 1 || stdout != "" || stderr != "g0-probe: timeout\n" {
		t.Errorf("code=%d; output=%q; stderr=%q", code, stdout, stderr)
	}
	if elapsed < 9*time.Second || elapsed > 12*time.Second {
		t.Errorf("expected fixed 10s overall deadline, elapsed=%s", elapsed)
	}
}

// Catches guessing endpoint/thread, accepting repeated or extra flags, and
// reflecting arbitrary arguments in usage/error output.
func TestInvalidArgumentsAreBoundedAndNeverReflected(t *testing.T) {
	tests := [][]string{
		nil,
		{"--socket", "/tmp/not-used"},
		{"--thread", "thread-explicit"},
		{"--socket", "relative", "--thread", "thread-explicit"},
		{"--socket", "/tmp/not-used", "--thread", ""},
		{"--socket", "/tmp/not-used", "--thread", "bad\nthread"},
		{"--socket", "/tmp/not-used", "--thread", strings.Repeat("x", 129)},
		{"--socket", "/" + strings.Repeat("x", 4096), "--thread", "thread-explicit"},
		{"--socket", "/tmp/not-used", "--thread", "thread-explicit", "--thread", "other"},
		{"--socket", "/tmp/not-used", "--thread", "thread-explicit", canary},
		{"--" + canary},
	}
	for i, args := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), args, &stdout, &stderr)
			if code != 2 || stdout.Len() != 0 || stderr.String() != "g0-probe: invalid_arguments\n" {
				t.Errorf("code=%d; output=%q; stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}
