package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
)

// CandidateIntentHash preserves the legacy three-field JSON byte contract.
// The versioned vector extends that contract only for explicitly opted-in grants.
func CandidateIntentHash(c LaunchCommand) (string, error) {
	var body []byte
	var err error
	if err = validateValidationToolAttachment(c); err != nil {
		return "", err
	}
	if c.ValidationTool != nil {
		body, err = json.Marshal(struct {
			Plan             int
			Payload          json.RawMessage
			Input            *AcceptedInput
			DependencyInputs *DependencyInputVector `json:",omitempty"`
			Diagnostics      *DiagnosticsGrant      `json:",omitempty"`
			HostArtifacts    *HostArtifactGrant
			ValidationTool   *ValidationToolGrant
		}{c.PlanRevision, c.AdapterPayload, c.Input, c.DependencyInputs, c.Diagnostics, c.HostArtifacts, c.ValidationTool})
	} else if c.HostArtifacts != nil {
		if err := RequireHostArtifacts(c); err != nil {
			return "", err
		}
		body, err = json.Marshal(struct {
			Plan             int
			Payload          json.RawMessage
			Input            *AcceptedInput
			DependencyInputs *DependencyInputVector `json:",omitempty"`
			Diagnostics      *DiagnosticsGrant      `json:",omitempty"`
			HostArtifacts    *HostArtifactGrant
		}{c.PlanRevision, c.AdapterPayload, c.Input, c.DependencyInputs, c.Diagnostics, c.HostArtifacts})
	} else if c.DependencyInputs == nil && c.Diagnostics == nil {
		body, err = json.Marshal(struct {
			Plan    int
			Payload json.RawMessage
			Input   *AcceptedInput
		}{c.PlanRevision, c.AdapterPayload, c.Input})
	} else if c.DependencyInputs != nil && c.Diagnostics == nil {
		body, err = json.Marshal(struct {
			Plan             int
			Payload          json.RawMessage
			Input            *AcceptedInput
			DependencyInputs *DependencyInputVector
		}{c.PlanRevision, c.AdapterPayload, c.Input, c.DependencyInputs})
	} else if c.DependencyInputs == nil {
		body, err = json.Marshal(struct {
			Plan        int
			Payload     json.RawMessage
			Input       *AcceptedInput
			Diagnostics *DiagnosticsGrant
		}{c.PlanRevision, c.AdapterPayload, c.Input, c.Diagnostics})
	} else {
		body, err = json.Marshal(struct {
			Plan             int
			Payload          json.RawMessage
			Input            *AcceptedInput
			DependencyInputs *DependencyInputVector
			Diagnostics      *DiagnosticsGrant
		}{c.PlanRevision, c.AdapterPayload, c.Input, c.DependencyInputs, c.Diagnostics})
	}
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(c.RunID + "\x00" + c.TaskID + "\x00" + c.AttemptID + "\x00" + c.SegmentID + "\x00" + c.SlotToken + "\x00" + string(body)))
	return hex.EncodeToString(hash[:]), nil
}

// ValidateDependencyInputs checks the evidence shape and source selection before
// a Host accesses files or starts work. Store separately verifies the exact DAG
// dependency set and current accepted tuples. This is not a signature verifier:
// the grant must still arrive over the existing authenticated control channel.
func ValidateDependencyInputs(c LaunchCommand) error {
	if err := validateValidationToolAttachment(c); err != nil {
		return err
	}
	if c.ValidationTool != nil {
		hash, err := CandidateIntentHash(c)
		if err != nil || hash != c.LaunchIntentHash {
			return errors.New("candidate_input_changed")
		}
	}
	if c.HostArtifacts != nil {
		if err := RequireHostArtifacts(c); err != nil {
			return err
		}
	}
	if c.Diagnostics != nil {
		if err := c.Diagnostics.Validate(); err != nil {
			return err
		}
	}
	if len(c.AdapterPayload) == 0 && c.DependencyInputs == nil {
		return nil
	}
	var payload struct {
		Action *struct {
			Version      int      `json:"version"`
			InputVersion int      `json:"dependency_input_version"`
			Operation    string   `json:"operation"`
			Source       string   `json:"source_task"`
			Ordered      []string `json:"ordered_source_tasks"`
		} `json:"candidate_action"`
	}
	invalid := errors.New("candidate_dependency_inputs_invalid")
	if json.Unmarshal(c.AdapterPayload, &payload) != nil {
		return invalid
	}
	if payload.Action == nil || payload.Action.InputVersion == 0 {
		if c.DependencyInputs != nil {
			return invalid
		}
		return nil
	}
	a := payload.Action
	v := c.DependencyInputs
	digest := func(s string) bool {
		b, e := hex.DecodeString(s)
		return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
	}
	if a.Version == 2 && a.InputVersion == 2 {
		if a.Operation != "compose" || v == nil || v.Version != 2 || len(v.Inputs) < 2 || len(v.Inputs) > 63 || len(a.Ordered) != len(v.Inputs) || a.Source != "" || c.Input != nil || c.PlanRevision < 1 {
			return invalid
		}
		seen := make(map[string]bool, len(a.Ordered))
		for i, input := range v.Inputs {
			e, r := input.Event, input.Event.Artifact
			if a.Ordered[i] == "" || seen[a.Ordered[i]] || e.Version != 1 || e.RunID != c.RunID || e.TaskID != a.Ordered[i] || e.TaskID == c.TaskID || e.AttemptID == "" || e.SegmentID == "" || e.WorkRevision < 1 || e.ProducerID == "" || e.EventID == "" || e.EventRevision < 1 || e.ActionSlot == "" || e.CommandID == "" || e.Sequence < 1 || e.Kind != EventResult || !digest(e.PayloadHash) || input.DecisionCommandID == "" || r == nil || !filepath.IsAbs(r.Path) || r.Size < 1 || r.Size > 1024*1024 || !digest(r.SHA256) || r.SHA256 != e.PayloadHash {
				return invalid
			}
			seen[a.Ordered[i]] = true
			var sourcePayload map[string]json.RawMessage
			if json.Unmarshal(input.AdapterPayload, &sourcePayload) != nil || sourcePayload == nil {
				return invalid
			}
		}
		hash, err := CandidateIntentHash(c)
		if err != nil || hash != c.LaunchIntentHash {
			return errors.New("candidate_input_changed")
		}
		return nil
	}
	if a.Version != 1 || a.InputVersion != 1 || v == nil || v.Version != 1 || len(v.Inputs) < 1 || len(v.Inputs) > 63 || c.Input == nil || c.PlanRevision < 1 {
		return invalid
	}
	found := false
	for i, input := range v.Inputs {
		var sourcePayload map[string]json.RawMessage
		if json.Unmarshal(input.AdapterPayload, &sourcePayload) != nil || sourcePayload == nil {
			return invalid
		}
		e := input.Event
		r := e.Artifact
		if e.Version != 1 || e.RunID != c.RunID || e.TaskID == "" || e.TaskID == c.TaskID || e.AttemptID == "" || e.SegmentID == "" || e.WorkRevision < 1 || e.ProducerID == "" || e.EventID == "" || e.EventRevision < 1 || e.ActionSlot == "" || e.CommandID == "" || e.Sequence < 1 || e.Kind != EventResult || !digest(e.PayloadHash) || input.DecisionCommandID == "" || r == nil || !filepath.IsAbs(r.Path) || r.Size < 1 || r.Size > 1024*1024 || !digest(r.SHA256) || r.SHA256 != e.PayloadHash {
			return invalid
		}
		if i > 0 && v.Inputs[i-1].Event.TaskID >= e.TaskID {
			return invalid
		}
		if e.TaskID == a.Source {
			left, err := json.Marshal(input)
			if err != nil {
				return invalid
			}
			right, err := json.Marshal(c.Input)
			if err != nil || string(left) != string(right) {
				return invalid
			}
			found = true
		}
	}
	if !found {
		return invalid
	}
	hash, err := CandidateIntentHash(c)
	if err != nil || hash != c.LaunchIntentHash {
		return errors.New("candidate_input_changed")
	}
	return nil
}
