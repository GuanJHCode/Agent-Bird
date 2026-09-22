package host

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestNativeCodexImmutableSnapshotMetadataOnly(t *testing.T) {
	if os.Getenv("AGENT_BIRD_NATIVE_CODEX_SNAPSHOT") != "1" {
		t.Skip("opt-in metadata-only snapshot acceptance; no model")
	}
	base, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	source := os.Getenv("CODEX_HOME")
	if source == "" {
		source = filepath.Join(base, ".codex")
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	env := codexChildEnvironment(nil)
	if err := codexAuthEnvironment(env); err != nil {
		t.Fatal(err)
	}
	input, err := codexInputIdentity(filepath.Join(source, "config.toml"), false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(input.path)
	if err != nil {
		t.Fatal(err)
	}
	if hashBytes(raw) != input.digest {
		t.Fatal("config changed")
	}
	snapshot, err := compileCodexSnapshot(raw, source, base)
	if err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp("/private/tmp", "bird-codex-snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("private metadata evidence: %s", work)
	home, err := createCodexHome(source, work, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(home.path, "auth.json"))
	binary := os.Getenv("AGENT_BIRD_NATIVE_CODEX_BINARY")
	if binary == "" {
		binary, err = exec.LookPath("codex")
	}
	if err != nil {
		t.Fatal(err)
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.VerifyExecutable(adapter.BinaryPin{Path: binary, SHA256: adapter.CodexSHA256, Version: adapter.CodexVersion}, adapter.CodexVersion); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--strict-config", "--disable", "multi_agent", "--disable", "multi_agent_v2", "--disable", "hooks", "--disable", "plugins", "--disable", "apps", "--disable", "shell_snapshot", "--disable", "memories", "-c", "agents.enabled=false", "-c", "notify=[]", "-c", "sqlite_home=" + strconv.Quote(filepath.Join(home.runtime, "sqlite")), "-c", "log_dir=" + strconv.Quote(filepath.Join(home.runtime, "log")), "app-server", "--listen", "stdio://"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd, err := codexSandboxCommand(ctx, process.Command{Path: binary, Args: flags, Dir: work, Env: []string{"CODEX_HOME=" + home.path, "CODEX_REFRESH_TOKEN_URL_OVERRIDE=invalid://refresh-blocked"}}, &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 20000}, work, home)
	if err != nil {
		t.Fatal(err)
	}
	native := exec.Command(cmd.Path, cmd.Args...)
	native.Dir = work
	native.Env = codexChildEnvironment(cmd.Env)
	var diagnostic bytes.Buffer
	native.Stderr = &diagnostic
	rpc, stop, err := codexrpc.StartCommand(ctx, native)
	if err != nil {
		t.Fatalf("%v; unknown config field=%v; permission denied=%v", err, strings.Contains(diagnostic.String(), "unknown field"), strings.Contains(diagnostic.String(), "not permitted"))
	}
	defer stop()
	requirements, err := rpc.Call(ctx, "configRequirements/read", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if value, exists := requirements["requirements"]; !exists || value != nil {
		t.Fatal("nonempty or unavailable managed requirements")
	}
	result, err := rpc.Call(ctx, "config/read", map[string]any{"includeLayers": true, "cwd": work})
	if err != nil {
		t.Fatal(err)
	}
	cfg, ok := result["config"].(map[string]any)
	if !ok {
		t.Fatal("configuration absent")
	}
	features, ok := cfg["features"].(map[string]any)
	if !ok || features["multi_agent"] != false || features["multi_agent_v2"] != false {
		t.Fatal("native recursive agent tools remain enabled")
	}
	t.Logf("snapshot loaded; auth store file=%v; no model called", cfg["cli_auth_credentials_store"] == "file")
	account, err := rpc.Call(ctx, "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		t.Fatal(err)
	}
	observed := codexBootstrap{environmentVerified: true}
	for _, entry := range []struct {
		home   string
		result *string
	}{{source, &observed.sourceLogin}, {home.path, &observed.overlayLogin}} {
		login, err := codexSandboxCommand(ctx, process.Command{PinnedPath: binary, PinnedSHA256: adapter.CodexSHA256, Path: binary, Args: []string{"login", "status"}, Dir: work, Env: []string{"CODEX_HOME=" + entry.home, "CODEX_REFRESH_TOKEN_URL_OVERRIDE=invalid://refresh-blocked"}}, &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 20000}, work, home)
		if err != nil {
			t.Fatal(err)
		}
		*entry.result, err = readCodexLogin(ctx, login)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := codexAuthProjection(cfg, account, observed); err != nil {
		t.Fatal(err)
	}
	t.Log("source/overlay native login and file-store account proof matched")
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if err := home.verifyInputs(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCodexVerifiedRuntimePreparationOnly(t *testing.T) {
	if os.Getenv("AGENT_BIRD_NATIVE_CODEX_SNAPSHOT") != "1" {
		t.Skip("opt-in full runtime preparation; no model")
	}
	binary := os.Getenv("AGENT_BIRD_NATIVE_CODEX_BINARY")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("codex")
		if err != nil {
			t.Fatal(err)
		}
	}
	binary, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp("/private/tmp", "bird-codex-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("private runtime evidence: %s", work)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	state := &codexRuntimeState{}
	defer state.close()
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 40000}
	command, err := prepareCodexRuntime(ctx, process.Command{Path: binary, PinnedPath: binary, PinnedSHA256: adapter.CodexSHA256, Dir: work, Args: []string{"exec", "--json", "--ephemeral", "--sandbox", "read-only", "-"}}, profile, filepath.Join(work, "scratch"), true, state)
	if err != nil {
		t.Fatal(err)
	}
	if command.Path != "/usr/bin/sandbox-exec" {
		t.Fatal("worker sandbox missing")
	}
	if err := state.verify(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state.home.path, "skills/.system/.codex-system-skills.marker")); err != nil {
		t.Fatal("native system skills were not initialized")
	}
	helpArgs := append(append([]string{}, command.Args...), "--help")
	help := exec.CommandContext(ctx, command.Path, helpArgs...)
	help.Dir = command.Dir
	help.Env = codexChildEnvironment(command.Env)
	output, err := help.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("Usage:")) {
		t.Fatal("assembled worker arguments rejected by native exec help")
	}
	if err := state.verify(); err != nil {
		t.Fatal(err)
	}
	t.Log("full native preparation and exec argument parsing passed; model was not launched")
}
