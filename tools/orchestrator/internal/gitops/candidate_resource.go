package gitops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// CandidateResourceSource describes the original producer of one frozen
// candidate. It contains no caller-selected cleanup path or file inventory.
type CandidateResourceSource struct {
	Kind             string           `json:"kind"`
	HostID           string           `json:"host_id"`
	CommandID        string           `json:"command_id"`
	Binding          CandidateBinding `json:"binding"`
	JournalRelative  string           `json:"journal_relative"`
	ProjectionSHA256 string           `json:"projection_sha256"`
}

// CandidateResourceHeader is the <=8 KiB registration and recovery index. The
// full receipt remains in the immutable source journal. A maintenance actor
// must reopen that exact journal before using any path from its receipt.
type CandidateResourceHeader struct {
	Version           int                     `json:"version"`
	ResourceID        string                  `json:"resource_id"`
	Source            CandidateResourceSource `json:"source"`
	ReceiptSHA256     string                  `json:"receipt_sha256"`
	JournalSHA256     string                  `json:"journal_sha256"`
	JournalIdentity   FileIdentity            `json:"journal_identity"`
	SpoolIdentity     DirectoryIdentity       `json:"spool_identity"`
	NamespaceIdentity DirectoryIdentity       `json:"namespace_identity"`
	DirectoryIdentity DirectoryIdentity       `json:"directory_identity"`
	SealSHA256        string                  `json:"seal_sha256"`
}

func candidateResourceHash(value any) string {
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
func CandidateResourceID(source CandidateResourceSource, receiptSHA256 string) string {
	return candidateResourceHash(struct {
		Source  CandidateResourceSource `json:"source"`
		Receipt string                  `json:"receipt_sha256"`
	}{source, receiptSHA256})
}
func CandidateResourceSealDigest(header CandidateResourceHeader) string {
	header.SealSHA256 = ""
	return candidateResourceHash(header)
}
func CandidateResourceReceiptDigest(receipt CandidateReceipt) string {
	return candidateResourceHash(receipt)
}
func validResourceDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}
func (s CandidateResourceSource) Validate() error {
	bad := errors.New("candidate_resource_source_invalid")
	for _, v := range []string{s.HostID, s.CommandID, s.Binding.RunID, s.Binding.TaskID, s.Binding.AttemptID, s.Binding.SegmentID} {
		if v == "" || len(v) > 256 || strings.ContainsAny(v, "/\\\x00\r\n") {
			return bad
		}
	}
	if s.Binding.WorkRevision < 1 || s.Binding.PlanRevision < 1 || !validResourceDigest(s.ProjectionSHA256) {
		return bad
	}
	for _, v := range []string{s.Binding.RevisionSHA256, s.Binding.NewBaseSHA256} {
		if v != "" && !validResourceDigest(v) {
			return bad
		}
	}
	var key, prefix string
	switch s.Kind {
	case "producer":
		key = s.Binding.RunID + "\x00" + s.Binding.TaskID + "\x00" + s.Binding.AttemptID + "\x00" + s.Binding.SegmentID
		prefix = ".gitops-journal/"
	case "compose":
		key = s.CommandID
		prefix = "gitops-journal/"
	default:
		return bad
	}
	sum := sha256.Sum256([]byte(key))
	if s.JournalRelative != prefix+hex.EncodeToString(sum[:]) {
		return bad
	}
	return nil
}
func (h CandidateResourceHeader) Validate() error {
	bad := errors.New("candidate_resource_header_invalid")
	if h.Version != 1 || h.Source.Validate() != nil || !validResourceDigest(h.ReceiptSHA256) || !validResourceDigest(h.JournalSHA256) || h.ResourceID != CandidateResourceID(h.Source, h.ReceiptSHA256) || h.SealSHA256 != CandidateResourceSealDigest(h) || h.JournalIdentity.Inode <= 0 || h.JournalIdentity.Size <= 0 || h.JournalIdentity.Size > 16*1024*1024 {
		return bad
	}
	for _, d := range []DirectoryIdentity{h.SpoolIdentity, h.NamespaceIdentity, h.DirectoryIdentity} {
		if d.Inode <= 0 || d.UID < 0 {
			return bad
		}
	}
	body, err := json.Marshal(h)
	if err != nil || len(body) > 8192 {
		return bad
	}
	return nil
}
