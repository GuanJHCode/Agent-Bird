package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Exercise the actual open/hash admission path on each native platform. These
// cases guard the file checks while their stat representation is made portable.
func TestReportArtifactFileAdmission(t *testing.T) {
	for _, name := range []string{"valid", "mode", "hardlink", "symlink", "size", "hash"} {
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "result.txt")
			const contents = "owned report artifact\n"
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			ref := contract.ArtifactRef{ID: "result", Path: path, Size: int64(len(contents)), SHA256: runtimeHash(contents)}
			want := "unsafe_report_artifact"
			switch name {
			case "valid":
				want = ""
			case "mode":
				err = os.Chmod(path, 0644)
			case "hardlink":
				err = os.Link(path, filepath.Join(root, "alias"))
			case "symlink":
				ref.Path = filepath.Join(root, "alias")
				err = os.Symlink(path, ref.Path)
			case "size":
				ref.Size++
			case "hash":
				ref.SHA256 = runtimeHash("other report artifact\n")
				want = "report_artifact_hash_mismatch"
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{"status": "completed", "artifact": ref})
			if err != nil {
				t.Fatal(err)
			}
			event, err := validateReportPayload("result", raw, root)
			if want == "" {
				if err != nil || event.Artifact == nil || *event.Artifact != ref {
					t.Fatalf("valid artifact: event=%+v err=%v", event, err)
				}
			} else if err == nil || err.Error() != want {
				t.Fatalf("admission error=%v want=%s", err, want)
			}
		})
	}
}
