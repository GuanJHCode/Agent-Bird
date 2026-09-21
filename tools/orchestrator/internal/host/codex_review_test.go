package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexReviewRequiresPrivateFileMatchingFinalNativeMessage(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "review-result.json")
	body := `{"decision":"approve","summary":"verified candidate"}`
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readCodexReview(d, body); err != nil || got != body {
		t.Fatalf("valid native final rejected: %v", err)
	}
	if _, err := readCodexReview(d, `{"decision":"reject","summary":"different"}`); err == nil {
		t.Fatal("native file/message mismatch accepted")
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodexReview(d, body); err == nil {
		t.Fatal("nonprivate result accepted")
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(d, "outside"), p); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodexReview(d, body); err == nil {
		t.Fatal("symlink result accepted")
	}
}
