package contract

const FilterViewMaximumBytes = 128 << 10

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

func (v FilterPathViewV1) Validate() error     { return nil }
func (v FilterViewEvidenceV1) Validate() error { return nil }
func (v FilterViewEvidenceV1) ValidateBinding(m GitFilterManifestV1, contextSHA256 string) error {
	return nil
}
func (v FilterViewEvidenceV1) Clone() FilterViewEvidenceV1 { return v }
