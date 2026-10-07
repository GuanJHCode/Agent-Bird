package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const (
	HostTerminalArtifactBytes int64 = 1 << 20
	HostProgressArtifactBytes int64 = 64 << 10
	HostProgressArtifactSlots       = 21
	HostArtifactReservedBytes       = HostTerminalArtifactBytes + HostProgressArtifactSlots*HostProgressArtifactBytes
)

// Version1 fixes capacity: one terminal/prelaunch alternative and21 progress
// slots. Shape validation is not Store authorization or filesystem permission.
type HostArtifactGrant struct {
	Version               int    `json:"version"`
	TerminalReservationID string `json:"terminal_reservation_id"`
	ProgressReservationID string `json:"progress_reservation_id"`
	ScopeSHA256           string `json:"scope_sha256"`
}

var ErrHostArtifactGrant = errors.New("host_artifact_grant_invalid")

func artifactGrantID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (g HostArtifactGrant) Validate() error {
	digest, err := hex.DecodeString(g.ScopeSHA256)
	if g.Version != 1 || !artifactGrantID(g.TerminalReservationID) || !artifactGrantID(g.ProgressReservationID) || g.TerminalReservationID == g.ProgressReservationID || err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != g.ScopeSHA256 {
		return ErrHostArtifactGrant
	}
	return nil
}

func (g *HostArtifactGrant) UnmarshalJSON(raw []byte) error {
	type plain HostArtifactGrant
	var value plain
	if DecodeBridgeStrict(raw, &value) != nil {
		return ErrHostArtifactGrant
	}
	next := HostArtifactGrant(value)
	if err := next.Validate(); err != nil {
		return err
	}
	*g = next
	return nil
}

// Scope excludes mutable execution epoch and business input. It binds the
// original segment; a resume segment receives independent capacity.
func HostArtifactScopeSHA256(c LaunchCommand) (string, error) {
	if c.RunID == "" || c.TaskID == "" || len(c.RunID) > 64<<10 || len(c.TaskID) > 64<<10 || c.WorkRevision < 1 || !artifactGrantID(c.AttemptID) || !artifactGrantID(c.SegmentID) || !artifactGrantID(c.HostID) || !artifactGrantID(c.CommandID) {
		return "", ErrHostArtifactGrant
	}
	raw, err := json.Marshal(struct {
		Version                                        int
		Run, Task, Attempt, Segment, Producer, Command string
		Revision                                       int
	}{1, c.RunID, c.TaskID, c.AttemptID, c.SegmentID, c.HostID, c.CommandID, c.WorkRevision})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func RequireHostArtifacts(c LaunchCommand) error {
	if c.HostArtifacts == nil || c.HostArtifacts.Validate() != nil {
		return ErrHostArtifactGrant
	}
	scope, err := HostArtifactScopeSHA256(c)
	if err != nil || c.HostArtifacts.ScopeSHA256 != scope {
		return ErrHostArtifactGrant
	}
	return nil
}
