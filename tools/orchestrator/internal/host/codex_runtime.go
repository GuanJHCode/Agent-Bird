package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/execbridge"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

var errCodexAuthCleanup = errors.New("codex_auth_cleanup_failed")

type codexRuntimeState struct {
	home   *codexHome
	config map[string]any
	bridge *execbridge.Broker
	turn   *codexrpc.Turn
	input  *os.File
	mu     sync.Mutex
	bound  bool
}

func (s *codexRuntimeState) verify() error {
	if s == nil || s.home == nil {
		return errors.New("codex_runtime_unprepared")
	}
	s.mu.Lock()
	bound := s.bound
	s.mu.Unlock()
	if bound && s.bridge != nil {
		if err := s.bridge.Finish(); err != nil {
			return err
		}
		if err := s.bridge.Err(); err != nil {
			return err
		}
		if err := s.turn.Err(); err != nil {
			return err
		}
	}
	return s.home.verifyInputs()
}
func (s *codexRuntimeState) close() error {
	if s == nil {
		return nil
	}
	var result error
	if s.bridge != nil {
		result = s.bridge.Close()
	}
	if s.turn != nil {
		_ = s.turn.Close()
	}
	if s.input != nil {
		_ = s.input.Close()
	}
	if s.home != nil {
		result = errors.Join(result, s.home.detachAuth())
	}
	return result
}
func (s *codexRuntimeState) bind(identity process.Identity) error {
	if s.input != nil {
		_ = s.input.Close()
	}
	if err := s.bridge.Bind(identity); err != nil {
		return err
	}
	s.mu.Lock()
	s.bound = true
	s.mu.Unlock()
	return nil
}

func (h *codexHome) detachAuth() error {
	path := filepath.Join(h.path, "auth.json")
	target, err := os.Readlink(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || target != filepath.Join(h.source, "auth.json") {
		return errCodexAuthCleanup
	}
	if err := os.Remove(path); err != nil {
		return errCodexAuthCleanup
	}
	return nil
}

func codexRuntimeFlags(home *codexHome) []string {
	return []string{"--strict-config", "--disable", "multi_agent", "--disable", "multi_agent_v2", "--disable", "hooks", "--disable", "plugins", "--disable", "apps", "--disable", "shell_snapshot", "--disable", "memories", "-c", "agents.enabled=false", "-c", "notify=[]", "-c", "sqlite_home=" + strconv.Quote(filepath.Join(home.runtime, "sqlite")), "-c", "log_dir=" + strconv.Quote(filepath.Join(home.runtime, "log"))}
}

// Metadata-only preparation. No model/thread method is sent here. Source files
// are read only; only the task home contains a normalized configuration snapshot.
func prepareCodexRuntime(ctx context.Context, cmd process.Command, profile *adapter.ExecutionProfile, scratch string, review bool, state *codexRuntimeState) (prepared process.Command, prepareErr error) {
	if state == nil || state.home != nil {
		return cmd, errors.New("codex_runtime_state_required")
	}
	if profile == nil {
		return cmd, errors.New("codex_typed_profile_required")
	}
	if cmd.Path != cmd.PinnedPath || cmd.PinnedSHA256 != adapter.CodexSHA256 {
		return cmd, errors.New("codex_binary_pin_mismatch")
	}
	if err := adapter.VerifyExecutable(adapter.BinaryPin{Path: cmd.Path, SHA256: cmd.PinnedSHA256, Version: adapter.CodexVersion}, adapter.CodexVersion); err != nil {
		return cmd, err
	}
	environment := codexChildEnvironment(cmd.Env)
	if err := codexAuthEnvironment(environment); err != nil {
		return cmd, err
	}
	source, userHome := "", ""
	for _, entry := range environment {
		if key, value, ok := strings.Cut(entry, "="); ok {
			switch key {
			case "CODEX_HOME":
				source = value
			case "HOME":
				userHome = value
			}
		}
	}
	if !filepath.IsAbs(userHome) {
		return cmd, errors.New("codex_source_home_unverified")
	}
	if source == "" {
		source = filepath.Join(userHome, ".codex")
	}
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return cmd, errors.New("codex_source_home_unverified")
	}
	input, err := codexInputIdentity(filepath.Join(source, "config.toml"), false)
	if err != nil {
		return cmd, err
	}
	raw, err := os.ReadFile(input.path)
	if err != nil || hashBytes(raw) != input.digest {
		return cmd, errors.New("codex_source_home_changed")
	}
	snapshot, err := compileCodexSnapshot(raw, source, userHome)
	if err != nil {
		return cmd, err
	}
	if err := grokOwnedDirectory(scratch, true, true); err != nil {
		return cmd, err
	}
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return cmd, err
	}
	home, err := createCodexHome(source, scratch, snapshot)
	if err != nil {
		return cmd, err
	}
	state.home = home
	defer func() {
		if prepareErr != nil {
			prepareErr = errors.Join(prepareErr, state.close())
		}
	}()
	if len(home.inputs) == 0 || home.inputs[0] != input {
		return cmd, errors.New("codex_source_home_changed")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := os.MkdirAll(filepath.Join(home.path, "skills"), 0700); err != nil {
		return cmd, err
	}
	home.provisionSystem = true
	flags := codexRuntimeFlags(home)
	childEnv := append(append([]string{}, cmd.Env...), "CODEX_HOME="+home.path, "CODEX_REFRESH_TOKEN_URL_OVERRIDE=invalid://refresh-blocked")
	metadata := process.Command{PinnedPath: cmd.PinnedPath, PinnedSHA256: cmd.PinnedSHA256, Path: cmd.Path, Args: append(append([]string{}, flags...), "app-server", "--listen", "stdio://"), Dir: cmd.Dir, Env: childEnv}
	readonly := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 30000}
	restricted, err := codexSandboxCommand(probeCtx, metadata, readonly, scratch, home)
	if err != nil {
		return cmd, err
	}
	rpc, stop, err := codexrpc.StartPinnedCommand(probeCtx, restricted)
	if err != nil {
		return cmd, err
	}
	closed := false
	defer func() {
		if !closed {
			if err := stop(); err != nil {
				prepareErr = errors.Join(prepareErr, process.ErrProcessTreeUnknown, err)
			}
		}
	}()
	if rpc.UserConfig() != filepath.Join(home.path, "config.toml") {
		return cmd, errors.New("codex_runtime_home_mismatch")
	}
	requirements, err := rpc.Call(probeCtx, "configRequirements/read", map[string]any{})
	if err != nil {
		return cmd, err
	}
	if value, exists := requirements["requirements"]; !exists || value != nil {
		return cmd, errors.New("codex_managed_requirements_unverified")
	}
	result, err := rpc.Call(probeCtx, "config/read", map[string]any{"includeLayers": true, "cwd": cmd.Dir})
	if err != nil {
		return cmd, err
	}
	cfg, ok := result["config"].(map[string]any)
	if !ok {
		return cmd, errors.New("codex_policy_unverified")
	}
	if err := verifyCodexConfigLayers(result, home.runtime); err != nil {
		return cmd, err
	}
	if profile.Permission == adapter.WorkspaceWrite && cfg["sandbox_mode"] != "workspace-write" && cfg["sandbox_mode"] != "danger-full-access" {
		return cmd, errors.New("codex_source_policy_read_only")
	}
	state.config = cfg
	restrictions, err := codexPolicyRestrictions(cfg)
	if err != nil {
		return cmd, err
	}
	account, err := rpc.Call(probeCtx, "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		return cmd, err
	}
	observed := codexBootstrap{environmentVerified: true}
	for _, entry := range []struct {
		path   string
		result *string
	}{{source, &observed.sourceLogin}, {home.path, &observed.overlayLogin}} {
		login := process.Command{Path: cmd.Path, PinnedPath: cmd.PinnedPath, PinnedSHA256: cmd.PinnedSHA256, Args: []string{"login", "status"}, Dir: cmd.Dir, Env: append(append([]string{}, cmd.Env...), "CODEX_HOME="+entry.path, "CODEX_REFRESH_TOKEN_URL_OVERRIDE=invalid://refresh-blocked")}
		login, err = codexSandboxCommand(probeCtx, login, readonly, scratch, home)
		if err != nil {
			return cmd, err
		}
		*entry.result, err = readCodexLogin(probeCtx, login)
		if err != nil {
			return cmd, err
		}
	}
	skills, err := rpc.Call(probeCtx, "skills/list", map[string]any{"cwds": []string{cmd.Dir}, "forceReload": true})
	if err != nil {
		return cmd, err
	}
	skillsProof, err := codexSystemSkillsProjection(skills, home.path, cmd.Dir)
	if err != nil {
		return cmd, err
	}
	proof, err := codexAuthProjection(cfg, account, observed)
	if err != nil {
		return cmd, err
	}
	if err := stop(); err != nil {
		return cmd, errors.Join(process.ErrProcessTreeUnknown, err)
	}
	closed = true
	home.provisionSystem = false
	if err := home.sealSystemSkills(); err != nil {
		return cmd, err
	}
	readonlyMetadata, err := codexSandboxCommand(probeCtx, metadata, readonly, scratch, home)
	if err != nil {
		return cmd, err
	}
	sealedRPC, sealedStop, err := codexrpc.StartPinnedCommand(probeCtx, readonlyMetadata)
	if err != nil {
		return cmd, err
	}
	sealedSkills, readErr := sealedRPC.Call(probeCtx, "skills/list", map[string]any{"cwds": []string{cmd.Dir}, "forceReload": true})
	stopErr := sealedStop()
	if stopErr != nil {
		return cmd, errors.Join(process.ErrProcessTreeUnknown, readErr, stopErr)
	}
	if readErr != nil {
		return cmd, readErr
	}
	sealedProof, err := codexSystemSkillsProjection(sealedSkills, home.path, cmd.Dir)
	if err != nil || sealedProof != skillsProof {
		return cmd, errors.New("codex_system_skills_changed")
	}

	proof["metadata_sha256"] = hashBytes(mustCodexJSON(proof))
	if err := writeCodexPrivate(filepath.Join(home.path, "policy.json"), mustCodexJSON(proof)); err != nil {
		return cmd, err
	}
	if err := home.recordSnapshot(filepath.Join(home.path, "policy.json"), mustCodexJSON(proof)); err != nil {
		return cmd, err
	}
	cmd.Args = append(append(append([]string{}, flags...), restrictions...), cmd.Args...)
	cmd.Env = childEnv
	if review {
		schema := filepath.Join(home.path, "review-schema.json")
		if err := writeCodexPrivate(schema, []byte(adapter.CandidateReviewSchema())); err != nil {
			return cmd, err
		}
		if err := home.recordSnapshot(schema, []byte(adapter.CandidateReviewSchema())); err != nil {
			return cmd, err
		}
		result := filepath.Join(scratch, "review-result.json")
		if err := writeCodexPrivate(result, nil); err != nil {
			return cmd, err
		}
		cmd.Args = append(cmd.Args, "--output-schema", schema, "--output-last-message", result)
	}
	if err := home.verifyInputs(); err != nil {
		return cmd, err
	}
	return codexSandboxCommand(ctx, cmd, profile, scratch, home)
}

func writeCodexPrivate(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Sync(), file.Close())
}
func mustCodexJSON(value map[string]any) []byte { data, _ := json.Marshal(value); return data }
