package host

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type claudeGatewayBranchInvocation struct {
	launchInvocation
	profile   *adapter.ExecutionProfile
	action    *adapter.CandidateAction
	workspace *adapter.CandidateWorkspace
}

func (i claudeGatewayBranchInvocation) OutputProvider() string                      { return string(adapter.ProviderClaude) }
func (i claudeGatewayBranchInvocation) ExecutionProfile() *adapter.ExecutionProfile { return i.profile }
func (i claudeGatewayBranchInvocation) CandidateAction() *adapter.CandidateAction   { return i.action }
func (i claudeGatewayBranchInvocation) CandidateWorkspace() *adapter.CandidateWorkspace {
	return i.workspace
}

func TestClaudeGatewayRejectsEveryUnsupportedBranchBeforeSpawn(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example.invalid")
	for _, kind := range []string{"untyped", "implementer", "candidate-review", "candidate-workspace"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			h, err := NewIPC(filepath.Join(root, "spool"), "producer")
			if err != nil {
				t.Fatal(err)
			}
			inv := claudeGatewayBranchInvocation{launchInvocation: launchInvocation{args: []string{"/usr/bin/true"}, dir: root}, profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 1000}}
			switch kind {
			case "untyped":
				inv.profile = nil
			case "implementer":
				inv.profile.Role = adapter.Implementer
				inv.profile.Permission = adapter.WorkspaceWrite
			case "candidate-review":
				inv.action = &adapter.CandidateAction{Operation: "review"}
			case "candidate-workspace":
				inv.workspace = &adapter.CandidateWorkspace{}
			}
			_, err = h.ExecuteLaunch(context.Background(), contract.LaunchCommand{CommandID: "c", ReservationID: "r", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", GrantedActiveMS: 1000}, inv)
			if err == nil || err.Error() != "claude_gateway_readonly_required" {
				t.Fatalf("unsupported branch admitted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "spool", "attempt")); !os.IsNotExist(err) {
				t.Fatal("unsupported branch reached execute/spawn")
			}
		})
	}
}

func claudeGatewayFixture(t *testing.T) (process.Command, *adapter.ExecutionProfile, string, claudeGatewaySource) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, work := filepath.Join(root, "source-home"), filepath.Join(root, "work")
	for _, dir := range []string{home, work} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := process.Command{Path: "/bin/sh", Args: []string{"-c", "printf ok"}, Dir: work}
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 5000}
	source := claudeGatewaySource{env: map[string]string{"HOME": home, "ANTHROPIC_BASE_URL": "https://gateway.example.invalid/anthropic", "ANTHROPIC_AUTH_TOKEN": "SYNTHETIC_GATEWAY_CREDENTIAL", "NODE_OPTIONS": "--require untrusted.js", "HTTPS_PROXY": "http://untrusted.invalid", "DYLD_INSERT_LIBRARIES": "/untrusted.dylib"}, homes: []string{home}, checkManaged: func(context.Context) error { return nil }}
	return cmd, profile, filepath.Join(root, "scratch"), source
}

func TestClaudeGatewayUsesExactPrivateEnvironmentWithoutCopyingAuth(t *testing.T) {
	cmd, profile, scratch, source := claudeGatewayFixture(t)
	prepared, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.ExactEnv || prepared.Path != "/usr/bin/sandbox-exec" {
		t.Fatalf("gateway not isolated: %+v", prepared)
	}
	joined := strings.Join(prepared.Env, "\n")
	for _, bad := range []string{"NODE_OPTIONS=", "HTTPS_PROXY=", "DYLD_INSERT_LIBRARIES=", "CLAUDE_CODE_OAUTH_TOKEN="} {
		if strings.Contains(joined, bad) {
			t.Fatalf("inherited environment %s", bad)
		}
	}
	if !strings.Contains(joined, "HOME="+filepath.Join(scratch, "claude-home")) || !strings.Contains(joined, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+filepath.Join(scratch, "claude-home/.claude")) {
		t.Fatal("private state not selected")
	}
	if strings.Contains(strings.Join(prepared.Args, "\n"), source.env["ANTHROPIC_AUTH_TOKEN"]) {
		t.Fatal("credential exposed in argv/SBPL")
	}
	for _, file := range []string{"mcp.json", "settings.json"} {
		body, err := os.ReadFile(filepath.Join(scratch, "claude-home", "policy", file))
		if err != nil || strings.Contains(string(body), "CREDENTIAL") {
			t.Fatalf("config not private/minimal: %s", file)
		}
	}
}

func TestClaudeGatewayRejectsConflictingOrUnverifiedSources(t *testing.T) {
	for _, kind := range []string{"oauth", "dual-key", "userinfo", "http", "query", "managed", "project", "reused-state"} {
		t.Run(kind, func(t *testing.T) {
			cmd, profile, scratch, source := claudeGatewayFixture(t)
			switch kind {
			case "oauth":
				source.env["CLAUDE_CODE_OAUTH_TOKEN"] = "SYNTHETIC_OAUTH"
			case "dual-key":
				source.env["ANTHROPIC_API_KEY"] = "SYNTHETIC_SECOND_KEY"
			case "userinfo":
				source.env["ANTHROPIC_BASE_URL"] = "https://key@gateway.example.invalid"
			case "http":
				source.env["ANTHROPIC_BASE_URL"] = "http://gateway.example.invalid"
			case "query":
				source.env["ANTHROPIC_BASE_URL"] = "https://gateway.example.invalid?credential=x"
			case "managed":
				source.checkManaged = func(context.Context) error { return errors.New("managed") }
			case "project":
				if err := os.Mkdir(filepath.Join(cmd.Dir, ".claude"), 0700); err != nil {
					t.Fatal(err)
				}
			case "reused-state":
				if _, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source); err == nil {
				t.Fatal("unverified source admitted")
			}
		})
	}
}

func TestClaudeGatewayNativeSandboxPreservesAuthAndWorkspaceBoundaries(t *testing.T) {
	cmd, profile, scratch, source := claudeGatewayFixture(t)
	secret := filepath.Join(source.homes[0], "oauth-sentinel")
	if err := os.WriteFile(secret, []byte("SYNTHETIC_SOURCE_AUTH"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd.Args = []string{"-c", `test ! -r "$1" && ! touch "$2/forbidden" 2>/dev/null && ! rm -rf "$TMPDIR/claude-home/policy" 2>/dev/null && touch "$TMPDIR/allowed" && printf ISOLATED`, "sh", secret, cmd.Dir}
	prepared, err := prepareClaudeGateway(context.Background(), cmd, profile, scratch, source)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := process.Start(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Wait(context.Background()); err != nil || handle.ExitCode() != 0 || handle.Output() != "ISOLATED" {
		t.Fatalf("sandbox failed: exit=%d output=%q err=%v", handle.ExitCode(), handle.Output(), err)
	}
	if _, err := os.Stat(filepath.Join(scratch, "allowed")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cmd.Dir, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("workspace write allowed")
	}
	if body, err := os.ReadFile(filepath.Join(scratch, "claude-home/policy/settings.json")); err != nil || string(body) != "{}" {
		t.Fatal("private policy was mutable")
	}
}

// A bootstrap lookup opens no Keychain and changes no authentication state.
// The positive control ensures that a missing service cannot fake a denial.
func TestClaudeGatewayNativeSandboxDeniesSecurityServices(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	sourceFile, binary := filepath.Join(root, "lookup.c"), filepath.Join(root, "lookup")
	source := `#include <servers/bootstrap.h>
#include <mach/mach.h>
int main(int argc, char **argv) {
 if (argc < 2) return 2;
 mach_port_t service = MACH_PORT_NULL;
 kern_return_t result = bootstrap_look_up(bootstrap_port, argv[1], &service);
 if (result != KERN_SUCCESS) return 1;
 mach_port_deallocate(mach_task_self(), service);
 return 0;
}`
	if err := os.WriteFile(sourceFile, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "/usr/bin/cc", sourceFile, "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("compile native Mach probe: %v %s", err, output)
	}
	positive := 0
	for _, name := range []string{"com.apple.SecurityServer", "com.apple.securityd", "com.apple.securityd.system", "com.apple.securityd.systemkeychain"} {
		if exec.CommandContext(ctx, binary, name).Run() != nil {
			continue
		}
		positive++
		cmd, profile, scratch, source := claudeGatewayFixture(t)
		cmd.Path, cmd.Args = binary, []string{name}
		prepared, err := prepareClaudeGateway(ctx, cmd, profile, scratch, source)
		if err != nil {
			t.Fatal(err)
		}
		child, err := process.Start(ctx, prepared)
		if err != nil {
			t.Fatal(err)
		}
		if err := child.Wait(ctx); err != nil || child.ExitCode() != 1 {
			t.Fatalf("security service reachable: %s exit=%d err=%v", name, child.ExitCode(), err)
		}
	}
	if positive == 0 {
		t.Fatal("no positive security service lookup; denial not established")
	}
	t.Logf("positive_controls=%d; same services denied in gateway sandbox", positive)
}
