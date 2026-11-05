package host

import (
	"encoding/json"
	"errors"
	"path/filepath"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

// ReworkPrompt resolves control-plane objects before constructing worker input.
// state is independently established by the source Host, never by a reference.
func ReworkPrompt(grant contract.LaunchCommand, p adapter.InvocationPayload, state string) (string, error) {
	bad := errors.New("rework_reference_invalid")
	if p.CandidateWorkspace == nil || p.CandidateWorkspace.Rework == nil || p.CandidateWorkspace.Rework.Version != 3 || p.Prompt != adapter.ReworkPromptMarker || !filepath.IsAbs(state) || filepath.Clean(state) != state {
		return "", bad
	}
	c := p.CandidateWorkspace.Rework
	r := c.RefsV3
	if validateCandidateRework(p.CandidateWorkspace, grant) != nil || r == nil || r.Version != 3 || r.Owner == "" || r.RunID != grant.RunID || r.TaskID != grant.TaskID || r.WorkRevision != grant.WorkRevision || len(r.Conditions) != grant.WorkRevision-1 || len(r.Conditions) > 2 {
		return "", bad
	}
	raw, e := json.Marshal(r)
	if e != nil || len(raw) > adapter.ReworkManifestMaxBytes {
		return "", bad
	}
	root := filepath.Join(state, "rework-objects")
	bodies := []adapter.ReworkBody{}
	refs := append([]adapter.ReworkObjectRef{r.Brief}, r.Conditions...)
	for i, ref := range refs {
		kind, revision := "condition", i
		if i == 0 {
			kind, revision = "brief", 1
		}
		if ref.Owner != r.Owner || ref.RunID != r.RunID || ref.TaskID != r.TaskID || ref.WorkRevision != revision || ref.Kind != kind {
			return "", bad
		}
		data, e := adapter.ReadReworkObject(root, ref)
		if e != nil {
			return "", e
		}
		// Hashing, parsing and assembly all consume this same bounded byte slice.
		body, e := adapter.DecodeReworkBody(data)
		if e != nil || body.Owner != ref.Owner || body.RunID != ref.RunID || body.TaskID != ref.TaskID || body.WorkRevision != ref.WorkRevision || body.Kind != ref.Kind {
			return "", bad
		}
		bodies = append(bodies, body)
	}
	return adapter.AssembleReworkBodies(bodies)
}
