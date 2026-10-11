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
	requestedState := fs.String("state-dir", "", "explicit private runtime state")
	destination := fs.String("install-root", "", "private runtime installation")

	if fs.Parse(args) != nil || *source == "" || fs.NArg() == 0 {
		return codeError("invalid_args")
	}
	action := fs.Arg(0)
	if err := portableRunPolicy(runtime.GOOS, action); err != nil {
		return err
	}
	state, err := resolveState(*requestedState)
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
	var binary string
	if runtime.GOOS == "linux" {
		binary, err = install.InstallPortableCollectOnly(*source, *destination, state)
	} else {
		binary, err = install.InstallPortable(*source, *destination, state)
	}
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

// portableRunPolicy is deliberately pure so platform admission can be unit
// tested without representing that test as native Linux execution.
func portableRunPolicy(platform, action string) error {
	switch platform {
	case "darwin":
		if pluginActionAllowed(action) {
			return nil
		}
		return codeError("portable_action_unsupported")
	case "linux":
		if action == "check" || action == "runtime-status" {
			return nil
		}
		return codeError("portable_action_unsupported")
	default:
		return codeError("portable_platform_unsupported")
	}
}

func pluginActionAllowed(action string) bool {
	switch action {
	case "bridge", "config-plan", "config-apply", "config-remove", "provider", "check", "runtime-status", "preflight", "task", "routing", "owner-bind", "controller", "provider-probe", "provider-lock", "ensure-running", "submit", "status", "summary", "collect", "wait-events", "ack", "accept", "answer", "retry", "rework", "resume", "stop", "rebind-owner", "recover-host":
		return true
	default:
		return false
	}
}
