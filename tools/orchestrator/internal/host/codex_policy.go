package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Native config/read is projected in-process. Server configuration values,
// credentials and account identifiers are never copied into a request or log.
func codexPolicyRestrictions(config map[string]any) ([]string, error) {
	raw, exists := config["mcp_servers"]
	if !exists || raw == nil {
		return nil, nil
	}
	servers, ok := raw.(map[string]any)
	if !ok || len(servers) > 128 {
		return nil, errors.New("codex_mcp_policy_unverified")
	}
	names := make([]string, 0, len(servers))
	for name, raw := range servers {
		if name == "" || len(name) > 256 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return nil, errors.New("codex_mcp_policy_unverified")
		}
		if _, ok := raw.(map[string]any); !ok {
			return nil, errors.New("codex_mcp_policy_unverified")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var args []string
	for _, name := range names {
		args = append(args, "-c", "mcp_servers."+strconv.Quote(name)+".enabled=false")
	}
	return args, nil
}

func codexAuthProjection(config, account map[string]any) (map[string]any, error) {
	// account/read does not establish bootstrap auth or environment/external
	// credential provenance. Never manufacture those fields from account.type.
	return nil, errors.New("codex_auth_metadata_unverified")
}

func codexChildEnvironment(overrides []string) []string {
	values := map[string]string{}
	for _, entry := range append(os.Environ(), overrides...) {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func prepareCodexCommand(ctx context.Context, cmd process.Command, profile *adapter.ExecutionProfile, scratch string, review bool) (prepared process.Command, prepareErr error) {
	if adapter.CodexWorkerAdmissionReason != "" {
		return cmd, errors.New(adapter.CodexWorkerAdmissionReason)
	}
	if profile == nil {
		return cmd, errors.New("codex_typed_profile_required")
	}
	if err := adapter.VerifyExecutable(adapter.BinaryPin{Path: cmd.PinnedPath, SHA256: cmd.PinnedSHA256, Version: adapter.CodexVersion}, adapter.CodexVersion); err != nil {
		return cmd, err
	}
	if cmd.PinnedSHA256 != adapter.CodexSHA256 || cmd.Path != cmd.PinnedPath {
		return cmd, errors.New("codex_binary_pin_mismatch")
	}
	for _, entry := range codexChildEnvironment(cmd.Env) {
		if strings.HasPrefix(entry, "CODEX_REFRESH_TOKEN_URL_OVERRIDE=") {
			return cmd, errors.New("refresh_override_already_present")
		}
	}
	if err := grokOwnedDirectory(scratch, true, true); err != nil {
		return cmd, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	flags := []string{"--disable", "multi_agent", "--disable", "hooks", "--disable", "plugins", "--disable", "apps", "--disable", "shell_snapshot", "-c", "agents.enabled=false"}
	for _, slot := range []struct{ key, name string }{{"sqlite_home", "codex-sqlite"}, {"log_dir", "codex-log"}} {
		path := filepath.Join(scratch, slot.name)
		if err := grokOwnedDirectory(path, true, true); err != nil {
			return cmd, err
		}
		flags = append(flags, "-c", slot.key+"="+strconv.Quote(path))
	}
	metadata := process.Command{Path: cmd.Path, Args: append(append([]string{}, flags...), "app-server", "--listen", "stdio://"), Dir: cmd.Dir, Env: append(append([]string{}, cmd.Env...), "CODEX_REFRESH_TOKEN_URL_OVERRIDE=invalid://refresh-blocked")}
	restricted, err := sandboxCommand(probeCtx, metadata, &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 15000}, scratch)
	if err != nil {
		return cmd, err
	}
	native := exec.Command(restricted.Path, restricted.Args...)
	native.Dir = cmd.Dir
	native.Env = codexChildEnvironment(restricted.Env)
	rpc, closeRPC, err := codexrpc.StartCommand(probeCtx, native)
	if err != nil {
		return cmd, err
	}
	closed := false
	defer func() {
		if !closed {
			if stopErr := closeRPC(); stopErr != nil {
				prepareErr = errors.Join(prepareErr, process.ErrProcessTreeUnknown, stopErr)
			}
		}
	}()
	result, err := rpc.Call(probeCtx, "config/read", map[string]any{"includeLayers": false, "cwd": cmd.Dir})
	if err != nil {
		return cmd, err
	}
	config, ok := result["config"].(map[string]any)
	if !ok {
		return cmd, errors.New("codex_policy_unverified")
	}
	restrictions, err := codexPolicyRestrictions(config)
	if err != nil {
		return cmd, err
	}
	// Never lift a stricter source sandbox in an implementation child.
	if profile.Permission == adapter.WorkspaceWrite && config["sandbox_mode"] != "workspace-write" && config["sandbox_mode"] != "danger-full-access" {
		return cmd, errors.New("codex_source_policy_read_only")
	}
	account, err := rpc.Call(probeCtx, "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		return cmd, err
	}
	projection, err := codexAuthProjection(config, account)
	if err != nil {
		return cmd, err
	}
	if err := closeRPC(); err != nil {
		return cmd, errors.Join(process.ErrProcessTreeUnknown, err)
	}
	closed = true
	encoded, err := json.Marshal(projection)
	if err != nil {
		return cmd, err
	}
	projection["metadata_sha256"] = hashBytes(encoded)
	evidence, _ := json.Marshal(projection)
	f, err := os.OpenFile(filepath.Join(scratch, "codex-policy.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return cmd, err
	}
	_, writeErr := f.Write(evidence)
	if err = errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return cmd, err
	}
	cmd.Args = append(append(append([]string{}, cmd.Args...), flags...), restrictions...)
	cmd.Env = append(append([]string{}, cmd.Env...), "CODEX_REFRESH_TOKEN_URL_OVERRIDE=invalid://refresh-blocked")
	if review {
		schema := filepath.Join(scratch, "review-schema.json")
		f, err := os.OpenFile(schema, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return cmd, err
		}
		_, writeErr := f.WriteString(adapter.CandidateReviewSchema())
		if err = errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
			return cmd, err
		}
		resultFile := filepath.Join(scratch, "review-result.json")
		f, err = os.OpenFile(resultFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return cmd, err
		}
		if err = f.Close(); err != nil {
			return cmd, err
		}
		cmd.Args = append(cmd.Args, "--output-schema", schema, "--output-last-message", resultFile)
	}
	return sandboxCommand(ctx, cmd, profile, scratch)
}
