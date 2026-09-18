package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	runtimeRoot := filepath.Join(source, "runtime", "g0")
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeNames := []string{"projectproxy_launchd_entrypoint.py", "launch_activation.py", "activation_service.py", "proxy_transport.py", "proxy_observer.py", "owned_child_guard.py", "owner_helper.py", "auth_isolation.py", "delivery_adapter.py", "delivery_audit.py", "receipt_store.py"}
	var runtimeFiles []map[string]any
	for _, name := range runtimeNames {
		path := filepath.Join(runtimeRoot, name)
		mode := os.FileMode(0o644)
		if name == "projectproxy_launchd_entrypoint.py" {
			mode = 0o700
		}
		if err := os.WriteFile(path, []byte("# "+name+"\n"), mode); err != nil {
			t.Fatal(err)
		}
		digest, err := hashFile(path)
		if err != nil {
			t.Fatal(err)
		}
		runtimeFiles = append(runtimeFiles, map[string]any{"logical_id": strings.TrimSuffix(name, ".py"), "path": name, "sha256": digest, "mode": mode.Perm()})
	}
	interpreter := filepath.Join(root, "python-"+version)
	if err := os.WriteFile(interpreter, []byte("synthetic pinned interpreter "+version+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	interpreterHash, err := hashFile(interpreter)
	if err != nil {
		t.Fatal(err)
	}
	runtimeManifest, err := json.Marshal(map[string]any{"version": 1, "files": runtimeFiles, "interpreter": map[string]any{"path": interpreter, "sha256": interpreterHash}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(runtimeRoot, "runtime-manifest.json"), append(runtimeManifest, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "codex-orchestrator-"+version)
	if err := os.WriteFile(binary, []byte("binary-"+version), 0o700); err != nil {
		t.Fatal(err)
	}
	return source, binary
}

func TestInstallAndDoctorRequirePinnedG0Runtime(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	runtimeModule := filepath.Join(source, "runtime", "g0", "delivery_adapter.py")
	if err := os.WriteFile(runtimeModule, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: filepath.Join(root, "rejected"), Version: "1.0.0", DataRoot: filepath.Join(root, "data-rejected")}); err == nil {
		t.Fatal("install accepted changed G0 runtime")
	}

	source, binary = makePackage(t, root, "1.1.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.1.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(filepath.Join(installed.VersionRoot, "runtime", "g0", "runtime-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Interpreter struct {
			Path string `json:"path"`
		} `json:"interpreter"`
	}
	if err = json.Unmarshal(manifestData, &manifest); err != nil || manifest.Interpreter.Path == "" {
		t.Fatalf("runtime manifest err=%v", err)
	}
	if err = os.WriteFile(manifest.Interpreter.Path, []byte("changed interpreter\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := Doctor(filepath.Dir(filepath.Dir(installed.VersionRoot)))
	if err != nil || report.Healthy || !slices.Contains(report.Issues, "1.1.0:runtime_interpreter") {
		t.Fatalf("doctor=%#v err=%v", report, err)
	}
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

func TestInstallAcceptsPackagedExecutableAlreadyInsidePlugin(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	packagedBinary := filepath.Join(source, "bin", "codex-orchestrator")
	if err = os.MkdirAll(filepath.Dir(packagedBinary), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(packagedBinary, data, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: packagedBinary, DestinationRoot: filepath.Join(root, "destination"), Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(result.VersionRoot, "bin", "codex-orchestrator"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("binary mode=%v", info.Mode().Perm())
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

func TestRunningPackageLifecyclePinIsDurableAndIdempotent(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := PinRunningVersion(filepath.Join(installed.VersionRoot, "bin", "codex-orchestrator"), "task-running")
	if err != nil {
		t.Fatal(err)
	}
	if identity.DestinationRoot != filepath.Dir(filepath.Dir(installed.VersionRoot)) || identity.Version != "1.0.0" {
		t.Fatalf("identity=%#v", identity)
	}
	if repeated, repeatErr := PinRunningVersion(filepath.Join(installed.VersionRoot, "bin", "codex-orchestrator"), "task-running"); repeatErr != nil || repeated != identity {
		t.Fatalf("repeat=%#v err=%v", repeated, repeatErr)
	}
	if _, err = UninstallPackage(destination, "1.0.0"); !errors.Is(err, ErrVersionPinned) {
		t.Fatalf("pinned uninstall err=%v", err)
	}
	if err = UnpinRunningVersion(filepath.Join(installed.VersionRoot, "bin", "codex-orchestrator"), "task-running"); err != nil {
		t.Fatal(err)
	}
	if err = UnpinRunningVersion(filepath.Join(installed.VersionRoot, "bin", "codex-orchestrator"), "task-running"); err != nil {
		t.Fatalf("idempotent unpin: %v", err)
	}
	if _, err = UninstallPackage(destination, "1.0.0"); err != nil {
		t.Fatal(err)
	}
}

func TestRunningPackagePinRejectsReplacedPinsDirectory(t *testing.T) {
	root := t.TempDir()
	source, binary := makePackage(t, root, "1.0.0")
	destination := filepath.Join(root, "package")
	installed, err := InstallPackage(InstallOptions{SourceRoot: source, BinaryPath: binary, DestinationRoot: destination, Version: "1.0.0", DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	canonicalDestination := filepath.Dir(filepath.Dir(installed.VersionRoot))
	pins := filepath.Join(canonicalDestination, "pins")
	if err = os.Rename(pins, pins+"-owned"); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, "foreign-pins")
	if err = os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(foreign, pins); err != nil {
		t.Fatal(err)
	}
	if _, err = PinRunningVersion(filepath.Join(installed.VersionRoot, "bin", "codex-orchestrator"), "task-unsafe"); err == nil {
		t.Fatal("pin followed replaced pins directory")
	}
	if _, err = os.Lstat(filepath.Join(foreign, "task-unsafe.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign path changed: %v", err)
	}
}

func TestRunningPackagePinRejectsUninstalledExecutable(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "codex-orchestrator")
	if err := os.WriteFile(executable, []byte("not installed"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := PinRunningVersion(executable, "task-running"); err == nil {
		t.Fatal("uninstalled executable was pinned")
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
