package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
)

// CodexGuardRuntime uses the installed receipt, not a caller-supplied helper or
// metadata DTO. No source-tree/test-fake fallback is allowed for authentication.
func CodexGuardRuntime(executable string) (RuntimeIdentity, string, error) {
	identity, err := runningPackageIdentity(executable)
	if err != nil {
		return RuntimeIdentity{}, "", errors.New("codex_guard_package_untrusted")
	}
	root := filepath.Dir(filepath.Dir(executable))
	manifestPath := filepath.Join(root, "manifest.json")
	body, err := readOwnedRegular(manifestPath, 0600, 1024*1024)
	if err != nil {
		return RuntimeIdentity{}, "", errors.New("codex_guard_package_untrusted")
	}
	receiptBody, err := readOwnedRegular(filepath.Join(identity.DestinationRoot, "manifests", identity.Version+".json"), 0600, 1024*1024)
	var receipt manifestReceipt
	digest := sha256.Sum256(body)
	if err != nil || json.Unmarshal(receiptBody, &receipt) != nil || receipt.Version != identity.Version || receipt.ManifestSHA256 != hex.EncodeToString(digest[:]) {
		return RuntimeIdentity{}, "", errors.New("codex_guard_package_untrusted")
	}
	value, err := readManifest(manifestPath)
	if err != nil {
		return RuntimeIdentity{}, "", errors.New("codex_guard_package_untrusted")
	}
	found := map[string]bool{}
	for _, file := range value.Files {
		found[file.Path] = true
	}
	for _, path := range []string{"lib/trial/auth_guard.py", "lib/trial/codex_exec.py"} {
		if !found[path] {
			return RuntimeIdentity{}, "", errors.New("codex_guard_package_untrusted")
		}
	}
	changed, err := changedFiles(root, value, false)
	if err != nil || len(changed) != 0 {
		return RuntimeIdentity{}, "", errors.New("codex_guard_package_untrusted")
	}
	runtime, err := ValidateRuntimeBundle(root)
	if err != nil || runtime.Kind == "collect-only" {
		return RuntimeIdentity{}, "", errors.New("codex_guard_runtime_unavailable")
	}
	return runtime, filepath.Join(root, "lib/trial/codex_exec.py"), nil
}
