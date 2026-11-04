package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/gitops"
)

// Real controller, store, Host and Git materialization; provider is the existing
// local protocol fixture. No real CLI, model or account is used.
func TestManagedAutoWorkspaceDispatchAndSubmit(t *testing.T) {
	for _, action := range []string{"dispatch", "submit"} {
		t.Run(action, func(t *testing.T) {
			root := privateTaskTemp(t)
			state := filepath.Join(root, "state")
			bin := deliveryTestBinary(t, filepath.Join(root, "bin", "orchestrator"))
			prepareControllerSkill(t, root)
			defer func() {
				if _, err := os.Stat(filepath.Join(state, "coordinator.pid")); err == nil {
					stopCoordinator(t, state)
				}
			}()
			repo := filepath.Join(root, "repo")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			git := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("/usr/bin/git", append([]string{"-C", repo}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git: %s %v", out, err)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-q")
			git("config", "user.name", "Test")
			git("config", "user.email", "test@example.invalid")
			for _, name := range []string{"README", "obsolete.txt"} {
				if err := os.WriteFile(filepath.Join(repo, name), []byte("base\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("add", ".")
			git("commit", "-qm", "base")
			base := git("rev-parse", "HEAD")
			fixture, err := filepath.EvalSymlinks(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			lock := adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: fixture, Version: "2.1.267 (Claude Code)", SHA256: hex.EncodeToString(digest[:])}}
			lockPath := filepath.Join(root, "lock.json")
			if err = writeExclusiveJSON(lockPath, lock); err != nil {
				t.Fatal(err)
			}
			req := dispatchRequest{Version: 1, RequestID: "implementation", Provider: "claude", ProviderLock: lockPath, Directory: repo, Prompt: "fixture implement", Role: adapter.Implementer, Paths: []string{"README", "added.txt", "obsolete.txt"}, Acceptance: []string{"fixture changes retained"}, MaxAttempts: 2, MaxActiveMS: 15000}
			if err = writeExclusiveJSON(filepath.Join(root, "request.json"), req); err != nil {
				t.Fatal(err)
			}
			command := `"$AGENT_BIRD_COMMAND" task dispatch --request "$BIRD_FIXTURE/request.json" --state-dir "$BIRD_FIXTURE/state"`
			if action == "submit" {
				plan, err := dispatchPlan(req, lock, "submit-auto", base)
				if err != nil {
					t.Fatal(err)
				}
				if err = writeExclusiveJSON(filepath.Join(root, "plan.json"), plan); err != nil {
					t.Fatal(err)
				}
				command = `"$AGENT_BIRD_COMMAND" task submit --request "$BIRD_FIXTURE/plan.json" --handle "$BIRD_FIXTURE/handle.json" --state-dir "$BIRD_FIXTURE/state"`
			}
			script := `#!/bin/sh
set -eu
case "${1-}" in
--version) echo 1.2.5;;
--help) echo '--input-format --output-format --mode --model --add-dir';;
--driver)
` + command + ` > "$BIRD_FIXTURE/first.json"
handle=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["handle"])' "$BIRD_FIXTURE/first.json")
"$AGENT_BIRD_COMMAND" task wait --handle "$handle" --timeout-ms 10000 > "$BIRD_FIXTURE/result.json"
`
			if action == "dispatch" {
				script += `cp "$(dirname "$handle")/plan.json" "$BIRD_FIXTURE/frozen.json"
` + command + ` > "$BIRD_FIXTURE/second.json"
cmp "$(dirname "$handle")/plan.json" "$BIRD_FIXTURE/frozen.json"
`
			}
			script += ";;\n*) exit 99;;\nesac\n"
			if err = os.WriteFile(filepath.Join(root, "agy"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "controller", "start", "--provider", "agy", "--state-dir", state, "--", "--driver")
			cmd.Env = environmentWith(environmentWith(environmentWith(environmentWith(os.Environ(), "PATH", root+":"+os.Getenv("PATH")), "BIRD_FIXTURE", root), "CANDIDATE_FIXTURE_MODE", "success"), "GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
			cmd.Env = environmentWith(cmd.Env, "ORCHESTRATOR_ENABLE_TEST_FAKE", "1")
			if out, err := cmd.CombinedOutput(); err != nil {
				first, _ := os.ReadFile(filepath.Join(root, "first.json"))
				t.Fatalf("controller: %s %s %v", out, first, err)
			}
			var result struct {
				Events []contract.Event `json:"events"`
			}
			body, err := os.ReadFile(filepath.Join(root, "result.json"))
			if err != nil || json.Unmarshal(body, &result) != nil || len(result.Events) != 1 {
				t.Fatalf("result: %s %v", body, err)
			}
			var artifact struct {
				Candidate *gitops.CandidateReceipt `json:"candidate"`
			}
			artifactBytes, err := os.ReadFile(result.Events[0].Artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(artifactBytes, &artifact); err != nil {
				t.Fatal(err)
			}
			if artifact.Candidate == nil {
				t.Fatal("missing materialized candidate")
			}
			assertOutsideState(t, state, artifact.Candidate.Worktree)
			validateManagedWorkspaceNativePolicy(t, state, artifact.Candidate.Worktree)
			if !strings.Contains(artifact.Candidate.Worktree, ".attempt-") {
				t.Fatal("automatic attempt identity lost")
			}
			if action == "dispatch" {
				var plan taskPlan
				if err = readPrivateJSON(filepath.Join(root, "frozen.json"), &plan); err != nil {
					t.Fatal(err)
				}
				p, err := adapter.DecodeInvocationPayload(plan.Tasks[0].AdapterPayload)
				if err != nil {
					t.Fatal(err)
				}
				assertOutsideState(t, state, p.Directory)
				// Frozen directory survives the allocator; a repeated request retains the
				// immutable path and budget, and never invokes the fixture a second time.
				before, _ := json.Marshal(plan.Tasks)
				if err = prepareTaskWorkspaces(state, "/another/handle", plan.Tasks); err != nil {
					t.Fatal(err)
				}
				after, _ := json.Marshal(plan.Tasks)
				if string(before) != string(after) {
					t.Fatal("frozen directory or budget changed")
				}
			}
		})
	}
}

// Check the unchanged Python admission boundary against the actual Git worktree
// created above. No provider entry point or credentials are involved.
func validateManagedWorkspaceNativePolicy(t *testing.T, state, workspace string) {
	t.Helper()
	lib, err := filepath.Abs("../../../../plugins/codex-orchestrator/lib/trial")
	if err != nil {
		t.Fatal(err)
	}
	code := `import pathlib, subprocess, sys
sys.path.insert(0, sys.argv[1])
from codex_exec import validate_host_policy
state, work = map(pathlib.Path, sys.argv[2:])
scratch = state / 'host-spool' / 'policy-fixture' / 'task' / 'segment' / 'scratch'
scratch.mkdir(parents=True, mode=0o700)
scratch.chmod(0o700)
def git_path(flag):
    return str(pathlib.Path(subprocess.check_output(['/usr/bin/git', '-C', str(work), 'rev-parse', flag], text=True).strip()).resolve())
policy = dict(version=2, scratch=str(scratch), state_root=str(state), role='implementer', timeout_ms=15000, workspace=str(work), write_paths=[str(work/'README')], git_paths=[str(work/'.git'), git_path('--git-dir'), git_path('--git-common-dir')])
assert validate_host_policy(policy, scratch) == policy
`
	cmd := exec.Command("python3", "-c", code, lib, state, workspace)
	cmd.Env = environmentWith(os.Environ(), "PYTHONDONTWRITEBYTECODE", "1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native policy rejected managed workspace: %s %v", out, err)
	}
}
