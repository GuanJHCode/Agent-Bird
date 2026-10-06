package gitops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type reviewReadJournal struct{ records []GitProcessRecord }

func (j *reviewReadJournal) RecordGitProcess(_ context.Context, r GitProcessRecord) error {
	j.records = append(j.records, r)
	return nil
}

func TestIndexedReviewBlobTotalBoundaryBeforeRead(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sizes        []int
		wantRejected bool
	}{
		{"one_at_limit", []int{8 * 1024 * 1024}, false},
		{"multiple_at_limit", []int{4 * 1024 * 1024, 4 * 1024 * 1024}, false},
		{"one_over_limit", []int{8*1024*1024 + 1}, true},
		{"multiple_over_limit", []int{4 * 1024 * 1024, 4*1024*1024 + 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := repo(t)
			index := ReviewIndex{Version: 1}
			for i, size := range tc.sizes {
				path := fmt.Sprintf("blob-%d", i)
				if err := os.WriteFile(filepath.Join(d, path), []byte(strings.Repeat("x", size)), 0600); err != nil {
					t.Fatal(err)
				}
				oid := git(t, d, "hash-object", "-w", path)
				journal := &reviewReadJournal{}
				priorBytes := indexedTextBytes(index.Chunks)
				err := appendIndexedReviewBlob(WithCandidateJournal(context.Background(), journal), d, path, "candidate", reviewTreeEntry{mode: "100644", oid: oid}, &index)
				reject := tc.wantRejected && i == len(tc.sizes)-1
				if (err != nil) != reject {
					t.Fatalf("blob %d: err=%v, wantRejected=%v", i, err, reject)
				}
				if reject {
					// Only the size query may run. No cat-file blob process may be
					// started when this blob exceeds the remaining logical budget.
					if len(journal.records) != 3 || journal.records[0].Phase != "intent" || journal.records[1].Phase != "spawned" || journal.records[2].Phase != "exited" {
						t.Fatalf("oversize blob was read: %+v", journal.records)
					}
					if indexedTextBytes(index.Chunks) != priorBytes {
						t.Fatal("rejected blob changed the index")
					}
					continue
				}
				if indexedTextBytes(index.Chunks) != priorBytes+size {
					t.Fatal("logical bytes omitted or double counted")
				}
				for _, c := range index.Chunks {
					if c.Content != "" || c.Length > indexedReviewChunkLimit {
						t.Fatal("manifest retained source bytes or oversized chunk")
					}
				}
			}
		})
	}
}

func TestIndexedReviewUTF8AcrossWriterAndChunkBoundaries(t *testing.T) {
	// The four-byte rune straddles both a Write boundary and the 32KiB chunk.
	text := strings.Repeat("a", indexedReviewChunkLimit-1) + "😀" + strings.Repeat("é", 20000) + "tail"
	index := ReviewIndex{}
	c := indexedChunkCollector{path: "unicode", side: "candidate", entry: reviewTreeEntry{mode: "100644", oid: strings.Repeat("a", 40)}, index: &index, expected: len(text), whole: sha256.New()}
	for start := 0; start < len(text); {
		end := start + 7
		if end > len(text) {
			end = len(text)
		}
		if _, err := c.Write([]byte(text[start:end])); err != nil {
			t.Fatal(err)
		}
		start = end
	}
	if err := c.finish(); err != nil {
		t.Fatal(err)
	}
	position := 0
	for _, chunk := range index.Chunks {
		if chunk.Content != "" || chunk.Offset != position || chunk.Length > indexedReviewChunkLimit {
			t.Fatalf("invalid metadata: %+v", chunk)
		}
		part := text[chunk.Offset : chunk.Offset+chunk.Length]
		if !utf8.ValidString(part) || chunk.SHA256 != reviewChunkDigest([]byte(part)) || chunk.WholeSHA256 != reviewChunkDigest([]byte(text)) {
			t.Fatal("UTF8 chunk lost bytes or digest")
		}
		position += chunk.Length
	}
	if position != len(text) {
		t.Fatal("incomplete Unicode coverage")
	}
}
