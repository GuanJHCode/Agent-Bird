package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"io"
	"math"
	"path/filepath"
	"strconv"
)

type birdMCPMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type birdMCPCompletion struct {
	id     json.RawMessage
	result providerCallResult
	err    error
}

func providerMCPEntry(ctx context.Context, args []string, in io.ReadCloser, out io.Writer) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	state := fs.String("state-dir", "", "private runtime state")
	locks := fs.String("provider-locks", "", "directory containing provider lock JSON files")
	if fs.Parse(args) != nil || fs.NArg() != 0 || !filepath.IsAbs(*locks) {
		return codeError("invalid_args")
	}
	return serveProviderMCP(ctx, in, out, func(ctx context.Context, req providerCallRequest) (providerCallResult, error) {
		// The plugin supplies pin paths; model arguments cannot replace them.
		req.ProviderLock = filepath.Join(*locks, req.Provider+".json")
		return executeProviderCall(ctx, req, *state)
	})
}

func serveProviderMCP(parent context.Context, in io.ReadCloser, out io.Writer, execute func(context.Context, providerCallRequest) (providerCallResult, error)) error {
	ctx, cancelAll := context.WithCancel(parent)
	defer cancelAll()
	defer in.Close()
	messages := make(chan []byte)
	readErr := make(chan error, 1)
	readerDone := make(chan struct{})
	defer func() { cancelAll(); _ = in.Close(); <-readerDone }()
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 4096), 128*1024)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case messages <- line:
			case <-ctx.Done():
				return
			}
		}
		readErr <- scanner.Err()
	}()
	encoder := json.NewEncoder(out)
	reply := func(id json.RawMessage, result any, code int, message string) error {
		if len(id) == 0 {
			id = json.RawMessage("null")
		}
		response := map[string]any{"jsonrpc": "2.0", "id": id}
		if code != 0 {
			response["error"] = map[string]any{"code": code, "message": message}
		} else {
			response["result"] = result
		}
		return encoder.Encode(response)
	}
	completions := make(chan birdMCPCompletion, 1)
	var active context.CancelFunc
	var activeID string
	var last *providerCallResult
	initialized, ready := false, false
	inputClosed := false
	finishActive := func() {
		if active != nil {
			active()
			<-completions
			active = nil
		}
	}
	defer finishActive()
	for {
		if inputClosed && active == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			inputClosed = true
			readErr = nil
			if active != nil {
				active()
			}
			if err != nil {
				return err
			}
		case completed := <-completions:
			active()
			active = nil
			activeID = ""
			if completed.err != nil {
				completed.result.Status = "failed"
				completed.result.FailureReason = errorCode(completed.err)
			}
			last = &completed.result
			body, _ := json.Marshal(compactProviderResult(completed.result))
			failed := completed.err != nil || completed.result.FailureReason != ""
			result := map[string]any{"content": []any{map[string]any{"type": "text", "text": string(body)}}, "isError": failed}
			if !inputClosed {
				if err := reply(completed.id, result, 0, ""); err != nil {
					return err
				}
			}
		case raw := <-messages:
			var message birdMCPMessage
			if json.Unmarshal(raw, &message) != nil || message.JSONRPC != "2.0" {
				if err := reply(nil, nil, -32600, "Invalid Request"); err != nil {
					return err
				}
				continue
			}
			notification := len(message.ID) == 0
			if notification {
				switch message.Method {
				case "notifications/initialized":
					if initialized {
						ready = true
					}
				case "notifications/cancelled":
					var request struct {
						RequestID json.RawMessage `json:"requestId"`
					}
					if json.Unmarshal(message.Params, &request) == nil && active != nil && mcpRequestID(request.RequestID) != "" && mcpRequestID(request.RequestID) == activeID {
						active()
					}
				}
				continue
			}
			identity := mcpRequestID(message.ID)
			if identity == "" {
				if err := reply(nil, nil, -32600, "Invalid request ID"); err != nil {
					return err
				}
				continue
			}

			var result any
			code, messageText := 0, ""
			switch message.Method {
			case "initialize":
				if initialized {
					code, messageText = -32600, "Already initialized"
					break
				}
				initialized = true
				result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "agent-bird", "version": "1.0.0-candidate"}}
			case "ping":
				result = map[string]any{}
			case "tools/list":
				if !ready {
					code, messageText = -32600, "Initialize first"
					break
				}
				result = map[string]any{"tools": []any{map[string]any{"name": "agent_bird_call", "description": "Run one serial child task. Writes require explicit paths in an isolated candidate. Returns a bounded result excerpt; use agent_bird_result only if details are needed. No recursive delegation.", "inputSchema": json.RawMessage(birdCallSchema)}, map[string]any{"name": "agent_bird_result", "description": "Read a bounded detail page from this connection’s last completed call. Does not launch a Provider. Available until the next call starts or this connection closes.", "inputSchema": json.RawMessage(birdResultSchema)}}}
			case "tools/call":
				if !ready {
					code, messageText = -32600, "Initialize first"
					break
				}
				if active != nil {
					code, messageText = -32000, "call_busy"
					break
				}
				var call struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				}
				var req providerCallRequest
				if json.Unmarshal(message.Params, &call) != nil {
					code, messageText = -32602, "Invalid tool arguments"
					break
				}
				if call.Name == "agent_bird_result" {
					var request birdResultRequest
					var page birdResultPage
					var err error
					if decode(call.Arguments, &request) != nil {
						err = codeError("call_result_request_invalid")
					} else {
						page, err = providerResultPage(last, request)
					}
					if err != nil {
						code, messageText = -32602, errorCode(err)
						break
					}
					body, _ := json.Marshal(page)
					result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(body)}}, "isError": false}
					break
				}
				if call.Name != "agent_bird_call" || decode(call.Arguments, &req) != nil || req.ProviderLock != "" {
					code, messageText = -32602, "Invalid tool arguments"
					break
				}
				req.Version = 1
				if err := normalizeProviderCall(&req); err != nil {
					code, messageText = -32602, errorCode(err)
					break
				}
				last = nil
				child, stop := context.WithCancel(ctx)
				active = stop
				activeID = identity
				id := append(json.RawMessage(nil), message.ID...)
				go func() { result, err := execute(child, req); completions <- birdMCPCompletion{id, result, err} }()
				continue
			default:
				code, messageText = -32601, "Method not found"
			}
			if err := reply(message.ID, result, code, messageText); err != nil {
				return err
			}
		}
	}
}

const birdCallSchema = `{"type":"object","required":["provider","task","directory"],"additionalProperties":false,"properties":{"provider":{"type":"string","enum":["codex","claude","agy","grok"]},"task":{"type":"string","minLength":1,"maxLength":60000},"directory":{"type":"string","description":"Absolute project directory"},"mode":{"type":"string","enum":["read-only","workspace-write"],"default":"read-only"},"paths":{"type":"array","items":{"type":"string"},"maxItems":256},"timeout_ms":{"type":"integer","minimum":1,"maximum":3600000,"default":120000}}}`

// MCP uses string or integer request IDs. Compare decoded identity; preserve
// original representation only when echoing a response. Restrict numeric IDs
// to exactly representable JSON integers to avoid cross-request aliasing.
func mcpRequestID(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	switch id := value.(type) {
	case string:
		return "s:" + id
	case float64:
		if math.Abs(id) > 9007199254740991 || math.Trunc(id) != id {
			return ""
		}
		if id == 0 {
			return "n:0"
		}
		return "n:" + strconv.FormatFloat(id, 'f', 0, 64)
	}
	return ""
}
