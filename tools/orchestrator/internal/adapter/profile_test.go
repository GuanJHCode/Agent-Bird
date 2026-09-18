package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func profileRequest(t *testing.T, profile string) Request {
	t.Helper()
	req := Request{Provider: ProviderClaude, Binary: pinForTest(), CWD: "/private/workspace", Prompt: "inspect", Permission: Permission{Mode: "plan"}}
	if err := json.Unmarshal([]byte(`{"Profile":`+profile+`,"Lock":{"version":1,"provider":"claude-code","protocol":"claude-stream-json-v1","binary":{"Path":"/private/bin/agent","Version":"1.2.3","SHA256":"`+strings.Repeat("a", 64)+`"}}}`), &req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestProfileControlsModelReasoningAndRole(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"reviewer","model":"review-model","reasoning":"high","permission":"read-only","timeout_ms":9000}`)
	inv, err := BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(inv.Args(), " ")
	if !strings.Contains(args, "--model review-model") || !strings.Contains(args, "--effort high") || !strings.Contains(args, "--permission-mode plan") {
		t.Fatalf("profile not applied: %s", args)
	}
}

func TestAGYReviewerProfileUsesNativePlanAndTypedModel(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"reviewer","model":"gemini-3.8-flash-low","reasoning":"low","permission":"read-only","timeout_ms":9000}`)
	req.Provider = ProviderAGY
	req.Lock.Provider, req.Lock.Protocol = req.Provider, ProtocolID(req.Provider)
	help := "--input-format --output-format --mode --model --effort"
	if err := CheckCapabilities(req, help); err != nil {
		t.Fatal(err)
	}
	inv, err := BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(inv.Args(), " ")
	for _, want := range []string{"--mode plan", "--model gemini-3.8-flash-low", "--effort low"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q: %s", want, args)
		}
	}
	if err := CheckCapabilities(req, "--input-format --output-format --model --effort"); err == nil {
		t.Fatal("accepted CLI without native plan capability")
	}
	req.Profile.Role, req.Profile.Permission = Implementer, WorkspaceWrite
	if _, err := BuildInvocation(req); err == nil || err.Error() != "managed_workspace_required" {
		t.Fatalf("unmanaged AGY editing accepted: %v", err)
	}
}

func TestProfileRejectsUnknownOrExpandedConfiguration(t *testing.T) {
	for _, profile := range []string{
		`{"version":2,"role":"reviewer","permission":"read-only","timeout_ms":9000}`,
		`{"version":1,"role":"worker","permission":"read-only","timeout_ms":9000}`,
		`{"version":1,"role":"reviewer","permission":"workspace-write","timeout_ms":9000}`,
		`{"version":1,"role":"reviewer","permission":"read-only","reasoning":"unlimited","timeout_ms":9000}`,
		`{"version":1,"role":"reviewer","permission":"read-only","model":"--yolo","timeout_ms":9000}`,
		`{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":0}`,
	} {
		if _, err := BuildInvocation(profileRequest(t, profile)); err == nil {
			t.Errorf("accepted unsupported profile: %s", profile)
		}
	}
}

func TestProfileLockValidationAndSnapshot(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
	inv, err := BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	req.Profile.Role = Implementer
	if inv.ExecutionProfile().Role != Reviewer {
		t.Fatal("caller mutated granted role")
	}
	req.Profile = nil
	req.Lock.Version = 99
	if _, err = BuildInvocation(req); err == nil {
		t.Fatal("unknown lock version accepted without profile")
	}
}
func TestCodexTypedProfileExplicitlyRejectsUnavailableProtocol(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"reviewer","model":"custom-model","permission":"read-only","timeout_ms":9000}`)
	req.Provider = ProviderCodex
	req.Lock.Provider = ProviderCodex
	req.Lock.Protocol = ProtocolID(ProviderCodex)
	req.Binary.Version = CodexVersion
	req.Binary.SHA256 = CodexSHA256
	req.Lock.Binary = req.Binary
	if _, err := BuildInvocation(req); err == nil {
		t.Fatal("typed Codex profile silently ignored model")
	}
}

func TestProfileRejectsUnverifiedResume(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
	req.Session = SessionRef{Kind: SessionID, ID: "session-1234"}
	if _, err := BuildInvocation(req); err == nil || err.Error() != "profile_resume_not_verified" {
		t.Fatalf("unverified profile resume not refused: %v", err)
	}
}

func TestCandidateReviewRequiresNativeStructuredOutput(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
	req.Action = &CandidateAction{Version: 1, Operation: "review", SourceTask: "validate"}
	inv, err := BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(inv.Args(), " "), "--json-schema") {
		t.Fatal("candidate review relies on prompt-only JSON")
	}
	if err := CheckCapabilities(req, "--input-format --output-format --permission-mode --disallowed-tools"); err == nil {
		t.Fatal("candidate review accepted missing native schema capability")
	}
	if err := CheckCapabilities(req, "--input-format --output-format --permission-mode --disallowed-tools --json-schema"); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateReviewRequiresVerifiedStructuredProvider(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
	req.Provider = ProviderAGY
	req.Lock.Provider, req.Lock.Protocol = req.Provider, ProtocolID(req.Provider)
	req.Action = &CandidateAction{Version: 1, Operation: "review", SourceTask: "validate"}
	if _, err := BuildInvocation(req); err == nil {
		t.Fatal("unverified candidate review provider admitted without structured channel")
	}
	req.Action = nil
	if _, err := BuildInvocation(req); err != nil {
		t.Fatalf("ordinary AGY read-only task regressed: %v", err)
	}
}

func TestClaudeTypedProfilesDenyNativeDelegation(t *testing.T) {
	for _, role := range []Role{Reviewer, Implementer} {
		t.Run(string(role), func(t *testing.T) {
			req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
			if role == Implementer {
				req.Profile.Role, req.Profile.Permission = Implementer, WorkspaceWrite
				req.Workspace = &CandidateWorkspace{Version: 1}
				req.Permission.Mode = "default"
			}
			inv, err := BuildInvocation(req)
			if err != nil {
				t.Fatal(err)
			}
			args := inv.Args()
			denyCount := 0
			permissionMode := "default"
			for i, arg := range args {
				if arg == "--disallowed-tools" {
					denyCount++
					if i+1 >= len(args) || args[i+1] != "Agent,Task" {
						t.Fatalf("native delegation deny missing: %v", args)
					}
				}
				if arg == "--permission-mode" && i+1 < len(args) {
					permissionMode = args[i+1]
				}
				if arg == "--allowedTools" || arg == "--allowed-tools" {
					t.Fatal("profile expanded tool permissions")
				}
			}
			if denyCount != 1 {
				t.Fatalf("native delegation deny count=%d, want 1; args=%v", denyCount, args)
			}
			wantMode := "default"
			if role == Reviewer {
				wantMode = "plan"
			}
			if permissionMode != wantMode {
				t.Fatalf("permission mode=%s want=%s", permissionMode, wantMode)
			}
		})
	}
}

func TestClaudeTypedProfileRequiresNativeDelegationDenyCapability(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
	base := "--input-format --output-format --permission-mode"
	for _, help := range []string{base, base + " --disallowed-tools-extra"} {
		if err := CheckCapabilities(req, help); err == nil {
			t.Fatal("typed Claude accepted without native delegation deny capability")
		}
	}
	if err := CheckCapabilities(req, base+" --disallowed-tools <tools...>"); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeTypedDelegationDenyCannotBeOverridden(t *testing.T) {
	for _, kind := range []string{"allow", "deny", "extra_args"} {
		t.Run(kind, func(t *testing.T) {
			req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
			switch kind {
			case "allow":
				req.Permission.Allow = []string{"Agent", "Task"}
			case "deny":
				req.Permission.Deny = []string{"Bash"}
			case "extra_args":
				req.ExtraArgs = []string{"--disallowed-tools", ""}
			}
			if _, err := BuildInvocation(req); err == nil {
				t.Fatal("caller overrode fixed typed Worker restrictions")
			}
		})
	}
}

func TestWriteProfileRequiresManagedWorkspace(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"implementer","permission":"workspace-write","timeout_ms":9000}`)
	req.Permission.Mode = "default"
	if _, err := BuildInvocation(req); err == nil || err.Error() != "managed_workspace_required" {
		t.Fatalf("unmanaged write: %v", err)
	}
}
