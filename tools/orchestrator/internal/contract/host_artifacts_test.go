package contract

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func withHostArtifactsForTest(t *testing.T, g LaunchCommand) LaunchCommand {
	t.Helper()
	scope, err := HostArtifactScopeSHA256(g)
	if err != nil {
		t.Fatal(err)
	}
	g.HostArtifacts = &HostArtifactGrant{Version: 1, TerminalReservationID: "terminal-reservation", ProgressReservationID: "progress-reservation", ScopeSHA256: scope}
	return g
}

func TestOwnerLaunchRequiresHostArtifactGrant(t *testing.T) {
	_, permit, key := ownerWirePermit(t)
	g := LaunchCommand{CommandID: "command", HostID: "producer", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, ExecutionEpoch: permit.CoordinatorEpoch, GrantedActiveMS: 1000}
	raw, _ := json.Marshal(g)
	if _, err := SignOwnerCommand(OwnerCommand{Version: 1, Permit: permit, Kind: "launch", RequestID: g.CommandID, Body: raw}, key); err == nil {
		t.Fatal("signed live launch granted execution without artifact reservations")
	}
	g = withHostArtifactsForTest(t, g)
	raw, _ = json.Marshal(g)
	signed, err := SignOwnerCommand(OwnerCommand{Version: 1, Permit: permit, Kind: "launch", RequestID: g.CommandID, Body: raw}, key)
	if err != nil || VerifyOwnerCommand(signed, key.Public().(ed25519.PublicKey), permit) != nil {
		t.Fatal("complete Host artifact authority did not survive signed union", err)
	}
}

func TestHostArtifactGrantStrictWireAndScope(t *testing.T) {
	g := withHostArtifactsForTest(t, LaunchCommand{CommandID: "command", HostID: "producer", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1})
	raw, _ := json.Marshal(g.HostArtifacts)
	for name, body := range map[string][]byte{
		"null":            []byte("null"),
		"unknown_limit":   append([]byte(`{"max_bytes":99999999,`), raw[1:]...),
		"duplicate":       bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		"alias":           bytes.Replace(raw, []byte(`"scope_sha256"`), []byte(`"Scope_sha256"`), 1),
		"version":         bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1),
		"class_collision": bytes.Replace(raw, []byte("progress-reservation"), []byte("terminal-reservation"), 1),
		"unsafe_id":       bytes.Replace(raw, []byte("terminal-reservation"), []byte("../terminal"), 1),
	} {
		t.Run(name, func(t *testing.T) {
			var decoded HostArtifactGrant
			if json.Unmarshal(body, &decoded) == nil {
				t.Fatal("invalid descriptor decoded", name)
			}
		})
	}
	for _, field := range []string{"run", "task", "attempt", "segment", "producer", "command", "revision", "scope"} {
		t.Run(field, func(t *testing.T) {
			changed := g
			copy := *g.HostArtifacts
			changed.HostArtifacts = &copy
			switch field {
			case "run":
				changed.RunID += "-other"
			case "task":
				changed.TaskID += "-other"
			case "attempt":
				changed.AttemptID += "-other"
			case "segment":
				changed.SegmentID += "-other"
			case "producer":
				changed.HostID += "-other"
			case "command":
				changed.CommandID += "-other"
			case "revision":
				changed.WorkRevision++
			case "scope":
				copy.ScopeSHA256 = strings.Repeat("f", 64)
			}
			if RequireHostArtifacts(changed) == nil {
				t.Fatal("grant applied to a different original scope")
			}
		})
	}
	g.ExecutionEpoch++
	if err := RequireHostArtifacts(g); err != nil {
		t.Fatal("same original scope lost authority on reconnect", err)
	}
}

func TestCandidateIntentBindsHostArtifactAuthority(t *testing.T) {
	g := LaunchCommand{CommandID: "command", HostID: "producer", RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, SlotToken: "slot", PlanRevision: 1, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}
	legacy, err := CandidateIntentHash(g)
	if err != nil {
		t.Fatal(err)
	}
	g = withHostArtifactsForTest(t, g)
	bound, err := CandidateIntentHash(g)
	if err != nil || legacy == bound {
		t.Fatal("new authority omitted from candidate intent", err)
	}
	g.HostArtifacts.TerminalReservationID += "-changed"
	changed, err := CandidateIntentHash(g)
	if err != nil || changed == bound {
		t.Fatal("reservation mutation kept candidate intent", err)
	}
	g.HostArtifacts = nil
	again, err := CandidateIntentHash(g)
	if err != nil || again != legacy {
		t.Fatal("historical nil-grant intent was rewritten", err)
	}
}
