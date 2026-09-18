package install

import (
	"errors"
	"os"
	"path/filepath"
)

type PackageIdentity struct {
	DestinationRoot string `json:"destination_root"`
	Version         string `json:"version"`
}

// PinRunningVersion derives package identity from the exact installed binary.
// Callers pin before making a task durable; ambiguous commits retain the pin.
func PinRunningVersion(executable, taskID string) (PackageIdentity, error) {
	identity, err := runningPackageIdentity(executable)
	if err != nil {
		return PackageIdentity{}, err
	}
	if err = PinVersion(identity.DestinationRoot, taskID, identity.Version); err != nil {
		return PackageIdentity{}, err
	}
	return identity, nil
}

// UnpinRunningVersion is idempotent. The control plane calls it only after it
// has proved that no active, recoverable, unknown, or pending-delivery state
// still references the task.
func UnpinRunningVersion(executable, taskID string) error {
	identity, err := runningPackageIdentity(executable)
	if err != nil {
		return err
	}
	return UnpinVersion(identity.DestinationRoot, taskID)
}

func runningPackageIdentity(executable string) (PackageIdentity, error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return PackageIdentity{}, errors.New("running_package_invalid")
	}
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return PackageIdentity{}, errors.New("running_package_invalid")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil || resolved != executable || filepath.Base(executable) != "codex-orchestrator" || filepath.Base(filepath.Dir(executable)) != "bin" {
		return PackageIdentity{}, errors.New("running_package_invalid")
	}
	versionRoot := filepath.Dir(filepath.Dir(executable))
	version := filepath.Base(versionRoot)
	versionsRoot := filepath.Dir(versionRoot)
	if !versionPattern.MatchString(version) || filepath.Base(versionsRoot) != "versions" {
		return PackageIdentity{}, errors.New("running_package_invalid")
	}
	destination := filepath.Dir(versionsRoot)
	canonical, err := canonicalManagedPath(destination)
	if err != nil || canonical != destination {
		return PackageIdentity{}, errors.New("running_package_invalid")
	}
	return PackageIdentity{DestinationRoot: destination, Version: version}, nil
}
