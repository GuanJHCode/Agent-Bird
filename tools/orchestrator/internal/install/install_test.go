package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func makePackage(t *testing.T, root, version string) (string, string) {
	t.Helper()
	source := filepath.Join(root, "source-"+version)
	if err := os.MkdirAll(filepath.Join(source, ".codex-plugin", "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".codex-plugin", "plugin.json"), []byte(`{"name":"codex-orchestrator","version":"`+version+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".codex-plugin", "hooks", "hooks.json"), []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "codex-orchestrator-"+version)
	if err := os.WriteFile(binary, []byte("binary-"+version), 0o700); err != nil {
		t.Fatal(err)
	}
	return source, binary
}

func TestInstallKeepsVersionsAndDoesNotTouchDataRoot(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "package")
	data := filepath.Join(root, "native-data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(data, "session.jsonl")
	if err := os.WriteFile(sentinel, []byte("session"), 0o600); err != nil {
		t.Fatal(err)
	}
	source1, binary1 := makePackage(t, root, "1.0.0")
	source2, binary2 := makePackage(t, root, "1.1.0")
	first, err := InstallPackage(InstallOptions{SourceRoot: source1, BinaryPath: binary1, DestinationRoot: destination, Version: "1.0.0", DataRoot: data})
	if err != nil {
		t.Fatal(err)
	}
	second, err := InstallPackage(InstallOptions{SourceRoot: source2, BinaryPath: binary2, DestinationRoot: destination, Version: "1.1.0", DataRoot: data})
	if err != nil {
		t.Fatal(err)
	}
	if first.VersionRoot == second.VersionRoot || !fileExists(first.ManifestPath) || !fileExists(second.ManifestPath) {
		t.Fatalf("versions not independent: %#v %#v", first, second)
	}
	if got, _ := os.ReadFile(sentinel); string(got) != "session" {
		t.Fatal("native data changed")
	}
	if _, err := os.Stat(filepath.Join(destination, "versions", "1.0.0", "bin", "codex-orchestrator")); err != nil {
		t.Fatal(err)
	}
}

func TestActivePinBlocksUninstallThenUnmodifiedVersionIsRemoved(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if err := PinVersion(destination, "task-1", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallPackage(destination, "1.0.0"); !errors.Is(err, ErrVersionPinned) {
		t.Fatalf("uninstall err = %v", err)
	}
	if !fileExists(installed.VersionRoot) {
		t.Fatal("pinned version removed")
	}
	if err := UnpinVersion(destination, "task-1"); err != nil {
		t.Fatal(err)
	}
	result, err := UninstallPackage(destination, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "removed" || fileExists(installed.VersionRoot) {
		t.Fatalf("uninstall result = %#v", result)
	}
}

func TestChangedFileIsRetainedAndReported(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(installed.VersionRoot, ".codex-plugin", "plugin.json")
	if err := os.WriteFile(changed, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := UninstallPackage(destination, "1.0.0")
	if !errors.Is(err, ErrChangedFiles) || result.Status != "retained" || !fileExists(installed.VersionRoot) || !fileExists(changed) {
		t.Fatalf("changed uninstall result = %#v err=%v", result, err)
	}
}

func TestChangedManifestIsRetainedAndReported(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed.ManifestPath, []byte(`{"schema_version":1,"package_name":"codex-orchestrator","version":"1.0.0","files":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := UninstallPackage(destination, "1.0.0")
	if !errors.Is(err, ErrChangedFiles) || result.Status != "retained" || !fileExists(installed.VersionRoot) {
		t.Fatalf("manifest change result = %#v err=%v", result, err)
	}
}

func TestDoctorReportsPinnedAndChangedStateWithoutTouchingData(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if err := PinVersion(destination, "task-1", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	report, err := Doctor(destination)
	if err != nil || !report.Healthy || len(report.Versions) != 1 || len(report.Pins) != 1 {
		t.Fatalf("doctor = %#v err=%v", report, err)
	}
	if err := os.WriteFile(filepath.Join(installed.VersionRoot, ".codex-plugin", "plugin.json"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = Doctor(destination)
	if err != nil || report.Healthy || len(report.Issues) == 0 {
		t.Fatalf("changed doctor = %#v err=%v", report, err)
	}
}

func TestMalformedPinPreventsUninstallAndRetainsVersion(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "pins", "active.json"), []byte(`{"task_id":"active"`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := UninstallPackage(destination, "1.0.0")
	if err == nil || result.Status != "unknown" || !fileExists(installed.VersionRoot) {
		t.Fatalf("malformed pin uninstall result = %#v err=%v", result, err)
	}
}

func TestPinAndUninstallHonorInstallLock(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(destination, ".install.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := PinVersion(destination, "task-locked", "1.0.0"); err == nil {
		t.Fatal("pin bypassed install lock")
	}
	result, err := UninstallPackage(destination, "1.0.0")
	if err == nil || result.Status != "unknown" || !fileExists(installed.VersionRoot) {
		t.Fatalf("locked uninstall result = %#v err=%v", result, err)
	}
}

func TestUnknownNestedManifestRetainsVersion(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(installed.VersionRoot, ".codex-plugin", "nested", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(unknown), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unknown, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := UninstallPackage(destination, "1.0.0")
	if !errors.Is(err, ErrChangedFiles) || result.Status != "retained" || !fileExists(unknown) || !fileExists(installed.VersionRoot) {
		t.Fatalf("nested manifest uninstall result = %#v err=%v", result, err)
	}
}

func TestUninstallResumesAfterOwnedManifestWasAlreadyRemoved(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	value, err := readManifest(installed.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	intent := uninstallIntent{SchemaVersion: 1, Version: "1.0.0", ManifestSHA256: installed.ManifestSHA256, Manifest: value, CreatedAt: "test"}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	intentPath := filepath.Join(destination, "uninstall-intents", "1.0.0.json")
	if err := os.WriteFile(intentPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(installed.VersionRoot, "bin", "codex-orchestrator")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(installed.ManifestPath); err != nil {
		t.Fatal(err)
	}
	result, err := UninstallPackage(destination, "1.0.0")
	if err != nil || result.Status != "removed" || fileExists(installed.VersionRoot) || fileExists(intentPath) {
		t.Fatalf("resume result=%#v err=%v", result, err)
	}
}

func TestInstallRejectsFinalSymlinkAndDoesNotChangeExistingDirectoryMode(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	realDestination := filepath.Join(root, "real-package")
	if err := os.Mkdir(realDestination, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDestination := filepath.Join(root, "package-link")
	if err := os.Symlink(realDestination, linkDestination); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: linkDestination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")}); err == nil {
		t.Fatal("symlink destination accepted")
	}
	if _, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: realDestination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")}); err == nil {
		t.Fatal("unsafe existing destination mode accepted")
	}
	info, err := os.Stat(realDestination)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("existing directory mode changed: info=%v err=%v", info, err)
	}
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
