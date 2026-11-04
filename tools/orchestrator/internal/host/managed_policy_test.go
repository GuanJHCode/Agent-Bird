package host

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
)

func managedPolicyFixture(t *testing.T) (string, *adapter.CandidateWorkspace) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, work := filepath.Join(root, "repo"), filepath.Join(root, "work")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", repo)
	if err := os.WriteFile(filepath.Join(repo, "CHANGE.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", repo, "add", "CHANGE.txt")
	git("-C", repo, "commit", "-qm", "fixture")
	oid := git("-C", repo, "rev-parse", "HEAD")
	git("-C", repo, "worktree", "add", "--detach", work, oid)
	return work, &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: oid, Paths: []string{"CHANGE.txt"}}
}

func TestManagedWritePolicyProjectsOnlyDeclaredRegularFiles(t *testing.T) {
	work, spec := managedPolicyFixture(t)
	paths, git, err := managedWritePaths(context.Background(), work, spec)
	if err != nil || len(paths) != 1 || paths[0] != filepath.Join(work, "CHANGE.txt") || len(git) != 3 {
		t.Fatalf("paths=%v git=%v err=%v", paths, git, err)
	}
	for _, path := range []string{"../escape", ".git", ".git/config", ".codex/config.toml", ".claude/settings.json", ".", "CHANGE.txt/child"} {
		copy := *spec
		copy.Paths = []string{path}
		if _, _, err := managedWritePaths(context.Background(), work, &copy); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	spec.Paths = []string{"link/escape"}
	if _, _, err := managedWritePaths(context.Background(), work, spec); err == nil {
		t.Fatal("symlink granted write")
	}
}

func TestCodexManagedPolicyCarriesRoleDeadlineAndGitProtection(t *testing.T) {
	work, spec := managedPolicyFixture(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	scratch := filepath.Join(root, "state/host-spool/launch/attempt/segment/scratch")
	if err := os.MkdirAll(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	policy, err := codexNativePolicy(scratch, nil, codexPolicyOptions{profile: &adapter.ExecutionProfile{Role: adapter.Implementer, Permission: adapter.WorkspaceWrite}, cwd: work, workspace: spec, timeoutMS: 1750})
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Version int      `json:"version"`
		Role    string   `json:"role"`
		Timeout int64    `json:"timeout_ms"`
		Paths   []string `json:"write_paths"`
		Git     []string `json:"git_paths"`
	}
	if json.Unmarshal([]byte(policy), &value) != nil || value.Version != 2 || value.Role != "implementer" || value.Timeout != 1750 || len(value.Paths) != 1 || len(value.Git) != 3 {
		t.Fatalf("policy=%s", policy)
	}
	if _, err := codexNativePolicy(scratch, nil, codexPolicyOptions{profile: &adapter.ExecutionProfile{Role: adapter.Implementer}, cwd: work, timeoutMS: 1750}); err == nil {
		t.Fatal("unmanaged write policy")
	}
}

func TestCodexHelperExactEnvironmentRejectsAmbientAuthAndLoader(t *testing.T) {
	// The current process may carry provider metadata; use explicit supported values.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PYTHONPATH", "untrusted")
	t.Setenv("DYLD_INSERT_LIBRARIES", "untrusted")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "CODEX_") {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
	}
	env, err := codexHelperEnvironment(process.Command{}, []string{"TMPDIR=/private/owned"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "PYTHONPATH=") || strings.Contains(joined, "DYLD_") || !strings.Contains(joined, "TMPDIR=/private/owned") {
		t.Fatalf("env=%v", env)
	}
	t.Setenv("CODEX_REFRESH_TOKEN_URL_OVERRIDE", "")
	if _, err := codexHelperEnvironment(process.Command{}, nil); err == nil {
		t.Fatal("ambient refresh override accepted")
	}
}

func TestClaudeManagedGatewaySandboxRestrictsWritePathsAndMetadata(t *testing.T) {
	work, spec := managedPolicyFixture(t)
	cmd, profile, scratch, source := claudeGatewayFixture(t)
	cmd.Dir = work
	profile.Role = adapter.Implementer
	profile.Permission = adapter.WorkspaceWrite
	source.workspace = spec
	source.help = "--tools"
	paths, git, err := managedWritePaths(context.Background(), work, spec)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Args = []string{"-c", `printf changed > "$1" || exit 10; if (printf forbidden > "$2/other.txt") 2>/dev/null; then exit 11; fi; for path in "$3" "$4/config" "$5/HEAD"; do if (printf forbidden > "$path") 2>/dev/null; then exit 12; fi; done; printf ISOLATED`, "probe", paths[0], work, git[0], git[1], git[2]}
	prepared, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := process.Start(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err = handle.Wait(context.Background()); err != nil || handle.ExitCode() != 0 || handle.Output() != "ISOLATED" {
		t.Fatalf("exit=%d out=%q err=%v", handle.ExitCode(), handle.Output(), err)
	}
	if b, err := os.ReadFile(paths[0]); err != nil || string(b) != "changed" {
		t.Fatal("declared write denied")
	}
}

func TestClaudeManagedGatewayRequiresPositiveToolsCapability(t *testing.T) {
	work, spec := managedPolicyFixture(t)
	cmd, profile, scratch, source := claudeGatewayFixture(t)
	cmd.Dir = work
	profile.Role = adapter.Implementer
	profile.Permission = adapter.WorkspaceWrite
	source.workspace = spec
	if _, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source); err == nil || err.Error() != "claude_gateway_capability_unsupported" {
		t.Fatalf("missing tools accepted: %v", err)
	}
}

func TestClaudeAtomicNamespaceRejectsExistingObjectsAndEscapesRegex(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			path := filepath.Join(root, "source[1]+.txt")
			collision := path + ".tmp.123.abcdef123456"
			switch kind {
			case "file":
				if err := os.WriteFile(collision, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("/outside", collision); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(collision, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := claudeAtomicWriteRules([]string{path}); err == nil || err.Error() != "claude_atomic_path_collision" {
				t.Fatalf("collision=%v", err)
			}
			if err := os.Remove(collision); err != nil {
				t.Fatal(err)
			}
			rules, err := claudeAtomicWriteRules([]string{path})
			if err != nil || len(rules) != 1 {
				t.Fatalf("rules=%v err=%v", rules, err)
			}
			if !strings.Contains(rules[0], `source\\[1\\]\\+\\.txt\\.tmp\\.`) || strings.Count(rules[0], "[0-9a-f]") != 12 || !strings.Contains(rules[0], `$`) {
				t.Fatalf("unescaped/unanchored SBPL=%s", rules[0])
			}
		})
	}
}

func TestClaudeAtomicWriteNativeSandboxAllowsOnlyExactDerivedNames(t *testing.T) {
	work, spec := managedPolicyFixture(t)
	spec.Paths = []string{"source[1]+.txt"}
	cmd, profile, scratch, source := claudeGatewayFixture(t)
	cmd.Dir = work
	profile.Role = adapter.Implementer
	profile.Permission = adapter.WorkspaceWrite
	source.workspace = spec
	source.help = "--tools"
	scratch = filepath.Join(scratch, "state/host-spool/launch/attempt/segment/scratch")
	state := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(scratch)))))
	secret := filepath.Join(state, "owners/secret")
	if err := os.MkdirAll(filepath.Dir(secret), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(work, spec.Paths[0])
	cmd.Args = []string{"-c", `printf changed > "$1.tmp.123.abcdef123456" || exit 10; /bin/mv "$1.tmp.123.abcdef123456" "$1" || exit 11; for path in "$1.tmp.123.abcdef12345" "$1.tmp.123.abcdef1234567" "$1.tmp.x.abcdef123456" "$1.tmp.123.abcdef123456.extra" "$2/adjacent" "$3"; do if (printf forbidden > "$path") 2>/dev/null; then exit 12; fi; done; printf ATOMIC`, "probe", file, work, secret}
	prepared, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := process.Start(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Wait(context.Background()); err != nil || handle.ExitCode() != 0 || handle.Output() != "ATOMIC" {
		t.Fatalf("exit=%d out=%q err=%v", handle.ExitCode(), handle.Output(), err)
	}
}

func TestClaudeAtomicResidualCannotBecomeFrozenCandidate(t *testing.T) {
	work, spec := managedPolicyFixture(t)
	cmd, profile, scratch, source := claudeGatewayFixture(t)
	cmd.Dir = work + "-fresh"
	profile.Role = adapter.Implementer
	profile.Permission = adapter.WorkspaceWrite
	source.workspace = spec
	source.help = "--tools"
	inv := automaticWriteInvocation{profiledInvocation: profiledInvocation{launchInvocation: launchInvocation{args: []string{"/bin/sh"}, dir: cmd.Dir}, profile: *profile}, workspace: spec}
	grant := contract.LaunchCommand{CommandID: "residual", ReservationID: "residual", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1, GrantedActiveMS: 5000}
	h, err := NewIPC(filepath.Join(t.TempDir(), "spool"), "residual")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Args = []string{"-c", `printf changed > CHANGE.txt; printf residual > CHANGE.txt.tmp.123.abcdef123456; printf RESULT`}
	meta := launchMetadata{prepareCandidate: func(ctx context.Context) (process.Command, func(context.Context, string) ([]byte, error), func(), error) {
		freeze, close, err := h.prepareCandidate(ctx, grant, inv, profile)
		if err != nil {
			return cmd, nil, close, err
		}
		wrapped, err := prepareClaudeGateway(ctx, cmd, profile, scratch, source)
		return wrapped, freeze, close, err
	}}
	result, err := h.execute(context.Background(), store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID}, grant.RunID, grant.TaskID, cmd, false, meta)
	if err != nil || result.Status != "failed" {
		t.Fatalf("residual status=%s err=%v", result.Status, err)
	}
	body, err := os.ReadFile(result.ArtifactPath)
	if err != nil || !strings.Contains(string(body), `"code":"unknown_object"`) || strings.Contains(string(body), "candidate_oid") {
		t.Fatalf("residual freeze=%s %v", body, err)
	}
}

// Codex runs SourceHost from its own normal execution environment. These fields
// describe that outer session; they grant no native worker auth or context.
func TestCodexHelperDiscardsNormalAmbientContextWithoutBlockingLaunch(t *testing.T) {
	contextNames := strings.Fields("CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS CODEX_CI CODEX_SESSION_ID CODEX_THREAD_ID CODEX_TOOL_CALL_ID CODEX_VERSION CODEX_INTERNAL_ORIGINATOR_OVERRIDE CODEX_SQLITE_HOME")
	for _, name := range contextNames {
		t.Setenv(name, "outer-session-context")
	}
	t.Setenv("CODEX_HOME", "/private/source-codex-home")
	env, err := codexHelperEnvironment(process.Command{}, []string{"TMPDIR=/private/owned"})
	if err != nil {
		t.Fatalf("normal Codex ambient context blocked native helper: %v", err)
	}
	values := map[string]string{}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "OPENAI_") || (strings.HasPrefix(name, "CODEX_") && name != "CODEX_HOME") {
			t.Fatalf("context/auth input reached helper ExactEnv: %s", name)
		}
		values[name] = value
	}
	for _, name := range contextNames {
		if _, present := values[name]; present {
			t.Fatalf("outer context reached helper ExactEnv: %s", name)
		}
	}
	if values["CODEX_HOME"] != "/private/source-codex-home" || values["TMPDIR"] != "/private/owned" {
		t.Fatalf("owned launch inputs missing: %v", values)
	}
	for _, tc := range []struct{ name, value, code string }{
		{"CODEX_UNRECOGNIZED_AUTH_ROUTE", "", "auth_environment_unsupported"},
		{"CODEX_ACCESS_TOKEN", "SYNTHETIC_CREDENTIAL", "auth_environment_unsupported"},
		{"OPENAI_API_KEY", "SYNTHETIC_CREDENTIAL", "auth_environment_unsupported"},
		{"OPENAI_UNRECOGNIZED_CONTEXT", "", "auth_environment_unsupported"},
		{"CODEX_REFRESH_TOKEN_URL_OVERRIDE", "", "refresh_override_already_present"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			env, err := codexHelperEnvironment(process.Command{}, nil)
			if err == nil || err.Error() != tc.code || env != nil {
				t.Fatalf("unsupported input accepted: name=%s env=%v err=%v", tc.name, env, err)
			}
		})
	}
}
