package host

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"testing"
)

func filterReviewGuardPayload(t *testing.T) adapter.InvocationPayload {
	_, p := filterGuardGrant(t)
	p.Kind = ""
	p.CandidateWorkspace = nil
	p.Profile.Role = adapter.Reviewer
	p.Profile.Permission = adapter.ReadOnly
	p.CandidateAction = &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "validator", DependencyInputVersion: 1, AutoDirectory: true}
	return p
}

func TestFilteredReviewNativeInvocationRequired(t *testing.T) {
	for _, mode := range []string{"native", "plain", "wrong_provider", "completion", "pin", "argv", "write"} {
		t.Run(mode, func(t *testing.T) {
			p := filterReviewGuardPayload(t)
			inv := filterInvocation{argv: []string{p.ProviderLock.Binary.Path}, payload: p, pinPath: p.ProviderLock.Binary.Path, pinSHA: p.ProviderLock.Binary.SHA256, output: p.Provider}
			switch mode {
			case "plain":
				inv.output = ""
			case "wrong_provider":
				inv.output = string(adapter.ProviderAGY)
			case "completion":
				inv.completion = adapter.TaskCompletionV1
			case "pin":
				inv.pinSHA = "bad"
			case "argv":
				inv.argv[0] = "/other"
			case "write":
				prof := *p.Profile
				prof.Permission = adapter.WorkspaceWrite
				inv.payload.Profile = &prof
			}
			_, err := freezeFilterInvocation(inv, p)
			if (err == nil) != (mode == "native") {
				t.Fatalf("freeze=%v mode=%s", err, mode)
			}
		})
	}
}
