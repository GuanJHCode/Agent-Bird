//go:build !windows

package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"golang.org/x/sys/unix"
)

func generationDecode(raw []byte, value any, limit int) error {
	if contract.DecodeBridgeStrict(raw, value) != nil || strictJSONBounded(raw, value, false, limit) != nil {
		return generationInvalid()
	}
	return nil
}

func generationDirectory(parent *os.File, name string, create bool) (*os.File, error) {
	if create {
		if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
		if err := parent.Sync(); err != nil {
			return nil, err
		}
	}
	f, err := publicationOpen(parent, name, true)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		var id publicationDirectoryIdentity
		id, err = publicationDirectory(info)
		if err == nil && id.Mode != 0700 {
			err = generationInvalid()
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func generationNames(dir *os.File, limit int) ([]string, error) {
	f, err := publicationOpen(dir, ".", true)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.Readdirnames(limit + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("config_generation_capacity")
	}
	sort.Strings(entries)
	return entries, nil
}

func generationOwnedRoot(root string, create bool) (*os.File, *os.File, error) {
	canonical, err := canonicalManagedPath(root)
	if err != nil || canonical != root || verifyPrivateDirectory(root) != nil {
		return nil, nil, generationInvalid()
	}
	f, err := publicationOpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	config, err := generationDirectory(f, "owned-config", create)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	names, err := generationNames(config, generationCapacity)
	if err == nil {
		for _, name := range names {
			if !generationHash(name) {
				err = generationInvalid()
				break
			}
			child, e := generationDirectory(config, name, false)
			if e != nil {
				err = e
				break
			}
			child.Close()
		}
	}
	if err != nil {
		config.Close()
		f.Close()
		return nil, nil, err
	}
	return f, config, nil
}

func generationReadActive(owner *os.File, scope string) (*ConfigGenerationActive, error) {
	raw, _, err := publicationReadAt(owner, "active.json", generationRecordLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var value ConfigGenerationActive
	if err != nil || generationDecode(raw, &value, generationRecordLimit) != nil || value.Validate() != nil || value.Ref != nil && value.Ref.OwnerScopeSHA256 != scope {
		return nil, generationInvalid()
	}
	return &value, nil
}

// Called under the original install lock. Any unrecognized owned state blocks
// deletion; its absence remains the legacy installation compatibility branch.
func configGenerationCheckUninstall(root, version string) error {
	f, config, err := generationOwnedRoot(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	defer config.Close()
	names, err := generationNames(config, generationCapacity)
	if err != nil {
		return err
	}
	for _, scope := range names {
		g, e := openGenerationOwner(root, scope, false)
		if e != nil {
			return e
		}
		err = func() error {
			defer g.close()
			if e := g.quiescent(""); e != nil {
				return e
			}
			if e := g.validateManifests(); e != nil {
				return e
			}
			return g.checkReferences("", version)
		}()
		if err != nil {
			return err
		}
	}
	return publicationSamePath(f, root)
}

func (g *generationOwner) validateManifests() error {
	names, err := generationRecordNames(g.dirs["generations"])
	if err != nil {
		return err
	}
	for _, name := range names {
		if len(name) > 0 && name[0] == '.' {
			return ErrConfigGenerationPending
		}
		body, _, err := publicationReadAt(g.dirs["generations"], name, generationManifestLimit)
		var m ConfigGenerationManifest
		if err != nil || generationDecode(body, &m, generationManifestLimit) != nil {
			return generationInvalid()
		}
		canonical, ref, err := generationManifestBytes(m)
		if err != nil || m.OwnerScopeSHA256 != g.scope || name != ref.GenerationID+".json" || !bytes.Equal(body, canonical) {
			return generationInvalid()
		}
	}
	return nil
}

func generationManifestBytes(m ConfigGenerationManifest) ([]byte, contract.ConfigGenerationRef, error) {
	if validateGenerationManifest(m) != nil {
		return nil, contract.ConfigGenerationRef{}, generationInvalid()
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw)+1 > generationManifestLimit {
		return nil, contract.ConfigGenerationRef{}, generationInvalid()
	}
	raw = append(raw, '\n')
	ref := contract.ConfigGenerationRef{SchemaVersion: 1, OwnerScopeSHA256: m.OwnerScopeSHA256, GenerationID: publicationBytesDigest(append([]byte("agent-bird-config-generation-v1\x00"), raw...)), ManifestSHA256: publicationBytesDigest(raw), PackageVersion: m.PackageVersion}
	return raw, ref, nil
}

func verifiedGenerationPackage(executable string) (ConfigGenerationPackage, error) {
	return verifiedConfigPackage(executable, false)
}

// Inspect without creating a use; only the original validated legacy package
// branch may lack a payload manifest. Generation publication always requires it.
func ConfigPreparationPackageIdentity(executable string) (ConfigGenerationPackage, error) {
	return verifiedConfigPackage(executable, true)
}

func verifiedConfigPackage(executable string, allowLegacy bool) (ConfigGenerationPackage, error) {
	id, err := runningPackageIdentity(executable)
	if err != nil {
		return ConfigGenerationPackage{}, err
	}
	if verifyInstalledVersion(id.DestinationRoot, id.Version) != nil {
		return ConfigGenerationPackage{}, errors.New("config_package_invalid")
	}
	root := filepath.Join(id.DestinationRoot, "versions", id.Version)
	manifestRaw, err := readOwnedRegular(filepath.Join(root, "manifest.json"), 0600, 4<<20)
	if err != nil {
		return ConfigGenerationPackage{}, err
	}
	payloadRaw, err := readOwnedRegular(filepath.Join(root, payloadManifestName), 0600, 1<<20)
	if err != nil && (!allowLegacy || !errors.Is(err, os.ErrNotExist)) {
		return ConfigGenerationPackage{}, errors.New("config_package_payload_required")
	}
	if _, err = validatePayload(root, true); err != nil {
		return ConfigGenerationPackage{}, err
	}
	binaryHash, err := hashFile(executable)
	if err != nil {
		return ConfigGenerationPackage{}, err
	}
	payloadHash := ""
	if payloadRaw != nil {
		payloadHash = publicationBytesDigest(payloadRaw)
	}
	return ConfigGenerationPackage{id.DestinationRoot, id.Version, publicationBytesDigest(manifestRaw), payloadHash, binaryHash}, nil
}

// Read-only health projection: completed historical receipts are not live
// references and are never scanned or recovered by Doctor.
func doctorConfigGenerations(root string) []string {
	parent, config, err := generationOwnedRoot(root, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []string{"owned_config_invalid"}
	}
	defer parent.Close()
	defer config.Close()
	scopes, err := generationNames(config, generationCapacity)
	if err != nil {
		return []string{"owned_config_invalid"}
	}
	var issues []string
	for _, scope := range scopes {
		g, e := openGenerationOwner(root, scope, false)
		if e != nil {
			issues = append(issues, "owned_config_invalid:"+scope)
			continue
		}
		e = func() error {
			defer g.close()
			if e := g.quiescent(""); e != nil {
				return e
			}
			if e := g.validateManifests(); e != nil {
				return e
			}
			active, e := generationReadActive(g.owner, scope)
			if e != nil {
				return e
			}
			if active != nil && active.Ref != nil {
				if _, e = g.loadManifest(*active.Ref); e != nil {
					return e
				}
			}
			uses, e := g.preparationUses()
			if e != nil {
				return e
			}
			for _, use := range uses {
				if use.Ref != nil {
					if _, e = g.loadManifest(*use.Ref); e != nil {
						return e
					}
				}
			}
			return g.validate()
		}()
		if errors.Is(e, ErrConfigGenerationPending) {
			issues = append(issues, "owned_config_pending:"+scope)
		} else if e != nil {
			issues = append(issues, "owned_config_invalid:"+scope)
		}
	}
	if publicationSamePath(parent, root) != nil {
		issues = append(issues, "owned_config_invalid")
	}
	return issues
}
