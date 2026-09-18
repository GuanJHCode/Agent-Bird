package adapter

import (
	"strings"
	"testing"
)

func TestCodingProfilesKeepManagedWorkspaceAndNativePermissions(t *testing.T) {
	for _, provider := range []Provider{ProviderAGY, ProviderGrok} {
		t.Run(string(provider), func(t *testing.T) {
			req := profileRequest(t, `{"version":1,"role":"implementer","permission":"workspace-write","timeout_ms":9000}`)
			req.Provider = provider
			req.Permission = Permission{}
			req.Lock.Provider, req.Lock.Protocol = provider, ProtocolID(provider)
			if provider == ProviderGrok {
				req.Binary.Version = "grok 1.0.34 (3736acbc8658)"
				req.Lock.Binary = req.Binary
				req.Profile.GrokSessionWrite = true
			}
			if _, err := BuildInvocation(req); err == nil || err.Error() != "managed_workspace_required" {
				t.Fatalf("unmanaged coding accepted: %v", err)
			}
			req.Workspace = &CandidateWorkspace{Version: 1}
			inv, err := BuildInvocation(req)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Join(inv.Args(), " ")
			want := "--mode accept-edits"
			if provider == ProviderGrok {
				want = "--permission-mode acceptEdits"
			}
			if !strings.Contains(args, want) {
				t.Fatalf("missing native edit mode: %s", args)
			}
			for _, forbidden := range []string{"bypassPermissions", "--always-approve", "--allow ", "--yolo"} {
				if strings.Contains(args, forbidden) {
					t.Fatalf("expanded permissions: %s", args)
				}
			}
			if provider == ProviderGrok && (!strings.Contains(args, "--no-subagents") || !strings.Contains(args, "--deny MCPTool(*)") || !strings.Contains(args, "read_file,list_dir,grep,search_replace")) {
				t.Fatalf("coding tool boundary missing: %s", args)
			}
		})
	}
}

func TestLegacyPermissionCannotOptIntoCodingMode(t *testing.T) {
	for _, tc := range []struct {
		provider Provider
		mode     string
	}{{ProviderGrok, "acceptEdits"}, {ProviderAGY, "accept-edits"}} {
		req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":1000}`)
		req.Provider = tc.provider
		req.Profile = nil
		req.Lock = nil
		req.Permission = Permission{Mode: tc.mode}
		if _, err := BuildInvocation(req); err == nil {
			t.Fatalf("legacy permission expanded for %s", tc.provider)
		}
	}
}
