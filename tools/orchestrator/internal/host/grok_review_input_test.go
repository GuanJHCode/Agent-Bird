package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	chunk := gitops.ReviewIndexChunk{Ordinal: 2, Path: "a.txt", Side: "candidate", BlobOID: strings.Repeat("a", 40), WholeSHA256: strings.Repeat("b", 64), Length: 3, SHA256: strings.Repeat("c", 64), Content: "new"}
	index := gitops.ReviewIndex{Version: 1, Scope: "complete-tracked-text-base-and-candidate", CandidateReceiptSHA256: strings.Repeat("d", 64), Chunks: []gitops.ReviewIndexChunk{chunk}, Pages: []gitops.ReviewIndexPage{{Ordinal: 0, ChunkOrdinals: []int{2}}}}
	result, _, _, err := prepareGrokIndexedPrompt(process.Command{Args: []string{"-p", "review"}}, index, []gitops.ReviewIndexChunk{chunk}, strings.Repeat("e", 64), filepath.Join(root, "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(result.Args[1])
	if err != nil || !strings.Contains(string(body), `"content":"new"`) || !strings.Contains(string(body), `"ordinal":2`) {
		t.Fatalf("body=%s err=%v", body, err)
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
