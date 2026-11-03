package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/install"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
)

func codexCommand(ctx context.Context, cmd process.Command, pin adapter.BinaryPin, profile *adapter.ExecutionProfile, scratch string) (process.Command, error) {
	if err := adapter.LaunchAdmission(adapter.ProviderCodex, pin.Version, pin.SHA256); err != nil {
		return cmd, err
	}
	executable, err := os.Executable()
	if err != nil {
		return cmd, errors.New("codex_guard_package_untrusted")
	}
	runtime, helper, err := install.CodexGuardRuntime(executable)
	if err != nil {
		return cmd, err
	}
	if err = os.MkdirAll(scratch, 0700); err != nil {
		return cmd, err
	}
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return cmd, err
	}
	if profile == nil {
		profile = &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 3_600_000}
	}
	// Validate the existing Host boundary, then project its owned paths into
	// a native named permission profile. Native applies Seatbelt to tools.
	sandboxed, err := sandboxCommand(ctx, cmd, profile, scratch)
	if err != nil {
		return cmd, err
	}
	policy, err := codexNativePolicy(scratch, sandboxed.Env)
	if err != nil {
		return cmd, err
	}
	cmd.Env = sandboxed.Env
	cmd.Args = append([]string{"-E", "-s", "-B", helper, "--host-policy", policy, "--binary", pin.Path, "--version", strings.TrimPrefix(pin.Version, "codex-cli "), "--sha256", strings.ToLower(pin.SHA256), "--scratch", scratch, "--"}, cmd.Args...)
	cmd.Path = runtime.Interpreter
	return cmd, nil
}

// Authentication snapshots are private launch inputs, never retained artifacts.
// Removing our own leaf is safe even after an unknown outcome; no new token
// reads should be possible from an attempt the Host has ceased executing.
func cleanupCodexAuth(scratch string) {
	home := filepath.Join(scratch, "codex-home")
	resolved, err := filepath.EvalSymlinks(home)
	canonicalScratch, scratchErr := filepath.EvalSymlinks(scratch)
	if err != nil || scratchErr != nil || resolved != filepath.Join(canonicalScratch, "codex-home") {
		return
	}
	// EvalSymlinks above rejects a replacement pointing outside this launch.
	_ = os.Remove(filepath.Join(home, "auth.json"))
}

// This contract carries only paths owned and validated by Host, never SBPL or
// caller-supplied permission rules. The fixed native engine owns tool sandboxing.
func codexNativePolicy(scratch string, env []string) (string, error) {
	launch := filepath.Dir(filepath.Dir(filepath.Dir(scratch)))
	if filepath.Base(filepath.Dir(launch)) != "host-spool" {
		return "", errors.New("codex_policy_untrusted")
	}
	paths := map[string]any{"version": 1, "scratch": scratch, "state_root": filepath.Dir(filepath.Dir(launch))}
	for _, entry := range env {
		cap, ok := strings.CutPrefix(entry, "ORCHESTRATOR_REPORT_CAPABILITY=")
		if !ok {
			continue
		}
		resolved, err := filepath.EvalSymlinks(cap)
		reports := filepath.Join(launch, ".reports")
		relative, relErr := filepath.Rel(reports, resolved)
		parts := strings.Split(relative, string(filepath.Separator))
		if err != nil || relErr != nil || len(parts) != 2 || parts[0] == ".." || parts[1] != "capability.json" || filepath.Base(filepath.Dir(cap)) != parts[0] || filepath.Base(cap) != "capability.json" {
			return "", errors.New("codex_policy_untrusted")
		}
		paths["report_capability"] = resolved
	}
	body, err := json.Marshal(paths)
	return string(body), err
}
