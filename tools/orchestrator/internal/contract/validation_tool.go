package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const ValidationToolMaximumBytes int64 = 32 << 20

// Two bounded journal versions may coexist during atomic publication. This
// physical/control escrow remains charged even after the executable is gone.
const ValidationToolMetadataBytes int64 = 8192
const ValidationToolJournalLimit = 4096

var ErrValidationToolGrant = errors.New("validation_tool_grant_invalid")

type ValidationToolGrant struct {
	Version       int    `json:"version"`
	ReservationID string `json:"reservation_id"`
	ScopeSHA256   string `json:"scope_sha256"`
	ToolSHA256    string `json:"tool_sha256"`
	MaximumBytes  int64  `json:"maximum_bytes"`
}

// Only the authenticated original Host may report removal after its own
// never-started or original Wait/tree proof. This is not standalone authority
// to unlink a path, release another grant or reconstruct a process proof.
type ValidationToolCleanup struct {
	Version       int    `json:"version"`
	ReservationID string `json:"reservation_id"`
	ScopeSHA256   string `json:"scope_sha256"`
	ToolSHA256    string `json:"tool_sha256"`
	JournalSHA256 string `json:"journal_sha256"`
	RemovedBytes  int64  `json:"removed_bytes"`
}

func validationToolDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}

func (g ValidationToolGrant) Validate() error {
	if g.Version != 1 || !artifactGrantID(g.ReservationID) || !validationToolDigest(g.ScopeSHA256) || !validationToolDigest(g.ToolSHA256) || g.MaximumBytes != ValidationToolMaximumBytes {
		return ErrValidationToolGrant
	}
	return nil
}

func (r ValidationToolCleanup) Validate() error {
	if r.Version != 1 || !artifactGrantID(r.ReservationID) || !validationToolDigest(r.ScopeSHA256) || !validationToolDigest(r.ToolSHA256) || !validationToolDigest(r.JournalSHA256) || r.RemovedBytes < 0 || r.RemovedBytes > ValidationToolMaximumBytes {
		return ErrValidationToolGrant
	}
	return nil
}

func (g *ValidationToolGrant) UnmarshalJSON(raw []byte) error {
	type plain ValidationToolGrant
	var v plain
	if DecodeBridgeStrict(raw, &v) != nil || ValidationToolGrant(v).Validate() != nil {
		return ErrValidationToolGrant
	}
	*g = ValidationToolGrant(v)
	return nil
}

func (r *ValidationToolCleanup) UnmarshalJSON(raw []byte) error {
	type plain ValidationToolCleanup
	var v plain
	if DecodeBridgeStrict(raw, &v) != nil || ValidationToolCleanup(v).Validate() != nil {
		return ErrValidationToolGrant
	}
	*r = ValidationToolCleanup(v)
	return nil
}

// Like the existing artifact scope, this excludes mutable execution epoch and
// generated input/directory bindings. The complete launch intent binds those.
func ValidationToolScopeSHA256(c LaunchCommand, toolSHA string) (string, error) {
	hostScope, err := HostArtifactScopeSHA256(c)
	if err != nil || c.PlanRevision < 1 || !validationToolDigest(toolSHA) {
		return "", ErrValidationToolGrant
	}
	body, err := json.Marshal(struct {
		Version      int
		HostScope    string
		PlanRevision int
		ToolSHA256   string
	}{1, hostScope, c.PlanRevision, toolSHA})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("validation-tool-v1\x00"), body...))
	return hex.EncodeToString(digest[:]), nil
}

func RequireValidationTool(c LaunchCommand, toolSHA string) error {
	if c.ValidationTool == nil || c.ValidationTool.Validate() != nil || c.ValidationTool.ToolSHA256 != toolSHA {
		return ErrValidationToolGrant
	}
	scope, err := ValidationToolScopeSHA256(c, toolSHA)
	if err != nil || scope != c.ValidationTool.ScopeSHA256 {
		return ErrValidationToolGrant
	}
	return nil
}
