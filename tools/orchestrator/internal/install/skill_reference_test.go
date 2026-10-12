package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPortableSkillReferenceClosedAndOptional(t *testing.T) {
	for _, variant := range []string{"old", "reference", "changed", "unlisted", "wrong-mode", "unknown", "outer-omitted"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			source := b07Payload(t, root)
			rel := "skills/orchestrate/legacy-cli.md"
			body, err := os.ReadFile(filepath.Join(source, payloadManifestName))
			if err != nil {
				t.Fatal(err)
			}
			var payload payloadManifest
			if err = json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			if variant != "old" {
				if variant == "unknown" {
					rel = "skills/orchestrate/arbitrary.md"
				}
				if err = os.WriteFile(filepath.Join(source, rel), []byte("reference"), 0600); err != nil {
					t.Fatal(err)
				}
				digest, err := hashFile(filepath.Join(source, rel))
				if err != nil {
					t.Fatal(err)
				}
				if variant != "unlisted" {
					payload.Files = append(payload.Files, payloadFile{Path: rel, SHA256: digest, Mode: 0600})
				}
			}
			body, _ = json.Marshal(payload)
			if err = os.WriteFile(filepath.Join(source, payloadManifestName), body, 0600); err != nil {
				t.Fatal(err)
			}
			metadata := portableManifest{Version: "1.0.0", PayloadSchema: 1, Files: map[string]string{}}
			for _, file := range payload.Files {
				metadata.Files[file.Path] = file.SHA256
			}
			if variant == "outer-omitted" {
				delete(metadata.Files, rel)
			}
			metadata.Files[payloadManifestName], _ = hashFile(filepath.Join(source, payloadManifestName))
			body, _ = json.Marshal(metadata)
			if err = os.WriteFile(filepath.Join(source, "portable.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			if variant == "changed" {
				if err = os.WriteFile(filepath.Join(source, rel), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if variant == "wrong-mode" {
				if err = os.Chmod(filepath.Join(source, rel), 0700); err != nil {
					t.Fatal(err)
				}
			}
			dest := filepath.Join(root, "installed")
			binary, err := InstallPortableCollectOnly(source, dest, filepath.Join(root, "state"))
			if variant != "old" && variant != "reference" {
				if err == nil {
					t.Fatal("invalid reference installed")
				}
				if variant == "outer-omitted" && err.Error() != "portable_payload_invalid" {
					t.Fatalf("cross-manifest omission escaped preflight: %v", err)
				}
				if _, err = os.Lstat(dest); !os.IsNotExist(err) {
					t.Fatal("rejection created destination")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if variant == "reference" {
				path := filepath.Join(filepath.Dir(filepath.Dir(binary)), rel)
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("reference mode: %v", err)
				}
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "reference" {
					t.Fatal("reference changed")
				}
			}
			report, err := Doctor(dest)
			if err != nil || !report.Healthy {
				t.Fatalf("doctor: %v %+v", err, report)
			}
		})
	}
}
