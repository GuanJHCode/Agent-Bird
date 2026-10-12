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

func TestCodexPluginWriteRequiresFreshOwnedCandidate(t *testing.T) {
	r := codexManagedRequestFixture()
	r.Profile.CodexHostToolsVersion = 1
	r.Profile.Role, r.Profile.Permission = Implementer, WorkspaceWrite
	r.Workspace = &CandidateWorkspace{Version: 1, AutoDirectory: true, RepoRoot: "/private/project", BaseOID: "0123456789012345678901234567890123456789", Paths: []string{"answer.txt"}}
	inv, err := BuildCodexPluginInvocation(r)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := ManagedCodexDescriptorFor(inv)
	if d.Backend != "codex-app-server-plugin-write-v1" || inv.CandidateWorkspace() == nil {
		t.Fatal("missing host candidate contract")
	}
	r.Workspace.Paths[0] = "mutated"
	if inv.CandidateWorkspace().Paths[0] != "answer.txt" {
		t.Fatal("mutable candidate alias")
	}
	r.Workspace.Rework = &ReworkContext{}
	if _, err = BuildCodexPluginInvocation(r); err == nil {
		t.Fatal("rework admitted")
	}
}

func TestCodexPluginCompatibilityModelDoesNotChangeLegacy(t *testing.T) {
	r := codexManagedRequestFixture()
	r.Profile.CodexHostToolsVersion = 1
	r.Profile.CLIModelDefault = false
	r.Profile.Model = CodexPluginCompatibilityModel
	if _, err := BuildCodexPluginInvocation(r); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildManagedCodexInvocation(r); err == nil {
		t.Fatal("legacy model override")
	}
	r.Profile.Model = "arbitrary"
	if _, err := BuildCodexPluginInvocation(r); err == nil {
		t.Fatal("unqualified model override")
	}
}
