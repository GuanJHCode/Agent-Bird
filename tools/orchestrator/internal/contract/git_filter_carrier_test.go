package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func carrierGrantFixture(t *testing.T, operation string) LaunchCommand {
	t.Helper()
	m := filterManifestFixture()
	p := map[string]any{"kind": "candidate", "candidate_action": map[string]any{"version": 1, "operation": operation}, "git_filter": m}
	body, _ := json.Marshal(p)
	c := LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", HostID: "host", CommandID: "command", WorkRevision: 1, PlanRevision: 1, SlotToken: "slot", AdapterPayload: body}
	hs, _ := HostArtifactScopeSHA256(c)
	c.HostArtifacts = &HostArtifactGrant{1, "terminal", "progress", hs}
	md, _ := m.Digest()
	scope, _ := GitFilterResourceScopeSHA256(c, md)
	c.GitFilter = &GitFilterResourceGrant{1, "filter", scope, md, m.Clean.SHA256, m.Smudge.SHA256, GitFilterToolMaximumBytes, GitFilterStreamMaximumBytes, GitFilterStreamSlots}
	if operation == "filter_integrate" {
		scope, err := GitFilterCarrierScopeSHA256(c)
		if err != nil {
			t.Fatal(err)
		}
		c.GitFilterCarrier = &GitFilterCarrierGrant{1, "carrier", scope, c.GitFilter.ReservationID, c.GitFilter.ScopeSHA256, md, GitFilterCarrierMetadataBytes, GitFilterCarrierNamespace, 1, 1}
	}
	return c
}

func TestGitFilterCarrierIntentAndStrictWire(t *testing.T) {
	c := carrierGrantFixture(t, "filter_integrate")
	original, err := CandidateIntentHash(c)
	if err != nil {
		t.Fatal(err)
	}
	changed := c
	copy := *c.GitFilterCarrier
	changed.GitFilterCarrier = &copy
	copy.ReservationID = "another"
	h, err := CandidateIntentHash(changed)
	if err != nil || h == original {
		t.Fatalf("companion not bound: %v", err)
	}
	for _, mode := range []string{"missing", "scope", "manifest", "original", "budget", "extra-slot", "legacy-injection"} {
		t.Run(mode, func(t *testing.T) {
			x := c
			g := *c.GitFilterCarrier
			x.GitFilterCarrier = &g
			switch mode {
			case "missing":
				x.GitFilterCarrier = nil
			case "scope":
				g.ScopeSHA256 = strings.Repeat("e", 64)
			case "manifest":
				g.ManifestSHA256 = strings.Repeat("e", 64)
			case "original":
				g.FilterReservationID = "foreign"
			case "budget":
				g.MetadataBytes--
			case "extra-slot":
				g.SocketSlots++
			case "legacy-injection":
				x = carrierGrantFixture(t, "validate")
				x.GitFilterCarrier = &g
			}
			if _, err := CandidateIntentHash(x); err == nil {
				t.Fatal("invalid carrier admitted")
			}
		})
	}
	raw, _ := json.Marshal(c.GitFilterCarrier)
	for _, wire := range []string{strings.TrimSuffix(string(raw), "}") + `,"extra":1}`, strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1)} {
		var g GitFilterCarrierGrant
		if json.Unmarshal([]byte(wire), &g) == nil {
			t.Fatal("non-strict carrier")
		}
	}
	legacy := carrierGrantFixture(t, "validate")
	body, _ := json.Marshal(struct {
		Plan             int
		Payload          json.RawMessage
		Input            *AcceptedInput
		DependencyInputs *DependencyInputVector `json:",omitempty"`
		Diagnostics      *DiagnosticsGrant      `json:",omitempty"`
		HostArtifacts    *HostArtifactGrant
		ValidationTool   *ValidationToolGrant `json:",omitempty"`
		GitFilter        *GitFilterResourceGrant
	}{legacy.PlanRevision, legacy.AdapterPayload, legacy.Input, legacy.DependencyInputs, legacy.Diagnostics, legacy.HostArtifacts, legacy.ValidationTool, legacy.GitFilter})
	sum := sha256.Sum256([]byte(legacy.RunID + "\x00" + legacy.TaskID + "\x00" + legacy.AttemptID + "\x00" + legacy.SegmentID + "\x00" + legacy.SlotToken + "\x00" + string(body)))
	h, err = CandidateIntentHash(legacy)
	if err != nil || h != hex.EncodeToString(sum[:]) {
		t.Fatal("legacy bytes changed", err)
	}
}
