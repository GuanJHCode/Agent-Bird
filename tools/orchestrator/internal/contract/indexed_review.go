package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const IndexedReviewScope = "complete-tracked-text-base-and-candidate"

// IndexedReviewManifest contains metadata only. Both Host and Store validate
// the same finite logical coverage before accepting a review as authority.
type IndexedReviewChunk struct {
	Ordinal     int    `json:"ordinal"`
	Path        string `json:"path"`
	Side        string `json:"side"`
	Mode        string `json:"mode"`
	BlobOID     string `json:"blob_oid"`
	WholeSHA256 string `json:"whole_sha256"`
	Offset      int    `json:"offset"`
	Length      int    `json:"length"`
	SHA256      string `json:"sha256"`
	Content     string `json:"-"`
}

type IndexedReviewPage struct {
	Ordinal       int   `json:"ordinal"`
	ChunkOrdinals []int `json:"chunk_ordinals"`
}

type IndexedReviewManifest struct {
	Version                int                  `json:"version"`
	Scope                  string               `json:"scope"`
	CandidateReceiptSHA256 string               `json:"candidate_receipt_sha256"`
	BaseOID                string               `json:"base_oid"`
	CandidateOID           string               `json:"candidate_oid"`
	TreeOID                string               `json:"tree_oid"`
	Chunks                 []IndexedReviewChunk `json:"chunks"`
	Pages                  []IndexedReviewPage  `json:"pages"`
}

// IndexedReviewCoverage binds one actual consumer to its immutable grant and
// accepted validation input. The containing accepted artifact authenticates
// these hashes; hashes alone are not new signing authority.
type IndexedReviewCoverage struct {
	Version                int                  `json:"version"`
	ManifestSHA256         string               `json:"manifest_sha256"`
	AssignmentSHA256       string               `json:"assignment_sha256"`
	GrantIntentSHA256      string               `json:"grant_intent_sha256"`
	AcceptedInputSHA256    string               `json:"accepted_input_sha256"`
	ConsumerInputSHA256    string               `json:"consumer_input_sha256"`
	ConsumerSnapshotSHA256 string               `json:"consumer_snapshot_sha256"`
	ReviewOrdinal          int                  `json:"review_ordinal"`
	Shards                 int                  `json:"shards"`
	ConfigGeneration       *ConfigGenerationRef `json:"config_generation,omitempty"`
}

func IndexedReviewJSONSHA256(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func indexedDigest(value string, bytes int) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == bytes && hex.EncodeToString(raw) == value
}

func indexedOID(value string) bool { return indexedDigest(value, 20) || indexedDigest(value, 32) }

func indexedPath(path string) bool {
	if path == "" || len(path) > 512 || !utf8.ValidString(path) || strings.ContainsAny(path, "\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func ValidateIndexedReviewManifest(m IndexedReviewManifest) error {
	bad := errors.New("candidate_review_index_invalid")
	if m.Version != 1 || m.Scope != IndexedReviewScope || !indexedDigest(m.CandidateReceiptSHA256, 32) || !indexedOID(m.BaseOID) || !indexedOID(m.CandidateOID) || !indexedOID(m.TreeOID) || len(m.BaseOID) != len(m.CandidateOID) || len(m.BaseOID) != len(m.TreeOID) || len(m.Chunks) == 0 || len(m.Chunks) > 2048 {
		return bad
	}
	paths := map[string]bool{}
	pathBytes, textBytes := 0, 0
	for i, c := range m.Chunks {
		if c.Ordinal != i || !indexedPath(c.Path) || (c.Side != "base" && c.Side != "candidate") || (c.Mode != "100644" && c.Mode != "100755") || !indexedOID(c.BlobOID) || len(c.BlobOID) != len(m.TreeOID) || !indexedDigest(c.WholeSHA256, 32) || !indexedDigest(c.SHA256, 32) || c.Offset < 0 || c.Length < 0 || c.Length > 32*1024 || c.Content != "" {
			return bad
		}
		if !paths[c.Path] {
			paths[c.Path] = true
			pathBytes += len(c.Path)
		}
		textBytes += c.Length
		if len(paths) > 512 || pathBytes > 64*1024 || textBytes > 8*1024*1024 {
			return bad
		}
		if i > 0 {
			prev := m.Chunks[i-1]
			if c.Path < prev.Path || (c.Path == prev.Path && c.Side < prev.Side) {
				return bad
			}
			if c.Path == prev.Path && c.Side == prev.Side {
				if prev.Length == 0 || c.Length == 0 || c.Offset != prev.Offset+prev.Length || c.Mode != prev.Mode || c.BlobOID != prev.BlobOID || c.WholeSHA256 != prev.WholeSHA256 {
					return bad
				}
			} else if c.Offset != 0 {
				return bad
			}
		} else if c.Offset != 0 {
			return bad
		}
		if c.Length == 0 {
			empty := sha256.Sum256(nil)
			if c.Offset != 0 || c.SHA256 != hex.EncodeToString(empty[:]) || c.WholeSHA256 != c.SHA256 {
				return bad
			}
		}
	}
	covered := 0
	for i, p := range m.Pages {
		if p.Ordinal != i || len(p.ChunkOrdinals) == 0 || len(p.ChunkOrdinals) > 64 {
			return bad
		}
		for _, ordinal := range p.ChunkOrdinals {
			if ordinal != covered || ordinal >= len(m.Chunks) {
				return bad
			}
			covered++
		}
		raw, _ := json.Marshal(p)
		if len(raw) > 64*1024 {
			return bad
		}
	}
	if covered != len(m.Chunks) {
		return bad
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > 1024*1024 {
		return bad
	}
	return nil
}

func IndexedReviewAssignment(m IndexedReviewManifest, shards, ordinal int) ([]IndexedReviewChunk, error) {
	if ValidateIndexedReviewManifest(m) != nil || shards < 1 || shards > 63 || ordinal < 0 || ordinal >= shards {
		return nil, errors.New("candidate_review_assignment_invalid")
	}
	assigned := make([]IndexedReviewChunk, 0)
	for _, c := range m.Chunks {
		if c.Ordinal%shards == ordinal {
			assigned = append(assigned, c)
		}
	}
	return assigned, nil
}

func ValidateIndexedReviewCoverage(m IndexedReviewManifest, c IndexedReviewCoverage) error {
	assigned, err := IndexedReviewAssignment(m, c.Shards, c.ReviewOrdinal)
	if err != nil || c.Version != 1 || c.ManifestSHA256 != IndexedReviewJSONSHA256(m) || c.AssignmentSHA256 != IndexedReviewJSONSHA256(assigned) || !indexedDigest(c.GrantIntentSHA256, 32) || !indexedDigest(c.AcceptedInputSHA256, 32) || !indexedDigest(c.ConsumerInputSHA256, 32) || !indexedDigest(c.ConsumerSnapshotSHA256, 32) || (c.ConfigGeneration != nil && c.ConfigGeneration.Validate() != nil) {
		return errors.New("candidate_review_coverage_invalid")
	}
	return nil
}
