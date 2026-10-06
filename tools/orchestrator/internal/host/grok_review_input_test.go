package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func TestGrokReviewUsesPrivateBoundInput(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := process.Command{Path: "/private/pinned/grok", Args: []string{"-p", "Check addition", "--output-format", "streaming-json", "--json-schema", "schema"}}
	snapshot := []byte(`{"scope":"complete-tracked-text-base-and-candidate","files":[{"path":"calc.py","candidate":{"content":"return a - b"}}]}`)
	result, inputDigest, sourceDigest, err := prepareGrokSnapshotPrompt(cmd, snapshot, strings.Repeat("b", 64), filepath.Join(root, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(result.Args, " "), "return a - b") || result.Args[0] != "--prompt-file" {
		t.Fatal("source exposed in argv")
	}
	body, err := os.ReadFile(result.Args[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), string(snapshot)) || !strings.Contains(string(body), "Check addition") || inputDigest != hashBytes(body) || sourceDigest != hashBytes(snapshot) {
		t.Fatal("input binding lost")
	}
	info, _ := os.Stat(result.Args[1])
	if info.Mode().Perm() != 0600 {
		t.Fatal("nonprivate prompt")
	}
	if _, _, _, err := prepareGrokSnapshotPrompt(cmd, snapshot, strings.Repeat("b", 64), filepath.Join(root, "scratch")); err == nil {
		t.Fatal("replaced input")
	}
}

func TestGrokIndexedReviewUsesOnlyAssignedChunksInPrivatePrompt(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	chunk := gitops.ReviewIndexChunk{Ordinal: 2, Path: "a.txt", Side: "candidate", Mode: "100755", BlobOID: strings.Repeat("a", 40), WholeSHA256: strings.Repeat("b", 64), Length: 3, SHA256: strings.Repeat("c", 64), Content: "new"}
	index := gitops.ReviewIndex{Version: 1, Scope: "complete-tracked-text-base-and-candidate", CandidateReceiptSHA256: strings.Repeat("d", 64), Chunks: []gitops.ReviewIndexChunk{chunk}, Pages: []gitops.ReviewIndexPage{{Ordinal: 0, ChunkOrdinals: []int{2}}}}
	result, _, _, err := prepareGrokIndexedPrompt(process.Command{Args: []string{"-p", "review"}}, index, []gitops.ReviewIndexChunk{chunk}, strings.Repeat("e", 64), filepath.Join(root, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(result.Args[1])
	if err != nil || !strings.Contains(string(body), `"content":"new"`) || !strings.Contains(string(body), `"ordinal":2`) {
		t.Fatalf("body=%s err=%v", body, err)
	}
	if !strings.Contains(string(body), "Review only the supplied assigned chunks") || strings.Contains(string(body), "Both base and candidate contents are provided") || !strings.Contains(string(body), `"mode":"100755"`) {
		t.Fatal("partial review prompt misrepresented its scope or lost mode metadata")
	}
}
func TestGrokReviewRejectsUnboundedOrAmbiguousInput(t *testing.T) {
	for _, kind := range []string{"large", "invalid-json", "duplicate-prompt"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			cmd := process.Command{Args: []string{"-p", "brief"}}
			snapshot := []byte(`{}`)
			if kind == "large" {
				snapshot = []byte(`{"content":"` + strings.Repeat("x", 384*1024) + `"}`)
			}
			if kind == "invalid-json" {
				snapshot = []byte("not-json")
			}
			if kind == "duplicate-prompt" {
				cmd.Args = append(cmd.Args, "-p", "other")
			}
			if _, _, _, err := prepareGrokSnapshotPrompt(cmd, snapshot, strings.Repeat("b", 64), filepath.Join(root, "scratch")); err == nil {
				t.Fatal("unsafe review input")
			}
		})
	}
}

func TestGrokReviewInputMustRemainBound(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	scratch := filepath.Join(root, "scratch")
	_, digest, _, err := prepareGrokSnapshotPrompt(process.Command{Args: []string{"-p", "brief"}}, []byte(`{}`), strings.Repeat("b", 64), scratch)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyGrokReviewInput(scratch, digest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "review-input.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyGrokReviewInput(scratch, digest); err == nil {
		t.Fatal("accepted changed review input")
	}
}

func indexedPromptBoundaryFixture(t *testing.T, content string) (gitops.ReviewIndex, []gitops.ReviewIndexChunk) {
	t.Helper()
	m := gitops.ReviewIndex{Version: 1, Scope: contract.IndexedReviewScope, CandidateReceiptSHA256: strings.Repeat("a", 64), BaseOID: strings.Repeat("b", 40), CandidateOID: strings.Repeat("c", 40), TreeOID: strings.Repeat("d", 40)}
	var assigned []gitops.ReviewIndexChunk
	for offset := 0; offset < len(content); {
		end := min(offset+32*1024, len(content))
		part := content[offset:end]
		chunk := gitops.ReviewIndexChunk{Ordinal: len(assigned), Path: "added.txt", Side: "candidate", Mode: "100644", BlobOID: strings.Repeat("e", 40), WholeSHA256: hashBytes([]byte(content)), Offset: offset, Length: len(part), SHA256: hashBytes([]byte(part))}
		m.Chunks = append(m.Chunks, chunk)
		chunk.Content = part
		assigned = append(assigned, chunk)
		offset = end
	}
	page := gitops.ReviewIndexPage{Ordinal: 0}
	for i := range m.Chunks {
		page.ChunkOrdinals = append(page.ChunkOrdinals, i)
	}
	m.Pages = []gitops.ReviewIndexPage{page}
	if err := contract.ValidateIndexedReviewManifest(m); err != nil {
		t.Fatal(err)
	}
	return m, assigned
}

func TestGrokIndexedReviewBoundsCompleteEncodedPrompt(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	index, assigned := indexedPromptBoundaryFixture(t, strings.Repeat("x", 10*32*1024))
	digest := strings.Repeat("f", 64)
	// Measure the real envelope including manifest, escaped content, digest
	// and instruction; pad only the public task prompt to the exact boundary.
	base, _, _, err := prepareGrokIndexedPrompt(process.Command{Args: []string{"-p", ""}}, index, assigned, digest, filepath.Join(root, "baseline"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(base.Args[1])
	if err != nil {
		t.Fatal(err)
	}
	padding := 384*1024 - len(body)
	if padding <= 0 || padding+1 > 64*1024 {
		t.Fatalf("fixture cannot isolate complete input bound: padding=%d", padding)
	}
	for _, extra := range []int{0, 1} {
		name := "at_limit"
		if extra == 1 {
			name = "over_limit"
		}
		t.Run(name, func(t *testing.T) {
			scratch := filepath.Join(root, name)
			out, inputHash, _, err := prepareGrokIndexedPrompt(process.Command{Args: []string{"-p", strings.Repeat("p", padding+extra)}}, index, assigned, digest, scratch)
			if extra == 1 {
				if err == nil || err.Error() != "grok_review_input_too_large" {
					t.Fatalf("oversize complete prompt: %v", err)
				}
				if _, statErr := os.Lstat(scratch); !os.IsNotExist(statErr) {
					t.Fatalf("rejected input created scratch: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(out.Args[1])
			if err != nil || len(actual) != 384*1024 || inputHash != hashBytes(actual) || out.Args[0] != "--prompt-file" {
				t.Fatalf("exact input boundary not retained: bytes=%d err=%v", len(actual), err)
			}
			if err := verifyGrokReviewInput(scratch, inputHash); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGrokIndexedReviewRejectsJSONExpansionAndPromptLimit(t *testing.T) {
	for _, kind := range []string{"escaped_source", "task_at_limit", "task_over_limit"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			content, prompt, want := "x", strings.Repeat("p", 64*1024), ""
			if kind == "escaped_source" {
				// Valid 70KiB source expands beyond 384KiB in JSON.
				content, prompt, want = strings.Repeat("<", 70*1024), "", "grok_indexed_review_input_invalid"
			} else if kind == "task_over_limit" {
				prompt += "p"
				want = "grok_review_prompt_invalid"
			}
			index, assigned := indexedPromptBoundaryFixture(t, content)
			scratch := filepath.Join(root, "scratch")
			_, inputHash, _, err := prepareGrokIndexedPrompt(process.Command{Args: []string{"-p", prompt}}, index, assigned, strings.Repeat("f", 64), scratch)
			if want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if err := verifyGrokReviewInput(scratch, inputHash); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.Error() != want {
				t.Fatalf("unsafe prompt accepted: %v want=%s", err, want)
			}
			if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
				t.Fatalf("rejection created scratch: %v", err)
			}
		})
	}
}
