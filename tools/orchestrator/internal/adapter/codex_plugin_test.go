package adapter

import "testing"

func TestCodexPluginReadBackendIsExplicitAndFresh(t *testing.T) {
	req := codexManagedRequestFixture()
	req.Profile.CodexHostToolsVersion = 1
	inv, err := BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, ok := ManagedCodexDescriptorFor(inv)
	if !ok || descriptor.Backend != CodexPluginBackend || inv.ExecutionProfile().CodexHostToolsVersion != 1 {
		t.Fatal("plugin backend missing")
	}
	if _, err := BuildManagedCodexInvocation(req); err == nil {
		t.Fatal("legacy constructor silently gained tools")
	}
	for _, kind := range []string{"write", "version", "resume", "pin", "provider"} {
		t.Run(kind, func(t *testing.T) {
			r := codexManagedRequestFixture()
			r.Profile.CodexHostToolsVersion = 1
			switch kind {
			case "write":
				r.Profile.Role = Implementer
				r.Profile.Permission = WorkspaceWrite
			case "version":
				r.Profile.CodexHostToolsVersion = 2
			case "resume":
				r.Session = SessionRef{Kind: SessionID, ID: "550e8400-e29b-41d4-a716-446655440000"}
			case "pin":
				r.Binary.SHA256 = "wrong"
			case "provider":
				r.Provider = ProviderClaude
			}
			if _, err := BuildInvocation(r); err == nil {
				t.Fatal("expanded plugin contract admitted")
			}
		})
	}
}
