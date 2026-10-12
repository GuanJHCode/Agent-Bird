package adapter

import (
	"encoding/json"
	"testing"
)

func TestFilteredReadonlyReviewAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*InvocationPayload)
		ok     bool
	}{
		{"claude", func(*InvocationPayload) {}, true},
		{"agy", func(p *InvocationPayload) {
			p.Provider = string(ProviderAGY)
			p.ProviderLock.Provider = ProviderAGY
			p.ProviderLock.Protocol = ProtocolID(ProviderAGY)
		}, true},
		{"target", func(p *InvocationPayload) { p.CandidateAction.Target.Ref = "refs/heads/main" }, false},
		{"write", func(p *InvocationPayload) { p.Profile.Permission = WorkspaceWrite }, false},
		{"plain_kind", func(p *InvocationPayload) { p.Kind = "provider" }, false},
		{"command", func(p *InvocationPayload) { p.CandidateAction.Command = []string{"/private/tool"} }, false},
		{"grok", func(p *InvocationPayload) {
			p.Provider = string(ProviderGrok)
			p.ProviderLock.Provider = ProviderGrok
			p.ProviderLock.Protocol = ProtocolID(ProviderGrok)
		}, false},
		{"indexed", func(p *InvocationPayload) { p.CandidateAction.ReviewIndex = &ReviewIndex{Version: 1} }, false},
		{"session", func(p *InvocationPayload) { p.SessionID = "prior" }, false},
		{"integrate", func(p *InvocationPayload) { p.Kind = "candidate"; p.CandidateAction.Operation = "integrate" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filterPipelineProducerFixture()
			p.Kind = ""
			p.CandidateWorkspace = nil
			p.Profile.Role = Reviewer
			p.Profile.Permission = ReadOnly
			p.CandidateAction = &CandidateAction{Version: 1, Operation: "review", SourceTask: "validate", DependencyInputVersion: 1, AutoDirectory: true}
			tc.change(&p)
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeInvocationPayload(raw)
			if err == nil {
				err = ValidateGitFilterPipelinePayload(decoded)
			}
			if (err == nil) != tc.ok {
				t.Fatalf("admission=%v wantOK=%t", err, tc.ok)
			}
		})
	}
}
