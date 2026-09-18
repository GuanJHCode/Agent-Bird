package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type portableManifest struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

// InstallPortable normalizes a public plugin cache into an owner-only staging
// directory, verifies every declared payload, and uses the existing immutable
// installation/pin lifecycle. It never changes the plugin cache or old versions.
func InstallPortable(source, destination, state string) (string, error) {
	source, err := canonicalManagedPath(source)
	if err != nil {
		return "", err
	}
	destination, err = canonicalManagedPath(destination)
	if err != nil {
		return "", err
	}
	data, err := portableRead(filepath.Join(source, "portable.json"), 1024*1024)
	if err != nil {
		return "", err
	}
	var metadata portableManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil || decoder.Decode(&struct{}{}) != io.EOF || !versionPattern.MatchString(metadata.Version) || len(metadata.Files) < 3 || len(metadata.Files) > 64 {
		return "", errors.New("portable_manifest_invalid")
	}
	for _, required := range []string{"bin/codex-orchestrator", ".codex-plugin/plugin.json", "runtime/g0/runtime-manifest.json"} {
		if metadata.Files[required] == "" {
			return "", errors.New("portable_manifest_invalid")
		}
	}
	stage, err := os.MkdirTemp("", "codex-bird-install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	for rel, want := range metadata.Files {
		if filepath.IsAbs(rel) || filepath.Clean(rel) != rel || rel == "." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "\\") || !validHexDigest(want) {
			return "", errors.New("portable_manifest_invalid")
		}
		if rel != "defaults.json" && rel != "bin/codex-orchestrator" && rel != ".codex-plugin/plugin.json" && rel != "runtime/g0/runtime-manifest.json" && rel != "scripts/invoke.sh" && rel != "skills/orchestrate/SKILL.md" {
			return "", errors.New("portable_file_unsupported")
		}
		path := filepath.Join(source, rel)
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return "", errors.New("portable_path_unsafe")
		}
		limit := int64(1024 * 1024)
		mode := os.FileMode(0600)
		if rel == "bin/codex-orchestrator" {
			limit = 512 * 1024 * 1024
			mode = 0700
		} else if rel == "scripts/invoke.sh" {
			mode = 0700
		}
		body, err := portableRead(path, limit)
		if err != nil {
			return "", err
		}
		if rel == "defaults.json" {
			if err = validateDefaultPolicy(body); err != nil {
				return "", err
			}
		}
		actual := sha256.Sum256(body)
		if hex.EncodeToString(actual[:]) != want {
			return "", errors.New("portable_payload_changed")
		}
		target := filepath.Join(stage, rel)
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "", err
		}
		if err = os.WriteFile(target, body, mode); err != nil {
			return "", err
		}
	}
	identity, err := ValidateRuntimeBundle(stage)
	if err != nil || identity.Kind != "collect-only" {
		return "", errors.New("portable_runtime_invalid")
	}
	versionRoot := filepath.Join(destination, "versions", metadata.Version)
	binary := filepath.Join(versionRoot, "bin", "codex-orchestrator")
	if _, err = os.Lstat(versionRoot); errors.Is(err, os.ErrNotExist) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, err = InstallPackage(InstallOptions{SourceRoot: stage, BinaryPath: filepath.Join(stage, "bin", "codex-orchestrator"), DestinationRoot: destination, DataRoot: state, Version: metadata.Version})
			if err == nil || errors.Is(err, ErrVersionExists) {
				break
			}
			if err.Error() != "install_locked" || !time.Now().Before(deadline) {
				return "", err
			}
			time.Sleep(25 * time.Millisecond)
		}
	} else if err != nil {
		return "", err
	}
	// Repeated/concurrent first use may observe an already installed version.
	// Verify it rather than overwriting it or trusting a version string alone.
	// A rename can become visible before its receipt is synced. Wait for the
	// installer to release its lock before validating the committed version.
	deadline := time.Now().Add(5 * time.Second)
	var lock *os.File
	for {
		lock, err = acquireInstallLock(destination)
		if err == nil {
			break
		}
		if err.Error() != "install_locked" || !time.Now().Before(deadline) {
			return "", err
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer releaseInstallLock(lock)
	report, err := Doctor(destination)
	if err != nil || !report.Healthy {
		return "", errors.New("portable_install_changed")
	}
	for rel, want := range metadata.Files {
		actual, err := hashFile(filepath.Join(versionRoot, rel))
		if err != nil || actual != want {
			return "", errors.New("portable_version_conflict")
		}
	}
	return binary, nil
}

func portableRead(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("portable_file_unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("portable_file_unsafe")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("portable_file_unsafe")
	}
	return data, nil
}
