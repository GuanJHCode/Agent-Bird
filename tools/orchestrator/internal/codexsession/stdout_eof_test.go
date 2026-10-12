package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
)

// Two actual synchronous pipes give independent EOFs without a process/socket.
// The original transport reader is the only reader of the server output.
type eofPipe struct {
	r                    *io.PipeReader
	w                    *io.PipeWriter
	writeOnce, closeOnce sync.Once
	writeErr, closeErr   error
	halfClosed, eof      atomic.Bool
	mu                   sync.Mutex
	read                 strings.Builder
}

func (p *eofPipe) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.mu.Lock()
		p.read.Write(b[:n])
		p.mu.Unlock()
	}
	if errors.Is(err, io.EOF) {
		p.eof.Store(true)
	}
	return n, err
}
func (p *eofPipe) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *eofPipe) CloseWrite() error {
	p.writeOnce.Do(func() { p.halfClosed.Store(true); p.writeErr = p.w.Close() })
	return p.writeErr
}
func (p *eofPipe) Close() error {
	p.closeOnce.Do(func() {
		// Full close intentionally does not set halfClosed: the test must
		// distinguish intentional stdin EOF from interrupted full teardown.
		p.writeOnce.Do(func() { p.writeErr = p.w.Close() })
		p.closeErr = errors.Join(p.writeErr, p.r.Close())
	})
	return p.closeErr
}

func TestRunDetailedWaitsForOriginalStdoutEOF(t *testing.T) {
	for _, mode := range []string{"eof", "early_eof", "late_permission", "late_foreign", "malformed", "partial_eof", "retained", "cancel", "default"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			clientRead, peerWrite := io.Pipe()
			peerRead, clientWrite := io.Pipe()
			local := &eofPipe{r: clientRead, w: clientWrite}
			client, err := codexrpc.New(ctx, local, codexrpc.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			runReturned := make(chan struct{})
			serverDone := make(chan error, 1)
			var bound atomic.Bool
			go func() {
				defer peerRead.Close()
				defer peerWrite.Close()
				serverDone <- func() error {
					p := &fixturePeer{enc: json.NewEncoder(peerWrite), dec: json.NewDecoder(peerRead), writer: peerWrite}
					for _, method := range []string{"initialize", "initialized", "config/read", "thread/start", "turn/start"} {
						var frame map[string]json.RawMessage
						if err := p.dec.Decode(&frame); err != nil {
							return err
						}
						var actual string
						if json.Unmarshal(frame["method"], &actual) != nil || actual != method {
							return fmt.Errorf("unexpected RPC phase")
						}
						if err := checkFixtureRequest(method, frame["params"], false); err != nil {
							return err
						}
						if method == "initialized" {
							continue
						}
						if method == "turn/start" && !bound.Load() {
							return fmt.Errorf("turn before binding")
						}
						if err := p.enc.Encode(map[string]any{"id": frame["id"], "result": fixtureResponse(method)}); err != nil {
							return err
						}
					}
					if err := p.terminal(fixtureTerminal()); err != nil {
						return err
					}
					if mode == "early_eof" {
						if err := peerWrite.Close(); err != nil {
							return err
						}
						var extra any
						if err := p.dec.Decode(&extra); err != io.EOF {
							return fmt.Errorf("natural stdout EOF did not close stdin: %v", err)
						}
						return nil
					}
					var extra any
					if err := p.dec.Decode(&extra); err != io.EOF {
						return fmt.Errorf("extra RPC or missing stdin EOF: %v", err)
					}
					if mode == "default" {
						return nil
					}
					if err := p.notify("thread/name/updated", map[string]any{"threadId": "thread-1", "threadName": "AFTER_STDIN_EOF"}); err != nil {
						return err
					}
					switch mode {
					case "late_permission":
						return p.enc.Encode(map[string]any{"id": "late", "method": "item/commandExecution/requestApproval", "params": approvalCompatibilityParams()})
					case "late_foreign":
						return p.notify("thread/name/updated", map[string]any{"threadId": "foreign", "threadName": "late"})
					case "malformed":
						_, err := io.WriteString(peerWrite, "{bad}\n")
						return err
					case "partial_eof":
						_, err := io.WriteString(peerWrite, "{\"partial\":")
						return err
					case "retained", "cancel":
						if mode == "cancel" {
							cancel()
						}
						// Keep stdout genuinely open until Run has returned, so
						// cancellation cannot race a peer EOF into the receipt.
						<-runReturned
					}
					return nil
				}()
			}()
			projection, err := codexconfig.Project([]byte("model=\"gpt-5.4\"\nmodel_reasoning_effort=\"high\"\nservice_tier=\"priority\"\n"), codexconfig.ReadOnly)
			if err != nil {
				t.Fatal(err)
			}
			options := Options{RequireStdoutEOF: mode != "default", CWD: "/private/owned/project", OwnedHome: "/private/owned/codex", PlatformFamily: "unix", PlatformOS: "macos", Prompt: "Review API", OnThread: func(context.Context, ThreadBinding) error { bound.Store(true); return nil }}
			out, runErr := Run(ctx, client, projection, options)
			close(runReturned)
			peerErr := <-serverDone
			if peerErr != nil {
				t.Fatalf("original peer failed: %v; Run=%v", peerErr, runErr)
			}
			if !bound.Load() {
				t.Fatal("original binding never reached")
			}
			local.mu.Lock()
			observed := local.read.String()
			local.mu.Unlock()
			if mode != "default" && mode != "early_eof" && (!local.halfClosed.Load() || !strings.Contains(observed, "AFTER_STDIN_EOF")) {
				t.Fatal("success path closed the original stdout reader before its tail")
			}
			switch mode {
			case "eof", "early_eof":
				if runErr != nil || out.Completion.Text != "Which API?" || !local.eof.Load() {
					t.Fatalf("missing original EOF success: %+v %v eof=%v", out, runErr, local.eof.Load())
				}
			case "default":
				if runErr != nil || out.Completion.Text != "Which API?" || local.halfClosed.Load() {
					t.Fatalf("default changed: %+v %v", out, runErr)
				}
			default:
				want := ErrProtocol
				if mode == "retained" {
					want = context.DeadlineExceeded
				}
				if mode == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(runErr, want) || out.Completion != (adapter.TaskCompletion{}) {
					t.Fatalf("late failure became success: %+v %v, want %v", out, runErr, want)
				}
				if mode == "partial_eof" && !local.eof.Load() {
					t.Fatal("partial frame EOF lost raw I/O observation")
				}
				if (mode == "retained" || mode == "cancel") && local.eof.Load() {
					t.Fatal("force close fabricated EOF")
				}
			}
		})
	}
}
