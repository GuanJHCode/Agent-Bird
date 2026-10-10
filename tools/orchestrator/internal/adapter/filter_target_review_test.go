package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFilterTargetReviewAdmission(t *testing.T) {
	for _, mode := range []string{"claude", "agy", "zero_target", "content_review", "write", "process_free", "command", "source_vector", "continuation"} {
		t.Run(mode, func(t *testing.T) {
			p := filterPipelineProducerFixture()
			p.Kind, p.CandidateWorkspace = "", nil
			p.Profile.Role, p.Profile.Permission = Reviewer, ReadOnly
			p.CandidateAction = &CandidateAction{Version: 1, Operation: "filter_target_review", SourceTask: "preflight", DependencyInputVersion: 1, AutoDirectory: true, Target: CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: strings.Repeat("a", 40)}}
			switch mode {
			case "agy":
				p.Provider = string(ProviderAGY)
				p.ProviderLock.Provider = ProviderAGY
				p.ProviderLock.Protocol = ProtocolID(ProviderAGY)
			case "zero_target":
				p.CandidateAction.Target = CandidateTarget{}
			case "content_review":
				p.CandidateAction.Operation = "review"
			case "write":
				p.Profile.Permission = WorkspaceWrite
			case "process_free":
				p.Kind = "candidate"
				p.Provider = ""
				p.Profile = nil
				p.ProviderLock = nil
			case "command":
				p.CandidateAction.Command = []string{"/private/extra"}
			case "source_vector":
				p.CandidateAction.OrderedSourceTasks = []string{"one", "two"}
			case "continuation":
				p.SessionID = "old"
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeInvocationPayload(raw)
			if err == nil {
				err = ValidateGitFilterPipelinePayload(decoded)
			}
			ok := mode == "claude" || mode == "agy"
			if (err == nil) != ok {
				t.Fatalf("mode=%s error=%v wantValid=%t", mode, err, ok)
			}
		})
	}
}
