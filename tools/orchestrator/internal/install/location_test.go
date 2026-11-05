package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func locationInstall(t *testing.T) (InstallResult, string) {
	t.Helper()
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: filepath.Join(root, "installed"), DataRoot: filepath.Join(root, "data"), Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	return installed, filepath.Dir(filepath.Dir(installed.VersionRoot))
}

func locationManifest(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

// Re-sign only synthetic fixture metadata, so tests reach format/location
// admission instead of being rejected by the unchanged receipt hash check.
func locationRewrite(t *testing.T, installed InstallResult, destination string, value map[string]any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(installed.ManifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(installed.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(manifestReceipt{Version: installed.Version, ManifestSHA256: digest})
	if err = os.WriteFile(filepath.Join(destination, "manifests", installed.Version+".json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInstalledManifestBindsNewVersionRoot(t *testing.T) {
	i, destination := locationInstall(t)
	m := locationManifest(t, i.ManifestPath)
	if m["schema_version"] != json.Number("2") || m["installed_root"] != i.VersionRoot {
		t.Fatalf("new install lacks versioned canonical location: schema=%v root=%v", m["schema_version"], m["installed_root"])
	}
	report, err := Doctor(destination)
	if err != nil || !report.Healthy {
		t.Fatalf("original location: %+v %v", report, err)
	}
	if err = PinVersion(destination, "same-root", i.Version); err != nil {
		t.Fatal(err)
	}
}

func TestInstalledManifestRejectsWholeDestinationRename(t *testing.T) {
	i, destination := locationInstall(t)
	before, err := os.Stat(filepath.Join(i.VersionRoot, "bin", "codex-orchestrator"))
	if err != nil {
		t.Fatal(err)
	}
	moved := destination + "-relocated"
	if err = os.Rename(destination, moved); err != nil {
		t.Fatal(err)
	}
	versionRoot := filepath.Join(moved, "versions", i.Version)
	executable := filepath.Join(versionRoot, "bin", "codex-orchestrator")
	after, err := os.Stat(executable)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("fixture must retain the same executable inode")
	}
	report, err := Doctor(moved)
	if err != nil || report.Healthy || !slices.Contains(report.Issues, i.Version+":install_location_changed_reinstall_required") {
		t.Fatalf("renamed install silently adopted: %+v %v", report, err)
	}
	if err = PinVersion(moved, "moved", i.Version); err == nil {
		t.Fatal("moved install obtained a pin")
	}
	if _, err = PinRunningVersion(executable, "moved-running"); err == nil {
		t.Fatal("moved executable obtained a running pin")
	}
	if _, _, err = CodexGuardRuntime(executable); err == nil {
		t.Fatal("moved install supplied authentication runtime")
	}
	if files, _ := os.ReadDir(filepath.Join(moved, "pins")); len(files) != 0 {
		t.Fatalf("rejection created pins: %d", len(files))
	}
	result, err := UninstallPackage(moved, i.Version)
	if err == nil || result.Status == "removed" {
		t.Fatalf("moved install was automatically deleted: %+v %v", result, err)
	}
	if _, err = os.Stat(executable); err != nil {
		t.Fatal("rejected relocation must retain its files")
	}
}

func TestInstalledManifestLegacyLocationRemainsCompatible(t *testing.T) {
	i, destination := locationInstall(t)
	m := locationManifest(t, i.ManifestPath)
	m["schema_version"] = 1
	delete(m, "installed_root")
	locationRewrite(t, i, destination, m)
	moved := destination + "-legacy-relocated"
	if err := os.Rename(destination, moved); err != nil {
		t.Fatal(err)
	}
	report, err := Doctor(moved)
	if err != nil || !report.Healthy {
		t.Fatalf("legacy location contract changed: %+v %v", report, err)
	}
	if err = PinVersion(moved, "legacy", i.Version); err != nil {
		t.Fatal(err)
	}
}

func TestInstalledManifestRejectsMalformedVersionedLocation(t *testing.T) {
	for _, kind := range []string{"missing", "relative", "unclean", "other-root", "unknown-schema", "legacy-with-binding"} {
		t.Run(kind, func(t *testing.T) {
			i, destination := locationInstall(t)
			m := locationManifest(t, i.ManifestPath)
			m["schema_version"] = 2
			m["installed_root"] = i.VersionRoot
			switch kind {
			case "missing":
				delete(m, "installed_root")
			case "relative":
				m["installed_root"] = "relative"
			case "unclean":
				m["installed_root"] = i.VersionRoot + "/."
			case "other-root":
				m["installed_root"] = i.VersionRoot + "-other"
			case "unknown-schema":
				m["schema_version"] = 3
			case "legacy-with-binding":
				m["schema_version"] = 1
			}
			locationRewrite(t, i, destination, m)
			report, err := Doctor(destination)
			if err != nil || report.Healthy {
				t.Fatalf("invalid binding admitted: %+v %v", report, err)
			}
			if err = PinVersion(destination, "invalid", i.Version); err == nil {
				t.Fatal("invalid location obtained pin")
			}
		})
	}
}

func TestInstalledManifestQuarantineResumeUsesLogicalLocation(t *testing.T) {
	for _, moveDestination := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "moved-reject"}[moveDestination], func(t *testing.T) {
			i, destination := locationInstall(t)
			value, err := readManifest(i.ManifestPath)
			if err != nil {
				t.Fatal(err)
			}
			intent := uninstallIntent{SchemaVersion: 1, Version: i.Version, ManifestSHA256: i.ManifestSHA256, Manifest: value, CreatedAt: "test"}
			intent.ManifestBody, err = os.ReadFile(i.ManifestPath)
			if err != nil {
				t.Fatal(err)
			}
			intentPath := filepath.Join(destination, "uninstall-intents", i.Version+".json")
			if err = writeExclusive(intentPath, mustLocationJSON(t, intent), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(i.ManifestPath); err != nil {
				t.Fatal(err)
			}
			quarantine := filepath.Join(destination, "versions", ".uninstall-root-"+i.Version)
			if err = os.Rename(i.VersionRoot, quarantine); err != nil {
				t.Fatal(err)
			}
			if moveDestination {
				moved := destination + "-moved"
				if err = os.Rename(destination, moved); err != nil {
					t.Fatal(err)
				}
				destination = moved
				quarantine = filepath.Join(destination, "versions", ".uninstall-root-"+i.Version)
			}
			result, err := UninstallPackage(destination, i.Version)
			if moveDestination {
				if err == nil || result.Status == "removed" {
					t.Fatalf("moved recovery adopted: %+v %v", result, err)
				}
				if _, err = os.Stat(filepath.Join(quarantine, "bin", "codex-orchestrator")); err != nil {
					t.Fatal("rejected moved recovery lost files")
				}
			} else if err != nil || result.Status != "removed" {
				t.Fatalf("owned quarantine could not resume: %+v %v", result, err)
			}
		})
	}
}

func TestInstalledManifestIntentCannotRebindMovedLocation(t *testing.T) {
	i, destination := locationInstall(t)
	value, err := readManifest(i.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	intent := uninstallIntent{SchemaVersion: 1, Version: i.Version, ManifestSHA256: i.ManifestSHA256, Manifest: value, CreatedAt: "test"}
	intent.ManifestBody, err = os.ReadFile(i.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	moved := destination + "-moved"
	if err = os.Rename(destination, moved); err != nil {
		t.Fatal(err)
	}
	// Editing recovery metadata cannot change the location covered by the
	// original receipt hash, even if all files retain their original inodes.
	intent.Manifest.InstalledRoot = filepath.Join(moved, "versions", i.Version)
	intentPath := filepath.Join(moved, "uninstall-intents", i.Version+".json")
	if err = writeExclusive(intentPath, mustLocationJSON(t, intent), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := UninstallPackage(moved, i.Version)
	if err == nil || result.Status == "removed" {
		t.Fatalf("mutable intent rebound original receipt: %+v %v", result, err)
	}
	if _, err = os.Stat(filepath.Join(intent.Manifest.InstalledRoot, "bin", "codex-orchestrator")); err != nil {
		t.Fatal("unproved recovery deleted files")
	}
}

func mustLocationJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestInstalledManifestIntentCannotRedefineReceiptHash(t *testing.T) {
	for _, downgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "rehashed-binding", true: "downgraded-schema"}[downgrade], func(t *testing.T) {
			i, destination := locationInstall(t)
			value, err := readManifest(i.ManifestPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(i.ManifestPath); err != nil {
				t.Fatal(err)
			}
			moved := destination + "-moved"
			if err = os.Rename(destination, moved); err != nil {
				t.Fatal(err)
			}
			value.InstalledRoot = filepath.Join(moved, "versions", i.Version)
			if downgrade {
				value.SchemaVersion = 1
				value.InstalledRoot = ""
			}
			body, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, '\n')
			digest := sha256.Sum256(body)
			intent := uninstallIntent{SchemaVersion: 1, Version: i.Version, ManifestSHA256: hex.EncodeToString(digest[:]), Manifest: value, ManifestBody: body, CreatedAt: "test"}
			if downgrade {
				intent.ManifestBody = nil
			}
			if err = writeExclusive(filepath.Join(moved, "uninstall-intents", i.Version+".json"), mustLocationJSON(t, intent), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := UninstallPackage(moved, i.Version)
			if err == nil || result.Status == "removed" {
				t.Fatalf("recovery metadata replaced original receipt authority: %+v %v", result, err)
			}
			if _, err = os.Stat(filepath.Join(moved, "versions", i.Version, "bin", "codex-orchestrator")); err != nil {
				t.Fatal("receipt mismatch discovered only after destructive deletion")
			}
		})
	}
}
