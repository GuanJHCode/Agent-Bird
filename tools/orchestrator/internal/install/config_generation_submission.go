//go:build !windows

package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"golang.org/x/sys/unix"
)

func ValidateConfigGenerationSubmission(actualExecutable, authenticatedScope, runID, preparedPlanSHA256 string, tasks []ConfigGenerationTaskUse) error {
	id, err := runningPackageIdentity(actualExecutable)
	if err != nil {
		return err
	}
	lock, err := acquireInstallLock(id.DestinationRoot)
	if err != nil {
		return err
	}
	defer releaseInstallLock(lock)
	return validateConfigGenerationSubmissionLocked(actualExecutable, authenticatedScope, runID, preparedPlanSHA256, tasks)
}

type ConfigGenerationTaskUse struct {
	ExecutionPolicy *contract.ExecutionPolicy
	// Submission constraints are copied from the authenticated resolved request.
	// FS-only manifest checks do not establish a Store admission capability.
	PlanRevision           int
	Dependencies           []string
	MaxAttempts            int
	WorkRevision           int
	BudgetGroupID          string
	MaxActiveMS            int64
	CompletionPolicy       string
	ExpectedArtifactSHA256 string
	TaskID                 string
	AdapterPayload         json.RawMessage
	Fallbacks              []json.RawMessage
}

func validateConfigGenerationSubmissionLocked(actualExecutable, authenticatedScope, runID, preparedPlanSHA256 string, tasks []ConfigGenerationTaskUse) error {
	var ref *contract.ConfigGenerationRef
	payloads := make([][]adapter.InvocationPayload, len(tasks))
	for i, task := range tasks {
		for _, raw := range append([]json.RawMessage{task.AdapterPayload}, task.Fallbacks...) {
			if len(raw) == 0 {
				payloads[i] = append(payloads[i], adapter.InvocationPayload{})
				continue
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil {
				return generationInvalid()
			}
			has := false
			for key := range fields {
				if strings.EqualFold(key, "config_generation") || strings.EqualFold(key, "config_model_source") {
					has = true
				}
			}
			if !has {
				payloads[i] = append(payloads[i], adapter.InvocationPayload{})
				continue
			}
			p, err := adapter.DecodeInvocationPayload(raw)
			if err != nil || p.ConfigGeneration == nil {
				return generationInvalid()
			}
			if ref == nil {
				original := *p.ConfigGeneration
				ref = &original
			} else if *ref != *p.ConfigGeneration {
				return ErrConfigGenerationConflict
			}
			payloads[i] = append(payloads[i], p)
		}
	}
	if ref == nil {
		return nil
	} // No new authority imposed on the original legacy Submit.
	if !generationHash(authenticatedScope) || ref.OwnerScopeSHA256 != authenticatedScope || !generationHash(preparedPlanSHA256) || !idPattern.MatchString(runID) || len(tasks) == 0 || len(tasks) > 64 {
		return ErrConfigGenerationConflict
	}
	pkg, err := verifiedGenerationPackage(actualExecutable)
	if err != nil {
		return err
	}
	g, err := openGenerationOwner(pkg.DestinationRoot, authenticatedScope, false)
	if err != nil {
		return err
	}
	defer g.close()
	if err = g.quiescent(""); err != nil {
		return err
	}
	if err = g.rejectAbandoned(runID); err != nil {
		return err
	}
	m, err := g.loadManifest(*ref)
	if err != nil || m.PackageVersion != pkg.Version || m.InstalledManifestSHA256 != pkg.InstalledManifestSHA256 || m.PayloadManifestSHA256 != pkg.PayloadManifestSHA256 {
		return errors.New("config_package_mismatch")
	}
	u, found, e := g.readOriginalUse(runID)
	if e != nil {
		return e
	}
	if !found || u.Authority == nil {
		return ErrConfigPreparationStoreUnbound
	}
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.TaskID
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, u.TaskIDs) || u.Package != pkg || u.Ref == nil || *u.Ref != *ref {
		return ErrConfigPreparationStoreMismatch
	}
	seen := map[string]bool{}
	for i, task := range tasks {
		if seen[task.TaskID] || !idPattern.MatchString(task.TaskID) {
			return generationInvalid()
		}
		seen[task.TaskID] = true
		for _, p := range payloads[i] {
			if p.ConfigGeneration == nil || *p.ConfigGeneration != *ref || p.ValidateConfigModelSource() != nil {
				return ErrConfigGenerationConflict
			}
			// The unchanged manifest has no Grok saved default. Explicit Grok
			// model selection and CLI default retain the same generation proof;
			// other providers keep the existing supported-provider validation.
			grokOwnModel := p.Provider == string(adapter.ProviderGrok) && (p.ConfigModelSource == "explicit" || p.ConfigModelSource == "cli-default")
			if p.Profile != nil && !grokOwnModel {
				model, e := m.SavedDefaults.Model(p.Provider)
				if e != nil {
					return e
				}
				if p.ConfigModelSource == "saved-default" && string(p.Profile.Model) != model {
					return errors.New("config_model_mismatch")
				}
			}
		}
		pin, exists, e := readPin(filepath.Join(pkg.DestinationRoot, "pins", task.TaskID+".json"))
		if e == nil && exists && pin.SchemaVersion == 2 {
			return ErrConfigPreparationStoreUnbound
		}
		if e != nil || !exists || pin.SchemaVersion != 3 || pin.RunID != runID || pin.TaskID != task.TaskID || pin.Version != pkg.Version || pin.ConfigGeneration == nil || *pin.ConfigGeneration != *ref || pin.PreparationSHA256 != preparedPlanSHA256 || pin.Authority == nil || *pin.Authority != *u.Authority {
			return ErrConfigGenerationConflict
		}
	}
	return g.validate()
}

// Only Coordinator's existing ReleasableTasks proof may call this path. A JSON
// administrator request cannot supply a terminal flag or erase a version2 use.
func ReleaseConfigTaskUse(actualExecutable, runID, taskID string, ref *contract.ConfigGenerationRef) error {
	return releaseConfigTaskUse(actualExecutable, runID, taskID, ref)
}

func releaseConfigTaskUse(actualExecutable, runID, taskID string, ref *contract.ConfigGenerationRef) error {
	if !idPattern.MatchString(taskID) || ref != nil && (!idPattern.MatchString(runID) || ref.Validate() != nil) {
		return generationInvalid()
	}
	id, err := runningPackageIdentity(actualExecutable)
	if err != nil {
		return err
	}
	lock, err := acquireInstallLock(id.DestinationRoot)
	if err != nil {
		return err
	}
	defer releaseInstallLock(lock)
	if err = verifyInstalledVersion(id.DestinationRoot, id.Version); err != nil {
		return err
	}
	if err = verifyPrivateDirectory(filepath.Join(id.DestinationRoot, "pins")); err != nil {
		return err
	}
	path := filepath.Join(id.DestinationRoot, "pins", taskID+".json")
	p, exists, err := readPin(path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if p.TaskID != taskID || p.Version != id.Version {
		return ErrConfigGenerationConflict
	}
	if p.SchemaVersion == 2 {
		return ErrConfigPreparationStoreUnbound
	}
	if p.SchemaVersion == 3 {
		return ErrConfigPreparationStoreMismatch
	}
	if ref != nil {
		return ErrConfigGenerationConflict
	}
	parent, err := publicationOpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	if err = unix.Unlinkat(int(parent.Fd()), filepath.Base(path), 0); err != nil {
		return err
	}
	if err = parent.Sync(); err != nil {
		return err
	}
	return publicationSamePath(parent, filepath.Dir(path))
}

func ReadConfigGenerationActive(actualExecutable, scope string) (*ConfigGenerationActive, error) {
	if !generationHash(scope) {
		return nil, generationInvalid()
	}
	id, err := runningPackageIdentity(actualExecutable)
	if err != nil {
		return nil, err
	}
	lock, err := acquireInstallLock(id.DestinationRoot)
	if err != nil {
		return nil, err
	}
	defer releaseInstallLock(lock)
	if _, err = ConfigPreparationPackageIdentity(actualExecutable); err != nil {
		return nil, err
	}
	g, err := openGenerationOwner(id.DestinationRoot, scope, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer g.close()
	if err = g.quiescent(""); err != nil {
		return nil, err
	}
	active, err := generationReadActive(g.owner, scope)
	if err == nil {
		err = g.validate()
	}
	return active, err
}
