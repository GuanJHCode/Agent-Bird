package gitops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

type reviewReadJournal struct{ records []GitProcessRecord }

func (j *reviewReadJournal) RecordGitProcess(_ context.Context, r GitProcessRecord) error {
	j.records = append(j.records, r)
	return nil
}

// Keep the complete tree at the requested size; only its first path changes.
// This checks limits on unchanged context as well as on receipt.Changes.
func indexedBoundaryReceipt(t *testing.T, paths []string, before, after string) CandidateReceipt {
	t.Helper()
	d, _ := repo(t)
	git(t, d, "rm", "README")
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(d, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, path), []byte(before), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, d, "add", "-A")
	git(t, d, "commit", "-qm", "index boundary base")
	base := git(t, d, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(d, paths[0]), []byte(after), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, d, "add", "--", paths[0])
	git(t, d, "commit", "-qm", "index boundary candidate")
	r := frozenReviewReceipt(t, d, base, git(t, d, "rev-parse", "HEAD"), git(t, d, "rev-parse", "HEAD^{tree}"))
	r.Changes = []CandidateChange{{Path: paths[0], Mode: "100644", BlobOID: git(t, d, "rev-parse", "HEAD:"+paths[0])}}
	return r
}

func TestCandidateReviewIndexRejectsUnsupportedUnchangedContext(t *testing.T) {
	for _, kind := range []string{"binary", "invalid_utf8", "lfs", "symlink", "submodule"} {
		t.Run(kind, func(t *testing.T) {
			d, original := repo(t)
			want := "unsupported tracked content"
			switch kind {
			case "symlink":
				if err := os.Symlink("README", filepath.Join(d, "unsupported")); err != nil {
					t.Fatal(err)
				}
				git(t, d, "add", "unsupported")
				want = "unsupported tree entry"
			case "submodule":
				git(t, d, "update-index", "--add", "--cacheinfo", "160000,"+original+",unsupported")
				want = "unsupported tree entry"
			default:
				body := map[string][]byte{
					"binary":       {'x', 0, 'y'},
					"invalid_utf8": {'x', 0xff, 'y'},
					"lfs":          []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize 1\n"),
				}[kind]
				if err := os.WriteFile(filepath.Join(d, "unsupported"), body, 0600); err != nil {
					t.Fatal(err)
				}
				git(t, d, "add", "unsupported")
			}
			git(t, d, "commit", "-qm", "unsupported base context")
			base := git(t, d, "rev-parse", "HEAD")
			if err := os.WriteFile(filepath.Join(d, "README"), []byte("legal candidate change\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, d, "add", "README")
			git(t, d, "commit", "-qm", "candidate")
			r := frozenReviewReceipt(t, d, base, git(t, d, "rev-parse", "HEAD"), git(t, d, "rev-parse", "HEAD^{tree}"))
			r.Changes = []CandidateChange{{Path: "README", Mode: "100644", BlobOID: git(t, d, "rev-parse", "HEAD:README")}}
			index, err := CandidateReviewIndex(context.Background(), r, strings.Repeat("a", 64))
			if err == nil || !strings.Contains(err.Error(), want) || len(index.Chunks) != 0 {
				t.Fatalf("unsupported unchanged context returned partial/successful index: chunks=%d err=%v", len(index.Chunks), err)
			}
		})
	}
}

func TestCandidateReviewIndexPathBoundaries(t *testing.T) {
	for _, kind := range []string{"paths_at_limit", "paths_over_limit", "path_bytes_at_limit", "path_bytes_over_limit"} {
		t.Run(kind, func(t *testing.T) {
			var paths []string
			wantErr := ""
			if strings.HasPrefix(kind, "paths_") {
				count := 512
				if kind == "paths_over_limit" {
					count++
					wantErr = "too many tracked paths"
				}
				for i := 0; i < count; i++ {
					paths = append(paths, fmt.Sprintf("p-%03d", i))
				}
			} else {
				// Each 512-byte path has components below the filesystem's
				// NAME_MAX. 128 distinct paths total exactly 64KiB.
				for i := 0; i < 128; i++ {
					paths = append(paths, fmt.Sprintf("%03d", i)+strings.Repeat("a", 167)+"/"+strings.Repeat("b", 170)+"/"+strings.Repeat("c", 170))
				}
				if kind == "path_bytes_over_limit" {
					paths = append(paths, "z")
					wantErr = "path bytes exceed limit"
				}
			}
			r := indexedBoundaryReceipt(t, paths, "", "x")
			index, err := CandidateReviewIndex(context.Background(), r, strings.Repeat("a", 64))
			if wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), wantErr) || len(index.Chunks) != 0 {
					t.Fatalf("over-limit index: chunks=%d err=%v", len(index.Chunks), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]map[string]bool{}
			for _, chunk := range index.Chunks {
				if seen[chunk.Path] == nil {
					seen[chunk.Path] = map[string]bool{}
				}
				if seen[chunk.Path][chunk.Side] || chunk.Content != "" {
					t.Fatal("duplicate side or source bytes retained in manifest")
				}
				seen[chunk.Path][chunk.Side] = true
			}
			if len(index.Chunks) != len(paths)*2 || len(seen) != len(paths) {
				t.Fatal("boundary silently omitted tracked context")
			}
			for _, path := range paths {
				if !seen[path]["base"] || !seen[path]["candidate"] {
					t.Fatalf("missing logical side for %q", path)
				}
			}
		})
	}
}

func TestCandidateReviewIndexTotalTextBoundary(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprintf("extra_%d", extra), func(t *testing.T) {
			r := indexedBoundaryReceipt(t, []string{"text"}, strings.Repeat("x", 4*1024*1024), strings.Repeat("y", 4*1024*1024+extra))
			index, err := CandidateReviewIndex(context.Background(), r, strings.Repeat("a", 64))
			if extra == 1 {
				if err == nil || !strings.Contains(err.Error(), "logical text exceeds limit") || len(index.Chunks) != 0 {
					t.Fatalf("over-limit index: chunks=%d err=%v", len(index.Chunks), err)
				}
				return
			}
			if err != nil || indexedTextBytes(index.Chunks) != 8*1024*1024 {
				t.Fatalf("exact logical byte boundary lost content: err=%v", err)
			}
		})
	}
}

func TestHydrateReviewChunksConsumerBoundaryBeforeBlobRead(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprintf("extra_%d", extra), func(t *testing.T) {
			body := strings.Repeat("x", 384*1024+extra)
			r := indexedBoundaryReceipt(t, []string{"text"}, "", body)
			index, err := CandidateReviewIndex(context.Background(), r, strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			var assigned []ReviewIndexChunk
			for _, chunk := range index.Chunks {
				if chunk.Side == "candidate" {
					assigned = append(assigned, chunk)
				}
			}
			validation := &reviewReadJournal{}
			ctx := context.WithValue(WithCandidateJournal(context.Background(), validation), candidateRunnerKey{}, true)
			if err := validateReviewSnapshotReceipt(ctx, r); err != nil {
				t.Fatal(err)
			}
			journal := &reviewReadJournal{}
			hydrated, err := HydrateReviewChunks(WithCandidateJournal(context.Background(), journal), r, assigned)
			if extra == 1 {
				if err == nil || !strings.Contains(err.Error(), "assigned text exceeds consumer limit") || len(hydrated) != 0 {
					t.Fatalf("over-limit hydration: chunks=%d err=%v", len(hydrated), err)
				}
				commands := func(records []GitProcessRecord) []string {
					var out []string
					for _, record := range records {
						if record.Phase == "intent" {
							out = append(out, record.CommandDigest)
						}
					}
					return out
				}
				if !reflect.DeepEqual(commands(validation.records), commands(journal.records)) {
					t.Fatal("over-limit hydration ran Git commands beyond receipt validation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var reconstructed strings.Builder
			for _, chunk := range hydrated {
				reconstructed.WriteString(chunk.Content)
			}
			if reconstructed.String() != body {
				t.Fatal("exact consumer boundary lost source bytes")
			}
		})
	}
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
