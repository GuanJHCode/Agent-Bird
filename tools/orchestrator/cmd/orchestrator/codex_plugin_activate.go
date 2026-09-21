package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"io"
	"os/exec"
	"path/filepath"
	"time"
)

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
	activateErr := activateConfigForUser(ctx, rpc, *cwd, rpc.UserConfig())
	stopErr := closeRPC()
	if activateErr != nil {
		return activateErr
	}
	if stopErr != nil {
		return stopErr
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

var appServerCommand = func(context.Context) *exec.Cmd { return exec.Command("codex", "app-server", "--listen", "stdio://") }

func startCodexAppServer(ctx context.Context) (*codexrpc.Client, func() error, error) {
	return codexrpc.StartCommand(ctx, appServerCommand(ctx))
}
