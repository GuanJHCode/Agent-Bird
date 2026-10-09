package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidationToolIntentBindsIndependentAuthority(t *testing.T) {
	c := LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", HostID: "host", CommandID: "command", WorkRevision: 1, PlanRevision: 1, SlotToken: "slot", AdapterPayload: json.RawMessage(`{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","command_sha256":"` + strings.Repeat("a", 64) + `"}}`)}
	hostScope, err := HostArtifactScopeSHA256(c)
	if err != nil {
		t.Fatal(err)
	}
	c.HostArtifacts = &HostArtifactGrant{Version: 1, TerminalReservationID: "terminal", ProgressReservationID: "progress", ScopeSHA256: hostScope}
	scope, err := ValidationToolScopeSHA256(c, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	c.ValidationTool = &ValidationToolGrant{Version: 1, ReservationID: "tool", ScopeSHA256: scope, ToolSHA256: strings.Repeat("a", 64), MaximumBytes: ValidationToolMaximumBytes}
	c.LaunchIntentHash, err = CandidateIntentHash(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateDependencyInputs(c); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"replace_id", "missing", "terminal_reuse", "other_tool", "scope", "maximum"} {
		t.Run(mode, func(t *testing.T) {
			next := c
			g := *c.ValidationTool
			next.ValidationTool = &g
			switch mode {
			case "replace_id":
				g.ReservationID = "different"
			case "missing":
				next.ValidationTool = nil
			case "terminal_reuse":
				g.ReservationID = c.HostArtifacts.TerminalReservationID
			case "other_tool":
				g.ToolSHA256 = strings.Repeat("b", 64)
			case "scope":
				g.ScopeSHA256 = strings.Repeat("c", 64)
			case "maximum":
				g.MaximumBytes--
			}
			if ValidateDependencyInputs(next) == nil {
				t.Fatal("changed tool authority kept old launch intent")
			}
		})
	}
}
