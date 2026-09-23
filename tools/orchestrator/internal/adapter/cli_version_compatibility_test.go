package adapter

import (
	"strings"
	"testing"
)

// Version coverage is additive: accepting a new CLI must not invalidate a
// previously confirmed lock or relax the task's permission/capability gates.
func TestHistoricalAndCurrentCLIProfiles(t *testing.T) {
	for _, tc := range []struct {
		provider Provider
		version  string
		help     string
	}{
		{ProviderGrok, "grok 1.0.34 (3736acbc8658)", "--output-format --permission-mode --no-subagents --disable-web-search --session-id --leader-socket --tools --deny --allow --json-schema --prompt-file --model --effort"},
		{ProviderGrok, "grok 1.0.40 (eb1a2256660d)", "--output-format --permission-mode --no-subagents --disable-web-search --session-id --leader-socket --tools --deny --allow --json-schema --prompt-file --model --effort"},
		{ProviderAGY, "1.2.5", "--input-format --output-format --mode --json-schema --model --effort"},
		{ProviderAGY, "1.2.7", "--input-format --output-format --mode --json-schema --model --effort"},
	} {
		t.Run(string(tc.provider)+"/"+tc.version, func(t *testing.T) {
			req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
			req.Provider, req.Binary.Version = tc.provider, tc.version
			req.Lock.Provider, req.Lock.Protocol, req.Lock.Binary = tc.provider, ProtocolID(tc.provider), req.Binary
			req.Profile.GrokSessionWrite = tc.provider == ProviderGrok
			originalLock := *req.Lock
			for _, mode := range []string{"read", "review", "implement"} {
				t.Run(mode, func(t *testing.T) {
					if mode == "review" {
						req.Action = &CandidateAction{Version: 1, Operation: "review", SourceTask: "candidate"}
					} else if mode == "implement" {
						req.Action = nil
						req.Profile.Role, req.Profile.Permission = Implementer, WorkspaceWrite
						req.Permission = Permission{}
						if _, err := BuildInvocation(req); err == nil || err.Error() != "managed_workspace_required" {
							t.Fatalf("unmanaged coding: %v", err)
						}
						req.Workspace = &CandidateWorkspace{Version: 1}
					}
					if err := CheckCapabilities(req, tc.help); err != nil {
						t.Fatal(err)
					}
					inv, err := BuildInvocation(req)
					if err != nil {
						t.Fatal(err)
					}
					if inv.Pin() != originalLock.Binary || *req.Lock != originalLock {
						t.Fatal("version compatibility rewrote a confirmed binary/lock")
					}
					if strings.Contains(strings.Join(inv.Args(), " "), "--model ") {
						t.Fatal("CLI default model was overridden")
					}
					guard := "--mode"
					if tc.provider == ProviderGrok {
						guard = "--no-subagents"
						if !strings.Contains(strings.Join(inv.Args(), " "), "--deny MCPTool(*)") {
							t.Fatal("MCP guard was removed")
						}
					}
					if err := CheckCapabilities(req, strings.ReplaceAll(tc.help, guard, "")); err == nil || err.Error() != "provider_capability_unsupported" {
						t.Fatalf("missing permission/delegation capability: %v", err)
					}
				})
			}
			req.Profile.Model, req.Profile.Reasoning = "explicit-model", "low"
			inv, err := BuildInvocation(req)
			if err != nil {
				t.Fatal(err)
			}
			if args := strings.Join(inv.Args(), " "); !strings.Contains(args, "--model explicit-model") || !strings.Contains(args, "--effort low") {
				t.Fatalf("explicit model settings lost: %s", args)
			}
			if tc.provider == ProviderGrok {
				req.Profile.GrokSessionWrite = false
				if _, err := BuildInvocation(req); err == nil || err.Error() != "grok_session_write_required" {
					t.Fatalf("version compatibility granted session writes: %v", err)
				}
			}
		})
	}
}

func TestGrokCompatibilityRejectsUnknownBuildAndChangedLock(t *testing.T) {
	for _, version := range []string{"grok 1.0.33 (unknown)", "grok 1.0.40 (unknown)", "grok 1.0.41 (unknown)"} {
		t.Run(version, func(t *testing.T) {
			req := grokProfileRequest(t)
			req.Binary.Version = version
			req.Lock.Binary = req.Binary
			if _, err := BuildInvocation(req); err == nil || err.Error() != "grok_version_not_verified" {
				t.Fatalf("unknown CLI build: %v", err)
			}
		})
	}
	for _, version := range []string{"grok 1.0.34 (3736acbc8658)", "grok 1.0.40 (eb1a2256660d)"} {
		req := grokProfileRequest(t)
		req.Binary.Version = version
		req.Lock.Binary = req.Binary
		req.Binary.SHA256 = strings.Repeat("b", 64)
		if _, err := BuildInvocation(req); err == nil || err.Error() != "provider_lock_invalid" {
			t.Fatalf("changed %s lock: %v", version, err)
		}
	}
}
