package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// The spool parent comes from the authenticated local bootstrap, never from a
// provider path or the artifact receipt. The accepted event fixes its producer.
func readManagedCandidateArtifact(e contract.Event, parent string) ([]byte, error) {
	bad := errors.New("candidate_input_changed")
	if parent == "" || !filepath.IsAbs(parent) || filepath.Clean(parent) != parent || e.Artifact == nil || e.Artifact.HostArtifact == nil {
		return nil, bad
	}
	r := e.Artifact.HostArtifact
	if r.Validate() != nil {
		return nil, bad
	}
	var d artifacts.HostSlotDescriptor
	var p artifacts.Proof
	if json.Unmarshal(r.Descriptor, &d) != nil || json.Unmarshal(r.Proof, &p) != nil {
		return nil, bad
	}
	raw, err := d.Canonical()
	if err != nil || !bytes.Equal(raw, r.Descriptor) {
		return nil, bad
	}
	proof, err := json.Marshal(p)
	if err != nil || !bytes.Equal(proof, r.Proof) {
		return nil, bad
	}
	scope, err := contract.HostArtifactScopeSHA256(contract.LaunchCommand{RunID: e.RunID, TaskID: e.TaskID, AttemptID: e.AttemptID, SegmentID: e.SegmentID, CommandID: e.CommandID, HostID: e.ProducerID, WorkRevision: e.WorkRevision})
	if err != nil || scope != d.ScopeSHA256 || d.Class != "terminal" || d.Index != 0 || d.Kind != "terminal" || e.Artifact.ID != "result-output.bin" {
		return nil, bad
	}
	id, err := d.ObjectID()
	if err != nil {
		return nil, bad
	}
	root := filepath.Join(parent, e.ProducerID)
	if p.IntakeID != id || p.Path != filepath.Join(root, "managed-artifacts", id, "data.bin") || e.Artifact.Path != p.Path || e.Artifact.Size != d.Size || e.Artifact.SHA256 != d.SHA256 || e.PayloadHash != d.SHA256 {
		return nil, bad
	}
	body, err := artifacts.ReadVerifiedHostSlot(context.Background(), root, d, p)
	if err != nil {
		return nil, bad
	}
	return body, nil
}
