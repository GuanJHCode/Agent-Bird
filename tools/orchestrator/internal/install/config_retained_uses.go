//go:build !windows

package install

import (
	"bytes"
	"path/filepath"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Retain existing subset pins through a Store transaction. Completed upstream
// pins need not exist. This never creates a pin or grants new configuration.
func WithRetainedConfigTaskUses(executable, run string, tasks []string, ref *contract.ConfigGenerationRef, commit func(ConfigPreparationUse, string, func() error) error) error {
	if ref == nil || ref.Validate() != nil || !idPattern.MatchString(run) || len(tasks) == 0 || len(tasks) > 64 || commit == nil {
		return generationInvalid()
	}
	id, err := runningPackageIdentity(executable)
	if err != nil {
		return err
	}
	lock, err := acquireInstallLock(id.DestinationRoot)
	if err != nil {
		return err
	}
	defer releaseInstallLock(lock)
	pkg, err := verifiedGenerationPackage(executable)
	if err != nil {
		return err
	}
	owner, err := openGenerationOwner(pkg.DestinationRoot, ref.OwnerScopeSHA256, false)
	if err != nil {
		return err
	}
	defer owner.close()
	if err = owner.quiescent(""); err != nil {
		return err
	}
	if err = owner.rejectAbandoned(run); err != nil {
		return err
	}
	u, found, err := owner.readOriginalUse(run)
	if err != nil {
		return err
	}
	if !found || u.Authority == nil || u.Ref == nil || *u.Ref != *ref || u.Package != pkg {
		return ErrConfigPreparationStoreMismatch
	}
	m, err := owner.loadManifest(*ref)
	if err != nil || m.PackageVersion != pkg.Version || m.InstalledManifestSHA256 != pkg.InstalledManifestSHA256 || m.PayloadManifestSHA256 != pkg.PayloadManifestSHA256 {
		return ErrConfigGenerationConflict
	}
	anchor, err := owner.anchorOriginalUse(u)
	if err != nil {
		return err
	}
	defer anchor.directory.Close()
	pins, err := generationDirectory(owner.root, "pins", false)
	if err != nil {
		return err
	}
	defer pins.Close()
	type pinned struct {
		name     string
		body     []byte
		identity publicationFileIdentity
	}
	var originals []pinned
	plan := ""
	seen := map[string]bool{}
	for _, task := range tasks {
		if !idPattern.MatchString(task) || seen[task] || !containsConfigTask(u.TaskIDs, task) {
			return ErrConfigPreparationStoreMismatch
		}
		seen[task] = true
		name := task + ".json"
		body, identity, err := publicationReadAt(pins, name, generationRecordLimit)
		if err != nil {
			return ErrConfigGenerationConflict
		}
		p, err := generationDecodePinRecord(body)
		if err != nil || p.SchemaVersion != 3 || p.Authority == nil || *p.Authority != *u.Authority || p.RunID != run || p.TaskID != task || p.Version != pkg.Version || p.ConfigGeneration == nil || *p.ConfigGeneration != *ref {
			return ErrConfigPreparationStoreMismatch
		}
		// The source and all retained descendants must carry the same exact
		// original preparation digest; a different valid digest is not enough.
		if plan == "" {
			plan = p.PreparationSHA256
		}
		if p.PreparationSHA256 != plan {
			return ErrConfigPreparationStoreMismatch
		}
		originals = append(originals, pinned{name, body, identity})
	}
	verify := func() error {
		if err := anchor.validate(owner); err != nil {
			return err
		}
		if publicationSamePath(pins, filepath.Join(pkg.DestinationRoot, "pins")) != nil || owner.validate() != nil {
			return ErrConfigGenerationConflict
		}
		for _, original := range originals {
			body, identity, err := publicationReadAt(pins, original.name, generationRecordLimit)
			if err != nil || identity != original.identity || !bytes.Equal(body, original.body) {
				return ErrConfigGenerationConflict
			}
		}
		return owner.validate()
	}
	if err = verify(); err != nil {
		return err
	}
	return commit(u, plan, verify)
}
