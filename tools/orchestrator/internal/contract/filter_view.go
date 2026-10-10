package contract

import "encoding/json"

const FilterViewMaximumBytes = 128 << 10

// These hashes describe canonical repository bytes and rendered workspace bytes.
// They do not reconstruct a live filter broker or authorize a process outcome.
type FilterPathViewV1 struct {
	Path            string `json:"path"`
	CanonicalSHA256 string `json:"canonical_sha256"`
	RenderedSHA256  string `json:"rendered_sha256"`
}

type FilterViewEvidenceV1 struct {
	Version             int                `json:"version"`
	ManifestSHA256      string             `json:"manifest_sha256"`
	FilterContextSHA256 string             `json:"filter_context_sha256"`
	Views               []FilterPathViewV1 `json:"views"`
}

func (v FilterPathViewV1) Validate() error {
	if !gitFilterRelativePath(v.Path) || !validationToolDigest(v.CanonicalSHA256) || !validationToolDigest(v.RenderedSHA256) {
		return ErrGitFilterContract
	}
	return nil
}

func (v FilterViewEvidenceV1) Validate() error {
	if v.Version != 1 || !validationToolDigest(v.ManifestSHA256) || !validationToolDigest(v.FilterContextSHA256) || len(v.Views) < 1 || len(v.Views) > 256 {
		return ErrGitFilterContract
	}
	paths := make([]string, len(v.Views))
	for i, view := range v.Views {
		if view.Validate() != nil {
			return ErrGitFilterContract
		}
		paths[i] = view.Path
	}
	if gitFilterPaths(paths) != nil {
		return ErrGitFilterContract
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw)+1 > FilterViewMaximumBytes {
		return ErrGitFilterContract
	}
	return nil
}

// ValidateBinding requires the independently verified live source context and
// the exact authorized manifest path vector; syntax-valid hashes alone fail.
func (v FilterViewEvidenceV1) ValidateBinding(m GitFilterManifestV1, contextSHA256 string) error {
	digest, err := m.Digest()
	if err != nil || v.Validate() != nil || v.ManifestSHA256 != digest || v.FilterContextSHA256 != contextSHA256 || len(v.Views) != len(m.Paths) {
		return ErrGitFilterContract
	}
	for i, path := range m.Paths {
		if v.Views[i].Path != path {
			return ErrGitFilterContract
		}
	}
	return nil
}

func (v FilterViewEvidenceV1) Clone() FilterViewEvidenceV1 {
	if v.Views != nil {
		v.Views = append(make([]FilterPathViewV1, 0, len(v.Views)), v.Views...)
	}
	return v
}

func (v *FilterPathViewV1) UnmarshalJSON(raw []byte) error {
	type plain FilterPathViewV1
	var next plain
	if len(raw)+1 > FilterViewMaximumBytes || gitFilterDecode(raw, &next) != nil {
		return ErrGitFilterContract
	}
	value := FilterPathViewV1(next)
	if err := value.Validate(); err != nil {
		return err
	}
	*v = value
	return nil
}

func (v *FilterViewEvidenceV1) UnmarshalJSON(raw []byte) error {
	type plain FilterViewEvidenceV1
	var next plain
	if len(raw)+1 > FilterViewMaximumBytes || gitFilterDecode(raw, &next) != nil {
		return ErrGitFilterContract
	}
	value := FilterViewEvidenceV1(next)
	if err := value.Validate(); err != nil {
		return err
	}
	*v = value
	return nil
}
