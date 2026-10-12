package host

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codextools"
)

const (
	codexPluginToolFileBytes = 32 << 10
	codexPluginToolTextBytes = 256 << 10
	codexPluginToolFailure   = "codex_plugin_tool_failed"
)

// openCodexReadTools binds the fixed read-only dynamic-tool inventory to one
// descriptor-anchored workspace Broker. It grants no writes, processes, or
// network access; callers must close the returned broker release function.
func openCodexReadTools(root string) (*codexsession.HostTools, func() error, error) {
	broker, err := codextools.Open(root, codextools.Options{})
	if err != nil {
		return nil, nil, err
	}
	return codexBrokerTools(broker), broker.Close, nil
}

func codexBrokerTools(broker *codextools.Broker) *codexsession.HostTools {
	tools := &codexsession.HostTools{Writable: false}
	tools.OnTool = func(ctx context.Context, call codexsession.ToolCall) (codexsession.ToolResult, error) {
		if ctx.Err() != nil {
			return codexPluginToolFailureResult(), nil
		}
		path, ok := codexPluginToolPath(call.Arguments)
		if !ok {
			return codexPluginToolFailureResult(), nil
		}
		switch call.Tool {
		case "read_file":
			content, sum, err := broker.Read(ctx, path)
			if err != nil || len(content) > codexPluginToolFileBytes || !utf8.Valid(content) || ctx.Err() != nil {
				return codexPluginToolFailureResult(), nil
			}
			return codexPluginToolJSON(map[string]string{"path": path, "sha256": sum, "content": string(content)})
		case "list_files":
			entries, err := broker.List(ctx, path)
			if err != nil || ctx.Err() != nil {
				return codexPluginToolFailureResult(), nil
			}
			for _, entry := range entries {
				if !utf8.ValidString(entry) {
					return codexPluginToolFailureResult(), nil
				}
			}
			return codexPluginToolJSON(struct {
				Entries []string `json:"entries"`
			}{Entries: entries})
		default:
			return codexPluginToolFailureResult(), nil
		}
	}
	return tools
}

func codexPluginToolJSON(value any) (codexsession.ToolResult, error) {
	body, err := json.Marshal(value)
	if err != nil || len(body) > codexPluginToolTextBytes || !utf8.Valid(body) {
		return codexPluginToolFailureResult(), nil
	}
	return codexsession.ToolResult{Text: string(body), Success: true}, nil
}

func codexPluginToolFailureResult() codexsession.ToolResult {
	return codexsession.ToolResult{Text: codexPluginToolFailure, Success: false}
}

func codexPluginToolPath(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || len(raw) > 64<<10 || !utf8.Valid(raw) {
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil {
		return "", false
	}
	if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
		return "", false
	}
	var path string
	havePath := false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return "", false
		}
		name, ok := key.(string)
		if !ok || name != "path" || havePath || decoder.Decode(&path) != nil {
			return "", false
		}
		havePath = true
	}
	end, err := decoder.Token()
	if err != nil {
		return "", false
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != '}' || !havePath || !utf8.ValidString(path) {
		return "", false
	}
	var extra any
	return path, decoder.Decode(&extra) == io.EOF
}
