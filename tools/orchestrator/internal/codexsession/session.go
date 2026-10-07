// Package codexsession reduces one pinned native turn into provisional task output.
package codexsession

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexconfig"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/pelletier/go-toml/v2"
)

// Persistence fields are native response identities, not a claim about which
// files native initialization already read. Host checks its pre-Start seal and
// validates these against the original owned scope before returning OnThread.
type ThreadBinding struct {
	ThreadID, Model, ModelProvider string
	RolloutPath, HistoryMode       string
}

// OnThread is a trusted cooperative callback: it must respond to its original
// context. Run never detaches it or claims to forcibly terminate Go code.
type Options struct {
	// RequireStdoutEOF preserves the original RPC reader through stdin EOF.
	// It changes no permission or output acceptance rules.
	RequireStdoutEOF                                                   bool
	CWD, OwnedHome, PlatformFamily, PlatformOS, Prompt, ResumeThreadID string
	OnThread                                                           func(context.Context, ThreadBinding) error
}
type Outcome struct {
	Binding        ThreadBinding
	TurnID, Status string
	Completion     adapter.TaskCompletion
}

var (
	ErrProtocol      = errors.New("codexsession: protocol")
	ErrInput         = errors.New("codexsession: invalid_input")
	ErrPolicy        = errors.New("codexsession: policy")
	ErrCallback      = errors.New("codexsession: binding_callback")
	ErrNativeFailure = errors.New("codexsession: native_failure")
	ErrPermission    = errors.New("codexsession: native_permission")
	ErrOutput        = errors.New("codexsession: final_output")
	ErrStateLimit    = errors.New("codexsession: state_limit")
)

type phaseResult struct {
	raw json.RawMessage
	err error
}

// Run consumes events itself, concurrently with at most one Call/Notify or
// cooperative callback job. No return detaches that job or the M1 transport.
func Run(ctx context.Context, client *codexrpc.Client, projection *codexconfig.Projection, options Options) (outcome Outcome, runErr error) {
	if client == nil {
		return Outcome{}, ErrInput
	}
	var job chan phaseResult
	var jobs sync.WaitGroup
	var cancel context.CancelFunc = func() {}
	defer func() {
		cancel()
		_ = client.Close()
		if job != nil {
			<-job
		}
		jobs.Wait()
	}()
	if ctx == nil || projection == nil || options.OnThread == nil || !validPath(options.CWD) || !validPath(options.OwnedHome) || !validScalar(options.PlatformFamily) || !validScalar(options.PlatformOS) || !utf8.ValidString(options.Prompt) || strings.TrimSpace(options.Prompt) == "" || (options.ResumeThreadID != "" && !validScalar(options.ResumeThreadID)) {
		return Outcome{}, ErrInput
	}
	if _, ok := ctx.Deadline(); !ok {
		return Outcome{}, ErrInput
	}
	if ctx.Err() != nil {
		return Outcome{}, ctx.Err()
	}
	// Generated projection contains only nonsecret scalars; this also rejects
	// workspace-write input before even the initialize/config-only sequence.
	var generated map[string]any
	if toml.Unmarshal(projection.ConfigTOML(), &generated) != nil || generated["sandbox_mode"] != "read-only" {
		return Outcome{}, ErrPolicy
	}
	shapes, err := loadShapes()
	if err != nil {
		return Outcome{}, err
	}
	runCtx, stop := context.WithCancel(ctx)
	cancel = stop
	s := newSessionState(runCtx, client, shapes, projection, options)
	defer func() { runErr = s.withFailure(runErr) }()
	// Check the entire potential turn frame before any RPC. Actual frames are
	// independently bounded by M1, including a caller-lowered transport limit.
	s.noteFailure("call", "bounds", "turn/start")
	worst := s.turnParams(ThreadBinding{ThreadID: strings.Repeat("x", codexrpc.MaxScalarBytes), Model: strings.Repeat("x", codexrpc.MaxScalarBytes), ModelProvider: "openai"}, generated["model_reasoning_effort"], generated["service_tier"])
	if _, err = boundedCall("turn/start", worst); err != nil {
		return Outcome{}, err
	}
	launch := func(method string, params map[string]any) error {
		s.noteFailure("call", "bounds", method)
		raw, err := boundedCall(method, params)
		if err != nil {
			return err
		}
		if method != "initialized" {
			s.failure.Branch, s.failure.Shape = "shape", FailureFailed
			if _, err = shapes.object("call:"+method, raw); err != nil {
				return err
			}
			s.failure.Shape = FailurePassed
		}
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		s.phase = method
		job = make(chan phaseResult, 1)
		channel := job
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			if method == "initialized" {
				channel <- phaseResult{err: client.Notify(runCtx, method, nil)}
			} else {
				body, err := client.Call(runCtx, method, raw)
				channel <- phaseResult{raw: body, err: err}
			}
		}()
		return nil
	}
	init := map[string]any{"clientInfo": map[string]any{"name": "agent-bird-managed", "version": "1", "title": "Agent Bird"}, "capabilities": map[string]any{"experimentalApi": false, "requestAttestation": false}}
	if err = launch("initialize", init); err != nil {
		return s.out, err
	}
	events := client.Events()
	writeClosed := false
	acceptClosedEvents := func() error {
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		if options.RequireStdoutEOF && client.ReadEOF() && errors.Is(client.Err(), io.EOF) {
			// Receiving ok=false is the barrier: every buffered event before
			// the reader's EOF has already passed through this same reducer.
			events = nil
			return nil
		}
		s.noteFailure("transport", "events_closed", "")
		return ErrProtocol
	}
	for {
		if runCtx.Err() != nil {
			return s.out, runCtx.Err()
		}
		if events == nil && job == nil && s.terminal == nil {
			s.noteFailure("transport", "events_closed", "")
			return s.out, ErrProtocol
		}
		if s.terminal != nil && job == nil {
			// A matched reply cannot jump over already queued permission/error
			// events. Drain them with the same original context before finishing.
			select {
			case event, ok := <-events:
				if !ok {
					if err = acceptClosedEvents(); err != nil {
						return s.out, err
					}
					continue
				}
				if err = s.event(event); err != nil {
					return s.out, err
				}
				continue
			default:
			}
			if events == nil {
				return s.finish()
			}
			if client.Err() != nil && !(options.RequireStdoutEOF && client.ReadEOF() && errors.Is(client.Err(), io.EOF)) {
				if runCtx.Err() != nil {
					return s.out, runCtx.Err()
				}
				s.noteFailure("transport", "client_error", "")
				return s.out, ErrProtocol
			}
			if runCtx.Err() != nil {
				return s.out, runCtx.Err()
			}
			if !options.RequireStdoutEOF {
				return s.finish()
			}
			if !writeClosed {
				if err = client.CloseWrite(); err != nil {
					if runCtx.Err() != nil {
						return s.out, runCtx.Err()
					}
					s.noteFailure("transport", "close_write", "")
					return s.out, ErrProtocol
				}
				writeClosed = true
			}
			// Block in the normal event/context select below; no polling,
			// raw drain or second stdout consumer can skip a late refusal.
		}
		select {
		case <-runCtx.Done():
			return s.out, runCtx.Err()
		case event, ok := <-events:
			if !ok {
				if err = acceptClosedEvents(); err != nil {
					return s.out, err
				}
				continue
			}
			if err = s.event(event); err != nil {
				return s.out, err
			}
		case result := <-job:
			job = nil
			if result.err != nil {
				if runCtx.Err() != nil {
					return s.out, runCtx.Err()
				}
				if errors.Is(result.err, ErrCallback) {
					s.noteFailure("callback", "returned_error", "")
					return s.out, ErrCallback
				}
				var rpcError *codexrpc.RPCError
				if errors.As(result.err, &rpcError) {
					s.noteFailure("call_result", "rpc_error", s.phase)
				} else {
					s.noteFailure("transport", "call_error", "")
				}
				return s.out, ErrProtocol
			}
			// M1 queues notifications in wire order before delivering a matched
			// reply. They must not jump over the thread/callback/turn barrier just
			// because select chose the phase result before the event channel.
			for draining := true; draining; {
				if runCtx.Err() != nil {
					return s.out, runCtx.Err()
				}
				select {
				case event, ok := <-events:
					if !ok {
						if err = acceptClosedEvents(); err != nil {
							return s.out, err
						}
						draining = false
						continue
					}
					if err = s.event(event); err != nil {
						return s.out, err
					}
				default:
					draining = false
				}
			}
			if s.phase == "binding" {
				s.callbackDone = true
				if err = launch("turn/start", s.turnParams(s.out.Binding, s.effort, s.tier)); err != nil {
					return s.out, err
				}
				continue
			}
			if s.phase == "initialized" {
				if err = launch("config/read", map[string]any{"cwd": options.CWD, "includeLayers": false}); err != nil {
					return s.out, err
				}
				continue
			}
			s.noteFailure("response", "shape", s.phase)
			s.failure.Shape = FailureFailed
			response, err := shapes.object("response:"+s.phase, result.raw)
			if err != nil {
				return s.out, err
			}
			s.failure.Shape = FailurePassed
			switch s.phase {
			case "initialize":
				s.failure.Branch = "initialize"
				if !s.initialize(response) {
					return s.out, ErrPolicy
				}
				if err = launch("initialized", nil); err != nil {
					return s.out, err
				}
			case "config/read":
				s.failure.Branch = "config"
				if err = s.config(response); err != nil {
					return s.out, err
				}
				method, params := s.threadParams()
				if err = launch(method, params); err != nil {
					return s.out, err
				}
			case "thread/start", "thread/resume":
				s.failure.Branch = "thread_binding"
				if err = s.bindThread(response); err != nil {
					return s.out, err
				}
				s.phase = "binding"
				job = make(chan phaseResult, 1)
				channel := job
				binding := s.out.Binding
				jobs.Add(1)
				go func() {
					defer jobs.Done()
					err := options.OnThread(runCtx, binding)
					if err != nil {
						channel <- phaseResult{err: ErrCallback}
					} else {
						channel <- phaseResult{}
					}
				}()
			case "turn/start":
				s.failure.Branch = "turn_binding"
				if err = s.bindTurn(response); err != nil {
					return s.out, err
				}
				s.phase = "running"
			default:
				s.failure.Branch = "phase"
				return s.out, ErrProtocol
			}
		}
	}
}

func validPath(path string) bool {
	return utf8.ValidString(path) && !strings.ContainsRune(path, 0) && filepath.IsAbs(path) && filepath.Clean(path) == path
}
func validScalar(value string) bool {
	return utf8.ValidString(value) && len(value) > 0 && len(value) <= codexrpc.MaxScalarBytes && !strings.ContainsRune(value, 0) && strings.TrimSpace(value) != ""
}
func boundedCall(method string, params map[string]any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, ErrInput
	}
	// M1 allocates a uint64 ID; use its maximum encoded length conservatively.
	frame, err := json.Marshal(map[string]any{"id": "local-18446744073709551615", "method": method, "params": json.RawMessage(raw)})
	if err != nil {
		return nil, ErrInput
	}
	if len(frame) > codexrpc.MaxFrameBytes {
		return nil, codexrpc.ErrFrameLimit
	}
	return raw, nil
}
