package adapter

import (
	"strings"
	"testing"
)

func TestClaudePublishedSeriesIncludeCurrentAndPriorFamilies(t *testing.T) {
	versions := PublishedVersions(ProviderClaude)
	if len(versions) != 405 {
		t.Fatalf("frozen Claude releases=%d, want 405", len(versions))
	}
	for _, version := range []string{"1.0.61", "1.0.108", "1.0.128", "2.0.0", "2.0.77", "2.1.0", "2.1.317"} {
		if err := AdmitVersion(ProviderClaude, version+" (Claude Code)"); err != nil {
			t.Errorf("published family %s: %v", version, err)
		}
	}
}

func TestClaudeBaseStreamContractDoesNotRequirePartialMessages(t *testing.T) {
	for _, version := range []string{"1.0.61", "2.0.0", "2.1.267", "2.1.317"} {
		args := invocationArgs(t, ProviderClaude, version+" (Claude Code)")
		for _, arg := range args {
			if arg == "--include-partial-messages" {
				t.Fatalf("%s requires optional partial events", version)
			}
		}
	}
}

func TestClaudeHistoricalDenyFlagKeepsPositiveToolsGate(t *testing.T) {
	for _, tc := range []struct{ version, flag string }{{"1.0.61", "--disallowedTools"}, {"1.0.85", "--disallowedTools"}, {"1.0.86", "--disallowed-tools"}, {"2.0.0", "--disallowed-tools"}, {"2.1.317", "--disallowed-tools"}} {
		t.Run(tc.version, func(t *testing.T) {
			req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000}`)
			req.Binary.Version = tc.version + " (Claude Code)"
			req.Lock.Binary = req.Binary
			inv, err := BuildInvocation(req)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Join(inv.Args(), " ")
			if !strings.Contains(args, tc.flag+" Bash,Agent,Task,WebFetch,WebSearch,mcp__*,Edit,MultiEdit,Write,NotebookEdit") || !strings.Contains(args, "--tools Read,Glob,Grep") {
				t.Fatalf("unsafe historical invocation: %s", args)
			}
			help := "--output-format --input-format --permission-mode " + tc.flag
			if err := CheckCapabilities(req, help); err == nil || err.Error() != "provider_capability_unsupported" {
				t.Fatalf("missing --tools admitted: %v", err)
			}
			if err := CheckCapabilities(req, help+" --tools"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClaudeRestoredCohortRemainsTypedReadonly(t *testing.T) {
	for _, version := range []string{"1.0.61", "1.0.128", "2.0.0", "2.0.77", "2.1.0", "2.1.266"} {
		t.Run(version, func(t *testing.T) {
			base := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000}`)
			base.Binary.Version = version + " (Claude Code)"
			base.Lock.Binary = base.Binary
			for _, kind := range []string{"legacy", "implementer", "workspace", "action"} {
				req := base
				profile := *base.Profile
				req.Profile = &profile
				switch kind {
				case "legacy":
					req.Profile = nil
				case "implementer":
					req.Profile.Role = Implementer
					req.Profile.Permission = WorkspaceWrite
					req.Workspace = &CandidateWorkspace{Version: 1}
				case "workspace":
					req.Workspace = &CandidateWorkspace{Version: 1}
				case "action":
					req.Action = &CandidateAction{Version: 1, Operation: "review"}
				}
				expected := "claude_legacy_typed_readonly_required"
				if kind == "workspace" {
					expected = "candidate_workspace_profile_invalid"
				}
				if _, err := BuildInvocation(req); err == nil || err.Error() != expected {
					t.Fatalf("%s %s=%v", version, kind, err)
				}
			}
		})
	}
}

func TestRestoredCatalogDoesNotGrantManagedWrites(t *testing.T) {
	known := map[string]string{"2.1.267": "a681f3008f0050029aeebcab3af51bb6a55ddeb625a3af3141a4416d43cd2558", "2.1.316": "64c327e92b2c5e0f78c0a980c5a518121e1f0d140ca771ca5dd4488d1a2dcb3f"}
	for _, version := range PublishedVersions(ProviderClaude) {
		digest := known[version]
		if digest == "" {
			digest = strings.Repeat("a", 64)
		}
		err := ClaudeGatewayWorkspaceWriteAdmission(version+" (Claude Code)", digest)
		if (known[version] != "") != (err == nil) {
			t.Fatalf("Claude write %s=%v", version, err)
		}
		if err := ClaudeGatewayWorkspaceWriteAdmission(version+" (Claude Code)", strings.Repeat("b", 64)); err == nil {
			t.Fatalf("unknown digest admitted %s", version)
		}
	}
	if len(CodexNativePins()) != 21 {
		t.Fatalf("Codex pins changed: %d", len(CodexNativePins()))
	}
	for raw, sha := range CodexNativePins() {
		if err := LaunchAdmission(ProviderCodex, raw, sha); err != nil {
			t.Fatal(err)
		}
		if err := CodexWorkspaceWriteAdmission(raw, sha); (raw == "codex-cli 0.166.0") != (err == nil) {
			t.Fatalf("Codex write %s=%v", raw, err)
		}
		if err := LaunchAdmission(ProviderCodex, raw, strings.Repeat("b", 64)); err == nil {
			t.Fatalf("unknown native digest %s", raw)
		}
	}
}
