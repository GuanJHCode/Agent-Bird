package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

const nativeReviewSchema = `{"type":"object","properties":{"decision":{"type":"string","enum":["approve","reject"]},"summary":{"type":"string","minLength":1,"maxLength":65536}},"required":["decision","summary"],"additionalProperties":false}`

// TestNativeSchemaContract is deliberately opt-in. It makes exactly one
// provider request, under the production Host sandbox and pinned executable,
// and saves bounded raw stdout for a human to establish the native envelope.
// It neither selects credentials nor changes provider configuration.
func TestNativeSchemaContract(t *testing.T) {
	provider := adapter.Provider(os.Getenv("AGENT_BIRD_NATIVE_SCHEMA_PROVIDER"))
	if provider != adapter.ProviderAGY && provider != adapter.ProviderGrok {
		t.Skip("set AGENT_BIRD_NATIVE_SCHEMA_PROVIDER to antigravity-cli or grok-build")
	}
	evidence := os.Getenv("AGENT_BIRD_NATIVE_SCHEMA_EVIDENCE_DIR")
	if evidence == "" || !filepath.IsAbs(evidence) {
		t.Fatal("AGENT_BIRD_NATIVE_SCHEMA_EVIDENCE_DIR must be an absolute private directory")
	}
	evidence, err := filepath.EvalSymlinks(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(evidence); err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("evidence directory is not private: %v %v", info, err)
	}

	pinFile := os.Getenv("AGENT_BIRD_NATIVE_SCHEMA_PIN")
	info, err := os.Lstat(pinFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("private native pin file required")
	}
	rawPin, err := os.ReadFile(pinFile)
	if err != nil {
		t.Fatal(err)
	}
	var pin adapter.BinaryPin
	if json.Unmarshal(rawPin, &pin) != nil {
		t.Fatal("invalid native pin")
	}
	if err := adapter.VerifyExecutable(pin, pin.Version); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	help, err := adapter.ProbeOutput(ctx, pin.Path, "--help")
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(evidence, "work-"+string(provider))
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "calc.py"), []byte("def add(a, b):\n    return a - b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 120000, GrokSessionWrite: provider == adapter.ProviderGrok}
	lock := &adapter.ProviderLock{Version: 1, Provider: provider, Protocol: adapter.ProtocolID(provider), Binary: pin}
	req := adapter.Request{Provider: provider, Binary: pin, Lock: lock, CWD: work, Prompt: fmt.Sprintf("Review ONLY this exact file: %s. Requirement: add(2, 3) must return 5. Read that absolute path using read_file (Grok) or view_file (AGY); do not search home or other directories. Use only read operations, do not invoke other agents, and return the schema object with your approve/reject decision and a concise reason.", filepath.Join(work, "calc.py")), Profile: profile}
	if provider == adapter.ProviderAGY {
		req.Action = &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "native-schema-contract"}
		if err = adapter.CheckCapabilities(req, string(help)); err != nil {
			t.Fatal(err)
		}
	} else if !strings.Contains(string(help), "--json-schema") {
		t.Fatal("provider_capability_unsupported")
	}
	inv, err := adapter.BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	args := inv.Args()
	if provider == adapter.ProviderGrok {
		args = append(args, "--json-schema", nativeReviewSchema)
	}
	cmd := process.Command{Path: args[0], Args: args[1:], Dir: inv.WorkingDirectory(), Stdin: inv.Stdin(), PinnedPath: pin.Path, PinnedSHA256: pin.SHA256}
	for key, value := range inv.Environment() {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	scratch := filepath.Join(evidence, "scratch-"+string(provider))
	if provider == adapter.ProviderGrok {
		cmd, err = prepareGrokCommand(ctx, cmd, profile, contract.LaunchCommand{CommandID: "native-schema-contract-" + string(provider), RunID: "native-schema-" + hashBytes([]byte(evidence))[:16], TaskID: "review"}, scratch)
	} else {
		cmd, err = sandboxCommand(ctx, cmd, profile, scratch)
	}
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(evidence, "native-schema-"+string(provider)+".jsonl")
	stdout, err := os.OpenFile(fixture, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	cmd.Stdout = stdout
	intent := filepath.Join(evidence, "launch-intent.json")
	intentFile, err := os.OpenFile(intent, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encodeErr := json.NewEncoder(intentFile).Encode(map[string]any{"provider": provider, "max_active_ms": 120000, "max_attempts": 1})
	if err := errors.Join(encodeErr, intentFile.Sync(), intentFile.Close()); err != nil {
		t.Fatal(err)
	}
	p, err := process.Start(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	waitCtx, waitCancel := context.WithDeadline(ctx, deadline.Add(-5*time.Second))
	waitErr := p.Wait(waitCtx)
	waitCancel()
	if waitErr != nil {
		stopCtx, stopCancel := context.WithDeadline(context.Background(), deadline)
		stopErr := p.Stop(stopCtx)
		stopCancel()
		if stopErr != nil {
			t.Errorf("native process stop: %v", stopErr)
		}
	}
	treeErr := p.ConfirmTreeExited()
	_ = stdout.Sync()
	_ = stdout.Close()
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evidence, "combined-output.txt"), []byte(p.Output()), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"exit_code": p.ExitCode(), "stdout_bytes": len(raw), "wait_error": fmtError(waitErr), "tree_error": fmtError(treeErr)})
	if err := os.WriteFile(filepath.Join(evidence, "result.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if waitErr != nil || treeErr != nil || len(raw) == 0 || len(raw) > 1024*1024 || p.ExitCode() != 0 {
		t.Fatalf("native execution unsuccessful; retained evidence: wait=%v tree=%v exit=%d", waitErr, treeErr, p.ExitCode())
	}
	source, err := os.ReadFile(filepath.Join(work, "calc.py"))
	if err != nil || string(source) != "def add(a, b):\n    return a - b\n" {
		t.Fatal("readonly source changed")
	}
	if provider != adapter.ProviderAGY {
		t.Logf("saved Grok raw contract fixture to %s for terminal-envelope review", fixture)
		return
	}
	parser := adapter.NewStreamParser(provider, providerEventLimit, 1024*1024)
	var terminal adapter.Event
	if err = parser.Consume(strings.NewReader(string(raw)), func(event adapter.Event) error {
		if event.Kind == adapter.EventResult {
			terminal = event
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if decision, e := decodeCandidateReview(terminal.StructuredText); e != nil || decision.Decision != "reject" {
		err = e
		t.Fatalf("AGY final result did not carry one strict review object: %v", err)
	}
}

func fmtError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
