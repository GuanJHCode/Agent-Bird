package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
)

func pluginRun(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plugin-run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("plugin-root", "", "installed Codex plugin root")
	destination := fs.String("install-root", "", "private runtime installation")

	if fs.Parse(args) != nil || *source == "" || fs.NArg() == 0 {
		return codeError("invalid_args")
	}
	action := fs.Arg(0)
	if !pluginActionAllowed(action) {
		return codeError("portable_action_unsupported")
	}
	if runtime.GOOS != "darwin" {
		return codeError("portable_platform_unsupported")
	}
	state, err := resolveState("")
	if err != nil {
		return err
	}
	if *destination == "" {
		home, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		*destination = filepath.Join(home, "OpenAI", "Codex Bird Packages")
	}
	binary, err := install.InstallPortable(*source, *destination, state)
	if err != nil {
		return err
	}
	if action == "check" {
		return json.NewEncoder(out).Encode(map[string]any{"status": "ready", "binary": binary, "runtime": "collect-only"})
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Replace the transient plugin-cache process. The durable runtime is outside
	// Codex's cache and keeps the existing installed-version pin lifecycle.
	return syscall.Exec(binary, append([]string{binary}, fs.Args()...), os.Environ())
}

func pluginActionAllowed(action string) bool {
	switch action {
	case "provider", "check", "runtime-status", "preflight", "task", "routing", "owner-bind", "controller", "provider-probe", "provider-lock", "ensure-running", "submit", "status", "summary", "collect", "wait-events", "ack", "accept", "answer", "retry", "rework", "resume", "stop", "rebind-owner", "recover-host":
		return true
	default:
		return false
	}
}
