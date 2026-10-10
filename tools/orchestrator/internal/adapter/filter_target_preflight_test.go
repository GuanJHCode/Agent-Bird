package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFilterTargetPreflightAdmission(t *testing.T) {
	for _, mode := range []string{"valid", "missing_target", "command", "provider", "ref_alias", "base", "review_stays_zero"} {
		t.Run(mode, func(t *testing.T) {
			p := filterPipelineValidatorFixture()
			p.CandidateAction.Operation = "filter_target_preflight"
			p.CandidateAction.SourceTask = "review"
			p.CandidateAction.Command = nil
			p.CandidateAction.CommandSHA256 = ""
			p.CandidateAction.Target = CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: strings.Repeat("a", 40)}
			switch mode {
			case "missing_target":
				p.CandidateAction.Target = CandidateTarget{}
			case "command":
				p.CandidateAction.Command = []string{"/usr/bin/true"}
			case "provider":
				p.Provider = "claude-code"
			case "ref_alias":
				p.CandidateAction.Target.Ref = "refs/heads/../other"
			case "base":
				p.CandidateAction.Target.BaseOID = "not-oid"
			case "review_stays_zero":
				p.CandidateAction.Operation = "review"
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeInvocationPayload(raw)
			if err == nil {
				err = ValidateGitFilterPipelinePayload(decoded)
			}
			if (err == nil) != (mode == "valid") {
				t.Fatalf("mode=%s admission=%v", mode, err)
			}
		})
	}
}
