package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"reflect"
	"strings"
	"unicode/utf8"
)

const (
	GitFilterManifestMaximumBytes       = 32768
	GitFilterToolMaximumBytes     int64 = 32 << 20
	GitFilterStreamMaximumBytes   int64 = 16 << 20
	GitFilterStreamSlots                = 3
	GitFilterResourceMaximumBytes       = 2*GitFilterToolMaximumBytes + GitFilterStreamSlots*GitFilterStreamMaximumBytes
)

var ErrGitFilterContract = errors.New("git_filter_contract_invalid")

type GitFilterGitPin struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}
type GitFilterToolPin struct {
	Path   string   `json:"path"`
	SHA256 string   `json:"sha256"`
	Args   []string `json:"args"`
}
type GitFilterAttributePin struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type GitFilterManifestV1 struct {
	Version      int                     `json:"version"`
	DriverID     string                  `json:"driver_id"`
	Paths        []string                `json:"paths"`
	Git          GitFilterGitPin         `json:"git"`
	Clean        GitFilterToolPin        `json:"clean"`
	Smudge       GitFilterToolPin        `json:"smudge"`
	Attributes   []GitFilterAttributePin `json:"attributes"`
	ConfigSHA256 string                  `json:"config_sha256"`
}
type GitFilterResourceGrant struct {
	Version            int    `json:"version"`
	ReservationID      string `json:"reservation_id"`
	ScopeSHA256        string `json:"scope_sha256"`
	ManifestSHA256     string `json:"manifest_sha256"`
	CleanToolSHA256    string `json:"clean_tool_sha256"`
	SmudgeToolSHA256   string `json:"smudge_tool_sha256"`
	ToolMaximumBytes   int64  `json:"tool_maximum_bytes"`
	StreamMaximumBytes int64  `json:"stream_maximum_bytes"`
	StreamSlots        int    `json:"stream_slots"`
}
type GitFilterCleanup struct {
	Version          int    `json:"version"`
	ReservationID    string `json:"reservation_id"`
	ScopeSHA256      string `json:"scope_sha256"`
	ManifestSHA256   string `json:"manifest_sha256"`
	CleanToolSHA256  string `json:"clean_tool_sha256"`
	SmudgeToolSHA256 string `json:"smudge_tool_sha256"`
	JournalSHA256    string `json:"journal_sha256"`
	RemovedBytes     int64  `json:"removed_bytes"`
}

// Validate checks the manifest's wire shape only. Source pins, configuration,
// native executables and repository context require independent runtime proof.
func (m GitFilterManifestV1) Validate() error {
	if m.Version != 1 || !artifactGrantID(m.DriverID) || len(m.DriverID) > 64 || strings.EqualFold(m.DriverID, "lfs") || len(m.Paths) < 1 || len(m.Paths) > 256 || m.Attributes == nil || len(m.Attributes) > 256 || !validationToolDigest(m.ConfigSHA256) {
		return ErrGitFilterContract
	}
	if gitFilterPaths(m.Paths) != nil || m.Git.Validate() != nil || m.Clean.Validate() != nil || m.Smudge.Validate() != nil {
		return ErrGitFilterContract
	}
	attributePaths := make([]string, len(m.Attributes))
	for i, a := range m.Attributes {
		if a.Validate() != nil {
			return ErrGitFilterContract
		}
		attributePaths[i] = a.Path
		for _, p := range m.Paths {
			if strings.EqualFold(p, a.Path) {
				return ErrGitFilterContract
			}
		}
	}
	if gitFilterPaths(attributePaths) != nil {
		return ErrGitFilterContract
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw)+1 > GitFilterManifestMaximumBytes {
		return ErrGitFilterContract
	}
	return nil
}

func (p GitFilterGitPin) Validate() error {
	if !gitFilterExecutablePath(p.Path) || !utf8.ValidString(p.Version) || len(p.Version) < 1 || len(p.Version) > 256 || !validationToolDigest(p.SHA256) {
		return ErrGitFilterContract
	}
	return nil
}
func (p GitFilterToolPin) Validate() error {
	if !gitFilterExecutablePath(p.Path) || !validationToolDigest(p.SHA256) || p.Args == nil || len(p.Args) > 16 {
		return ErrGitFilterContract
	}
	total := 0
	for _, arg := range p.Args {
		if !gitFilterText(arg) || strings.Contains(arg, "%f") {
			return ErrGitFilterContract
		}
		total += len(arg)
		if total > 4096 {
			return ErrGitFilterContract
		}
	}
	return nil
}
func (p GitFilterAttributePin) Validate() error {
	if !gitFilterRelativePath(p.Path) || path.Base(p.Path) != ".gitattributes" || !validationToolDigest(p.SHA256) {
		return ErrGitFilterContract
	}
	return nil
}

func gitFilterText(s string) bool { return utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n") }
func gitFilterExecutablePath(s string) bool {
	return gitFilterText(s) && len(s) <= 4096 && !strings.Contains(s, "\\") && path.IsAbs(s) && path.Clean(s) == s
}
func gitFilterRelativePath(s string) bool {
	if !gitFilterText(s) || len(s) < 1 || len(s) > 512 || strings.Contains(s, "\\") || path.IsAbs(s) || path.Clean(s) != s {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}
func gitFilterPaths(paths []string) error {
	for i, p := range paths {
		if !gitFilterRelativePath(p) || i > 0 && paths[i-1] >= p {
			return ErrGitFilterContract
		}
		for _, prior := range paths[:i] {
			if strings.EqualFold(prior, p) {
				return ErrGitFilterContract
			}
		}
	}
	return nil
}

// Digest hashes canonical JSON without a newline; the storage bound includes
// the newline used by published manifests. It is an observation, not authority.
func (m GitFilterManifestV1) Digest() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (m GitFilterManifestV1) Clone() GitFilterManifestV1 {
	m.Paths = gitFilterCloneStrings(m.Paths)
	m.Clean.Args = gitFilterCloneStrings(m.Clean.Args)
	m.Smudge.Args = gitFilterCloneStrings(m.Smudge.Args)
	if m.Attributes != nil {
		original := m.Attributes
		m.Attributes = make([]GitFilterAttributePin, len(original))
		copy(m.Attributes, original)
	}
	return m
}
func gitFilterCloneStrings(s []string) []string {
	if s == nil {
		return nil
	}
	clone := make([]string, len(s))
	copy(clone, s)
	return clone
}

// Grant shape and ScopeSHA256 do not authenticate a caller or reserve resources.
func (g GitFilterResourceGrant) Validate() error {
	if !gitFilterIdentity(g.Version, g.ReservationID, g.ScopeSHA256, g.ManifestSHA256, g.CleanToolSHA256, g.SmudgeToolSHA256) || g.ToolMaximumBytes != GitFilterToolMaximumBytes || g.StreamMaximumBytes != GitFilterStreamMaximumBytes || g.StreamSlots != GitFilterStreamSlots {
		return ErrGitFilterContract
	}
	return nil
}
func (g GitFilterCleanup) Validate() error {
	if !gitFilterIdentity(g.Version, g.ReservationID, g.ScopeSHA256, g.ManifestSHA256, g.CleanToolSHA256, g.SmudgeToolSHA256) || !validationToolDigest(g.JournalSHA256) || g.RemovedBytes < 0 || g.RemovedBytes > GitFilterResourceMaximumBytes {
		return ErrGitFilterContract
	}
	return nil
}
func gitFilterIdentity(version int, id string, digests ...string) bool {
	if version != 1 || !artifactGrantID(id) {
		return false
	}
	for _, digest := range digests {
		if !validationToolDigest(digest) {
			return false
		}
	}
	return true
}

func (m *GitFilterManifestV1) UnmarshalJSON(raw []byte) error {
	type plain GitFilterManifestV1
	var next plain
	if gitFilterDecode(raw, &next) != nil {
		return ErrGitFilterContract
	}
	value := GitFilterManifestV1(next)
	if err := value.Validate(); err != nil {
		return err
	}
	*m = value
	return nil
}
func (p *GitFilterGitPin) UnmarshalJSON(raw []byte) error {
	type plain GitFilterGitPin
	var next plain
	if gitFilterDecode(raw, &next) != nil {
		return ErrGitFilterContract
	}
	value := GitFilterGitPin(next)
	if err := value.Validate(); err != nil {
		return err
	}
	*p = value
	return nil
}
func (p *GitFilterToolPin) UnmarshalJSON(raw []byte) error {
	type plain GitFilterToolPin
	var next plain
	if gitFilterDecode(raw, &next) != nil {
		return ErrGitFilterContract
	}
	value := GitFilterToolPin(next)
	if err := value.Validate(); err != nil {
		return err
	}
	*p = value
	return nil
}
func (p *GitFilterAttributePin) UnmarshalJSON(raw []byte) error {
	type plain GitFilterAttributePin
	var next plain
	if gitFilterDecode(raw, &next) != nil {
		return ErrGitFilterContract
	}
	value := GitFilterAttributePin(next)
	if err := value.Validate(); err != nil {
		return err
	}
	*p = value
	return nil
}
func (g *GitFilterResourceGrant) UnmarshalJSON(raw []byte) error {
	type plain GitFilterResourceGrant
	var next plain
	if gitFilterDecode(raw, &next) != nil {
		return ErrGitFilterContract
	}
	value := GitFilterResourceGrant(next)
	if err := value.Validate(); err != nil {
		return err
	}
	*g = value
	return nil
}
func (g *GitFilterCleanup) UnmarshalJSON(raw []byte) error {
	type plain GitFilterCleanup
	var next plain
	if gitFilterDecode(raw, &next) != nil {
		return ErrGitFilterContract
	}
	value := GitFilterCleanup(next)
	if err := value.Validate(); err != nil {
		return err
	}
	*g = value
	return nil
}

// Keep the legacy decoder unchanged. Every declared field in these dedicated
// DTOs is required and no null, including array elements, is permitted.
func gitFilterDecode(raw []byte, dst any) error {
	if err := DecodeBridgeStrict(raw, dst); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := gitFilterRequiredValue(dec, reflect.TypeOf(dst).Elem()); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrGitFilterContract
	}
	return nil
}
func gitFilterRequiredValue(dec *json.Decoder, target reflect.Type) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return ErrGitFilterContract
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		if target.Kind() != reflect.Struct {
			return ErrGitFilterContract
		}
		fields := bridgeExactFields(target)
		seen := make(map[string]bool, len(fields))
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return ErrGitFilterContract
			}
			child, ok := fields[name]
			if !ok || seen[name] {
				return ErrGitFilterContract
			}
			seen[name] = true
			if err := gitFilterRequiredValue(dec, child); err != nil {
				return err
			}
		}
		if len(seen) != len(fields) {
			return ErrGitFilterContract
		}
		token, err = dec.Token()
		if err != nil || token != json.Delim('}') {
			return ErrGitFilterContract
		}
	case '[':
		if target.Kind() != reflect.Slice {
			return ErrGitFilterContract
		}
		for dec.More() {
			if err := gitFilterRequiredValue(dec, target.Elem()); err != nil {
				return err
			}
		}
		token, err = dec.Token()
		if err != nil || token != json.Delim(']') {
			return ErrGitFilterContract
		}
	default:
		return ErrGitFilterContract
	}
	return nil
}

// Scope binds the logical original segment, plan revision and manifest in a
// distinct domain. Physical Host identity and authorization are Store concerns.
func GitFilterResourceScopeSHA256(c LaunchCommand, manifestSHA256 string) (string, error) {
	hostScope, err := HostArtifactScopeSHA256(c)
	if err != nil || c.PlanRevision < 1 || !validationToolDigest(manifestSHA256) {
		return "", ErrGitFilterContract
	}
	raw, err := json.Marshal(struct {
		Version        int
		HostScope      string
		PlanRevision   int
		ManifestSHA256 string
	}{1, hostScope, c.PlanRevision, manifestSHA256})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("git-filter-resource-v1\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}
