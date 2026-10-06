package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func indexedManifestFixture() IndexedReviewManifest {
	m := IndexedReviewManifest{Version: 1, Scope: IndexedReviewScope, CandidateReceiptSHA256: strings.Repeat("a", 64), BaseOID: strings.Repeat("b", 40), CandidateOID: strings.Repeat("c", 40), TreeOID: strings.Repeat("d", 40)}
	for i := 0; i < 3; i++ {
		m.Chunks = append(m.Chunks, IndexedReviewChunk{Ordinal: i, Path: "a", Side: "candidate", Mode: "100644", BlobOID: strings.Repeat("e", 40), WholeSHA256: strings.Repeat("f", 64), Offset: i * 4, Length: 4, SHA256: strings.Repeat("1", 64)})
	}
	m.Pages = []IndexedReviewPage{{Ordinal: 0, ChunkOrdinals: []int{0, 1, 2}}}
	return m
}

func TestIndexedReviewManifestRejectsIncompleteOrAmbiguousCoverage(t *testing.T) {
	for _, kind := range []string{"valid", "overlap", "gap", "ordinal", "page_missing", "page_repeat", "page_order", "path", "side", "mode", "whole_hash", "oid_algorithm", "retained_content", "oversize_chunk", "empty_followed", "extra_page"} {
		t.Run(kind, func(t *testing.T) {
			m := indexedManifestFixture()
			switch kind {
			case "overlap":
				m.Chunks[1].Offset--
			case "gap":
				m.Chunks[1].Offset++
			case "ordinal":
				m.Chunks[1].Ordinal = 0
			case "page_missing":
				m.Pages[0].ChunkOrdinals = m.Pages[0].ChunkOrdinals[:2]
			case "page_repeat":
				m.Pages[0].ChunkOrdinals[1] = 0
			case "page_order":
				m.Pages[0].ChunkOrdinals[1], m.Pages[0].ChunkOrdinals[2] = 2, 1
			case "path":
				m.Chunks[0].Path = "../a"
			case "side":
				m.Chunks[0].Side = "other"
			case "mode":
				m.Chunks[0].Mode = "120000"
			case "whole_hash":
				m.Chunks[1].WholeSHA256 = strings.Repeat("2", 64)
			case "oid_algorithm":
				m.Chunks[1].BlobOID = strings.Repeat("e", 64)
			case "retained_content":
				m.Chunks[0].Content = "data"
			case "oversize_chunk":
				m.Chunks[0].Length = 32769
			case "empty_followed":
				m.Chunks[0].Length = 0
			case "extra_page":
				m.Pages = append(m.Pages, IndexedReviewPage{Ordinal: 1})
			}
			if err := ValidateIndexedReviewManifest(m); (err == nil) != (kind == "valid") {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}

func TestIndexedReviewCoverageBindsExactAssignment(t *testing.T) {
	m := indexedManifestFixture()
	assigned, err := IndexedReviewAssignment(m, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	c := IndexedReviewCoverage{Version: 1, ManifestSHA256: IndexedReviewJSONSHA256(m), AssignmentSHA256: IndexedReviewJSONSHA256(assigned), GrantIntentSHA256: strings.Repeat("a", 64), AcceptedInputSHA256: strings.Repeat("b", 64), ConsumerInputSHA256: strings.Repeat("c", 64), ConsumerSnapshotSHA256: strings.Repeat("d", 64), ReviewOrdinal: 1, Shards: 3}
	if err = ValidateIndexedReviewCoverage(m, c); err != nil {
		t.Fatal(err)
	}
	c.ReviewOrdinal = 0
	if ValidateIndexedReviewCoverage(m, c) == nil {
		t.Fatal("substituted assignment accepted")
	}
	raw, _ := json.Marshal(m)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["content"]; ok {
		t.Fatal("content serialized in manifest")
	}
}

func TestIndexedReviewManifestCapacityBoundaries(t *testing.T) {
	for _, kind := range []string{"2048_chunks_8MiB", "2049_chunks", "65_page_entries", "escaped_manifest_below_limit", "escaped_manifest_over_limit"} {
		t.Run(kind, func(t *testing.T) {
			m := indexedManifestFixture()
			prototype := m.Chunks[0]
			prototype.Length = 4096
			count, entries := 2048, 64
			valid := kind == "2048_chunks_8MiB" || kind == "escaped_manifest_below_limit"
			switch kind {
			case "2049_chunks":
				count++
				// Stay below the text budget to isolate the count guard.
				prototype.Length = 1
			case "65_page_entries":
				entries = 65
			case "escaped_manifest_below_limit":
				prototype.Path = strings.Repeat("<", 5)
			case "escaped_manifest_over_limit":
				prototype.Path = strings.Repeat("<", 64)
			}
			m.Chunks, m.Pages = nil, nil
			for i := 0; i < count; i++ {
				chunk := prototype
				chunk.Ordinal, chunk.Offset = i, i*chunk.Length
				m.Chunks = append(m.Chunks, chunk)
				if i%entries == 0 {
					m.Pages = append(m.Pages, IndexedReviewPage{Ordinal: len(m.Pages)})
				}
				last := &m.Pages[len(m.Pages)-1]
				last.ChunkOrdinals = append(last.ChunkOrdinals, i)
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(kind, "escaped_manifest_") {
				if !strings.Contains(string(raw), `\u003c`) || (len(raw) <= 1024*1024) != valid {
					t.Fatalf("fixture does not cross encoded manifest boundary: bytes=%d", len(raw))
				}
			}
			if err := ValidateIndexedReviewManifest(m); (err == nil) != valid {
				t.Fatalf("encoded bytes=%d validation=%v wantValid=%v", len(raw), err, valid)
			}
		})
	}
}
