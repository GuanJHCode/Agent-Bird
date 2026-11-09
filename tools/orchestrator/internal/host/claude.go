package host

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
)

type claudeGatewaySource struct {
	env          map[string]string
	homes        []string
	help         string
	checkManaged func(context.Context) error
	workspace    *adapter.CandidateWorkspace
}

func commandEnvironment(cmd process.Command) map[string]string {
	env := map[string]string{}
	for _, item := range append(os.Environ(), cmd.Env...) {
		if key, value, ok := strings.Cut(item, "="); ok {
			env[key] = value
		}
	}
	return env
}

func claudeGatewaySelected(cmd process.Command) bool {
	return commandEnvironment(cmd)["ANTHROPIC_BASE_URL"] != ""
}

func validateClaudeGatewayInvocation(inv contract.InvocationView, profile *adapter.ExecutionProfile) error {
	if profile == nil {
		return errors.New("claude_gateway_readonly_required")
	}
	if action, ok := inv.(interface {
		CandidateAction() *adapter.CandidateAction
	}); ok && action.CandidateAction() != nil {
		return errors.New("claude_gateway_readonly_required")
	}
	var workspace *adapter.CandidateWorkspace
	if value, ok := inv.(interface {
		CandidateWorkspace() *adapter.CandidateWorkspace
	}); ok {
		workspace = value.CandidateWorkspace()
	}
	if profile.Role == adapter.Reviewer && profile.Permission == adapter.ReadOnly && workspace == nil {
		return nil
	}
	if profile.Role == adapter.Implementer && profile.Permission == adapter.WorkspaceWrite && workspace != nil && workspace.Version == 1 {
		return nil
	}
	return errors.New("claude_gateway_readonly_required")
}

func claudeGatewayCommand(ctx context.Context, cmd process.Command, profile *adapter.ExecutionProfile, scratch string, workspace ...*adapter.CandidateWorkspace) (process.Command, error) {
	account, err := user.Current()
	if err != nil {
		return cmd, errors.New("claude_gateway_source_unverified")
	}
	env := commandEnvironment(cmd)
	if cmd.PinnedPath != cmd.Path || cmd.PinnedSHA256 == "" {
		return cmd, errors.New("claude_gateway_pin_missing")
	}
	if profile != nil && profile.Permission == adapter.WorkspaceWrite {
		rawVersion, err := adapter.ProbePinnedOutput(ctx, adapter.ProviderClaude, adapter.BinaryPin{Path: cmd.PinnedPath, SHA256: cmd.PinnedSHA256, Version: "launch"}, "--version")
		if err != nil {
			return cmd, err
		}
		if err := adapter.ClaudeGatewayWorkspaceWriteAdmission(rawVersion, cmd.PinnedSHA256); err != nil {
			return cmd, err
		}
	}
	help, err := adapter.ProbePinnedOutput(ctx, adapter.ProviderClaude, adapter.BinaryPin{Path: cmd.PinnedPath, SHA256: cmd.PinnedSHA256, Version: "launch"}, "--help")
	if err != nil {
		return cmd, err
	}
	for _, flag := range []string{"--strict-mcp-config", "--mcp-config", "--settings"} {
		matched, _ := regexp.MatchString(`(^|[\s,])`+regexp.QuoteMeta(flag)+`([\s=,]|$)`, help)
		if !matched {
			return cmd, errors.New("claude_gateway_capability_unsupported")
		}
	}
	return prepareClaudeGateway(ctx, cmd, profile, scratch, claudeGatewaySource{
		env: env, homes: []string{account.HomeDir, env["HOME"]}, help: help, checkManaged: checkClaudeManagedSources, workspace: firstWorkspace(workspace),
	})
}

// Gateway workers never load the caller's OAuth, hooks or settings. The API
// credential stays solely in the exact child environment, never in argv/files.
func prepareClaudeGateway(ctx context.Context, cmd process.Command, profile *adapter.ExecutionProfile, scratch string, source claudeGatewaySource) (process.Command, error) {
	if profile == nil || source.checkManaged == nil || ((profile.Role != adapter.Reviewer || profile.Permission != adapter.ReadOnly) && (profile.Role != adapter.Implementer || profile.Permission != adapter.WorkspaceWrite || source.workspace == nil)) {
		return cmd, errors.New("claude_gateway_readonly_required")
	}
	endpoint, err := url.Parse(source.env["ANTHROPIC_BASE_URL"])
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" || endpoint.RawPath != "" {
		return cmd, errors.New("claude_gateway_url_invalid")
	}
	if endpoint.Scheme != "https" {
		ip := net.ParseIP(endpoint.Hostname())
		if endpoint.Scheme != "http" || ip == nil || !ip.IsLoopback() || endpoint.Port() == "" {
			return cmd, errors.New("claude_gateway_url_invalid")
		}
	}
	key, token := source.env["ANTHROPIC_API_KEY"], source.env["ANTHROPIC_AUTH_TOKEN"]
	if (key == "") == (token == "") {
		return cmd, errors.New("claude_gateway_credential_ambiguous")
	}
	if strings.ContainsAny(key+token, "\r\n\x00") || len(key)+len(token) > 16384 {
		return cmd, errors.New("claude_gateway_credential_invalid")
	}
	for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FD", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if source.env[name] != "" {
			return cmd, errors.New("claude_gateway_auth_conflict")
		}
	}
	if err := source.checkManaged(ctx); err != nil {
		return cmd, err
	}
	var homes, denied []string
	for _, raw := range append(append([]string{}, source.homes...), source.env["CLAUDE_CONFIG_DIR"], source.env["CLAUDE_SECURESTORAGE_CONFIG_DIR"]) {
		if raw == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(raw)
		if err != nil || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw {
			return cmd, errors.New("claude_gateway_source_untrusted")
		}
		denied = append(denied, resolved)
	}
	for _, raw := range source.homes {
		if raw == "" {
			continue
		}
		home, err := filepath.EvalSymlinks(raw)
		if err != nil || home == "/" || cmd.Dir == home || strings.HasPrefix(home, cmd.Dir+string(filepath.Separator)) {
			return cmd, errors.New("claude_gateway_workspace_untrusted")
		}
		homes = append(homes, home)
	}
	if len(homes) == 0 {
		return cmd, errors.New("claude_gateway_source_unverified")
	}
	var projectPaths []string
	for dir := cmd.Dir; ; dir = filepath.Dir(dir) {
		for _, name := range []string{".claude", ".claude.json", ".mcp.json"} {
			path := filepath.Join(dir, name)
			projectPaths = append(projectPaths, path)
			isHome := false
			for _, home := range homes {
				isHome = isHome || dir == home
			}
			if !isHome {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					return cmd, errors.New("claude_gateway_project_config_unverified")
				}
			}
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	if err := grokOwnedDirectory(scratch, true, true); err != nil {
		return cmd, err
	}
	home := filepath.Join(scratch, "claude-home")
	if err := os.Mkdir(home, 0700); err != nil {
		return cmd, errors.New("claude_gateway_state_exists")
	}
	config := filepath.Join(home, ".claude")
	if err := os.Mkdir(config, 0700); err != nil {
		return cmd, err
	}
	policy := filepath.Join(home, "policy")
	if err := os.Mkdir(policy, 0700); err != nil {
		return cmd, err
	}
	mcp, settings := filepath.Join(policy, "mcp.json"), filepath.Join(policy, "settings.json")
	for path, body := range map[string]string{mcp: `{"mcpServers":{}}`, settings: `{}`} {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return cmd, err
		}
		_, writeErr := file.WriteString(body)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return cmd, errors.New("claude_gateway_config_untrusted")
		}
	}
	path := "/usr/bin:/bin:/usr/sbin:/sbin"
	var interpreter string
	if strings.HasSuffix(cmd.Path, ".js") {
		node, err := adapter.ClaudeNodePin()
		if err != nil {
			return cmd, errors.New("claude_node_runtime_unavailable")
		}
		interpreter, err = filepath.EvalSymlinks(node.Path)
		if err != nil {
			return cmd, errors.New("claude_node_runtime_unavailable")
		}
		// Node may live in the caller's home (for example nvm). Copy the exact
		// executable into our private state rather than reopening that home to
		// the gateway worker. The caller's installation remains unchanged.
		before, statErr := os.Lstat(interpreter)
		if statErr != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 || before.Size() > 512<<20 {
			return cmd, errors.New("claude_node_runtime_untrusted")
		}
		body, readErr := os.ReadFile(interpreter)
		after, statErr := os.Lstat(interpreter)
		if readErr != nil || statErr != nil || !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
			return cmd, errors.New("claude_node_runtime_untrusted")
		}
		nodeDir := filepath.Join(home, "bin")
		if err := os.Mkdir(nodeDir, 0700); err != nil {
			return cmd, err
		}
		interpreter = filepath.Join(nodeDir, "node")
		if err := os.WriteFile(interpreter, body, 0700); err != nil {
			return cmd, err
		}
		copied, err := os.ReadFile(interpreter)
		if err != nil || sha256.Sum256(copied) != sha256.Sum256(body) || fmt.Sprintf("%x", sha256.Sum256(copied)) != adapter.ClaudeNodeSHA256 {
			return cmd, errors.New("claude_node_runtime_untrusted")
		}
		path = filepath.Dir(interpreter) + ":" + path
	}
	child := map[string]string{"PATH": path, "HOME": home, "CLAUDE_CONFIG_DIR": config, "CLAUDE_SECURESTORAGE_CONFIG_DIR": config, "LANG": "en_US.UTF-8", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_AUTOUPDATER": "1", "ANTHROPIC_BASE_URL": strings.TrimRight(endpoint.String(), "/")}
	if profile.Model != "" {
		for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL"} {
			child[name] = string(profile.Model)
		}
	} else {
		// An isolated HOME cannot supply the caller's configured CLI defaults.
		// Preserve only model selectors, not headers, credentials or other CLI
		// policy. Explicit profile models retain the original override above.
		for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"} {
			if value, present := source.env[name]; present {
				child[name] = value
			}
		}
	}
	for name, directory := range map[string]string{"XDG_CONFIG_HOME": ".config", "XDG_CACHE_HOME": ".cache", "XDG_DATA_HOME": ".local/share", "XDG_STATE_HOME": ".local/state"} {
		child[name] = filepath.Join(home, directory)
	}
	if key != "" {
		child["ANTHROPIC_API_KEY"] = key
	} else {
		child["ANTHROPIC_AUTH_TOKEN"] = token
	}
	for _, entry := range cmd.Env {
		name, value, _ := strings.Cut(entry, "=")
		if name == "ORCHESTRATOR_REPORT_CAPABILITY" || name == "ORCHESTRATOR_REPORT_EXECUTABLE" {
			child[name] = value
		}
	}
	cmd.Env = nil
	for name, value := range child {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	sort.Strings(cmd.Env)
	cmd.ExactEnv = true
	for i, arg := range cmd.Args {
		if (arg == "--disallowed-tools" || arg == "--disallowedTools") && i+1 < len(cmd.Args) {
			cmd.Args[i+1] = "Bash,Agent,Task,WebFetch,WebSearch,mcp__*"
			if profile.Role == adapter.Reviewer {
				cmd.Args[i+1] += ",Edit,MultiEdit,Write,NotebookEdit"
			}
		}
	}
	if advertised, _ := regexp.MatchString(`(^|[\s,])--tools([\s=,]|$)`, source.help); advertised {
		tools := "Read,Glob,Grep"
		if profile.Role == adapter.Implementer {
			tools += ",Edit,MultiEdit,Write,NotebookEdit"
		}
		// Replace any inherited list: the positive allow-list is authoritative.
		for i := 0; i < len(cmd.Args); i++ {
			if cmd.Args[i] == "--tools" && i+1 < len(cmd.Args) {
				cmd.Args = append(cmd.Args[:i], cmd.Args[i+2:]...)
				i--
			}
		}
		cmd.Args = append(cmd.Args, "--tools", tools)
	}
	if profile.Role == adapter.Implementer {
		advertised, _ := regexp.MatchString(`(^|[\s,])--tools([\s=,]|$)`, source.help)
		if !advertised {
			return cmd, errors.New("claude_gateway_capability_unsupported")
		}
	}
	cmd.Args = append(cmd.Args, "--strict-mcp-config", "--mcp-config", mcp, "--settings", settings)
	prepared, err := sandboxCommand(ctx, cmd, profile, scratch)
	if err != nil {
		return cmd, err
	}
	rules := []string{}
	if profile.Role == adapter.Implementer {
		writable, protected, err := managedWritePaths(ctx, cmd.Dir, source.workspace)
		if err != nil {
			return cmd, err
		}
		rules = append(rules, "(deny file-write* (subpath "+strconv.Quote(cmd.Dir)+"))")
		for _, path := range writable {
			rules = append(rules, "(allow file-write* (literal "+strconv.Quote(path)+"))")
		}
		atomic, err := claudeAtomicWriteRules(writable)
		if err != nil {
			return cmd, err
		}
		rules = append(rules, atomic...)
		for _, path := range protected {
			rules = append(rules, "(deny file-write* (subpath "+strconv.Quote(path)+"))")
		}
	}
	for _, root := range denied {
		rules = append(rules, "(deny file-read* (subpath "+strconv.Quote(root)+"))")
	}
	// Current Claude vets every settings path component. Permit metadata for
	// this launch's scratch ancestors; sibling spool contents remain unreadable.
	for parent := filepath.Dir(scratch); ; parent = filepath.Dir(parent) {
		rules = append(rules, "(allow file-read-metadata (literal "+strconv.Quote(parent)+"))")
		if filepath.Base(parent) == "host-spool" || parent == filepath.Dir(parent) {
			break
		}
	}
	rules = append(rules, "(deny file-write* (subpath "+strconv.Quote(policy)+"))")
	rules = append(rules, "(allow file-read* (subpath "+strconv.Quote(cmd.Dir)+"))", "(allow file-read* (literal "+strconv.Quote(cmd.Path)+"))")
	if interpreter != "" {
		rules = append(rules, "(allow file-read* (literal "+strconv.Quote(interpreter)+"))")
		rules = append(rules, "(deny file-write* (subpath "+strconv.Quote(filepath.Dir(interpreter))+"))")
	}
	for _, root := range homes {
		for _, name := range []string{".claude", ".claude.json", "Library/Keychains"} {
			projectPaths = append(projectPaths, filepath.Join(root, name))
		}
	}
	for _, path := range projectPaths {
		rules = append(rules, "(deny file-read* (subpath "+strconv.Quote(path)+"))")
	}
	for _, path := range []string{"/Library/Application Support/ClaudeCode", "/etc/claude-code", "/Library/Managed Preferences/com.anthropic.claudecode.plist", "/Library/Managed Preferences/com.anthropic.ClaudeCode.plist"} {
		rules = append(rules, "(deny file-read* (subpath "+strconv.Quote(path)+"))")
	}
	for _, name := range []string{"com.apple.SecurityServer", "com.apple.securityd", "com.apple.securityd.system", "com.apple.securityd.systemkeychain"} {
		rules = append(rules, "(deny mach-lookup (global-name "+strconv.Quote(name)+"))")
	}
	prepared.Args[1] += "\n" + strings.Join(rules, "\n")
	for path, body := range map[string]string{mcp: `{"mcpServers":{}}`, settings: `{}`} {
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 {
			return cmd, errors.New("claude_gateway_config_untrusted")
		}
		identity, ok := before.Sys().(*syscall.Stat_t)
		observed, readErr := os.ReadFile(path)
		after, statErr := os.Lstat(path)
		if !ok || identity.Uid != uint32(os.Getuid()) || identity.Nlink != 1 || readErr != nil || statErr != nil || !os.SameFile(before, after) || string(observed) != body {
			return cmd, errors.New("claude_gateway_config_untrusted")
		}
	}
	return prepared, nil
}

func cleanupClaudeGatewayState(scratch string) {
	// RemoveAll does not follow a replacement symlink. Only this launch's fresh
	// child state is removed; caller auth/config paths are never cleanup inputs.
	_ = os.RemoveAll(filepath.Join(scratch, "claude-home"))
}

func checkClaudeManagedSources(ctx context.Context) error {
	for _, path := range []string{"/Library/Application Support/ClaudeCode/managed-settings.json", "/Library/Application Support/ClaudeCode/managed-mcp.json", "/Library/Application Support/ClaudeCode/managed-settings.d", "/etc/claude-code/managed-settings.json", "/etc/claude-code/managed-mcp.json", "/Library/Managed Preferences/com.anthropic.claudecode.plist", "/Library/Managed Preferences/com.anthropic.ClaudeCode.plist"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return errors.New("claude_gateway_managed_config_unverified")
		}
	}
	for _, domain := range []string{"com.anthropic.claudecode", "com.anthropic.ClaudeCode"} {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		command := exec.CommandContext(probeCtx, "/usr/bin/defaults", "read", domain)
		command.Env = []string{"PATH=/usr/bin:/bin", "LANG=en_US.UTF-8"}
		output, err := command.CombinedOutput()
		cancel()
		if err == nil || !strings.Contains(string(output), "does not exist") {
			return errors.New("claude_gateway_managed_config_unverified")
		}
	}
	return nil
}

func firstWorkspace(values []*adapter.CandidateWorkspace) *adapter.CandidateWorkspace {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}
