package contract

import "errors"

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

// Fail-closed scaffold: no shape is accepted until the contract is implemented.
func (m GitFilterManifestV1) Validate() error              { return ErrGitFilterContract }
func (m GitFilterManifestV1) Digest() (string, error)      { return "", ErrGitFilterContract }
func (m GitFilterManifestV1) Clone() GitFilterManifestV1   { return m }
func (g GitFilterResourceGrant) Validate() error           { return ErrGitFilterContract }
func (g GitFilterCleanup) Validate() error                 { return ErrGitFilterContract }
func (*GitFilterManifestV1) UnmarshalJSON([]byte) error    { return ErrGitFilterContract }
func (*GitFilterGitPin) UnmarshalJSON([]byte) error        { return ErrGitFilterContract }
func (*GitFilterToolPin) UnmarshalJSON([]byte) error       { return ErrGitFilterContract }
func (*GitFilterAttributePin) UnmarshalJSON([]byte) error  { return ErrGitFilterContract }
func (*GitFilterResourceGrant) UnmarshalJSON([]byte) error { return ErrGitFilterContract }
func (*GitFilterCleanup) UnmarshalJSON([]byte) error       { return ErrGitFilterContract }
func GitFilterResourceScopeSHA256(LaunchCommand, string) (string, error) {
	return "", ErrGitFilterContract
}
