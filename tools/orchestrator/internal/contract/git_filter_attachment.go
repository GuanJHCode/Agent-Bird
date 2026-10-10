package contract

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

const GitFilterJournalLimit = 64 << 10
const GitFilterMetadataBytes int64 = 2 * (GitFilterJournalLimit + 1)

// GitFilterManifestFromPayload is pure. Legacy absence remains absent; a newly
// declared filter must use its exact field name and strict manifest wire.
func GitFilterManifestFromPayload(raw json.RawMessage) (*GitFilterManifestV1, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, nil
	}
	present := false
	for key := range fields {
		if strings.EqualFold(key, "git_filter") {
			present = true
			if key != "git_filter" {
				return nil, ErrGitFilterContract
			}
		}
	}
	if !present {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrGitFilterContract
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err = dec.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return nil, ErrGitFilterContract
		}
		seen[key] = true
		var discard json.RawMessage
		if dec.Decode(&discard) != nil {
			return nil, ErrGitFilterContract
		}
	}
	if _, err = dec.Token(); err != nil {
		return nil, ErrGitFilterContract
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, ErrGitFilterContract
	}
	if bytes.Equal(bytes.TrimSpace(fields["git_filter"]), []byte("null")) {
		return nil, ErrGitFilterContract
	}
	var m GitFilterManifestV1
	if json.Unmarshal(fields["git_filter"], &m) != nil {
		return nil, ErrGitFilterContract
	}
	clone := m.Clone()
	return &clone, nil
}

func RequireGitFilter(c LaunchCommand) error {
	m, err := GitFilterManifestFromPayload(c.AdapterPayload)
	if err != nil {
		return err
	}
	if m == nil {
		if c.GitFilter != nil {
			return ErrGitFilterContract
		}
		return nil
	}
	var shape struct {
		Kind      string `json:"kind"`
		Workspace *struct {
			Version int `json:"version"`
		} `json:"candidate_workspace"`
		Profile *struct {
			Role       string `json:"role"`
			Permission string `json:"permission"`
		} `json:"profile"`
		Action *struct {
			Version   int    `json:"version"`
			Operation string `json:"operation"`
		} `json:"candidate_action"`
	}
	if json.Unmarshal(c.AdapterPayload, &shape) != nil {
		return ErrGitFilterContract
	}
	producer := shape.Kind == "provider" && shape.Workspace != nil && shape.Workspace.Version == 1 && shape.Profile != nil && shape.Profile.Role == "implementer" && shape.Profile.Permission == "workspace-write" && shape.Action == nil
	validator := shape.Kind == "candidate" && shape.Action != nil && shape.Action.Version == 1 && shape.Action.Operation == "validate" && shape.Workspace == nil && shape.Profile == nil
	reviewer := shape.Kind == "" && shape.Action != nil && shape.Action.Version == 1 && shape.Action.Operation == "review" && shape.Workspace == nil && shape.Profile != nil && shape.Profile.Role == "reviewer" && shape.Profile.Permission == "read-only"
	observer := shape.Kind == "candidate" && shape.Action != nil && shape.Action.Version == 1 && shape.Action.Operation == "filter_target_preflight" && shape.Workspace == nil && shape.Profile == nil
	if !producer && !validator && !reviewer && !observer {
		return ErrGitFilterContract
	}
	if err := validateValidationToolAttachment(c); err != nil {
		return err
	}
	g := c.GitFilter
	if g == nil || g.Validate() != nil || RequireHostArtifacts(c) != nil {
		return ErrGitFilterContract
	}
	if g.ReservationID == c.HostArtifacts.TerminalReservationID || g.ReservationID == c.HostArtifacts.ProgressReservationID {
		return ErrGitFilterContract
	}
	if c.ValidationTool != nil && g.ReservationID == c.ValidationTool.ReservationID {
		return ErrGitFilterContract
	}
	digest, err := m.Digest()
	if err != nil || digest != g.ManifestSHA256 || m.Clean.SHA256 != g.CleanToolSHA256 || m.Smudge.SHA256 != g.SmudgeToolSHA256 {
		return ErrGitFilterContract
	}
	scope, err := GitFilterResourceScopeSHA256(c, digest)
	if err != nil || scope != g.ScopeSHA256 {
		return ErrGitFilterContract
	}
	return nil
}
