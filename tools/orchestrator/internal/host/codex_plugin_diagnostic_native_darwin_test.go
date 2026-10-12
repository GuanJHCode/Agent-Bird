//go:build darwin

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexrpc"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/codexsession"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Explicit diagnostic model turn using the production fixed pin, state
// projection and sandbox. Synthetic owner, not plugin acceptance or TUI proof.
func TestCodexPluginNativeFailureDiagnostic(t *testing.T) {
	evidence := os.Getenv("BIRD_CODEX_MODEL_DIAGNOSTIC_DIR")
	if evidence == "" {
		t.Skip("explicit native model diagnostic only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := os.Mkdir(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	workspace, spool := filepath.Join(evidence, "workspace"), filepath.Join(evidence, "spool")
	for _, p := range []string{workspace, spool} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("BIRD_DIAGNOSTIC\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(spool, "diagnostic-host")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.BindContinuationOwner("diagnostic-owner"); err != nil {
		t.Fatal(err)
	}
	profile := adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, CLIModelDefault: true, TimeoutMS: 30000, CodexHostToolsVersion: 1}
	binary := adapter.BinaryPin{Path: codexHostToolsNativePin, Version: adapter.CodexVersion, SHA256: adapter.CodexSHA256}
	lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderCodex, Protocol: adapter.ProtocolID(adapter.ProviderCodex), Binary: binary}
	prompt := "Read README.md using agent_bird.read_file, return the marker. No other tools."
	inv, err := adapter.BuildCodexPluginInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: binary, Lock: &lock, Profile: &profile, CWD: workspace, Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderCodex), Directory: workspace, Prompt: prompt, Profile: &profile, ProviderLock: &lock})
	g := withHostArtifactsForTest(t, contract.LaunchCommand{RunID: "diagnostic-run", TaskID: "diagnostic-task", AttemptID: "diagnostic-attempt", SegmentID: "initial", WorkRevision: 1, PlanRevision: 1, CommandID: "diagnostic-command", HostID: "diagnostic-host", ExecutionEpoch: 1, AdapterPayload: payload})
	if _, err = h.spool(store.Attempt{ID: g.AttemptID, SegmentID: g.SegmentID}); err != nil {
		t.Fatal(err)
	}
	prepared, err := h.prepareManagedCodexState(ctx, g, inv, os.Environ(), codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	cmd, prepared, err := codexManagedCommand(ctx, prepared, os.Environ(), spool)
	if err != nil {
		t.Fatal(err)
	}
	var traceMu sync.Mutex
	var trace bytes.Buffer
	cmd.ObserveOutput = func(stream process.OutputStream, chunk []byte) {
		if stream != process.OutputStdout {
			return
		}
		traceMu.Lock()
		defer traceMu.Unlock()
		if trace.Len()+len(chunk) <= 1<<20 {
			trace.Write(chunk)
		}
	}
	defer func() {
		traceMu.Lock()
		defer traceMu.Unlock()
		for _, line := range bytes.Split(trace.Bytes(), []byte("\n")) {
			var v map[string]json.RawMessage
			if json.Unmarshal(line, &v) != nil {
				continue
			}
			var method string
			_ = json.Unmarshal(v["method"], &method)
			if method == "error" {
				_ = os.WriteFile(filepath.Join(evidence, "native-error.json"), v["params"], 0600)
			}
		}
	}()
	handle, stream, err := process.StartDuplex(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	defer func() {
		_ = handle.Stop(ctx)
		_ = handle.Wait(ctx)
		_ = handle.ConfirmTreeExited()
		_ = os.WriteFile(filepath.Join(evidence, "stderr.txt"), []byte(handle.Output()), 0600)
	}()
	client, err := codexrpc.New(ctx, stream, codexrpc.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	tools, closeTools, err := openCodexReadTools(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTools()
	out, runErr := codexsession.Run(ctx, client, prepared.Projection, codexsession.Options{CWD: workspace, OwnedHome: prepared.Layout.Home.Path, PlatformFamily: "unix", PlatformOS: "macos", Prompt: prompt, HostTools: tools, OnThread: func(context.Context, codexsession.ThreadBinding) error { return nil }})
	facts, _ := codexsession.FailureFactsOf(runErr)
	body, _ := json.Marshal(map[string]any{"status": out.Status, "failure_facts": facts, "diagnostic_only": true})
	if err = os.WriteFile(filepath.Join(evidence, "result.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Log("native model diagnostic returned a failure; private stderr retained")
	}
}
