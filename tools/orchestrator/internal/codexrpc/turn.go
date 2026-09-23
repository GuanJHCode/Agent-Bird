package codexrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
)

type TurnConfig struct {
	Home, Directory, Prompt, Model, Reasoning string
	Approval                                  any
	Reviewer                                  any
	Schema                                    any
	VerifyConfig                              func(map[string]any) error
	EnableTools                               func() error
	SaveFinal                                 func(string) error
	DynamicTools                              []DynamicTool
}
type Turn struct {
	config                  TurnConfig
	input                   io.WriteCloser
	output                  io.Writer
	mu                      sync.Mutex
	buffer                  []byte
	stage                   int
	thread, turn, final     string
	usage                   map[string]any
	queued                  []map[string]any
	queuedBytes             int
	complete                bool
	failure                 error
	toolCalls, toolRequests map[string]bool
}

func NewTurn(config TurnConfig, input io.WriteCloser) *Turn {
	return &Turn{config: config, input: input}
}
func (t *Turn) Output(output io.Writer) io.Writer {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output = output
	if err := t.request(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "agent-bird", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}); err != nil {
		t.fail(err)
	}
	return t
}
func (t *Turn) Close() error { return t.input.Close() }
func (t *Turn) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failure != nil {
		return len(p), nil
	}
	t.buffer = append(t.buffer, p...)
	for {
		i := bytes.IndexByte(t.buffer, '\n')
		if i < 0 {
			break
		}
		if i > maxAppServerLine {
			t.fail(CodeError("codex_turn_frame_too_large"))
			break
		}
		raw := t.buffer[:i]
		t.buffer = t.buffer[i+1:]
		var message map[string]any
		if json.Unmarshal(raw, &message) != nil {
			t.fail(CodeError("codex_turn_protocol_failed"))
			break
		}
		if err := t.accept(message); err != nil {
			t.fail(err)
			break
		}
	}
	if len(t.buffer) > maxAppServerLine {
		t.fail(CodeError("codex_turn_frame_too_large"))
	}
	return len(p), nil
}
func (t *Turn) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failure != nil {
		return t.failure
	}
	if !t.complete {
		return CodeError("codex_turn_incomplete")
	}
	return nil
}
func (t *Turn) fail(err error) {
	if t.failure == nil {
		t.failure = err
	}
	_ = t.input.Close()
}
func (t *Turn) send(v map[string]any) error { return json.NewEncoder(t.input).Encode(v) }
func (t *Turn) request(id int, method string, params map[string]any) error {
	t.stage = id
	return t.send(map[string]any{"id": id, "method": method, "params": params})
}
func (t *Turn) emit(v map[string]any) error { return json.NewEncoder(t.output).Encode(v) }
func (t *Turn) accept(m map[string]any) error {
	invalid := CodeError("codex_turn_protocol_failed")
	if method, ok := m["method"].(string); ok {
		if m["id"] != nil {
			if method == "item/tool/call" {
				return t.dynamicCall(m)
			}
			return CodeError("codex_native_decision_required")
		}
		params, _ := m["params"].(map[string]any)
		switch method {
		case "item/completed", "turn/completed", "thread/tokenUsage/updated":
			if params == nil {
				return invalid
			}
			if t.turn == "" {
				raw, err := json.Marshal(m)
				if err != nil || len(t.queued) >= 64 || t.queuedBytes+len(raw) > maxAppServerLine {
					return invalid
				}
				t.queuedBytes += len(raw)
				t.queued = append(t.queued, m)
				return nil
			}
			return t.notification(method, params)
		default:
			return nil // Other notifications do not constitute completion.
		}
	}
	id, ok := m["id"].(float64)
	if !ok || id != float64(t.stage) {
		return invalid
	}
	if m["error"] != nil {
		return CodeError("codex_turn_request_rejected")
	}
	r, ok := m["result"].(map[string]any)
	if !ok {
		return invalid
	}
	switch t.stage {
	case 1:
		if r["codexHome"] != t.config.Home {
			return CodeError("codex_runtime_home_mismatch")
		}
		if err := t.send(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
			return err
		}
		return t.request(2, "config/read", map[string]any{"includeLayers": true, "cwd": t.config.Directory})
	case 2:
		if t.config.VerifyConfig == nil {
			return invalid
		}
		if err := t.config.VerifyConfig(r); err != nil {
			return err
		}
		params := map[string]any{"cwd": t.config.Directory, "ephemeral": true, "sandbox": "read-only", "experimentalRawEvents": false}
		if len(t.config.DynamicTools) > 0 {
			definitions, err := t.dynamicDefinitions()
			if err != nil {
				return err
			}
			params["dynamicTools"] = definitions
		}
		if t.config.Model != "" {
			params["model"] = t.config.Model
		}
		return t.request(3, "thread/start", params)
	case 3:
		thread, _ := r["thread"].(map[string]any)
		t.thread, _ = thread["id"].(string)
		if t.thread == "" || len(t.thread) > 256 || r["cwd"] != t.config.Directory || !reflect.DeepEqual(r["approvalPolicy"], t.config.Approval) || !reflect.DeepEqual(r["approvalsReviewer"], t.config.Reviewer) {
			return CodeError("codex_thread_policy_changed")
		}
		if t.config.Model != "" && r["model"] != t.config.Model {
			return CodeError("codex_thread_model_changed")
		}
		if err := t.emit(map[string]any{"type": "thread.started", "thread_id": t.thread}); err != nil {
			return err
		}
		params := map[string]any{"threadId": t.thread, "input": []any{map[string]any{"type": "text", "text": t.config.Prompt}}, "sandboxPolicy": map[string]any{"type": "externalSandbox", "networkAccess": "restricted"}}
		if t.config.Reasoning != "" {
			params["effort"] = t.config.Reasoning
		}
		if t.config.Schema != nil {
			params["outputSchema"] = t.config.Schema
		}
		if t.config.EnableTools != nil {
			if err := t.config.EnableTools(); err != nil {
				return err
			}
		}
		return t.request(4, "turn/start", params)
	case 4:
		turn, _ := r["turn"].(map[string]any)
		t.turn, _ = turn["id"].(string)
		if t.turn == "" || len(t.turn) > 256 {
			return invalid
		}
		t.stage = 5
		for _, notification := range t.queued {
			if err := t.notification(notification["method"].(string), notification["params"].(map[string]any)); err != nil {
				return err
			}
		}
		t.queued = nil
		t.queuedBytes = 0
		return nil
	default:
		return invalid
	}
}
func (t *Turn) notification(method string, p map[string]any) error {
	invalid := CodeError("codex_turn_identity_changed")
	if p == nil || p["threadId"] != t.thread || t.complete {
		return invalid
	}
	if method == "turn/completed" {
		turn, _ := p["turn"].(map[string]any)
		if turn["id"] != t.turn {
			return invalid
		}
		if turn["status"] != "completed" || turn["error"] != nil {
			return CodeError("codex_turn_failed")
		}
		if t.final == "" || t.usage == nil {
			return CodeError("codex_turn_result_incomplete")
		}
		if t.config.SaveFinal != nil {
			if err := t.config.SaveFinal(t.final); err != nil {
				return err
			}
		}
		if err := t.emit(map[string]any{"type": "turn.completed", "usage": t.usage}); err != nil {
			return err
		}
		t.complete = true
		return t.input.Close()
	}
	if p["turnId"] != t.turn {
		return invalid
	}
	if method == "thread/tokenUsage/updated" {
		usage, _ := p["tokenUsage"].(map[string]any)
		total, _ := usage["total"].(map[string]any)
		projected := map[string]any{}
		for source, target := range map[string]string{"inputTokens": "input_tokens", "cachedInputTokens": "cached_input_tokens", "cacheWriteInputTokens": "cache_write_input_tokens", "outputTokens": "output_tokens", "reasoningOutputTokens": "reasoning_output_tokens"} {
			v := total[source]
			if source == "cacheWriteInputTokens" && v == nil {
				v = float64(0)
			}
			count, ok := v.(float64)
			if !ok || count < 0 || count > 9007199254740991 || count != float64(int64(count)) {
				return CodeError("codex_turn_usage_invalid")
			}
			projected[target] = int64(count)
		}
		t.usage = projected
		return nil
	}
	item, _ := p["item"].(map[string]any)
	if item["type"] != "agentMessage" {
		return nil
	}
	text, ok := item["text"].(string)
	if !ok || len(text) > maxAppServerLine {
		return errors.New("codex_turn_message_invalid")
	}
	id, ok := item["id"].(string)
	if !ok || id == "" {
		return invalid
	}
	t.final = text
	return t.emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": id, "type": "agent_message", "text": text}})
}
