package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// Experimental acceptance does not open public admission. One explicit opt-in,
// one fresh evidence directory, one 120-second attempt, with no automatic retry.
func TestNativeCodexLightweightCoding(t *testing.T) {
	if os.Getenv("AGENT_BIRD_NATIVE_CODEX_CODING") != "1" {
		t.Skip("real Codex model requires a separately authorized 120-second attempt")
	}
	root := os.Getenv("AGENT_BIRD_NATIVE_CODEX_CODING_EVIDENCE")
	if !filepath.IsAbs(root) || os.Getuid() == 0 {
		t.Fatal("fresh absolute evidence path and ordinary user required")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	pin := adapter.BinaryPin{Path: os.Getenv("AGENT_BIRD_NATIVE_CODEX_BINARY"), Version: adapter.CodexVersion, SHA256: adapter.CodexSHA256}
	if err := adapter.VerifyExecutable(pin, pin.Version); err != nil {
		t.Fatal(err)
	}

	// Fixture Git commands use no personal hooks, signing or configuration.
	repo, work := filepath.Join(root, "repo"), filepath.Join(root, "work")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git failed: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", repo)
	git("-C", repo, "config", "user.name", "Fixture")
	git("-C", repo, "config", "user.email", "fixture@example.invalid")
	baseline := []byte("def add(a, b):\n    return a - b\n")
	if err := os.WriteFile(filepath.Join(repo, "calc.py"), baseline, 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", repo, "add", "calc.py")
	git("-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
	base := git("-C", repo, "rev-parse", "HEAD")
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, Model: "gpt-5.5", Reasoning: "low", TimeoutMS: 120000}
	inv, err := adapter.BuildInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: pin, Lock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderCodex, Protocol: adapter.ProtocolID(adapter.ProviderCodex), Binary: pin}, CWD: work, Prompt: "Inspect calc.py yourself and fix add so add(2, 3) returns 5 and add(-2, 3) returns 1. Edit only calc.py using native apply_patch. Use the available bird read tools to inspect the existing code. Commands and tests belong to the main CLI and are unavailable here. Stop after the edit and briefly report the change without claiming to have run tests.", Profile: profile, Workspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: base, Paths: []string{"calc.py"}}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewIPC(filepath.Join(root, "spool"), "native-lightweight")
	if err != nil {
		t.Fatal(err)
	}
	grant := contract.LaunchCommand{RunID: "native-lightweight", TaskID: "coding", AttemptID: "first", SegmentID: "first", WorkRevision: 1, PlanRevision: 1}
	freeze, closeJournal, _, err := h.prepareCandidate(context.Background(), grant, inv, profile)
	if err != nil {
		t.Fatal(err)
	}
	defer closeJournal()
	originalModes := map[string]os.FileMode{}
	for _, path := range []string{work, filepath.Join(work, ".git"), filepath.Join(work, "calc.py")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		originalModes[path] = info.Mode().Perm()
	}
	// No file-creation, chmod or process capability exists in the worker. These
	// additional test-only mode restrictions make calc.py the sole writable file.
	if err := os.Chmod(filepath.Join(work, "calc.py"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(work, ".git"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0500); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "forbidden.py"), []byte("forbidden"), 0600); err == nil {
		t.Fatal("fixture directory did not enforce exact-file write scope")
	}
	if err := os.WriteFile(filepath.Join(work, ".git"), []byte("forbidden"), 0400); err == nil {
		t.Fatal("fixture Git input remained writable")
	}
	scratch := filepath.Join(root, "scratch")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(scratch, "provider-final.txt")
	if err := writeCodexPrivate(finalPath, nil); err != nil {
		t.Fatal(err)
	}
	intent := map[string]any{"provider": "codex", "model": string(profile.Model), "max_attempts": 1, "max_active_ms": 120000, "editable_paths": []string{"calc.py"}}
	if err := writeCodexPrivate(filepath.Join(root, "launch-intent.json"), mustCodexJSON(intent)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	state := &codexRuntimeState{}
	defer state.close()
	original := process.Command{Path: pin.Path, PinnedPath: pin.Path, PinnedSHA256: pin.SHA256, Dir: work, Args: []string{"exec", "--output-last-message", finalPath, "-"}, Stdin: inv.Stdin()}
	cmd, err := prepareCodexWorker(ctx, original, profile, scratch, false, state)
	if err != nil {
		t.Fatalf("native preflight: %v", err)
	}
	output, err := os.OpenFile(filepath.Join(root, "provider-events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd.Stdout = output // Only translated business events; never raw config/account RPC.
	child, err := process.Start(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	waitCtx, waitCancel := context.WithDeadline(ctx, deadline.Add(-5*time.Second))
	waitErr := child.Wait(waitCtx)
	waitCancel()
	if waitErr != nil {
		stopCtx, stopCancel := context.WithDeadline(context.Background(), deadline)
		waitErr = errors.Join(waitErr, child.Stop(stopCtx))
		stopCancel()
	}
	treeErr := child.ConfirmTreeExited()
	verifyErr := state.verify()
	closeErr := state.close()
	_ = output.Sync()
	readCalled := false
	for _, line := range strings.Split(child.Output(), "\n") {
		var frame map[string]any
		if json.Unmarshal([]byte(line), &frame) != nil || frame["method"] != "item/tool/call" {
			continue
		}
		params, _ := frame["params"].(map[string]any)
		args, _ := params["arguments"].(map[string]any)
		if params["tool"] == "bird_read_file" && args["path"] == "calc.py" {
			readCalled = true
		}
	}
	result := map[string]any{"exit_code": child.ExitCode(), "wait_error": fmtError(waitErr), "tree_error": fmtError(treeErr), "verify_error": fmtError(verifyErr), "close_error": fmtError(closeErr), "dynamic_read_called": readCalled, "bridge": state.bridge.Diagnostics()}
	if err := writeCodexPrivate(filepath.Join(root, "execution-result.json"), mustCodexJSON(result)); err != nil {
		t.Fatal(err)
	}
	if waitErr != nil || treeErr != nil || verifyErr != nil || closeErr != nil || child.ExitCode() != 0 || !readCalled {
		t.Fatalf("native acceptance failed: wait=%v tree=%v verify=%v close=%v exit=%d read=%v", waitErr, treeErr, verifyErr, closeErr, child.ExitCode(), readCalled)
	}
	if _, err := os.Lstat(filepath.Join(state.home.path, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("authentication alias not detached")
	}
	final, err := os.ReadFile(finalPath)
	if err != nil || len(final) == 0 {
		t.Fatal("native final answer not saved")
	}
	// Worker processes have exited and their immutable inputs were verified.
	// Restore only fixture modes needed by the main CLI's candidate lifecycle.
	for path, mode := range originalModes {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	freezeCtx, freezeCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer freezeCancel()
	artifact, err := freeze(freezeCtx, string(final))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCodexPrivate(filepath.Join(root, "candidate.json"), artifact); err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Candidate struct {
			CandidateOID string `json:"candidate_oid"`
		} `json:"candidate"`
	}
	if json.Unmarshal(artifact, &frozen) != nil || frozen.Candidate.CandidateOID == "" || frozen.Candidate.CandidateOID == base {
		t.Fatal("candidate not frozen")
	}
	changed := git("-C", repo, "show", frozen.Candidate.CandidateOID+":calc.py")
	if err := checkNativeCodexArithmetic(freezeCtx, root, changed); err != nil {
		t.Fatalf("main-CLI candidate checks failed: %v", err)
	}
	unchanged, err := os.ReadFile(filepath.Join(repo, "calc.py"))
	if err != nil || string(unchanged) != string(baseline) || git("-C", repo, "rev-parse", "HEAD") != base {
		t.Fatal("controller checkout changed")
	}
	if err := writeCodexPrivate(filepath.Join(root, "acceptance.json"), mustCodexJSON(map[string]any{"status": "passed", "candidate_oid": frozen.Candidate.CandidateOID, "main_cli_assertions": 4, "source_unchanged": true, "auth_alias_detached": true})); err != nil {
		t.Fatal(err)
	}
	t.Log("one real coding attempt; dynamic read and native patch; candidate frozen; four main-CLI assertions passed; source unchanged; both groups exited; authentication alias detached")
}

func checkNativeCodexArithmetic(ctx context.Context, root, source string) error {
	if len(source) > 4096 {
		return errors.New("native_fixture_source_too_large")
	}
	userHome, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(userHome) {
		return errors.New("native_fixture_home_unknown")
	}
	// AST comparison accepts harmless formatting/comments, but excludes calls,
	// imports, decorators, defaults, extra statements and all executable surprises.
	const check = `import ast, sys
actual = ast.parse(sys.stdin.read())
expected = ast.parse("def add(a, b):\n    return a + b\n")
assert ast.dump(actual) == ast.dump(expected), "unexpected candidate syntax"
scope = {}
exec(compile(actual, "<verified-fixture>", "exec"), scope)
add = scope["add"]
assert add(2, 3) == 5
assert add(-2, 3) == 1
assert add(0, 0) == 0
assert add(1.5, 2.5) == 4
`
	rules := "(version 1)\n(allow default)\n(deny network*)\n(deny file-write*)\n(deny file-read* (subpath " + strconv.Quote(userHome) + "))"
	cmd := process.Command{Path: "/usr/bin/sandbox-exec", Args: []string{"-p", rules, "/usr/bin/python3", "-I", "-S", "-B", "-c", check}, Dir: "/", ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + root}, Stdin: []byte(source)}
	child, err := process.Start(ctx, cmd)
	if err != nil {
		return err
	}
	if err := child.Wait(ctx); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return errors.Join(err, child.Stop(stopCtx))
	}
	if err := child.ConfirmTreeExited(); err != nil {
		return err
	}
	if child.ExitCode() != 0 {
		return errors.New("native_fixture_arithmetic_rejected")
	}
	return nil
}

func TestNativeCodexArithmeticRejectsExecutableCandidates(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, tc := range []struct {
		source string
		ok     bool
	}{
		{"# harmless comment\ndef add(a, b):\n    return a + b\n", true},
		{"def add(a, b):\n    return a - b\n", false},
		{"import os\nos.system('false')\ndef add(a, b):\n    return a + b\n", false},
		{"def add(a, b=print('unexpected')):\n    return a + b\n", false},
		{"@print\ndef add(a, b):\n    return a + b\n", false},
	} {
		if err := checkNativeCodexArithmetic(ctx, root, tc.source); (err == nil) != tc.ok {
			t.Fatalf("unexpected fixture validation (expected %v): %v", tc.ok, err)
		}
	}
}
