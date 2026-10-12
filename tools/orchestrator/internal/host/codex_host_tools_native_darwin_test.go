//go:build darwin

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

const (
	codexHostToolsNativeOptIn = "root-reviewed-codex-dynamic-tools-handshake-v1"
	codexHostToolsNativePin   = "/Users/guanjunhui/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex"
)

type codexHostToolsNativeEvidence struct {
	Version              int    `json:"version"`
	ProviderPath         string `json:"provider_path"`
	ProviderSHA256       string `json:"provider_sha256"`
	ProviderBeforeSHA256 string `json:"provider_before_sha256"`
	ProviderAfterSHA256  string `json:"provider_after_sha256"`
	SourceConfigSHA256   string `json:"source_config_sha256"`
	SourceAuthSHA256     string `json:"source_auth_sha256"`
	WorkspaceSHA256      string `json:"workspace_sha256"`
	ControlSHA256        string `json:"control_sha256"`
	OriginalStarted      bool   `json:"original_started"`
	OriginalWait         bool   `json:"original_wait"`
	TreeExited           bool   `json:"tree_exited"`
	PipeJoined           bool   `json:"pipe_joined"`
	StopInvoked          bool   `json:"stop_invoked"`
	ThreadCallbacks      int    `json:"thread_callbacks"`
	ToolCallbacks        int    `json:"tool_callbacks"`
	RunError             string `json:"run_error"`
	FailureStage         string `json:"failure_stage"`
	FailureBranch        string `json:"failure_branch"`
}

// TestManagedCodexHostToolsNativeHandshake is intentionally opt-in. It proves
// only the fixed 0.154.0 app-server accepts thread/start.dynamicTools through
// the existing managed outer sandbox. OnThread rejects before turn/start, so
// no model turn, tool call, workspace operation, account operation, resume, or
// TUI behavior is exercised.
func TestManagedCodexHostToolsNativeHandshake(t *testing.T) {
	if os.Getenv("C06_CODEX_HOST_TOOLS_NATIVE_OPT_IN") != codexHostToolsNativeOptIn || os.Getenv("C06_CODEX_HOST_TOOLS_NATIVE_EVIDENCE_DIR") == "" {
		t.Skip("root reviewed opt-in native Codex dynamic-tools handshake")
	}
	root, err := codexAnchorStateRoot(context.Background(), os.Getenv("C06_CODEX_HOST_TOOLS_NATIVE_EVIDENCE_DIR"))
	if err != nil || len(root.Parents) == 0 || root.Parents[len(root.Parents)-1].Mode != 0700 {
		t.Fatal("canonical 0700 native evidence root required")
	}
	directory := filepath.Join(root.Path, "codex-host-tools-native")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal("fresh native evidence directory required", err)
	}
	evidence := codexHostToolsNativeEvidence{}
	defer func() {
		if body, err := json.MarshalIndent(evidence, "", "  "); err == nil {
			if os.WriteFile(filepath.Join(directory, "result.json"), append(body, '\n'), 0600) != nil {
				t.Error("write bounded native evidence")
			}
		}
	}()

	pin, err := filepath.EvalSymlinks(codexHostToolsNativePin)
	if err != nil || !codexCanonicalPath(pin) {
		t.Fatal("canonical fixed provider pin required", err)
	}
	parent, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	before, err := process.ExecutableDigest(parent, pin)
	if err != nil || before != adapter.CodexSHA256 {
		t.Fatal("fixed provider hash mismatch", err)
	}
	evidence.Version, evidence.ProviderPath, evidence.ProviderSHA256, evidence.ProviderBeforeSHA256 = 1, pin, before, before

	source, workspace, spool := filepath.Join(directory, "source"), filepath.Join(directory, "workspace"), filepath.Join(directory, "spool")
	for _, path := range []string{source, workspace, spool} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal("create native fixture directory", err)
		}
	}
	config := []byte("model = \"gpt-5.4\"\nmodel_reasoning_effort = \"high\"\n")
	auth := []byte("{}\n")
	if err := os.WriteFile(filepath.Join(source, "config.toml"), config, 0600); err != nil {
		t.Fatal("write synthetic config", err)
	}
	if err := os.WriteFile(filepath.Join(source, "auth.json"), auth, 0600); err != nil {
		t.Fatal("write synthetic auth", err)
	}
	evidence.SourceConfigSHA256 = nativeSHA256(config)
	evidence.SourceAuthSHA256 = nativeSHA256(auth)
	evidence.WorkspaceSHA256 = nativeSHA256([]byte("empty-workspace-v1\n"))

	h, err := NewIPC(spool, "native-host-tools")
	if err != nil || h.BindContinuationOwner("native-owner") != nil {
		t.Fatal("create native Host")
	}
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 30000}
	binary := adapter.BinaryPin{Path: pin, Version: adapter.CodexVersion, SHA256: adapter.CodexSHA256}
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderCodex, Protocol: adapter.ProtocolID(adapter.ProviderCodex), Binary: binary}
	inv, err := adapter.BuildManagedCodexInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: binary, Lock: &lock, Profile: &profile, CWD: workspace, Prompt: "native handshake only"})
	if err != nil {
		t.Fatal("build managed invocation", err)
	}
	payload, err := json.Marshal(adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderCodex), Directory: workspace, Prompt: "native handshake only", Profile: &profile, ProviderLock: &lock})
	if err != nil {
		t.Fatal("marshal managed invocation")
	}
	g := withHostArtifactsForTest(t, contract.LaunchCommand{RunID: "native-run", TaskID: "native-task", AttemptID: "native-attempt", SegmentID: "initial", WorkRevision: 1, PlanRevision: 1, CommandID: "native-command", HostID: "native-host-tools", ExecutionEpoch: 1, AdapterPayload: payload})
	if _, err := h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}); err != nil {
		t.Fatal("prepare original spool")
	}
	env := []string{"HOME=" + directory, "CODEX_HOME=" + source}
	prepared, err := h.prepareManagedCodexState(parent, g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal("prepare managed state", err)
	}
	cmd, prepared, err := codexManagedCommand(parent, prepared, env, spool)
	if err != nil {
		t.Fatal("build managed command", err)
	}
	evidence.ControlSHA256 = nativeSHA256([]byte(prepared.Projection.ConfigTOML()))
	if cmd.PinnedPath != pin || cmd.PinnedSHA256 != adapter.CodexSHA256 || !cmd.ExactEnv {
		t.Fatal("managed command changed provider pin or environment boundary")
	}
	work, workCancel := context.WithTimeout(parent, 25*time.Second)
	defer workCancel()
	handle, stream, startErr := process.StartDuplex(work, cmd)
	evidence.OriginalStarted = handle != nil
	if startErr != nil || handle == nil || stream == nil {
		t.Fatal("original StartDuplex did not retain native handle and pipe", startErr)
	}
	naturalExit := false
	defer func() {
		_ = stream.Close()
		if naturalExit {
			return
		}
		// This is bounded failure cleanup only. It never establishes a natural
		// exit receipt for the positive handshake assertion below.
		evidence.StopInvoked = true
		_ = handle.Stop(parent)
		_ = handle.Wait(parent)
		_ = handle.ConfirmTreeExited()
	}()
	client, err := codexrpc.New(work, stream, codexrpc.Limits{})
	if err != nil {
		t.Fatal("create original RPC client", err)
	}
	threadCallbacks, toolCallbacks := 0, 0
	out, runErr := codexsession.Run(work, client, prepared.Projection, codexsession.Options{
		CWD: workspace, OwnedHome: prepared.Layout.Home.Path, PlatformFamily: "unix", PlatformOS: "macos", Prompt: "native handshake only",
		HostTools: &codexsession.HostTools{OnTool: func(context.Context, codexsession.ToolCall) (codexsession.ToolResult, error) {
			toolCallbacks++
			return codexsession.ToolResult{}, errors.New("unexpected host tool call")
		}},
		OnThread: func(context.Context, codexsession.ThreadBinding) error {
			threadCallbacks++
			return errors.New("intentional native handshake cutoff")
		},
	})
	evidence.ThreadCallbacks, evidence.ToolCallbacks = threadCallbacks, toolCallbacks
	facts, _ := codexsession.FailureFactsOf(runErr)
	evidence.RunError, evidence.FailureStage, evidence.FailureBranch = nativeNativeError(runErr), facts.Phase, facts.Branch
	if !errors.Is(runErr, codexsession.ErrCallback) || threadCallbacks != 1 || toolCallbacks != 0 || out.TurnID != "" {
		t.Fatal("native handshake did not stop at bound thread callback", runErr)
	}
	evidence.PipeJoined = true // Run owns and joins the original codexrpc transport before return.
	evidence.OriginalWait = handle.Wait(work) == nil
	evidence.TreeExited = handle.ConfirmTreeExited() == nil
	if !evidence.OriginalWait || !evidence.TreeExited {
		t.Fatal("native original Wait/tree receipt missing")
	}
	naturalExit = true
	after, err := process.ExecutableDigest(parent, pin)
	if err != nil || after != adapter.CodexSHA256 || after != before {
		t.Fatal("provider pin changed during native handshake", err)
	}
	evidence.ProviderAfterSHA256 = after
}

func nativeSHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func nativeNativeError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, codexsession.ErrCallback) {
		return "binding_callback"
	}
	return "other"
}
