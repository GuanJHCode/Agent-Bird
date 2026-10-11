package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
)

const GitFilterCarrierMetadataBytes int64 = 2 * (GitFilterJournalLimit + 1)
const GitFilterCarrierNamespace = "darwin-private-tmp-v1"

// The carrier has its own retained metadata promise. It never borrows the
// Broker's tool, stream or journal allowance.
type GitFilterCarrierGrant struct {
	Version             int    `json:"version"`
	ReservationID       string `json:"reservation_id"`
	ScopeSHA256         string `json:"scope_sha256"`
	FilterReservationID string `json:"filter_reservation_id"`
	FilterScopeSHA256   string `json:"filter_scope_sha256"`
	ManifestSHA256      string `json:"manifest_sha256"`
	MetadataBytes       int64  `json:"metadata_bytes"`
	RuntimeNamespace    string `json:"runtime_namespace"`
	DirectorySlots      int    `json:"directory_slots"`
	SocketSlots         int    `json:"socket_slots"`
}

type GitFilterCarrierIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Nlink  uint64 `json:"nlink"`
	UID    uint32 `json:"uid"`
	Mode   uint32 `json:"mode"`
}

// Only an authenticated original Host terminal event can carry this receipt.
// These scalar observations cannot reconstruct a live carrier owner.
type GitFilterCarrierCleanup struct {
	Version             int                      `json:"version"`
	ReservationID       string                   `json:"reservation_id"`
	ScopeSHA256         string                   `json:"scope_sha256"`
	FilterReservationID string                   `json:"filter_reservation_id"`
	ManifestSHA256      string                   `json:"manifest_sha256"`
	RuntimeNamespace    string                   `json:"runtime_namespace"`
	Outcome             string                   `json:"outcome"`
	Basename            string                   `json:"basename"`
	Parent              GitFilterCarrierIdentity `json:"parent"`
	Directory           GitFilterCarrierIdentity `json:"directory"`
	Socket              GitFilterCarrierIdentity `json:"socket"`
	FinalOrdinal        int64                    `json:"final_ordinal"`
	JournalSHA256       string                   `json:"journal_sha256"`
	RetainedBytes       int64                    `json:"retained_bytes"`
}

func (g GitFilterCarrierGrant) Validate() error {
	if g.Version != 1 || !artifactGrantID(g.ReservationID) || !artifactGrantID(g.FilterReservationID) || g.ReservationID == g.FilterReservationID || !validationToolDigest(g.ScopeSHA256) || !validationToolDigest(g.FilterScopeSHA256) || g.ScopeSHA256 == g.FilterScopeSHA256 || !validationToolDigest(g.ManifestSHA256) || g.MetadataBytes != GitFilterCarrierMetadataBytes || g.RuntimeNamespace != GitFilterCarrierNamespace || g.DirectorySlots != 1 || g.SocketSlots != 1 {
		return ErrGitFilterContract
	}
	return nil
}

func GitFilterCarrierScopeSHA256(c LaunchCommand) (string, error) {
	if c.GitFilter == nil || c.GitFilter.Validate() != nil || RequireHostArtifacts(c) != nil || c.PlanRevision < 1 {
		return "", ErrGitFilterContract
	}
	body, err := json.Marshal(struct {
		HostScope            string
		Plan                 int
		Filter               GitFilterResourceGrant
		Namespace            string
		Metadata             int64
		Directories, Sockets int
	}{c.HostArtifacts.ScopeSHA256, c.PlanRevision, *c.GitFilter, GitFilterCarrierNamespace, GitFilterCarrierMetadataBytes, 1, 1})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(append([]byte("git-filter-carrier-v1\x00"), body...))
	return hex.EncodeToString(hash[:]), nil
}

func IsGitFilterCarrierAction(raw json.RawMessage) bool {
	var p struct {
		Kind   string `json:"kind"`
		Action *struct {
			Version   int    `json:"version"`
			Operation string `json:"operation"`
		} `json:"candidate_action"`
	}
	return json.Unmarshal(raw, &p) == nil && p.Kind == "candidate" && p.Action != nil && p.Action.Version == 1 && p.Action.Operation == "filter_integrate"
}

func RequireGitFilterCarrier(c LaunchCommand) error {
	want := IsGitFilterCarrierAction(c.AdapterPayload)
	g := c.GitFilterCarrier
	if !want {
		if g != nil {
			return ErrGitFilterContract
		}
		return nil
	}
	if g == nil || g.Validate() != nil || c.GitFilter == nil || g.FilterReservationID != c.GitFilter.ReservationID || g.FilterScopeSHA256 != c.GitFilter.ScopeSHA256 || g.ManifestSHA256 != c.GitFilter.ManifestSHA256 || c.HostArtifacts == nil || g.ReservationID == c.HostArtifacts.TerminalReservationID || g.ReservationID == c.HostArtifacts.ProgressReservationID || c.ValidationTool != nil {
		return ErrGitFilterContract
	}
	scope, err := GitFilterCarrierScopeSHA256(c)
	if err != nil || g.ScopeSHA256 != scope {
		return ErrGitFilterContract
	}
	return nil
}

func (r GitFilterCarrierCleanup) Validate() error {
	if r.Version != 1 || !artifactGrantID(r.ReservationID) || !artifactGrantID(r.FilterReservationID) || r.ReservationID == r.FilterReservationID || !validationToolDigest(r.ScopeSHA256) || !validationToolDigest(r.ManifestSHA256) || !validationToolDigest(r.JournalSHA256) || r.RuntimeNamespace != GitFilterCarrierNamespace || r.FinalOrdinal < 0 || r.RetainedBytes < 0 || r.RetainedBytes > GitFilterJournalLimit+1 {
		return ErrGitFilterContract
	}
	if r.Outcome == "not_created" && r.FinalOrdinal == 0 {
		h := sha256.Sum256(nil)
		if r.Basename != "" || r.Parent != (GitFilterCarrierIdentity{}) || r.Directory != (GitFilterCarrierIdentity{}) || r.Socket != (GitFilterCarrierIdentity{}) || r.RetainedBytes != 0 || r.JournalSHA256 != hex.EncodeToString(h[:]) {
			return ErrGitFilterContract
		}
		return nil
	}
	if (r.Outcome != "removed" && r.Outcome != "not_created") || r.FinalOrdinal < 1 || r.RetainedBytes < 1 || !strings.HasPrefix(r.Basename, "codex-carrier-") || len(r.Basename) != len("codex-carrier-")+24 {
		return ErrGitFilterContract
	}
	for _, ch := range strings.TrimPrefix(r.Basename, "codex-carrier-") {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return ErrGitFilterContract
		}
	}
	if r.Parent.Device == 0 || r.Parent.Inode == 0 || r.Parent.UID != 0 || r.Parent.Mode != uint32(os.ModeDir|os.ModeSticky|0777) {
		return ErrGitFilterContract
	}
	if r.Outcome == "not_created" {
		if r.Directory != (GitFilterCarrierIdentity{}) || r.Socket != (GitFilterCarrierIdentity{}) {
			return ErrGitFilterContract
		}
		return nil
	}
	if r.Directory.Device != r.Parent.Device || r.Directory.Inode == 0 || r.Directory.Mode != uint32(os.ModeDir|0700) || r.Socket.Device != r.Directory.Device || r.Socket.Inode == 0 || r.Socket.Nlink != 1 || r.Socket.UID != r.Directory.UID || r.Socket.Mode != uint32(os.ModeSocket|0600) {
		return ErrGitFilterContract
	}
	return nil
}

func (g *GitFilterCarrierGrant) UnmarshalJSON(raw []byte) error {
	type plain GitFilterCarrierGrant
	var p plain
	if gitFilterDecode(raw, &p) != nil {
		return ErrGitFilterContract
	}
	v := GitFilterCarrierGrant(p)
	if err := v.Validate(); err != nil {
		return err
	}
	*g = v
	return nil
}
func (r *GitFilterCarrierCleanup) UnmarshalJSON(raw []byte) error {
	type plain GitFilterCarrierCleanup
	var p plain
	if gitFilterDecode(raw, &p) != nil {
		return ErrGitFilterContract
	}
	v := GitFilterCarrierCleanup(p)
	if err := v.Validate(); err != nil {
		return err
	}
	*r = v
	return nil
}
