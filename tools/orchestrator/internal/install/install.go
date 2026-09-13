package install

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

var (
	ErrVersionPinned = errors.New("version_pinned")
	ErrChangedFiles  = errors.New("changed_files_retained")
	ErrVersionExists = errors.New("version_exists")
)

type InstallOptions struct {
	SourceRoot      string
	BinaryPath      string
	DestinationRoot string
	DataRoot        string
	Version         string
}

type InstallResult struct {
	Version        string          `json:"version"`
	VersionRoot    string          `json:"version_root"`
	ManifestPath   string          `json:"manifest_path"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	BinarySHA256   string          `json:"binary_sha256"`
	Files          []string        `json:"files"`
	Runtime        RuntimeIdentity `json:"runtime"`
}

type UninstallResult struct {
	Version  string   `json:"version"`
	Status   string   `json:"status"`
	Retained []string `json:"retained,omitempty"`
}

type DoctorReport struct {
	Healthy  bool     `json:"healthy"`
	Versions []string `json:"versions"`
	Pins     []string `json:"pins"`
	Issues   []string `json:"issues"`
}

type manifest struct {
	SchemaVersion int            `json:"schema_version"`
	PackageName   string         `json:"package_name"`
	Version       string         `json:"version"`
	BinaryPath    string         `json:"binary_path"`
	BinarySHA256  string         `json:"binary_sha256"`
	Files         []manifestFile `json:"files"`
}

type manifestFile struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	Mode        uint32 `json:"mode"`
	Device      uint64 `json:"device"`
	Inode       uint64 `json:"inode"`
	Size        int64  `json:"size"`
	ModUnixNano int64  `json:"mod_unix_nano"`
}

type manifestReceipt struct {
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type uninstallIntent struct {
	SchemaVersion  int      `json:"schema_version"`
	Version        string   `json:"version"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	Manifest       manifest `json:"manifest"`
	CreatedAt      string   `json:"created_at"`
}

type pin struct {
	TaskID  string `json:"task_id"`
	Version string `json:"version"`
}

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func InstallPackage(options InstallOptions) (InstallResult, error) {
	if err := validateOptions(options); err != nil {
		return InstallResult{}, err
	}
	var err error
	if options.SourceRoot, err = canonicalManagedPath(options.SourceRoot); err != nil {
		return InstallResult{}, err
	}
	if options.BinaryPath, err = canonicalManagedPath(options.BinaryPath); err != nil {
		return InstallResult{}, err
	}
	if options.DestinationRoot, err = canonicalManagedPath(options.DestinationRoot); err != nil {
		return InstallResult{}, err
	}
	if options.DataRoot, err = canonicalManagedPath(options.DataRoot); err != nil {
		return InstallResult{}, err
	}
	if err := validatePluginSource(options.SourceRoot, options.Version); err != nil {
		return InstallResult{}, err
	}
	if _, err := ValidateRuntimeBundle(options.SourceRoot); err != nil {
		return InstallResult{}, err
	}
	if err := ensurePrivateDir(options.DestinationRoot); err != nil {
		return InstallResult{}, err
	}
	if err := ensureDataSeparate(options.DestinationRoot, options.DataRoot); err != nil {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(options.DestinationRoot, "versions"), 0o700); err != nil {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(options.DestinationRoot, "pins"), 0o700); err != nil {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(options.DestinationRoot, "manifests"), 0o700); err != nil {
		return InstallResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(options.DestinationRoot, "uninstall-intents"), 0o700); err != nil {
		return InstallResult{}, err
	}
	lock, err := acquireInstallLock(options.DestinationRoot)
	if err != nil {
		return InstallResult{}, err
	}
	defer releaseInstallLock(lock)
	versionRoot := filepath.Join(options.DestinationRoot, "versions", options.Version)
	if _, err := os.Lstat(versionRoot); err == nil {
		return InstallResult{}, ErrVersionExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return InstallResult{}, err
	}
	stage, err := makeStage(options.DestinationRoot, options.Version)
	if err != nil {
		return InstallResult{}, err
	}
	if err := copyTree(options.SourceRoot, stage); err != nil {
		return InstallResult{}, err
	}
	runtimeIdentity, err := ValidateRuntimeBundle(stage)
	if err != nil {
		return InstallResult{}, err
	}
	binaryDest := filepath.Join(stage, "bin", "codex-orchestrator")
	if err := os.MkdirAll(filepath.Dir(binaryDest), 0o700); err != nil {
		return InstallResult{}, err
	}
	if info, statErr := os.Lstat(binaryDest); statErr == nil {
		sourceHash, sourceErr := hashFile(options.BinaryPath)
		installedHash, installedErr := hashFile(binaryDest)
		if sourceErr != nil || installedErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 || sourceHash != installedHash {
			return InstallResult{}, errors.New("packaged_binary_mismatch")
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if err := copyRegular(options.BinaryPath, binaryDest, 0o700); err != nil {
			return InstallResult{}, err
		}
	} else {
		return InstallResult{}, statErr
	}
	files, err := collectFiles(stage)
	if err != nil {
		return InstallResult{}, err
	}
	binaryHash, err := hashFile(binaryDest)
	if err != nil {
		return InstallResult{}, err
	}
	value := manifest{SchemaVersion: 1, PackageName: "codex-orchestrator", Version: options.Version, BinaryPath: "bin/codex-orchestrator", BinarySHA256: binaryHash, Files: files}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return InstallResult{}, err
	}
	if err := writeExclusive(filepath.Join(stage, "manifest.json"), append(data, '\n'), 0o600); err != nil {
		return InstallResult{}, err
	}
	if err := os.Rename(stage, versionRoot); err != nil {
		return InstallResult{}, err
	}
	manifestPath := filepath.Join(versionRoot, "manifest.json")
	manifestHash, err := hashFile(manifestPath)
	if err != nil {
		return InstallResult{}, err
	}
	receiptData, _ := json.Marshal(manifestReceipt{Version: options.Version, ManifestSHA256: manifestHash})
	if err := writeExclusive(filepath.Join(options.DestinationRoot, "manifests", options.Version+".json"), append(receiptData, '\n'), 0o600); err != nil {
		return InstallResult{}, err
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	runtimeIdentity.Root = filepath.Join(versionRoot, "runtime", "g0")
	runtimeIdentity.ManifestPath = filepath.Join(runtimeIdentity.Root, "runtime-manifest.json")
	return InstallResult{Version: options.Version, VersionRoot: versionRoot, ManifestPath: manifestPath, ManifestSHA256: manifestHash, BinarySHA256: binaryHash, Files: paths, Runtime: runtimeIdentity}, nil
}

func PinVersion(destinationRoot, taskID, version string) error {
	if !validDestination(destinationRoot) || !idPattern.MatchString(taskID) || !versionPattern.MatchString(version) {
		return errors.New("pin_invalid")
	}
	destinationRoot, err := canonicalManagedPath(destinationRoot)
	if err != nil {
		return err
	}
	lock, err := acquireInstallLock(destinationRoot)
	if err != nil {
		return err
	}
	defer releaseInstallLock(lock)
	for _, directory := range []string{destinationRoot, filepath.Join(destinationRoot, "versions"), filepath.Join(destinationRoot, "manifests"), filepath.Join(destinationRoot, "pins")} {
		if err = verifyPrivateDirectory(directory); err != nil {
			return err
		}
	}
	if err = verifyInstalledVersion(destinationRoot, version); err != nil {
		return err
	}
	path := filepath.Join(destinationRoot, "pins", taskID+".json")
	data, _ := json.Marshal(pin{TaskID: taskID, Version: version})
	data = append(data, '\n')
	if active, exists, readErr := readPin(path); readErr != nil {
		return readErr
	} else if exists {
		if active.TaskID == taskID && active.Version == version {
			return nil
		}
		return errors.New("pin_conflict")
	}
	if err = writeExclusive(path, data, 0o600); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func UnpinVersion(destinationRoot, taskID string) error {
	if !validDestination(destinationRoot) || !idPattern.MatchString(taskID) {
		return errors.New("pin_invalid")
	}
	destinationRoot, err := canonicalManagedPath(destinationRoot)
	if err != nil {
		return err
	}
	lock, err := acquireInstallLock(destinationRoot)
	if err != nil {
		return err
	}
	defer releaseInstallLock(lock)
	for _, directory := range []string{destinationRoot, filepath.Join(destinationRoot, "pins")} {
		if err = verifyPrivateDirectory(directory); err != nil {
			return err
		}
	}
	path := filepath.Join(destinationRoot, "pins", taskID+".json")
	active, exists, err := readPin(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if active.TaskID != taskID {
		return errors.New("pin_conflict")
	}
	if err = os.Remove(path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func verifyInstalledVersion(destinationRoot, version string) error {
	versionRoot := filepath.Join(destinationRoot, "versions", version)
	if info, err := os.Lstat(versionRoot); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("installed_version_invalid")
	}
	manifestData, err := readOwnedRegular(filepath.Join(versionRoot, "manifest.json"), 0o600, 4*1024*1024)
	var value manifest
	if err != nil || json.Unmarshal(manifestData, &value) != nil || value.SchemaVersion != 1 || value.PackageName != "codex-orchestrator" || value.Version != version || value.BinaryPath != "bin/codex-orchestrator" {
		return errors.New("installed_version_invalid")
	}
	manifestDigest := sha256.Sum256(manifestData)
	manifestHash := hex.EncodeToString(manifestDigest[:])
	receiptData, err := readOwnedRegular(filepath.Join(destinationRoot, "manifests", version+".json"), 0o600, 4096)
	var receipt manifestReceipt
	if err != nil || json.Unmarshal(receiptData, &receipt) != nil || receipt.Version != version || receipt.ManifestSHA256 != manifestHash {
		return errors.New("installed_version_invalid")
	}
	binaryHash, err := hashFile(filepath.Join(versionRoot, filepath.FromSlash(value.BinaryPath)))
	if err != nil || binaryHash != value.BinarySHA256 {
		return errors.New("installed_version_invalid")
	}
	changed, err := changedFiles(versionRoot, value, false)
	if err != nil || len(changed) != 0 {
		return errors.New("installed_version_invalid")
	}
	return nil
}

func readPin(path string) (pin, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return pin{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSymlink != 0 {
		return pin{}, false, errors.New("pin_invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return pin{}, false, errors.New("pin_invalid")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return pin{}, false, errors.New("pin_invalid")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return pin{}, false, errors.New("pin_invalid")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	var value pin
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err != nil || len(data) > 4096 || decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF || !idPattern.MatchString(value.TaskID) || !versionPattern.MatchString(value.Version) {
		return pin{}, false, errors.New("pin_invalid")
	}
	return value, true, nil
}

func verifyPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("pin_directory_unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("pin_directory_unsafe")
	}
	return nil
}

func readOwnedRegular(path string, mode os.FileMode, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode {
		return nil, errors.New("installed_file_unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return nil, errors.New("installed_file_unsafe")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("installed_file_unsafe")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("installed_file_unsafe")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("installed_file_unsafe")
	}
	return data, nil
}

func Doctor(destinationRoot string) (DoctorReport, error) {
	if !filepath.IsAbs(destinationRoot) || filepath.Clean(destinationRoot) != destinationRoot {
		return DoctorReport{}, errors.New("doctor_path_invalid")
	}
	var err error
	destinationRoot, err = canonicalManagedPath(destinationRoot)
	if err != nil {
		return DoctorReport{}, err
	}
	report := DoctorReport{Healthy: true}
	versionsDir := filepath.Join(destinationRoot, "versions")
	entries, err := os.ReadDir(versionsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return DoctorReport{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			report.Issues = append(report.Issues, "retained:"+entry.Name())
			continue
		}
		report.Versions = append(report.Versions, entry.Name())
		value, readErr := readManifest(filepath.Join(versionsDir, entry.Name(), "manifest.json"))
		if readErr != nil {
			report.Issues = append(report.Issues, entry.Name()+":manifest")
			continue
		}
		receiptData, receiptErr := os.ReadFile(filepath.Join(destinationRoot, "manifests", entry.Name()+".json"))
		var receipt manifestReceipt
		manifestHash, hashErr := hashFile(filepath.Join(versionsDir, entry.Name(), "manifest.json"))
		if receiptErr != nil || hashErr != nil || json.Unmarshal(receiptData, &receipt) != nil || receipt.ManifestSHA256 != manifestHash {
			report.Issues = append(report.Issues, entry.Name()+":manifest_hash")
			continue
		}
		changed, changedErr := changedFiles(filepath.Join(versionsDir, entry.Name()), value, false)
		if changedErr != nil || len(changed) > 0 {
			report.Issues = append(report.Issues, entry.Name()+":files")
		}
		if _, runtimeErr := ValidateRuntimeBundle(filepath.Join(versionsDir, entry.Name())); runtimeErr != nil {
			issue := entry.Name() + ":runtime"
			if errors.Is(runtimeErr, ErrRuntimeInterpreter) {
				issue = entry.Name() + ":runtime_interpreter"
			}
			report.Issues = append(report.Issues, issue)
		}
	}
	pinsDir := filepath.Join(destinationRoot, "pins")
	pins, err := os.ReadDir(pinsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return DoctorReport{}, err
	}
	for _, entry := range pins {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			report.Pins = append(report.Pins, strings.TrimSuffix(entry.Name(), ".json"))
		}
	}
	intentEntries, err := os.ReadDir(filepath.Join(destinationRoot, "uninstall-intents"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return DoctorReport{}, err
	}
	for _, entry := range intentEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			report.Issues = append(report.Issues, "uninstall_intent_unknown:"+entry.Name())
			continue
		}
		version := strings.TrimSuffix(entry.Name(), ".json")
		if _, ok, readErr := readUninstallIntent(filepath.Join(destinationRoot, "uninstall-intents", entry.Name()), version); readErr != nil || !ok {
			report.Issues = append(report.Issues, "uninstall_intent_invalid:"+entry.Name())
		} else {
			report.Issues = append(report.Issues, "uninstall_pending:"+version)
		}
	}
	sort.Strings(report.Versions)
	sort.Strings(report.Pins)
	if len(report.Issues) > 0 {
		report.Healthy = false
	}
	return report, nil
}

func UninstallPackage(destinationRoot, version string) (UninstallResult, error) {
	if !validDestination(destinationRoot) || !versionPattern.MatchString(version) {
		return UninstallResult{Version: version, Status: "unknown"}, errors.New("uninstall_invalid")
	}
	var err error
	destinationRoot, err = canonicalManagedPath(destinationRoot)
	if err != nil {
		return UninstallResult{Version: version, Status: "unknown"}, err
	}
	versionRoot := filepath.Join(destinationRoot, "versions", version)
	lock, err := acquireInstallLock(destinationRoot)
	if err != nil {
		return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, err
	}
	defer releaseInstallLock(lock)
	manifestPath := filepath.Join(versionRoot, "manifest.json")
	receiptPath := filepath.Join(destinationRoot, "manifests", version+".json")
	intentPath := filepath.Join(destinationRoot, "uninstall-intents", version+".json")
	if err := checkPins(destinationRoot, version); err != nil {
		status := "unknown"
		if errors.Is(err, ErrVersionPinned) {
			status = "pinned"
		}
		return UninstallResult{Version: version, Status: status, Retained: []string{versionRoot}}, err
	}

	intent, resumed, err := readUninstallIntent(intentPath, version)
	if err != nil {
		return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot, intentPath}}, err
	}
	if !resumed {
		receiptData, readErr := os.ReadFile(receiptPath)
		var receipt manifestReceipt
		if readErr != nil || json.Unmarshal(receiptData, &receipt) != nil || receipt.Version != version {
			return UninstallResult{Version: version, Status: "retained", Retained: []string{versionRoot}}, ErrChangedFiles
		}
		manifestHash, hashErr := hashFile(manifestPath)
		if hashErr != nil || manifestHash != receipt.ManifestSHA256 {
			return UninstallResult{Version: version, Status: "retained", Retained: []string{versionRoot}}, ErrChangedFiles
		}
		value, readErr := readManifest(manifestPath)
		if readErr != nil {
			return UninstallResult{Version: version, Status: "retained", Retained: []string{versionRoot}}, ErrChangedFiles
		}
		changed, changedErr := changedFiles(versionRoot, value, false)
		if changedErr != nil {
			return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, changedErr
		}
		if len(changed) > 0 {
			return UninstallResult{Version: version, Status: "retained", Retained: changed}, ErrChangedFiles
		}
		intent = uninstallIntent{SchemaVersion: 1, Version: version, ManifestSHA256: manifestHash, Manifest: value, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		data, marshalErr := json.MarshalIndent(intent, "", "  ")
		if marshalErr != nil {
			return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, marshalErr
		}
		if err := writeExclusive(intentPath, append(data, '\n'), 0o600); err != nil {
			return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, err
		}
		if err := syncDir(filepath.Dir(intentPath)); err != nil {
			return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot, intentPath}}, err
		}
	}

	quarantineRoot := filepath.Join(filepath.Dir(versionRoot), ".uninstall-root-"+version)
	_, versionErr := os.Lstat(versionRoot)
	_, quarantineErr := os.Lstat(quarantineRoot)
	if errors.Is(versionErr, os.ErrNotExist) && errors.Is(quarantineErr, os.ErrNotExist) {
		if err := removeReceiptForIntent(receiptPath, intent); err != nil {
			return UninstallResult{Version: version, Status: "cleanup_pending", Retained: []string{receiptPath, intentPath}}, err
		}
		if err := os.Remove(intentPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return UninstallResult{Version: version, Status: "cleanup_pending", Retained: []string{intentPath}}, err
		}
		if err := syncDir(filepath.Dir(intentPath)); err != nil {
			return UninstallResult{Version: version, Status: "cleanup_pending", Retained: []string{intentPath}}, err
		}
		return UninstallResult{Version: version, Status: "removed"}, nil
	} else if versionErr != nil && !errors.Is(versionErr, os.ErrNotExist) {
		return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, versionErr
	} else if quarantineErr != nil && !errors.Is(quarantineErr, os.ErrNotExist) {
		return UninstallResult{Version: version, Status: "unknown", Retained: []string{quarantineRoot}}, quarantineErr
	}

	activeRoot := versionRoot
	if errors.Is(versionErr, os.ErrNotExist) {
		activeRoot = quarantineRoot
	}
	changed, err := changedFiles(activeRoot, intent.Manifest, resumed)
	if err != nil {
		return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, err
	}
	if len(changed) > 0 {
		return UninstallResult{Version: version, Status: "retained", Retained: changed}, ErrChangedFiles
	}
	retained, err := removeOwnedTreeAnchored(versionRoot, intent.Manifest, intent.ManifestSHA256, resumed)
	if err != nil || len(retained) > 0 {
		return UninstallResult{Version: version, Status: "cleanup_pending", Retained: retained}, err
	}
	if err := removeReceiptForIntent(receiptPath, intent); err != nil {
		return UninstallResult{Version: version, Status: "cleanup_pending", Retained: []string{receiptPath, intentPath}}, err
	}
	if err := os.Remove(intentPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return UninstallResult{Version: version, Status: "cleanup_pending", Retained: []string{intentPath}}, err
	}
	if err := syncDir(filepath.Dir(intentPath)); err != nil {
		return UninstallResult{Version: version, Status: "cleanup_pending", Retained: []string{intentPath}}, err
	}
	return UninstallResult{Version: version, Status: "removed"}, nil
}

func checkPins(destinationRoot, version string) error {
	entries, err := os.ReadDir(filepath.Join(destinationRoot, "pins"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(destinationRoot, "pins", entry.Name()))
		if readErr != nil {
			return readErr
		}
		var active pin
		if json.Unmarshal(data, &active) != nil || !idPattern.MatchString(active.TaskID) || !versionPattern.MatchString(active.Version) {
			return errors.New("pin_invalid")
		}
		if active.Version == version {
			return ErrVersionPinned
		}
	}
	return nil
}

func readUninstallIntent(path, version string) (uninstallIntent, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return uninstallIntent{}, false, nil
	}
	if err != nil {
		return uninstallIntent{}, false, err
	}
	var intent uninstallIntent
	if json.Unmarshal(data, &intent) != nil || intent.SchemaVersion != 1 || intent.Version != version || intent.ManifestSHA256 == "" || intent.Manifest.Version != version || intent.Manifest.PackageName != "codex-orchestrator" || intent.Manifest.SchemaVersion != 1 {
		return uninstallIntent{}, false, errors.New("uninstall_intent_invalid")
	}
	for _, file := range intent.Manifest.Files {
		clean := filepath.Clean(filepath.FromSlash(file.Path))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || len(file.SHA256) != sha256.Size*2 || file.Device == 0 || file.Inode == 0 {
			return uninstallIntent{}, false, errors.New("uninstall_intent_invalid")
		}
	}
	return intent, true, nil
}

func removeReceiptForIntent(path string, intent uninstallIntent) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var receipt manifestReceipt
	if json.Unmarshal(data, &receipt) != nil || receipt.Version != intent.Version || receipt.ManifestSHA256 != intent.ManifestSHA256 {
		return ErrChangedFiles
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func canonicalManagedPath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("path_invalid")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path_symlink_forbidden")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	existing := path
	missing := []string{}
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", errors.New("path_ancestor_missing")
		}
		missing = append(missing, filepath.Base(existing))
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return filepath.Clean(resolved), nil
}

func validateOptions(options InstallOptions) error {
	for _, path := range []string{options.SourceRoot, options.BinaryPath, options.DestinationRoot, options.DataRoot} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("path_invalid")
		}
	}
	if !versionPattern.MatchString(options.Version) {
		return errors.New("version_invalid")
	}
	return nil
}

func validDestination(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func acquireInstallLock(destinationRoot string) (*os.File, error) {
	rootBefore, err := os.Lstat(destinationRoot)
	if err != nil || !rootBefore.IsDir() || rootBefore.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("destination_unsafe")
	}
	lockPath := filepath.Join(destinationRoot, ".install.lock")
	if info, err := os.Lstat(lockPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("install_lock_unsafe")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	pathInfo, pathErr := os.Stat(lockPath)
	fileInfo, fileErr := file.Stat()
	if pathErr != nil || fileErr != nil || !os.SameFile(pathInfo, fileInfo) || !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, errors.New("install_lock_unsafe")
	}
	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || int(stat.Uid) != os.Getuid() {
		_ = file.Close()
		return nil, errors.New("install_lock_unsafe")
	}
	rootAfter, err := os.Lstat(destinationRoot)
	if err != nil || !os.SameFile(rootBefore, rootAfter) {
		_ = file.Close()
		return nil, errors.New("destination_changed")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("install_locked")
	}
	return file, nil
}

func releaseInstallLock(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("destination_unsafe")
	}
	return nil
}

func ensureDataSeparate(destination, data string) error {
	destination, _ = filepath.Abs(destination)
	data, _ = filepath.Abs(data)
	if destination == data || strings.HasPrefix(destination, data+string(os.PathSeparator)) || strings.HasPrefix(data, destination+string(os.PathSeparator)) {
		return errors.New("data_root_overlap")
	}
	return nil
}

func validatePluginSource(source, version string) error {
	manifestPath := filepath.Join(source, ".codex-plugin", "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var value struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &value); err != nil || value.Name != "codex-orchestrator" || value.Version != version {
		return errors.New("plugin_manifest_mismatch")
	}
	return nil
}

func makeStage(destination, version string) (string, error) {
	for i := 0; i < 8; i++ {
		var random [6]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		stage := filepath.Join(destination, "versions", ".stage-"+version+"-"+hex.EncodeToString(random[:]))
		if err := os.Mkdir(stage, 0o700); err == nil {
			return stage, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("stage_collision")
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("source_symlink_forbidden")
		}
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("source_file_unsafe")
		}
		mode := os.FileMode(0o600)
		runtimeFile := strings.HasPrefix(filepath.ToSlash(relative), "runtime/g0/") && strings.HasSuffix(relative, ".py")
		if runtimeFile && filepath.Base(relative) != "projectproxy_launchd_entrypoint.py" {
			if info.Mode().Perm() != 0o644 {
				return errors.New("runtime_source_mode_invalid")
			}
			mode = 0o644
		} else if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		return copyRegular(path, target, mode)
	})
}

func copyRegular(source, destination string, mode os.FileMode) error {
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("source_file_unsafe")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	if err = output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func collectFiles(root string) ([]manifestFile, error) {
	var files []manifestFile
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest, err := hashFile(path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() {
			return errors.New("installed_file_unsafe")
		}
		files = append(files, manifestFile{Path: filepath.ToSlash(relative), SHA256: digest, Mode: uint32(info.Mode().Perm()), Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Size: info.Size(), ModUnixNano: info.ModTime().UnixNano()})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

func changedFiles(root string, value manifest, allowMissing bool) ([]string, error) {
	known := map[string]manifestFile{}
	knownDirectories := map[string]bool{".": true}
	for _, file := range value.Files {
		known[file.Path] = file
		current := filepath.ToSlash(filepath.Dir(filepath.FromSlash(file.Path)))
		for current != "." && current != "" {
			knownDirectories[current] = true
			current = filepath.ToSlash(filepath.Dir(filepath.FromSlash(current)))
		}
	}
	knownDirectories[filepath.ToSlash(filepath.Dir("manifest.json"))] = true
	var changed []string
	for _, file := range value.Files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		info, err := os.Lstat(path)
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !matchesManifestIdentity(info, file) {
			changed = append(changed, file.Path)
			continue
		}
		digest, err := hashFile(path)
		if err != nil || digest != file.SHA256 {
			changed = append(changed, file.Path)
		}
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, _ := filepath.Rel(root, path)
		if info.IsDir() {
			if !knownDirectories[filepath.ToSlash(relative)] {
				changed = append(changed, filepath.ToSlash(relative))
			}
			return nil
		}
		if filepath.ToSlash(relative) == "manifest.json" {
			return nil
		}
		if _, ok := known[filepath.ToSlash(relative)]; !ok {
			changed = append(changed, filepath.ToSlash(relative))
		}
		return nil
	})
	return unique(changed), err
}

func matchesManifestIdentity(info os.FileInfo, file manifestFile) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && uint32(info.Mode().Perm()) == file.Mode && uint64(stat.Dev) == file.Device && uint64(stat.Ino) == file.Inode && info.Size() == file.Size && info.ModTime().UnixNano() == file.ModUnixNano
}

func readManifest(path string) (manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}
	var value manifest
	if err := json.Unmarshal(data, &value); err != nil || value.SchemaVersion != 1 || !versionPattern.MatchString(value.Version) || value.PackageName != "codex-orchestrator" {
		return manifest{}, errors.New("manifest_invalid")
	}
	return value, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func unique(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}
