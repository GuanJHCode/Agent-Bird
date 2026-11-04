package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/install"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
)

func codexCommand(ctx context.Context, cmd process.Command, pin adapter.BinaryPin, profile *adapter.ExecutionProfile, scratch string, workspace ...*adapter.CandidateWorkspace) (process.Command, error) {
	if err := adapter.LaunchAdmission(adapter.ProviderCodex, pin.Version, pin.SHA256); err != nil {
		return cmd, err
	}
	if profile != nil && profile.Permission == adapter.WorkspaceWrite {
		if err := adapter.CodexWorkspaceWriteAdmission(pin.Version, pin.SHA256); err != nil {
			return cmd, err
		}
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
	options := codexPolicyOptions{profile: profile, cwd: cmd.Dir}
	if len(workspace) > 0 {
		options.workspace = workspace[0]
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return cmd, errors.New("codex_deadline_required")
	}
	options.timeoutMS = time.Until(deadline).Milliseconds() + 1
	policy, err := codexNativePolicy(scratch, sandboxed.Env, options)
	if err != nil {
		return cmd, err
	}
	cmd.Env, err = codexHelperEnvironment(cmd, sandboxed.Env)
	if err != nil {
		return cmd, err
	}
	cmd.ExactEnv = true
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
type codexPolicyOptions struct {
	profile   *adapter.ExecutionProfile
	cwd       string
	workspace *adapter.CandidateWorkspace
	timeoutMS int64
}

func codexNativePolicy(scratch string, env []string, options ...codexPolicyOptions) (string, error) {
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
	if len(options) > 0 {
		o := options[0]
		if o.profile == nil || o.timeoutMS < 1 || o.timeoutMS > 3_600_000 {
			return "", errors.New("codex_policy_untrusted")
		}
		paths["version"] = 2
		paths["role"] = o.profile.Role
		paths["timeout_ms"] = o.timeoutMS
		if o.profile.Role == adapter.Implementer {
			if o.profile.Permission != adapter.WorkspaceWrite {
				return "", errors.New("codex_policy_untrusted")
			}
			writable, protected, err := managedWritePaths(context.Background(), o.cwd, o.workspace)
			if err != nil {
				return "", err
			}
			paths["workspace"] = o.cwd
			paths["write_paths"] = writable
			paths["git_paths"] = protected
		} else if o.profile.Role != adapter.Reviewer || o.profile.Permission != adapter.ReadOnly {
			return "", errors.New("codex_policy_untrusted")
		}
	}
	body, err := json.Marshal(paths)
	return string(body), err
}

// Only the auth guard may inject the refresh tripwire. The helper starts without
// Python/loader hooks or ambient provider credentials, even before its guard runs.
func codexHelperEnvironment(cmd process.Command, projected []string) ([]string, error) {
	source := commandEnvironment(cmd)
	if _, present := source["CODEX_REFRESH_TOKEN_URL_OVERRIDE"]; present {
		return nil, errors.New("refresh_override_already_present")
	}
	allowed := strings.Fields("HOME CODEX_HOME PATH LANG LC_ALL LC_CTYPE TERM TMPDIR HTTPS_PROXY HTTP_PROXY ALL_PROXY NO_PROXY https_proxy http_proxy all_proxy no_proxy SSL_CERT_FILE SSL_CERT_DIR NODE_EXTRA_CA_CERTS ORCHESTRATOR_REPORT_CAPABILITY ORCHESTRATOR_REPORT_EXECUTABLE")
	env := map[string]string{}
	for _, name := range allowed {
		if value, ok := source[name]; ok {
			env[name] = value
		}
	}
	// Refuse alternate OpenAI/Codex auth rather than silently changing its route.
	for name := range source {
		if strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "CODEX_") {
			switch name {
			case "CODEX_HOME":
				// The guard uses this source path only to create its private auth projection.
			case "CODEX_SQLITE_HOME", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE", "CODEX_CI", "CODEX_VERSION", "CODEX_SESSION_ID", "CODEX_THREAD_ID", "CODEX_TOOL_CALL_ID", "CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS":
				// Match codex_exec.clean_environment's known non-auth context.
				// These outer-session fields are deliberately absent from allowed.
			default:
				return nil, errors.New("auth_environment_unsupported")
			}
		}
	}
	for _, entry := range projected {
		if value, ok := strings.CutPrefix(entry, "TMPDIR="); ok {
			env["TMPDIR"] = value
		}
	}
	env["PATH"] = "/usr/bin:/bin:/usr/sbin:/sbin"
	result := []string{}
	for name, value := range env {
		result = append(result, name+"="+value)
	}
	sort.Strings(result)
	return result, nil
}
