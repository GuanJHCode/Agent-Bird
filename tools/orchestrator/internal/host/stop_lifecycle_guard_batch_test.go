package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchStopLifecycleMetadataBindsIdentityAndPrivateRegularFile(t *testing.T) {
	for _, name := range []string{"exited", "unknown", "prepared", "wrong-segment", "wrong-attempt", "wrong-version", "malformed", "oversized", "public-mode", "symlink"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "launch.json")
			record := map[string]any{"version": 1, "attempt_id": "attempt", "segment_id": "segment", "phase": name}
			if name == "wrong-segment" {
				record["segment_id"] = "other-segment"
			}
			if name == "wrong-attempt" {
				record["attempt_id"] = "other-attempt"
			}
			if name == "wrong-version" {
				record["version"] = 2
			}
			body, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if name == "malformed" {
				body = []byte("{")
			}
			if name == "oversized" {
				body = []byte(strings.Repeat(" ", 64*1024+1))
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			if name == "public-mode" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if name == "symlink" {
				link := filepath.Join(root, "link.json")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			terminal, err := readStopTerminal(path, "attempt", "segment")
			valid := name == "exited" || name == "unknown" || name == "prepared"
			if (err == nil) != valid || terminal != (name == "exited" || name == "unknown") {
				t.Fatalf("metadata decision=%v err=%v", terminal, err)
			}
		})
	}
}
