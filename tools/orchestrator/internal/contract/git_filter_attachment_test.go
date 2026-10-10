package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestGitFilterAttachmentIntentAndLegacyBytes(t *testing.T) {
	c := LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", HostID: "host", CommandID: "command", WorkRevision: 1, PlanRevision: 1, SlotToken: "slot", AdapterPayload: json.RawMessage(`{"kind":"fixture"}`)}
	t.Run("legacy", func(t *testing.T) {
		raw, _ := json.Marshal(struct {
			Plan    int
			Payload json.RawMessage
			Input   *AcceptedInput
		}{c.PlanRevision, c.AdapterPayload, c.Input})
		sum := sha256.Sum256([]byte(c.RunID + "\x00" + c.TaskID + "\x00" + c.AttemptID + "\x00" + c.SegmentID + "\x00" + c.SlotToken + "\x00" + string(raw)))
		got, err := CandidateIntentHash(c)
		if err != nil || got != hex.EncodeToString(sum[:]) {
			t.Fatal(got, err)
		}
		wire, _ := json.Marshal(c)
		if strings.Contains(string(wire), "git_filter") {
			t.Fatal("nil field changed wire")
		}
		event, _ := json.Marshal(Event{})
		if strings.Contains(string(event), "git_filter") {
			t.Fatal("nil cleanup changed wire")
		}
	})
	m := filterManifestFixture()
	payload := struct {
		Kind   string              `json:"kind"`
		Action any                 `json:"candidate_action"`
		Filter GitFilterManifestV1 `json:"git_filter"`
	}{"candidate", struct {
		Version   int    `json:"version"`
		Operation string `json:"operation"`
	}{1, "validate"}, m}
	c.AdapterPayload, _ = json.Marshal(payload)
	hs, _ := HostArtifactScopeSHA256(c)
	c.HostArtifacts = &HostArtifactGrant{1, "terminal", "progress", hs}
	digest, _ := m.Digest()
	scope, _ := GitFilterResourceScopeSHA256(c, digest)
	c.GitFilter = &GitFilterResourceGrant{1, "filter", scope, digest, m.Clean.SHA256, m.Smudge.SHA256, GitFilterToolMaximumBytes, GitFilterStreamMaximumBytes, GitFilterStreamSlots}
	var err error
	c.LaunchIntentHash, err = CandidateIntentHash(c)
	if err != nil {
		t.Fatal(err)
	}
	if ValidateDependencyInputs(c) != nil {
		t.Fatal("valid filter failed")
	}
	for _, mode := range []string{"missing", "foreign", "caps", "slot", "scope", "manifest", "extra"} {
		t.Run(mode, func(t *testing.T) {
			next := c
			g := *c.GitFilter
			next.GitFilter = &g
			switch mode {
			case "missing":
				next.GitFilter = nil
			case "foreign":
				g.ReservationID = "foreign"
			case "caps":
				g.StreamSlots--
			case "slot":
				g.ReservationID = c.HostArtifacts.TerminalReservationID
			case "scope":
				g.ScopeSHA256 = strings.Repeat("b", 64)
			case "manifest":
				g.ManifestSHA256 = strings.Repeat("c", 64)
			case "extra":
				next.AdapterPayload = json.RawMessage(`{"kind":"fixture"}`)
			}
			if ValidateDependencyInputs(next) == nil {
				t.Fatal("changed attachment accepted")
			}
		})
	}
	t.Run("parallel_validation_tool", func(t *testing.T) {
		next := c
		next.AdapterPayload = json.RawMessage(strings.Replace(string(c.AdapterPayload), `"operation":"validate"`, `"operation":"validate","command_sha256":"`+strings.Repeat("a", 64)+`"`, 1))
		scope, _ := ValidationToolScopeSHA256(next, strings.Repeat("a", 64))
		next.ValidationTool = &ValidationToolGrant{1, "validator", scope, strings.Repeat("a", 64), ValidationToolMaximumBytes}
		next.LaunchIntentHash, err = CandidateIntentHash(next)
		if err != nil || ValidateDependencyInputs(next) != nil {
			t.Fatal("independent validator grant rejected", err)
		}
		next.GitFilter = &GitFilterResourceGrant{}
		*next.GitFilter = *c.GitFilter
		next.GitFilter.ReservationID = next.ValidationTool.ReservationID
		if RequireGitFilter(next) == nil {
			t.Fatal("borrowed validator slot")
		}
	})
	t.Run("new_field_wire", func(t *testing.T) {
		for _, raw := range []string{`{"git_filter":null}`, strings.Replace(string(c.AdapterPayload), `"git_filter"`, `"Git_Filter"`, 1), strings.TrimSuffix(string(c.AdapterPayload), "}") + `,"git_filter":null}`} {
			if _, err := GitFilterManifestFromPayload([]byte(raw)); err == nil {
				t.Fatal("bad filter wire accepted", raw)
			}
		}
	})
}
