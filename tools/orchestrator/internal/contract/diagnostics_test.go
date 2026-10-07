package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnosticsPolicyRequiresExplicitDetailedConsent(t *testing.T) {
	for _, input := range []string{`null`, `{}`, `{"version":0,"mode":"detailed"}`, `{"version":2,"mode":"detailed"}`, `{"version":1,"mode":"raw"}`, `{"version":1,"mode":"DETAILED"}`} {
		t.Run(input, func(t *testing.T) {
			var policy DiagnosticsPolicy
			if err := json.Unmarshal([]byte(input), &policy); err == nil {
				if _, err := policy.CanonicalJSON(); err == nil {
					t.Fatal("invalid policy became capture consent")
				}
			}
		})
	}
	for _, mode := range []string{"summary", "detailed"} {
		policy := DiagnosticsPolicy{Version: 1, Mode: mode}
		body, err := policy.CanonicalJSON()
		if err != nil || string(body) != `{"version":1,"mode":"`+mode+`"}` {
			t.Fatalf("canonical consent mode=%s body=%s err=%v", mode, body, err)
		}
	}
}

func TestDiagnosticsPolicyRejectsAmbiguousConsent(t *testing.T) {
	for _, input := range []string{
		`{"version":1,"mode":"summary","mode":"detailed"}`,
		`{"version":1,"version":1,"mode":"detailed"}`,
		`{"version":1,"mode":"detailed","stdin":true}`,
		`{"Version":1,"mode":"detailed"}`,
		`{"version":1,"mode":"detailed"} {}`,
	} {
		t.Run(input, func(t *testing.T) {
			var policy DiagnosticsPolicy
			if err := json.Unmarshal([]byte(input), &policy); err == nil {
				t.Fatal("ambiguous capture consent accepted")
			}
		})
	}
}

func TestDiagnosticsGrantBindsDetailedPolicyAndReservation(t *testing.T) {
	policy := DiagnosticsPolicy{Version: 1, Mode: "detailed"}
	body, err := policy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	valid := DiagnosticsGrant{Policy: policy, PolicySHA256: hex.EncodeToString(sum[:]), ReservationID: "artifact-reservation-1", SegmentOrdinal: 1}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"summary", "policy_hash", "empty_reservation", "reservation_path", "zero_ordinal"} {
		t.Run(change, func(t *testing.T) {
			grant := valid
			switch change {
			case "summary":
				grant.Policy.Mode = "summary"
			case "policy_hash":
				grant.PolicySHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
			case "empty_reservation":
				grant.ReservationID = ""
			case "reservation_path":
				grant.ReservationID = "../other-attempt"
			case "zero_ordinal":
				grant.SegmentOrdinal = 0
			}
			if err := grant.Validate(); err == nil {
				t.Fatal("changed capture grant accepted")
			}
		})
	}
}

func TestDiagnosticsGrantRejectsWireDriftWithoutChangingReceiver(t *testing.T) {
	policy := DiagnosticsPolicy{Version: 1, Mode: "detailed"}
	body, _ := policy.CanonicalJSON()
	sum := sha256.Sum256(body)
	original := DiagnosticsGrant{Policy: policy, PolicySHA256: hex.EncodeToString(sum[:]), ReservationID: "artifact-reservation-1", SegmentOrdinal: 1}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DiagnosticsGrant
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded != original {
		t.Fatalf("valid grant lost binding: %+v %v", decoded, err)
	}
	for _, input := range []string{
		`null`,
		`{}`,
		strings.Replace(string(raw), `,"segment_ordinal":1`, "", 1),
		strings.Replace(string(raw), `"segment_ordinal":1`, `"segment_ordinal":0`, 1),
		strings.Replace(string(raw), `"segment_ordinal":1`, `"segment_ordinal":-1`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"segment_ordinal":1}`,
		strings.Replace(string(raw), `"segment_ordinal"`, `"SegmentOrdinal"`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"path":"/tmp/foreign"}`,
		strings.TrimSuffix(string(raw), "}") + `,"reservation_id":"other"}`,
		strings.Replace(string(raw), original.PolicySHA256, strings.ToUpper(original.PolicySHA256), 1),
		strings.Replace(string(raw), original.ReservationID, strings.Repeat("r", 129), 1),
	} {
		grant := original
		if err := json.Unmarshal([]byte(input), &grant); err == nil {
			t.Fatalf("wire drift accepted: %s", input)
		}
		if grant != original {
			t.Fatal("failed decode partially replaced original grant")
		}
	}
}

func TestDiagnosticsGrantValidatesEvenForEmptyLegacyPayload(t *testing.T) {
	command := LaunchCommand{Diagnostics: &DiagnosticsGrant{}}
	if err := ValidateDependencyInputs(command); err == nil || err.Error() != "diagnostics_grant_invalid" {
		t.Fatalf("empty payload skipped diagnostics validation: %v", err)
	}
}

func TestCandidateIntentHashPreservesLegacyAndBindsDiagnostics(t *testing.T) {
	base := LaunchCommand{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", SlotToken: "slot", PlanRevision: 1, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}
	legacy, err := CandidateIntentHash(base)
	if err != nil {
		t.Fatal(err)
	}
	legacyBody, _ := json.Marshal(struct {
		Plan    int
		Payload json.RawMessage
		Input   *AcceptedInput
	}{base.PlanRevision, base.AdapterPayload, base.Input})
	want := sha256.Sum256([]byte(base.RunID + "\x00" + base.TaskID + "\x00" + base.AttemptID + "\x00" + base.SegmentID + "\x00" + base.SlotToken + "\x00" + string(legacyBody)))
	if legacy != hex.EncodeToString(want[:]) {
		t.Fatal("legacy three-field bytes changed")
	}
	policy := DiagnosticsPolicy{Version: 1, Mode: "detailed"}
	raw, _ := policy.CanonicalJSON()
	sum := sha256.Sum256(raw)
	base.Diagnostics = &DiagnosticsGrant{Policy: policy, PolicySHA256: hex.EncodeToString(sum[:]), ReservationID: "reservation", SegmentOrdinal: 1}
	bound, err := CandidateIntentHash(base)
	if err != nil || bound == legacy {
		t.Fatalf("diagnostics absent from intent: %s %v", bound, err)
	}
	base.DependencyInputs = &DependencyInputVector{Version: 1}
	withVector, err := CandidateIntentHash(base)
	if err != nil || withVector == bound {
		t.Fatalf("vector branch did not bind diagnostics: %s %v", withVector, err)
	}
	for _, vector := range []*DependencyInputVector{nil, {Version: 1}} {
		current := base
		current.DependencyInputs = vector
		original, _ := CandidateIntentHash(current)
		for _, mutate := range []func(*DiagnosticsGrant){func(g *DiagnosticsGrant) { g.PolicySHA256 = strings.Repeat("0", 64) }, func(g *DiagnosticsGrant) { g.ReservationID = "other" }, func(g *DiagnosticsGrant) { g.SegmentOrdinal++ }} {
			changed := current
			value := *current.Diagnostics
			changed.Diagnostics = &value
			mutate(changed.Diagnostics)
			got, _ := CandidateIntentHash(changed)
			if got == original {
				t.Fatal("diagnostics field absent from candidate intent")
			}
		}
	}
}
