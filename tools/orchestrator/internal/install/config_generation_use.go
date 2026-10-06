//go:build !windows

package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func (g *generationOwner) preparationUses() ([]ConfigPreparationUse, error) {
	names, err := generationRecordNames(g.dirs["preparations"])
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []ConfigPreparationUse
	for _, name := range names {
		name = strings.TrimPrefix(name, ".pending-")
		if seen[name] {
			continue
		}
		seen[name] = true
		raw, _, exists, err := generationOriginalRecord(g.dirs["preparations"], name, generationRecordLimit)
		var use ConfigPreparationUse
		if err != nil || !exists || generationDecode(raw, &use, generationRecordLimit) != nil || validatePreparationUse(use) != nil || use.OwnerScopeSHA256 != g.scope || use.Package.DestinationRoot != g.path || name != publicationBytesDigest([]byte(use.RunID))+".json" {
			return nil, generationInvalid()
		}
		out = append(out, use)
	}
	return out, nil
}

func (g *generationOwner) checkReferences(generationID, version string) error {
	active, err := generationReadActive(g.owner, g.scope)
	if err != nil {
		return err
	}
	if active != nil && active.Ref != nil && (active.Ref.GenerationID == generationID || active.Ref.PackageVersion == version) {
		return ErrVersionPinned
	}
	uses, err := g.preparationUses()
	if err != nil {
		return err
	}
	for _, use := range uses {
		if use.Package.Version == version || use.Ref != nil && use.Ref.GenerationID == generationID {
			return ErrVersionPinned
		}
	}
	pins, err := generationDirectory(g.root, "pins", false)
	if err != nil {
		return err
	}
	defer pins.Close()
	names, err := generationNames(pins, 4096)
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") || !idPattern.MatchString(strings.TrimSuffix(name, ".json")) {
			return generationInvalid()
		}
		p, exists, err := readPin(filepath.Join(g.path, "pins", name))
		if err != nil || !exists {
			return generationInvalid()
		}
		if p.Version == version || p.ConfigGeneration != nil && p.ConfigGeneration.OwnerScopeSHA256 == g.scope && p.ConfigGeneration.GenerationID == generationID {
			return ErrVersionPinned
		}
	}
	return nil
}

func beginConfigPreparation(executable, scope, runID string, taskIDs []string, requestSHA string) (ConfigPreparationUse, error) {
	return beginConfigPreparationForTasks(executable, scope, runID, taskIDs, requestSHA, nil)
}

// Task inputs are checked under the same selection lock, before the original
// hold is written. A selected unsupported subset cannot reserve a new use.
func BeginConfigPreparationForTasks(executable, scope, runID, requestSHA string, tasks []ConfigGenerationTaskUse, authorities ...*contract.ConfigPreparationAuthority) (ConfigPreparationUse, error) {
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.TaskID
	}
	return beginConfigPreparationForTasks(executable, scope, runID, ids, requestSHA, tasks, authorities...)
}

func configPreparationInputs(tasks []ConfigGenerationTaskUse, selected bool) error {
	for _, task := range tasks {
		for _, raw := range append([]json.RawMessage{task.AdapterPayload}, task.Fallbacks...) {
			p, err := adapter.DecodeInvocationPayload(raw)
			if err != nil {
				return err
			}
			if p.ConfigGeneration != nil || p.ConfigModelSource != "" {
				return errors.New("config_input_provenance_forbidden")
			}
			// Grok has no saved-default field in this generation schema. It may
			// still bind the generation with an explicit model or CLI default.
			grokUnsupported := p.Provider == string(adapter.ProviderGrok) && (p.Profile == nil || !adapter.ValidModelID(string(p.Profile.Model)) || p.Profile.CLIModelDefault && p.Profile.Model != "" || !p.Profile.CLIModelDefault && p.Profile.Model == "")
			if selected && (grokUnsupported || p.Kind == "gitops" && p.CandidateAction == nil && p.CandidateWorkspace == nil) {
				return errors.New("config_generation_subset_unsupported")
			}
		}
	}
	return nil
}

func beginConfigPreparationForTasks(executable, scope, runID string, taskIDs []string, requestSHA string, tasks []ConfigGenerationTaskUse, authorities ...*contract.ConfigPreparationAuthority) (ConfigPreparationUse, error) {
	if !generationHash(scope) || !idPattern.MatchString(runID) || !generationHash(requestSHA) || len(taskIDs) == 0 || len(taskIDs) > 64 {
		return ConfigPreparationUse{}, generationInvalid()
	}
	taskIDs = append([]string(nil), taskIDs...)
	sort.Strings(taskIDs)
	for i, id := range taskIDs {
		if !idPattern.MatchString(id) || i > 0 && id == taskIDs[i-1] {
			return ConfigPreparationUse{}, generationInvalid()
		}
	}
	id, err := runningPackageIdentity(executable)
	if err != nil {
		return ConfigPreparationUse{}, err
	}
	lock, err := acquireInstallLock(id.DestinationRoot)
	if err != nil {
		return ConfigPreparationUse{}, err
	}
	defer releaseInstallLock(lock)
	pkg, err := ConfigPreparationPackageIdentity(executable)
	if err != nil {
		return ConfigPreparationUse{}, err
	}
	g, err := openGenerationOwner(pkg.DestinationRoot, scope, true)
	if err != nil {
		return ConfigPreparationUse{}, err
	}
	defer g.close()
	if err = g.quiescent(""); err != nil {
		return ConfigPreparationUse{}, err
	}
	var authority *contract.ConfigPreparationAuthority
	if len(authorities) > 1 {
		return ConfigPreparationUse{}, generationInvalid()
	}
	if len(authorities) == 1 {
		authority = authorities[0]
	}
	if authority != nil && authority.Validate() != nil {
		return ConfigPreparationUse{}, ErrConfigPreparationStoreMismatch
	}
	if err = g.rejectAbandoned(runID); err != nil {
		return ConfigPreparationUse{}, err
	}
	name := publicationBytesDigest([]byte(runID)) + ".json"
	raw, _, exists, err := generationOriginalRecord(g.dirs["preparations"], name, generationRecordLimit)
	if err != nil {
		return ConfigPreparationUse{}, err
	}
	if exists {
		var use ConfigPreparationUse
		if generationDecode(raw, &use, generationRecordLimit) != nil || validatePreparationUse(use) != nil || use.OwnerScopeSHA256 != scope || use.RunID != runID || use.RequestSHA256 != requestSHA || !reflect.DeepEqual(use.TaskIDs, taskIDs) || use.Package != pkg {
			return use, ErrConfigGenerationConflict
		}
		if use.Ref != nil {
			if use.Authority == nil {
				return use, ErrConfigPreparationStoreUnbound
			}
			if authority == nil || *authority != *use.Authority {
				return use, ErrConfigPreparationStoreMismatch
			}
		}
		if err = configPreparationInputs(tasks, use.Ref != nil); err != nil {
			return use, err
		}
		if err = g.writeRecord(g.dirs["preparations"], name, raw, generationRecordLimit); err != nil {
			return use, err
		}
		return use, g.validate()
	}
	if _, found, e := g.readOriginalUse(runID); e != nil {
		return ConfigPreparationUse{}, e
	} else if found {
		return ConfigPreparationUse{}, errors.New("config_original_preparation_required")
	}
	// A completed conversion must be recovered from its private checkpoint,
	// never interpreted as a new selection after loss/deletion of that file.
	for _, task := range taskIDs {
		_, exists, err := readPin(filepath.Join(pkg.DestinationRoot, "pins", task+".json"))
		if err != nil {
			return ConfigPreparationUse{}, err
		}
		if exists {
			return ConfigPreparationUse{}, errors.New("config_original_preparation_required")
		}
	}
	active, err := generationReadActive(g.owner, scope)
	if err != nil {
		return ConfigPreparationUse{}, err
	}
	use := ConfigPreparationUse{SchemaVersion: 1, OwnerScopeSHA256: scope, RunID: runID, TaskIDs: taskIDs, RequestSHA256: requestSHA, Mode: "legacy", Package: pkg}
	if active != nil && active.Ref != nil {
		manifest, err := g.loadManifest(*active.Ref)
		if err != nil || manifest.PackageVersion != pkg.Version || manifest.InstalledManifestSHA256 != pkg.InstalledManifestSHA256 || manifest.PayloadManifestSHA256 != pkg.PayloadManifestSHA256 {
			return use, errors.New("config_package_mismatch")
		}
		if authority == nil {
			return use, ErrConfigPreparationStoreUnbound
		}
		copy := *authority
		use.Authority = &copy
		use.SchemaVersion = 2
		use.Mode = "generation"
		ref := *active.Ref
		use.Ref = &ref
	}
	if err = configPreparationInputs(tasks, use.Ref != nil); err != nil {
		return use, err
	}
	if validatePreparationUse(use) != nil {
		return use, generationInvalid()
	}
	raw, _ = json.Marshal(use)
	raw = append(raw, '\n')
	if err = g.writeRecord(g.dirs["preparations"], name, raw, generationRecordLimit); err != nil {
		return use, err
	}
	if use.Ref != nil {
		if err = g.persistOriginalUse(use); err != nil {
			return use, err
		}
	}
	return use, g.validate()
}

// Called only after the private resolved checkpoint is durable. A partial
// prefix retains the hold; all pins and their parent are durable before removal.
func pinConfigPreparationImpl(executable string, use ConfigPreparationUse, preparationSHA string, checkpoint func(string) error) error {
	if validatePreparationUse(use) != nil || !generationHash(preparationSHA) {
		return generationInvalid()
	}
	if use.Ref != nil && use.Authority == nil {
		return ErrConfigPreparationStoreUnbound
	}
	id, err := runningPackageIdentity(executable)
	if err != nil || id.DestinationRoot != use.Package.DestinationRoot || id.Version != use.Package.Version {
		return errors.New("config_package_mismatch")
	}
	lock, err := acquireInstallLock(id.DestinationRoot)
	if err != nil {
		return err
	}
	defer releaseInstallLock(lock)
	pkg, err := ConfigPreparationPackageIdentity(executable)
	if err != nil || pkg != use.Package {
		return errors.New("config_package_mismatch")
	}
	g, err := openGenerationOwner(pkg.DestinationRoot, use.OwnerScopeSHA256, false)
	if err != nil {
		return err
	}
	defer g.close()
	if err = g.quiescent(""); err != nil {
		return err
	}
	if err = g.rejectAbandoned(use.RunID); err != nil {
		return err
	}
	name := publicationBytesDigest([]byte(use.RunID)) + ".json"
	raw, _, exists, err := generationOriginalRecord(g.dirs["preparations"], name, generationRecordLimit)
	if err != nil {
		return err
	}
	if exists {
		var original ConfigPreparationUse
		if generationDecode(raw, &original, generationRecordLimit) != nil || !reflect.DeepEqual(original, use) {
			return ErrConfigGenerationConflict
		}
	}
	if use.Ref != nil {
		if exists {
			if err = g.persistOriginalUse(use); err != nil {
				return err
			}
		}
		original, found, e := g.readOriginalUse(use.RunID)
		if e != nil {
			return e
		}
		if !found || !reflect.DeepEqual(original, use) {
			return ErrConfigGenerationConflict
		}
	}
	var useAnchor *configUseAnchor
	if use.Ref != nil {
		useAnchor, err = g.anchorOriginalUse(use)
		if err != nil {
			return err
		}
		defer useAnchor.directory.Close()
	}
	pins, err := generationDirectory(g.root, "pins", false)
	if err != nil {
		return err
	}
	defer pins.Close()
	type originalPin struct {
		raw      []byte
		identity publicationFileIdentity
	}
	originals := map[string]originalPin{}
	validatePins := func() error {
		if useAnchor != nil {
			if e := useAnchor.validate(g); e != nil {
				return e
			}
		}
		if g.validate() != nil || publicationSamePath(pins, filepath.Join(pkg.DestinationRoot, "pins")) != nil {
			return ErrConfigGenerationConflict
		}
		for name, original := range originals {
			body, id, e := publicationReadAt(pins, name, generationRecordLimit)
			if e != nil || id != original.identity || !bytes.Equal(body, original.raw) {
				return ErrConfigGenerationConflict
			}
		}
		return g.validate()
	}
	for _, task := range use.TaskIDs {
		if err = validatePins(); err != nil {
			return err
		}
		want := pin{TaskID: task, Version: pkg.Version}
		if use.Ref != nil {
			want.SchemaVersion = 3
			want.Authority = use.Authority
			want.RunID = use.RunID
			want.ConfigGeneration = use.Ref
			want.PreparationSHA256 = preparationSHA
		}
		pinName := task + ".json"
		body, identity, e := publicationReadAt(pins, pinName, generationRecordLimit)
		if e == nil {
			current, parseErr := generationDecodePinRecord(body)
			if parseErr != nil || !reflect.DeepEqual(current, want) {
				return ErrConfigGenerationConflict
			}
		} else if errors.Is(e, os.ErrNotExist) {
			if !exists {
				return errors.New("config_original_preparation_required")
			}
			body, _ = json.Marshal(want)
			body = append(body, '\n')
			identity, e = publicationWriteAt(pins, pinName, body)
			if e == nil {
				e = pins.Sync()
			}
			if e != nil {
				return e
			}
		} else {
			return e
		}
		originals[pinName] = originalPin{body, identity}
		if err = generationCheck(checkpoint, "pin:"+task); err != nil {
			return err
		}
	}
	if err = pins.Sync(); err != nil {
		return err
	}
	if err = validatePins(); err != nil {
		return err
	}
	if exists {
		// Recover a complete pending hold before removing its original name.
		if err = g.writeRecord(g.dirs["preparations"], name, raw, generationRecordLimit); err != nil {
			return err
		}
		if err = validatePins(); err != nil {
			return err
		}
		if err = g.removeRecord(g.dirs["preparations"], name, raw, generationRecordLimit); err != nil {
			return err
		}
	}
	return validatePins()
}

func readConfigGeneration(executable string, ref contract.ConfigGenerationRef) (ConfigGenerationManifest, error) {
	id, err := runningPackageIdentity(executable)
	if err != nil {
		return ConfigGenerationManifest{}, err
	}
	lock, err := acquireInstallLock(id.DestinationRoot)
	if err != nil {
		return ConfigGenerationManifest{}, err
	}
	defer releaseInstallLock(lock)
	pkg, err := verifiedGenerationPackage(executable)
	if err != nil {
		return ConfigGenerationManifest{}, err
	}
	g, err := openGenerationOwner(pkg.DestinationRoot, ref.OwnerScopeSHA256, false)
	if err != nil {
		return ConfigGenerationManifest{}, err
	}
	defer g.close()
	if err = g.quiescent(""); err != nil {
		return ConfigGenerationManifest{}, err
	}
	m, err := g.loadManifest(ref)
	if err != nil || m.PackageVersion != pkg.Version || m.InstalledManifestSHA256 != pkg.InstalledManifestSHA256 || m.PayloadManifestSHA256 != pkg.PayloadManifestSHA256 {
		return m, errors.New("config_package_mismatch")
	}
	return m, nil
}

func generationReadPinRecord(path string) (pin, bool, error) {
	if err := verifyPrivateDirectory(filepath.Dir(path)); err != nil {
		return pin{}, false, err
	}
	parent, err := publicationOpenRoot(filepath.Dir(path))
	if err != nil {
		return pin{}, false, err
	}
	defer parent.Close()
	raw, _, err := publicationReadAt(parent, filepath.Base(path), generationRecordLimit)
	if errors.Is(err, os.ErrNotExist) {
		return pin{}, false, nil
	}
	if err != nil {
		return pin{}, false, err
	}
	p, err := generationDecodePinRecord(raw)
	if err == nil {
		err = publicationSamePath(parent, filepath.Dir(path))
	}
	return p, err == nil, err
}

func generationDecodePinRecord(raw []byte) (pin, error) {
	var p pin
	if generationDecode(raw, &p, generationRecordLimit) != nil || !idPattern.MatchString(p.TaskID) || !versionPattern.MatchString(p.Version) {
		return p, errors.New("pin_invalid")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return p, errors.New("pin_invalid")
	}
	if p.SchemaVersion == 0 {
		if len(fields) != 2 || fields["task_id"] == nil || fields["version"] == nil {
			return p, errors.New("pin_invalid")
		}
	} else if (p.SchemaVersion != 2 && p.SchemaVersion != 3) || len(fields) != 6+map[bool]int{true: 1}[p.SchemaVersion == 3] || p.SchemaVersion == 2 && p.Authority != nil || p.SchemaVersion == 3 && (p.Authority == nil || p.Authority.Validate() != nil) || !idPattern.MatchString(p.RunID) || p.ConfigGeneration == nil || p.ConfigGeneration.Validate() != nil || p.ConfigGeneration.PackageVersion != p.Version || !generationHash(p.PreparationSHA256) {
		return p, errors.New("pin_invalid")
	}
	return p, nil
}
