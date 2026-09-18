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
	"sort"
	"strings"
	"syscall"
)

var (
	ErrRuntimeInvalid     = errors.New("g0_runtime_invalid")
	ErrRuntimeInterpreter = errors.New("g0_runtime_interpreter_unavailable")
)

var runtimeFileNames = []string{
	"activation_service.py",
	"auth_isolation.py",
	"delivery_adapter.py",
	"delivery_audit.py",
	"launch_activation.py",
	"owned_child_guard.py",
	"owner_helper.py",
	"projectproxy_launchd_entrypoint.py",
	"proxy_observer.py",
	"proxy_transport.py",
	"receipt_store.py",
}

type RuntimeIdentity struct {
	Kind              string `json:"kind,omitempty"`
	Root              string `json:"root"`
	ManifestPath      string `json:"manifest_path"`
	ManifestSHA256    string `json:"manifest_sha256"`
	Interpreter       string `json:"interpreter"`
	InterpreterSHA256 string `json:"interpreter_sha256"`
}

type runtimeManifest struct {
	Kind        string             `json:"kind,omitempty"`
	Version     int                `json:"version"`
	Interpreter runtimeInterpreter `json:"interpreter"`
	Files       []runtimeFile      `json:"files"`
}

type runtimeInterpreter struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type runtimeFile struct {
	LogicalID string `json:"logical_id"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Mode      uint32 `json:"mode"`
}

func ValidateRuntimeBundle(packageRoot string) (RuntimeIdentity, error) {
	if !filepath.IsAbs(packageRoot) || filepath.Clean(packageRoot) != packageRoot {
		return RuntimeIdentity{}, ErrRuntimeInvalid
	}
	runtimeRoot := filepath.Join(packageRoot, "runtime", "g0")
	if err := verifyPrivateDirectory(runtimeRoot); err != nil {
		return RuntimeIdentity{}, ErrRuntimeInvalid
	}
	manifestPath := filepath.Join(runtimeRoot, "runtime-manifest.json")
	data, err := readOwnedRegular(manifestPath, 0o600, 1024*1024)
	if err != nil {
		return RuntimeIdentity{}, ErrRuntimeInvalid
	}
	var manifest runtimeManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return RuntimeIdentity{}, ErrRuntimeInvalid
	}
	if manifest.Version == 2 && manifest.Kind == "collect-only" {
		if manifest.Interpreter.Path != "" || manifest.Interpreter.SHA256 != "" || len(manifest.Files) != 0 {
			return RuntimeIdentity{}, ErrRuntimeInvalid
		}
		entries, err := os.ReadDir(runtimeRoot)
		if err != nil || len(entries) != 1 || entries[0].Name() != "runtime-manifest.json" {
			return RuntimeIdentity{}, ErrRuntimeInvalid
		}
		digest := sha256.Sum256(data)
		return RuntimeIdentity{Kind: "collect-only", Root: runtimeRoot, ManifestPath: manifestPath, ManifestSHA256: hex.EncodeToString(digest[:])}, nil
	}
	if manifest.Version != 1 || manifest.Kind != "" || len(manifest.Files) != len(runtimeFileNames) {
		return RuntimeIdentity{}, ErrRuntimeInvalid
	}

	expected := append([]string(nil), runtimeFileNames...)
	sort.Strings(expected)
	actual := make([]string, 0, len(manifest.Files))
	seen := make(map[string]bool, len(manifest.Files))
	for _, file := range manifest.Files {
		if filepath.Base(file.Path) != file.Path || file.Path == "." || strings.TrimSuffix(file.Path, ".py") != file.LogicalID || !strings.HasSuffix(file.Path, ".py") || seen[file.Path] || !validHexDigest(file.SHA256) {
			return RuntimeIdentity{}, ErrRuntimeInvalid
		}
		mode := uint32(0o644)
		if file.Path == "projectproxy_launchd_entrypoint.py" {
			mode = 0o700
		}
		if file.Mode != mode {
			return RuntimeIdentity{}, ErrRuntimeInvalid
		}
		digest, err := hashOwnedRuntimeFile(filepath.Join(runtimeRoot, file.Path), os.FileMode(file.Mode), 16*1024*1024)
		if err != nil || digest != file.SHA256 {
			return RuntimeIdentity{}, ErrRuntimeInvalid
		}
		seen[file.Path] = true
		actual = append(actual, file.Path)
	}
	sort.Strings(actual)
	if !equalStrings(actual, expected) {
		return RuntimeIdentity{}, ErrRuntimeInvalid
	}
	if !filepath.IsAbs(manifest.Interpreter.Path) || filepath.Clean(manifest.Interpreter.Path) != manifest.Interpreter.Path || !validHexDigest(manifest.Interpreter.SHA256) {
		return RuntimeIdentity{}, ErrRuntimeInterpreter
	}
	digest, err := hashInterpreter(manifest.Interpreter.Path)
	if err != nil || digest != manifest.Interpreter.SHA256 {
		return RuntimeIdentity{}, ErrRuntimeInterpreter
	}
	manifestDigest := sha256.Sum256(data)
	return RuntimeIdentity{Root: runtimeRoot, ManifestPath: manifestPath, ManifestSHA256: hex.EncodeToString(manifestDigest[:]), Interpreter: manifest.Interpreter.Path, InterpreterSHA256: manifest.Interpreter.SHA256}, nil
}

func hashOwnedRuntimeFile(path string, mode os.FileMode, maximum int64) (string, error) {
	data, err := readOwnedRegular(path, mode, maximum)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func hashInterpreter(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return "", ErrRuntimeInterpreter
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || (stat.Uid != 0 && int(stat.Uid) != os.Geteuid()) {
		return "", ErrRuntimeInterpreter
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", ErrRuntimeInterpreter
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() <= 0 || opened.Size() > 512*1024*1024 {
		return "", ErrRuntimeInterpreter
	}
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return "", ErrRuntimeInterpreter
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validHexDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
