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
	Version        string
	VersionRoot    string
	ManifestPath   string
	ManifestSHA256 string
	BinarySHA256   string
	Files          []string
}

type UninstallResult struct {
	Version  string
	Status   string
	Retained []string
}

type DoctorReport struct {
	Healthy  bool
	Versions []string
	Pins     []string
	Issues   []string
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
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

type manifestReceipt struct {
	Version        string `json:"version"`
	ManifestSHA256 string `json:"manifest_sha256"`
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
	if err := validatePluginSource(options.SourceRoot, options.Version); err != nil {
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
	lock, err := acquireInstallLock(options.DestinationRoot)
	if err != nil {
		return InstallResult{}, err
	}
	defer func() { _ = os.Remove(lock) }()
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
	defer os.RemoveAll(stage)
	if err := copyTree(options.SourceRoot, stage); err != nil {
		return InstallResult{}, err
	}
	binaryDest := filepath.Join(stage, "bin", "codex-orchestrator")
	if err := os.MkdirAll(filepath.Dir(binaryDest), 0o700); err != nil {
		return InstallResult{}, err
	}
	if err := copyRegular(options.BinaryPath, binaryDest, 0o700); err != nil {
		return InstallResult{}, err
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
	return InstallResult{Version: options.Version, VersionRoot: versionRoot, ManifestPath: manifestPath, ManifestSHA256: manifestHash, BinarySHA256: binaryHash, Files: paths}, nil
}

func PinVersion(destinationRoot, taskID, version string) error {
	if !validDestination(destinationRoot) || !idPattern.MatchString(taskID) || !versionPattern.MatchString(version) {
		return errors.New("pin_invalid")
	}
	lock, err := acquireInstallLock(destinationRoot)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(lock) }()
	path := filepath.Join(destinationRoot, "pins", taskID+".json")
	data, _ := json.Marshal(pin{TaskID: taskID, Version: version})
	return writeExclusive(path, append(data, '\n'), 0o600)
}

func UnpinVersion(destinationRoot, taskID string) error {
	if !validDestination(destinationRoot) || !idPattern.MatchString(taskID) {
		return errors.New("pin_invalid")
	}
	lock, err := acquireInstallLock(destinationRoot)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(lock) }()
	return os.Remove(filepath.Join(destinationRoot, "pins", taskID+".json"))
}

func Doctor(destinationRoot string) (DoctorReport, error) {
	if !filepath.IsAbs(destinationRoot) || filepath.Clean(destinationRoot) != destinationRoot {
		return DoctorReport{}, errors.New("doctor_path_invalid")
	}
	report := DoctorReport{Healthy: true}
	versionsDir := filepath.Join(destinationRoot, "versions")
	entries, err := os.ReadDir(versionsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return DoctorReport{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
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
		changed, changedErr := changedFiles(filepath.Join(versionsDir, entry.Name()), value)
		if changedErr != nil || len(changed) > 0 {
			report.Issues = append(report.Issues, entry.Name()+":files")
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
	versionRoot := filepath.Join(destinationRoot, "versions", version)
	lock, err := acquireInstallLock(destinationRoot)
	if err != nil {
		return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, err
	}
	defer func() { _ = os.Remove(lock) }()
	manifestPath := filepath.Join(versionRoot, "manifest.json")
	receiptPath := filepath.Join(destinationRoot, "manifests", version+".json")
	receiptData, err := os.ReadFile(receiptPath)
	if err != nil {
		return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, err
	}
	var receipt manifestReceipt
	if json.Unmarshal(receiptData, &receipt) != nil || receipt.Version != version {
		return UninstallResult{Version: version, Status: "retained", Retained: []string{versionRoot}}, ErrChangedFiles
	}
	manifestHash, err := hashFile(manifestPath)
	if err != nil || manifestHash != receipt.ManifestSHA256 {
		return UninstallResult{Version: version, Status: "retained", Retained: []string{versionRoot}}, ErrChangedFiles
	}
	value, err := readManifest(manifestPath)
	if err != nil {
		return UninstallResult{Version: version, Status: "retained", Retained: []string{versionRoot}}, ErrChangedFiles
	}
	entries, err := os.ReadDir(filepath.Join(destinationRoot, "pins"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(destinationRoot, "pins", entry.Name()))
		if readErr != nil {
			return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, readErr
		}
		var active pin
		if json.Unmarshal(data, &active) != nil || !idPattern.MatchString(active.TaskID) || !versionPattern.MatchString(active.Version) {
			return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, errors.New("pin_invalid")
		}
		if active.Version == version {
			return UninstallResult{Version: version, Status: "pinned", Retained: []string{versionRoot}}, ErrVersionPinned
		}
	}
	changed, err := changedFiles(versionRoot, value)
	if err != nil {
		return UninstallResult{Version: version, Status: "unknown", Retained: []string{versionRoot}}, err
	}
	if len(changed) > 0 {
		return UninstallResult{Version: version, Status: "retained", Retained: changed}, ErrChangedFiles
	}
	retained, err := removeOwnedTree(versionRoot, value, receipt.ManifestSHA256)
	if err != nil || len(retained) > 0 {
		return UninstallResult{Version: version, Status: "cleanup_pending", Retained: retained}, err
	}
	if err := os.Remove(receiptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return UninstallResult{Version: version, Status: "cleanup_pending", Retained: []string{receiptPath}}, err
	}
	return UninstallResult{Version: version, Status: "removed"}, nil
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

func acquireInstallLock(destinationRoot string) (string, error) {
	return createExclusive(filepath.Join(destinationRoot, ".install.lock"), []byte("owned\n"), 0o600)
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
		mode := os.FileMode(0o600)
		if entry.Type().Perm()&0o111 != 0 {
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
		if info.IsDir() || filepath.Base(path) == "manifest.json" {
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
		files = append(files, manifestFile{Path: filepath.ToSlash(relative), SHA256: digest, Mode: uint32(info.Mode().Perm())})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

func changedFiles(root string, value manifest) ([]string, error) {
	known := map[string]manifestFile{}
	for _, file := range value.Files {
		known[file.Path] = file
	}
	var changed []string
	for _, file := range value.Files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || uint32(info.Mode().Perm()) != file.Mode {
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
		if info.IsDir() {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
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

// removeOwnedTree only removes files whose digest and mode are still covered by
// the install manifest. Directories are removed only when empty. This keeps an
// unknown file, including one inserted after changedFiles, in place.
func removeOwnedTree(root string, value manifest, manifestHash string) ([]string, error) {
	retained := []string{}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return []string{root}, errors.New("owned_root_unsafe")
	}
	for _, file := range value.Files {
		path, err := safeOwnedPath(root, file.Path)
		if err != nil {
			retained = append(retained, file.Path)
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || uint32(info.Mode().Perm()) != file.Mode {
			retained = append(retained, file.Path)
			continue
		}
		digest, err := hashFile(path)
		if err != nil || digest != file.SHA256 {
			retained = append(retained, file.Path)
			continue
		}
		if err := os.Remove(path); err != nil {
			retained = append(retained, file.Path)
		}
	}

	manifestPath := filepath.Join(root, "manifest.json")
	if info, err := os.Lstat(manifestPath); err != nil || !info.Mode().IsRegular() {
		retained = append(retained, "manifest.json")
	} else if digest, err := hashFile(manifestPath); err != nil || digest != manifestHash {
		retained = append(retained, "manifest.json")
	} else if err := os.Remove(manifestPath); err != nil {
		retained = append(retained, "manifest.json")
	}

	dirs, err := directoriesDeepestFirst(root)
	if err != nil {
		return unique(append(retained, root)), err
	}
	for _, directory := range dirs {
		if err := os.Remove(directory); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			relative, relErr := filepath.Rel(root, directory)
			if relErr == nil && relative != "." {
				retained = append(retained, filepath.ToSlash(relative))
			} else {
				retained = append(retained, root)
			}
		}
	}
	return unique(retained), nil
}

func safeOwnedPath(root, relative string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("owned_path_invalid")
	}
	parts := strings.Split(clean, string(os.PathSeparator))
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("owned_parent_unsafe")
		}
	}
	return filepath.Join(root, clean), nil
}

func directoriesDeepestFirst(root string) ([]string, error) {
	var directories []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			directories = append(directories, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i], string(os.PathSeparator)) > strings.Count(directories[j], string(os.PathSeparator))
	})
	return directories, nil
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

func createExclusive(path string, data []byte, mode os.FileMode) (string, error) {
	if err := writeExclusive(path, data, mode); err != nil {
		return "", err
	}
	return path, nil
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
