package adapter

import (
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestValidateGitFilterPipelinePayload(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*InvocationPayload)
	}{
		{"producer", func(*InvocationPayload) {}},
		{"validator", func(p *InvocationPayload) { *p = filterPipelineValidatorFixture() }},
		{"validator_target", func(p *InvocationPayload) {
			*p = filterPipelineValidatorFixture()
			p.CandidateAction.Target.Ref = "refs/heads/main"
		}},
		{"producer_rework", func(p *InvocationPayload) { p.CandidateWorkspace.Rework = &ReworkContext{Version: 1} }},
		{"producer_session", func(p *InvocationPayload) { p.SessionID = "prior-session" }},
		{"noncanonical_directory", func(p *InvocationPayload) { p.Directory = "/private/tmp/../tmp/filter-work" }},
		{"auto_directory_disabled", func(p *InvocationPayload) { p.CandidateWorkspace.AutoDirectory = false }},
		{"bad_provider_pin", func(p *InvocationPayload) { p.ProviderLock.Binary.SHA256 = "bad" }},
		{"case_alias_path", func(p *InvocationPayload) { p.CandidateWorkspace.Paths = []string{"FILE", "file"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := filterPipelineProducerFixture()
			tc.edit(&payload)
			err := ValidateGitFilterPipelinePayload(payload)
			wantOK := tc.name == "producer" || tc.name == "validator"
			if (err == nil) != wantOK {
				t.Fatalf("ValidateGitFilterPipelinePayload() error=%v wantOK=%t", err, wantOK)
			}
		})
	}
}

func filterPipelineProducerFixture() InvocationPayload {
	manifest := filterPipelineManifestFixture()
	return InvocationPayload{
		Kind:      "provider",
		Provider:  string(ProviderClaude),
		Directory: "/private/tmp/filter-work",
		GitFilter: &manifest,
		CandidateWorkspace: &CandidateWorkspace{
			Version: 1, AutoDirectory: true, RepoRoot: "/private/tmp/repo",
			BaseOID: strings.Repeat("a", 40), Paths: []string{"file"},
		},
		Profile: &ExecutionProfile{Version: 1, Role: Implementer, Permission: WorkspaceWrite, TimeoutMS: 1},
		ProviderLock: &ProviderLock{Version: 1, Provider: ProviderClaude, Protocol: ProtocolID(ProviderClaude), Binary: BinaryPin{
			Path: "/private/tmp/claude", Version: "fixture", SHA256: strings.Repeat("b", 64),
		}},
	}
}

func filterPipelineValidatorFixture() InvocationPayload {
	manifest := filterPipelineManifestFixture()
	return InvocationPayload{
		Kind: "candidate", Directory: "/private/tmp/filter-validate", GitFilter: &manifest,
		CandidateAction: &CandidateAction{Version: 1, Operation: "validate", DependencyInputVersion: 1, AutoDirectory: true,
			SourceTask: "implement", Command: []string{"/private/tmp/validator"}, CommandSHA256: strings.Repeat("c", 64)},
	}
}

func filterPipelineManifestFixture() contract.GitFilterManifestV1 {
	sha := strings.Repeat("d", 64)
	return contract.GitFilterManifestV1{Version: 1, DriverID: "sealed", Paths: []string{"file"},
		Git:        contract.GitFilterGitPin{Path: "/usr/bin/git", Version: "git fixture", SHA256: sha},
		Clean:      contract.GitFilterToolPin{Path: "/private/tmp/filter", SHA256: sha, Args: []string{"xor"}},
		Smudge:     contract.GitFilterToolPin{Path: "/private/tmp/filter", SHA256: sha, Args: []string{"xor"}},
		Attributes: []contract.GitFilterAttributePin{}, ConfigSHA256: sha}
}
