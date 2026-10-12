package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"unicode/utf8"
)

const EventTemporaryWorkspaceCleanup = "temporary_workspace_cleanup"

// An observation of the original Host's post-publication cleanup, not a grant
// to delete a path. Business result/Exited records remain immutable.
type TemporaryWorkspaceCleanup struct {
	Version       int    `json:"version"`
	Operation     string `json:"operation"`
	State         string `json:"state"`
	Worktree      string `json:"worktree"`
	ReceiptSHA256 string `json:"receipt_sha256"`
	Code          string `json:"code,omitempty"`
}

func (c TemporaryWorkspaceCleanup) Validate() error {
	invalid := errors.New("temporary_workspace_cleanup_invalid")
	if c.Version != 1 || c.Operation != "validate" && c.Operation != "review" || c.State != "pending" && c.State != "removed" || !filepath.IsAbs(c.Worktree) || filepath.Clean(c.Worktree) != c.Worktree || len(c.Worktree) > 4096 || !utf8.ValidString(c.Worktree) || bytes.IndexByte([]byte(c.Worktree), 0) >= 0 || len(c.ReceiptSHA256) != 64 || len(c.Code) > 64 || c.State == "removed" && c.Code != "" {
		return invalid
	}
	for _, b := range c.ReceiptSHA256 {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return invalid
		}
	}
	for _, b := range c.Code {
		if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_') {
			return invalid
		}
	}
	return nil
}
func (c *TemporaryWorkspaceCleanup) UnmarshalJSON(raw []byte) error {
	type plain TemporaryWorkspaceCleanup
	var v plain
	if len(raw) > 8192 || DecodeBridgeStrict(raw, &v) != nil {
		return errors.New("temporary_workspace_cleanup_invalid")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	for {
		v, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil || v == nil {
			return errors.New("temporary_workspace_cleanup_invalid")
		}
	}
	next := TemporaryWorkspaceCleanup(v)
	if e := next.Validate(); e != nil {
		return e
	}
	*c = next
	return nil
}
func TemporaryWorkspaceCleanupDigest(c TemporaryWorkspaceCleanup) (string, error) {
	if e := c.Validate(); e != nil {
		return "", e
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func ValidateTemporaryWorkspaceCleanupEvent(e Event) error {
	if e.TemporaryWorkspaceCleanup == nil {
		if e.Kind == EventTemporaryWorkspaceCleanup {
			return errors.New("temporary_workspace_cleanup_invalid")
		}
		return nil
	}
	digest, err := TemporaryWorkspaceCleanupDigest(*e.TemporaryWorkspaceCleanup)
	if err != nil || e.Kind != EventTemporaryWorkspaceCleanup || e.PayloadHash != digest || e.Artifact != nil || e.ExitCode != nil || e.Failure != nil || e.DiagnosticsFailure != nil || e.ValidationToolCleanup != nil || e.GitFilterCleanup != nil || e.GitFilterCarrierCleanup != nil || e.StopReason != "" || e.Process != nil || e.ActiveMS != 0 || e.QuestionID != "" || e.QuestionRevision != 0 || e.QuestionKind != "" || e.SessionKind != "" || e.SessionID != "" {
		return errors.New("temporary_workspace_cleanup_invalid")
	}
	return nil
}
