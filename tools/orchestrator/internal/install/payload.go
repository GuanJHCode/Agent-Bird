package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

const payloadManifestName = "payload-manifest.json"
const skillReferencePath = "skills/orchestrate/legacy-cli.md"

type payloadManifest struct {
	SchemaVersion int           `json:"schema_version"`
	Kind          string        `json:"kind"`
	Files         []payloadFile `json:"files"`
}
type payloadFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

func payloadPaths(kind string) map[string]os.FileMode {
	result := map[string]os.FileMode{
		".codex-plugin/plugin.json": 0600, "defaults.json": 0600, "bin/codex-orchestrator": 0700,
		"scripts/invoke.sh": 0700, "skills/orchestrate/SKILL.md": 0600,
		"scripts/agent-bird-hook": 0700, "scripts/agent-bird-agy-hook": 0700,
		"hooks/agy-proposed-hooks.json": 0600, "runtime/g0/runtime-manifest.json": 0600,
	}
	if kind == "full-native-hooks" {
		result["scripts/native-product-bridge.py"] = 0700
		for _, name := range []string{"lib/native_bridge/__init__.py", "lib/native_bridge/bridge.py", "lib/trial/auth_guard.py", "lib/trial/auth_wire.py", "lib/trial/rpc_policy.py"} {
			result[name] = 0600
		}
		for _, name := range runtimeFileNames {
			mode := os.FileMode(0644)
			if name == "projectproxy_launchd_entrypoint.py" {
				mode = 0700
			}
			result["runtime/g0/"+name] = mode
		}
	} else if kind != "collect-only-hooks" {
		return nil
	}
	return result
}

// strictJSON rejects duplicate keys and nulls at every depth before typed decode.
func strictJSON(body []byte, target any) error     { return strictJSONWithNull(body, target, false) }
func strictUserJSON(body []byte, target any) error { return strictJSONWithNull(body, target, true) }
func strictJSONWithNull(body []byte, target any, allowNull bool) error {
	return strictJSONBounded(body, target, allowNull, 1024*1024)
}
func strictJSONBounded(body []byte, target any, allowNull bool, maximum int) error {
	if len(body) == 0 || len(body) > maximum {
		return errors.New("json_invalid")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var value func() error
	value = func() error {
		token, err := dec.Token()
		if err != nil || token == nil && !allowNull {
			return errors.New("json_invalid")
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return errors.New("json_invalid")
				}
				seen[name] = true
				if err = value(); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("json_invalid")
			}
		case '[':
			for dec.More() {
				if err = value(); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("json_invalid")
			}
		default:
			return errors.New("json_invalid")
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("json_invalid")
	}
	if !allowNull {
		if err := requiredJSONFields(body, reflect.TypeOf(target)); err != nil {
			return err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("json_invalid")
	}
	return nil
}

// Manifest has no self hash. Installed manifest.json is a separate receipt.
func validatePayload(root string, installed bool) (string, error) {
	return validatePayloadDirectory(root, installed, false)
}
func validatePayloadDirectory(root string, installed, portable bool) (string, error) {
	if resolved, err := filepath.EvalSymlinks(root); err != nil || resolved != root {
		return "", errors.New("payload_path_unsafe")
	}
	data, err := readOwnedRegular(filepath.Join(root, payloadManifestName), 0600, 1024*1024)
	if errors.Is(err, os.ErrNotExist) {
		for _, rel := range []string{"scripts/agent-bird-hook", "scripts/agent-bird-agy-hook", "hooks/agy-proposed-hooks.json"} {
			if _, e := os.Lstat(filepath.Join(root, rel)); !errors.Is(e, os.ErrNotExist) {
				return "", errors.New("payload_manifest_required")
			}
		}
		return "", validateLegacyPayload(root, installed)
	}
	if err != nil {
		return "", errors.New("payload_manifest_invalid")
	}
	var metadata payloadManifest
	if strictJSON(data, &metadata) != nil || metadata.SchemaVersion != 1 {
		return "", errors.New("payload_manifest_invalid")
	}
	paths := payloadPaths(metadata.Kind)
	// The on-demand reference is an explicitly named optional asset, preserving
	// old packages while retaining a closed, mode/hash-checked file inventory.
	if paths != nil {
		for _, file := range metadata.Files {
			if file.Path == skillReferencePath {
				paths[skillReferencePath] = 0600
			}
		}
	}
	if len(paths) == 0 || len(metadata.Files) != len(paths) {
		return "", errors.New("payload_manifest_invalid")
	}
	seen := map[string]bool{}
	for _, file := range metadata.Files {
		mode, ok := paths[file.Path]
		if !ok || seen[file.Path] || file.Mode != uint32(mode) || !validHexDigest(file.SHA256) {
			return "", errors.New("payload_manifest_invalid")
		}
		digest, e := hashOwnedRuntimeFile(filepath.Join(root, file.Path), mode, 512*1024*1024)
		if e != nil || digest != file.SHA256 {
			return "", errors.New("payload_changed")
		}
		seen[file.Path] = true
	}
	allowedDirs := map[string]bool{".": true}
	for path := range paths {
		for p := filepath.Dir(path); p != "."; p = filepath.Dir(p) {
			allowedDirs[p] = true
		}
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if !allowedDirs[rel] {
				return errors.New("payload_extra")
			}
			return verifyPrivateDirectory(path)
		}
		if rel == payloadManifestName || installed && rel == "manifest.json" || portable && rel == "portable.json" {
			_, e = readOwnedRegular(path, 0600, 1024*1024)
			return e
		}
		if strings.Contains(rel, "\\") || !seen[rel] {
			return errors.New("payload_extra")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return metadata.Kind, nil
}

// InstalledExecutableKind verifies the executable against the actual immutable
// install receipt before publishing package-specific capability limits. A
// broken installed-shaped path is corruption, never a healthy legacy fallback.
func InstalledExecutableKind(executable string) (string, bool, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", false, errors.New("package_executable_invalid")
	}
	if !filepath.IsAbs(resolved) || filepath.Base(resolved) != "codex-orchestrator" || filepath.Base(filepath.Dir(resolved)) != "bin" {
		return "", false, nil
	}
	root := filepath.Dir(filepath.Dir(resolved))
	if filepath.Base(filepath.Dir(root)) != "versions" {
		return "", false, nil
	}
	destination := filepath.Dir(filepath.Dir(root))
	version := filepath.Base(root)
	if !versionPattern.MatchString(version) || verifyPrivateDirectory(destination) != nil || verifyInstalledVersion(destination, version) != nil {
		return "", true, errors.New("installed_package_invalid")
	}
	value, err := readManifest(filepath.Join(root, "manifest.json"))
	if err != nil || value.BinaryPath != "bin/codex-orchestrator" {
		return "", true, errors.New("installed_package_invalid")
	}
	digest, err := hashOwnedRuntimeFile(resolved, 0700, 512*1024*1024)
	if err != nil || digest != value.BinarySHA256 {
		return "", true, errors.New("installed_package_invalid")
	}
	if _, err = ValidateRuntimeBundle(root); err != nil {
		return "", true, errors.New("installed_package_invalid")
	}
	kind, err := validatePayload(root, true)
	if err != nil {
		return "", true, errors.New("installed_package_invalid")
	}
	if kind == "" {
		kind = "legacy"
	}
	return kind, true, nil
}

// Legacy packages retain their original optional assets and runtime formats,
// but are completely preflighted before destination creation.
func validateLegacyPayload(root string, installed bool) error {
	allowed := payloadPaths("full-native-hooks")
	for _, name := range []string{"scripts/agent-bird-hook", "scripts/agent-bird-agy-hook", "hooks/agy-proposed-hooks.json"} {
		delete(allowed, name)
	}
	allowed[".codex-plugin/hooks/hooks.json"] = 0600
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("legacy_path_unsafe")
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != os.Geteuid() || info.Mode().Perm()&0022 != 0 {
			return errors.New("legacy_file_unsafe")
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || st.Nlink != 1 {
			return errors.New("legacy_file_unsafe")
		}
		if _, ok = allowed[filepath.ToSlash(rel)]; !ok && !(installed && rel == "manifest.json") {
			return errors.New("legacy_file_unsupported")
		}
		return nil
	})
}

// Non-omitempty fields in the new typed schemas are required, including zero
// values such as missing:false and the empty base64 representation of bytes.
func requiredJSONFields(body []byte, kind reflect.Type) error {
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if kind == reflect.TypeOf(json.RawMessage{}) || kind.Kind() == reflect.Slice && kind.Elem().Kind() == reflect.Uint8 {
		return nil
	}
	switch kind.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) != nil || fields == nil {
			return errors.New("json_invalid")
		}
		for i := 0; i < kind.NumField(); i++ {
			field := kind.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := field.Tag.Get("json")
			parts := strings.Split(tag, ",")
			name := parts[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			value, exists := fields[name]
			optional := false
			for _, part := range parts[1:] {
				if part == "omitempty" {
					optional = true
				}
			}
			if !exists {
				if optional {
					continue
				}
				return errors.New("json_invalid")
			}
			if err := requiredJSONFields(value, field.Type); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if json.Unmarshal(body, &values) != nil || values == nil {
			return errors.New("json_invalid")
		}
		for _, value := range values {
			if err := requiredJSONFields(value, kind.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if json.Unmarshal(body, &values) != nil || values == nil {
			return errors.New("json_invalid")
		}
		for _, value := range values {
			if err := requiredJSONFields(value, kind.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
