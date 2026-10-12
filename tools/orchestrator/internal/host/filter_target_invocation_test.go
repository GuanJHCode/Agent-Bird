package host

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"strings"
	"testing"
)

func TestFilterTargetInvocationHasNoExternalCommand(t *testing.T) {
	for _, mode := range []string{"empty", "command", "stdin", "environment", "pin"} {
		t.Run(mode, func(t *testing.T) {
			p := filterReviewGuardPayload(t)
			p.Kind = "candidate"
			p.Provider = ""
			p.Profile = nil
			p.ProviderLock = nil
			p.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "filter_target_preflight", SourceTask: "review", DependencyInputVersion: 1, AutoDirectory: true, Target: adapter.CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: strings.Repeat("a", 40)}}
			inv := filterInvocation{payload: p}
			switch mode {
			case "command":
				inv.argv = []string{"/usr/bin/true"}
			case "stdin":
				inv.stdin = []byte("unexpected")
			case "environment":
				inv.env = map[string]string{"UNEXPECTED": "1"}
			case "pin":
				inv.pinPath = "/private/tool"
				inv.pinSHA = strings.Repeat("a", 64)
			}
			_, err := freezeFilterInvocation(inv, p)
			if (err == nil) != (mode == "empty") {
				t.Fatalf("mode=%s freeze=%v", mode, err)
			}
		})
	}
}
