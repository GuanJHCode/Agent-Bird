package adapter

import (
	"strings"
	"testing"
)

func TestCodexImplementerRequiresManagedWorkspaceAndExactNativePin(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"implementer","permission":"workspace-write","timeout_ms":9000}`)
	req.Permission.Mode = "default"
	req.Provider = ProviderCodex
	req.Binary = codexPinForTest()
	req.Binary.Version = "codex-cli 0.166.0"
	req.Binary.SHA256 = CodexNativePins()[req.Binary.Version]
	req.Lock = &ProviderLock{Version: 1, Provider: ProviderCodex, Protocol: ProtocolID(ProviderCodex), Binary: req.Binary}
	if _, err := BuildInvocation(req); err == nil || err.Error() != "managed_workspace_required" {
		t.Fatalf("unmanaged=%v", err)
	}
	req.Workspace = &CandidateWorkspace{Version: 1, Paths: []string{"CHANGE.txt"}}
	inv, err := BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(inv.Args(), " ")
	if strings.Contains(args, "--sandbox") || !strings.Contains(args, "--strict-config --ignore-rules") || inv.CandidateWorkspace() == nil {
		t.Fatalf("unsafe native argv=%s", args)
	}
	req.Binary.SHA256 = strings.Repeat("a", 64)
	req.Lock.Binary = req.Binary
	if _, err := BuildInvocation(req); err == nil {
		t.Fatal("unverified native write pin accepted")
	}
}

func TestClaudeImplementerToolsArePositiveAndCapabilityBound(t *testing.T) {
	req := profileRequest(t, `{"version":1,"role":"implementer","permission":"workspace-write","timeout_ms":9000}`)
	req.Permission.Mode = "default"
	req.Workspace = &CandidateWorkspace{Version: 1}
	inv, err := BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(inv.Args(), " ")
	if !strings.Contains(args, "--permission-mode acceptEdits") || !strings.Contains(args, "--tools Read,Glob,Grep,Edit,MultiEdit,Write,NotebookEdit") || !strings.Contains(args, "Bash,Agent,Task,WebFetch,WebSearch,mcp__*") {
		t.Fatalf("tools=%s", args)
	}
	help := "--output-format --input-format --permission-mode --disallowed-tools"
	if CheckCapabilities(req, help) == nil {
		t.Fatal("missing positive --tools capability accepted")
	}
	if err := CheckCapabilities(req, help+" --tools"); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []Provider{ProviderAGY, ProviderGrok} {
		req.Provider = provider
		req.Binary.Version = baselineRaw(provider)
		req.Lock = &ProviderLock{Version: 1, Provider: provider, Protocol: ProtocolID(provider), Binary: req.Binary}
		if _, err := BuildInvocation(req); err == nil {
			t.Fatalf("%s coding admitted", provider)
		}
	}
}

func TestManagedWriteAdmissionIsNarrowerThanReadonlyWindow(t *testing.T) {
	for raw, sha := range CodexNativePins() {
		if err := LaunchAdmission(ProviderCodex, raw, sha); err != nil {
			t.Fatal(err)
		}
		err := CodexWorkspaceWriteAdmission(raw, sha)
		if (raw == "codex-cli 0.166.0") != (err == nil) {
			t.Fatalf("write admission %s=%v", raw, err)
		}
	}
	for _, tc := range []struct {
		raw, sha string
		accept   bool
	}{
		{"2.1.267 (Claude Code)", "a681f3008f0050029aeebcab3af51bb6a55ddeb625a3af3141a4416d43cd2558", true},
		{"2.1.316 (Claude Code)", "64c327e92b2c5e0f78c0a980c5a518121e1f0d140ca771ca5dd4488d1a2dcb3f", true},
		{"2.1.300 (Claude Code)", strings.Repeat("a", 64), false},
		{"2.1.316 (Claude Code)", strings.Repeat("a", 64), false},
	} {
		if AdmitVersion(ProviderClaude, tc.raw) != nil {
			t.Fatal("read window changed")
		}
		err := ClaudeGatewayWorkspaceWriteAdmission(tc.raw, tc.sha)
		if (err == nil) != tc.accept {
			t.Fatalf("gateway write %s=%v", tc.raw, err)
		}
	}
}
