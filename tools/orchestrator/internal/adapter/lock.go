package adapter

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func ValidatePin(pin BinaryPin, actualVersion, actualSHA256 string) error {
	if pin.Path == "" || !filepath.IsAbs(pin.Path) || filepath.Clean(pin.Path) != pin.Path {
		return errors.New("binary_path_invalid")
	}
	if pin.Version == "" || actualVersion == "" || pin.Version != actualVersion {
		return errors.New("binary_version_mismatch")
	}
	if !hex64.MatchString(strings.ToLower(pin.SHA256)) || !hex64.MatchString(strings.ToLower(actualSHA256)) || strings.ToLower(pin.SHA256) != strings.ToLower(actualSHA256) {
		return errors.New("binary_sha256_mismatch")
	}
	return nil
}

// VerifyExecutable re-reads the current pinned executable. The caller supplies
// the output of an already bounded --version invocation; this function never
// reads provider configuration or authentication state.
func VerifyExecutable(pin BinaryPin, actualVersion string) error {
	if err := ValidatePin(pin, actualVersion, pin.SHA256); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(pin.Path)
	if err != nil || resolved != pin.Path {
		return errors.New("binary_path_changed")
	}
	info, err := os.Stat(pin.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 || info.Mode()&0o022 != 0 {
		return errors.New("binary_file_unsafe")
	}
	file, err := os.Open(pin.Path)
	if err != nil {
		return errors.New("binary_read_failed")
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return errors.New("binary_read_failed")
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != strings.ToLower(pin.SHA256) {
		return errors.New("binary_sha256_mismatch")
	}
	return nil
}
