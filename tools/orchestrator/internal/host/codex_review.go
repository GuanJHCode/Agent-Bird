package host

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Codex's schema result is a Host-owned file and must agree with the final
// agent_message of the single successful native turn. Prose alone is insufficient.
func readCodexReview(scratch, text string) (string, error) {
	f, err := os.OpenFile(filepath.Join(scratch, "review-result.json"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errors.New("codex_review_result_invalid")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", errors.New("codex_review_result_invalid")
	}
	body, err := io.ReadAll(io.LimitReader(f, 128*1024+1))
	if err != nil || len(body) > 128*1024 || strings.TrimSpace(string(body)) != strings.TrimSpace(text) {
		return "", errors.New("codex_review_result_mismatch")
	}
	if _, err := decodeCandidateReview(string(body)); err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}
