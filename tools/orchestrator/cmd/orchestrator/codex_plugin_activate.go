package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const maxAppServerLine = 1024 * 1024

const (
	legacyCodexPlugin = "codex-orchestrator@codex-bird"
	nativeCodexPlugin = "agent-bird@agent-bird"
)

type configRPC interface {
	Call(context.Context, string, map[string]any) (map[string]any, error)
}

func codexPluginActivate(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("codex-plugin-activate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cwd := fs.String("cwd", "", "absolute working directory for config/read")
	if fs.Parse(args) != nil || fs.NArg() != 0 || !filepath.IsAbs(*cwd) || filepath.Clean(*cwd) != *cwd {
		return codeError("invalid_args")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rpc, closeRPC, err := startCodexAppServer(ctx)
	if err != nil {
		return err
	}
	defer closeRPC()
	if err := activateConfigForUser(ctx, rpc, *cwd, rpc.userConfig); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "activated")
	return err
}

func activateConfig(ctx context.Context, rpc configRPC, cwd string) error {
	return activateConfigForUser(ctx, rpc, cwd, "")
}

func activateConfigForUser(ctx context.Context, rpc configRPC, cwd, expectedUserConfig string) error {
	before, err := rpc.Call(ctx, "config/read", map[string]any{"includeLayers": true, "cwd": cwd})
	if err != nil {
		return err
	}
	version, filePath, err := baseUserConfig(before, expectedUserConfig)
	if err != nil {
		return err
	}
	written, err := rpc.Call(ctx, "config/batchWrite", map[string]any{
		"filePath": filePath, "expectedVersion": version, "reloadUserConfig": false,
		"edits": []map[string]any{
			{"keyPath": `plugins."codex-orchestrator@codex-bird".enabled`, "mergeStrategy": "upsert", "value": false},
			{"keyPath": `plugins."agent-bird@agent-bird".enabled`, "mergeStrategy": "upsert", "value": true},
		},
	})
	if err != nil {
		return err
	}
	status, _ := written["status"].(string)
	if status != "ok" && status != "okOverridden" {
		return codeError("config_write_rejected")
	}
	after, err := rpc.Call(ctx, "config/read", map[string]any{"includeLayers": true, "cwd": cwd})
	if err != nil {
		return err
	}
	if !pluginEnabled(after, legacyCodexPlugin, false) || !pluginEnabled(after, nativeCodexPlugin, true) {
		return codeError("plugin_activation_overridden")
	}
	return nil
}

func baseUserConfig(result map[string]any, expectedUserConfig string) (string, string, error) {
	layers, ok := result["layers"].([]any)
	if !ok {
		return "", "", codeError("user_config_layer_invalid")
	}
	var version, filePath string
	for _, raw := range layers {
		layer, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, ok := layer["name"].(map[string]any)
		if !ok || name["type"] != "user" || name["profile"] != nil {
			continue
		}
		file, fileOK := name["file"].(string)
		candidate, versionOK := layer["version"].(string)
		if !fileOK || !versionOK || !filepath.IsAbs(file) || filepath.Base(file) != "config.toml" || (expectedUserConfig != "" && file != expectedUserConfig) || candidate == "" || version != "" {
			return "", "", codeError("user_config_layer_invalid")
		}
		filePath, version = file, candidate
	}
	if version == "" {
		return "", "", codeError("user_config_layer_invalid")
	}
	return version, filePath, nil
}

func pluginEnabled(result map[string]any, plugin string, want bool) bool {
	config, ok := result["config"].(map[string]any)
	if !ok {
		return false
	}
	plugins, ok := config["plugins"].(map[string]any)
	if !ok {
		return false
	}
	entry, ok := plugins[plugin].(map[string]any)
	if !ok {
		return false
	}
	enabled, ok := entry["enabled"].(bool)
	return ok && enabled == want
}

type appServerRPC struct {
	stdin      io.WriteCloser
	lines      *bufio.Scanner
	mu         sync.Mutex
	nextID     int
	userConfig string
}

func startCodexAppServer(ctx context.Context) (*appServerRPC, func(), error) {
	cmd := exec.CommandContext(ctx, "codex", "app-server", "--listen", "stdio://")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, codeError("app_server_start_failed")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, codeError("app_server_start_failed")
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, codeError("app_server_start_failed")
	}
	rpc := &appServerRPC{stdin: stdin, lines: bufio.NewScanner(stdout), nextID: 1}
	rpc.lines.Buffer(make([]byte, 4096), maxAppServerLine)
	closeRPC := func() { _ = stdin.Close(); _ = cmd.Wait() }
	initialized, err := rpc.Call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "agent-bird", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}})
	if err != nil {
		closeRPC()
		return nil, func() {}, err
	}
	codexHome, ok := initialized["codexHome"].(string)
	if !ok || !filepath.IsAbs(codexHome) || filepath.Clean(codexHome) != codexHome {
		closeRPC()
		return nil, func() {}, codeError("app_server_protocol_failed")
	}
	rpc.userConfig = filepath.Join(codexHome, "config.toml")
	if err := rpc.notify("initialized", map[string]any{}); err != nil {
		closeRPC()
		return nil, func() {}, err
	}
	return rpc, closeRPC, nil
}

func (r *appServerRPC) notify(method string, params map[string]any) error {
	return r.write(map[string]any{"method": method, "params": params})
}

func (r *appServerRPC) Call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextID
	r.nextID++
	if err := r.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, codeError("app_server_timeout")
		}
		if !r.lines.Scan() {
			return nil, codeError("app_server_protocol_failed")
		}
		var message map[string]any
		if json.Unmarshal(r.lines.Bytes(), &message) != nil {
			return nil, codeError("app_server_protocol_failed")
		}
		if _, requested := message["method"]; requested {
			if _, hasID := message["id"]; hasID {
				return nil, codeError("app_server_server_request")
			}
			continue
		}
		messageID, hasID := message["id"].(float64)
		if !hasID || int(messageID) != id {
			continue
		}
		if _, hasError := message["error"]; hasError {
			return nil, codeError("app_server_rejected")
		}
		result, ok := message["result"].(map[string]any)
		if !ok {
			return nil, codeError("app_server_protocol_failed")
		}
		return result, nil
	}
}

func (r *appServerRPC) write(message map[string]any) error {
	b, err := json.Marshal(message)
	if err != nil || len(b) > maxAppServerLine {
		return codeError("app_server_protocol_failed")
	}
	b = append(b, '\n')
	if _, err := r.stdin.Write(b); err != nil {
		return codeError("app_server_protocol_failed")
	}
	return nil
}
