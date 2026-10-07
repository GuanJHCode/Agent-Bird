//go:build !windows

package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"golang.org/x/sys/unix"
)

var ErrConfigPreparationStoreMismatch = errors.New("config_preparation_store_mismatch")
var ErrConfigPreparationStoreUnbound = errors.New("config_preparation_store_unbound")

// Direct-addressed immutable original-use history does not consume live capacity.
// A hold is durable before this record starts and remains until its final record
// and all original pins have passed their path/identity barriers.
func (g *generationOwner) useDirectory(run string, create bool) (*os.File, error) {
	if g.dirs["uses"] == nil {
		if !create {
			return nil, os.ErrNotExist
		}
		f, e := generationDirectory(g.owner, "uses", true)
		if e != nil {
			return nil, e
		}
		g.dirs["uses"] = f
	}
	key := publicationBytesDigest([]byte(run))
	f, e := generationDirectory(g.dirs["uses"], key[:2], create)
	if e == nil && (g.validate() != nil || publicationSamePath(f, filepath.Join(g.path, "owned-config", g.scope, "uses", key[:2])) != nil) {
		f.Close()
		return nil, ErrConfigGenerationConflict
	}
	return f, e
}
func (g *generationOwner) validateUseDirectory(f *os.File, run string) error {
	key := publicationBytesDigest([]byte(run))
	info, e := f.Stat()
	var id publicationDirectoryIdentity
	if e == nil {
		id, e = publicationDirectory(info)
	}
	if e != nil || id.Mode != 0700 || g.validate() != nil || publicationSamePath(f, filepath.Join(g.path, "owned-config", g.scope, "uses", key[:2])) != nil {
		return ErrConfigGenerationConflict
	}
	return nil
}
func (g *generationOwner) persistOriginalUse(u ConfigPreparationUse) error {
	f, e := g.useDirectory(u.RunID, true)
	if e != nil {
		return e
	}
	defer f.Close()
	raw, _ := json.Marshal(u)
	raw = append(raw, '\n')
	name := publicationBytesDigest([]byte(u.RunID)) + ".json"
	if e = g.writeOriginalRecord(f, name, raw, generationRecordLimit, false); e != nil {
		return e
	}
	body, _, e := publicationReadAt(f, name, generationRecordLimit)
	if e != nil || !bytes.Equal(body, raw) {
		return ErrConfigGenerationConflict
	}
	return g.validateUseDirectory(f, u.RunID)
}
func (g *generationOwner) readOriginalUse(run string) (ConfigPreparationUse, bool, error) {
	var u ConfigPreparationUse
	f, e := g.useDirectory(run, false)
	if errors.Is(e, os.ErrNotExist) {
		return u, false, nil
	}
	if e != nil {
		return u, false, e
	}
	defer f.Close()
	name := publicationBytesDigest([]byte(run)) + ".json"
	if _, _, e = publicationReadAt(f, ".pending-"+name, generationRecordLimit); !errors.Is(e, os.ErrNotExist) {
		return u, false, ErrConfigGenerationPending
	}
	raw, _, e := publicationReadAt(f, name, generationRecordLimit)
	if errors.Is(e, os.ErrNotExist) {
		return u, false, g.validateUseDirectory(f, run)
	}
	if e != nil || generationDecode(raw, &u, generationRecordLimit) != nil || validatePreparationUse(u) != nil || u.RunID != run || u.OwnerScopeSHA256 != g.scope || u.Package.DestinationRoot != g.path {
		return u, false, generationInvalid()
	}
	return u, true, g.validateUseDirectory(f, run)
}

// The guard owns the installation lock until Close. It is not serializable and
// is passed only within the authenticated receiver/Store call stack. Its scope,
// plan and task set are immutable; WithSubmission joins validation with the
// Store's short transaction without a filesystem-to-SQL unlock window.
type ConfigSubmissionGuard struct {
	mu                           sync.Mutex
	lock                         *os.File
	executable, scope, run, plan string
	tasks                        []ConfigGenerationTaskUse
	authority                    contract.ConfigPreparationAuthority
}

func OpenConfigSubmissionGuard(executable, scope, run, plan string, tasks []ConfigGenerationTaskUse) (*ConfigSubmissionGuard, error) {
	if len(tasks) == 0 || tasks[0].PlanRevision <= 0 || ConfigSubmissionPlanDigest(run, tasks) != plan {
		return nil, ErrConfigGenerationConflict
	}
	id, e := runningPackageIdentity(executable)
	if e != nil {
		return nil, e
	}
	lock, e := acquireInstallLock(id.DestinationRoot)
	if e != nil {
		return nil, e
	}
	copied := make([]ConfigGenerationTaskUse, len(tasks))
	for i, t := range tasks {
		copied[i] = t
		if t.Diagnostics != nil {
			policy := *t.Diagnostics
			copied[i].Diagnostics = &policy
		}
		copied[i].Dependencies = append([]string(nil), t.Dependencies...)
		copied[i].AdapterPayload = append(json.RawMessage(nil), t.AdapterPayload...)
		copied[i].Fallbacks = nil
		if copied[i].CompletionPolicy == "" {
			copied[i].CompletionPolicy = "owner_review"
		}
		for _, p := range t.Fallbacks {
			copied[i].Fallbacks = append(copied[i].Fallbacks, append(json.RawMessage(nil), p...))
		}
	}
	g := &ConfigSubmissionGuard{lock: lock, executable: executable, scope: scope, run: run, plan: plan, tasks: copied}
	if e = validateConfigGenerationSubmissionLocked(executable, scope, run, plan, copied); e != nil {
		g.Close()
		return nil, e
	}
	owner, e := openGenerationOwner(id.DestinationRoot, scope, false)
	if e != nil {
		g.Close()
		return nil, e
	}
	u, found, e := owner.readOriginalUse(run)
	owner.close()
	if e != nil || !found || u.Ref == nil || u.Authority == nil {
		g.Close()
		if e != nil {
			return nil, e
		}
		return nil, ErrConfigPreparationStoreUnbound
	}
	g.authority = *u.Authority
	return g, nil
}
func (g *ConfigSubmissionGuard) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lock != nil {
		releaseInstallLock(g.lock)
		g.lock = nil
	}
}
func (g *ConfigSubmissionGuard) WithSubmission(executable, run string, tasks []ConfigGenerationTaskUse, commit func(contract.ConfigPreparationAuthority) error) error {
	if g == nil {
		return ErrConfigPreparationStoreUnbound
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range tasks {
		if tasks[i].CompletionPolicy == "" {
			tasks[i].CompletionPolicy = "owner_review"
		}
		if len(tasks[i].Dependencies) == 0 {
			tasks[i].Dependencies = nil
		}
	}
	if g.lock == nil || executable != g.executable || run != g.run || !reflect.DeepEqual(tasks, g.tasks) {
		return ErrConfigPreparationStoreMismatch
	}
	if e := validateConfigGenerationSubmissionLocked(g.executable, g.scope, g.run, g.plan, g.tasks); e != nil {
		return e
	}
	return commit(g.authority)
}

// Store supplies the trusted terminal verifier while this function holds the
// original install lock. There is no wire terminal flag or administrator route.
func ReleaseConfigTaskUseWithStore(executable, run, task string, ref *contract.ConfigGenerationRef, verify func(ConfigPreparationUse, func() error) error) error {
	id, e := runningPackageIdentity(executable)
	if e != nil {
		return e
	}
	lock, e := acquireInstallLock(id.DestinationRoot)
	if e != nil {
		return e
	}
	defer releaseInstallLock(lock)
	if ref == nil || ref.Validate() != nil || !idPattern.MatchString(task) || !idPattern.MatchString(run) || verify == nil {
		return generationInvalid()
	}
	pkg, e := verifiedGenerationPackage(executable)
	if e != nil {
		return e
	}
	owner, e := openGenerationOwner(pkg.DestinationRoot, ref.OwnerScopeSHA256, false)
	if e != nil {
		return e
	}
	defer owner.close()
	if e = owner.quiescent(""); e != nil {
		return e
	}
	pins, e := generationDirectory(owner.root, "pins", false)
	if e != nil {
		return e
	}
	defer pins.Close()
	u, found, e := owner.readOriginalUse(run)
	if e != nil {
		return e
	}
	if !found || u.Authority == nil {
		return ErrConfigPreparationStoreUnbound
	}
	if u.Package != pkg || u.Ref == nil || *u.Ref != *ref || !containsConfigTask(u.TaskIDs, task) {
		return ErrConfigPreparationStoreMismatch
	}
	name := task + ".json"
	raw, pid, e := publicationReadAt(pins, name, generationRecordLimit)
	if errors.Is(e, os.ErrNotExist) {
		return verify(u, func() error { return publicationSamePath(pins, filepath.Join(pkg.DestinationRoot, "pins")) })
	}
	if e != nil {
		return e
	}
	p, e := generationDecodePinRecord(raw)
	if e != nil {
		return e
	}
	if p.SchemaVersion == 2 {
		return ErrConfigPreparationStoreUnbound
	}
	if p.SchemaVersion != 3 || p.Authority == nil || p.RunID != run || p.TaskID != task || p.Version != pkg.Version || p.ConfigGeneration == nil || *p.ConfigGeneration != *ref {
		return ErrConfigPreparationStoreMismatch
	}
	if *u.Authority != *p.Authority {
		return ErrConfigPreparationStoreMismatch
	}
	useAnchor, e := owner.anchorOriginalUse(u)
	if e != nil {
		return e
	}
	defer useAnchor.directory.Close()
	return verify(u, func() error {
		if e := useAnchor.validate(owner); e != nil {
			return e
		}
		body, current, e := publicationReadAt(pins, name, generationRecordLimit)
		if e != nil || current != pid || !bytes.Equal(body, raw) || owner.validate() != nil || publicationSamePath(pins, filepath.Join(pkg.DestinationRoot, "pins")) != nil {
			return ErrConfigGenerationConflict
		}
		if e = unix.Unlinkat(int(pins.Fd()), name, 0); e != nil {
			return e
		}
		if e = pins.Sync(); e != nil {
			return e
		}
		return owner.validate()
	})
}
func containsConfigTask(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// Reads only the directly named original use. It cannot create/promote authority.
func ReadConfigPreparationAuthority(executable, scope, run string) (*contract.ConfigPreparationAuthority, error) {
	if !generationHash(scope) || !idPattern.MatchString(run) {
		return nil, generationInvalid()
	}
	id, e := runningPackageIdentity(executable)
	if e != nil {
		return nil, e
	}
	lock, e := acquireInstallLock(id.DestinationRoot)
	if e != nil {
		return nil, e
	}
	defer releaseInstallLock(lock)
	g, e := openGenerationOwner(id.DestinationRoot, scope, false)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer g.close()
	raw, _, found, e := generationOriginalRecord(g.dirs["preparations"], publicationBytesDigest([]byte(run))+".json", generationRecordLimit)
	if e != nil {
		return nil, e
	}
	var u ConfigPreparationUse
	if found {
		if generationDecode(raw, &u, generationRecordLimit) != nil || validatePreparationUse(u) != nil || u.RunID != run || u.OwnerScopeSHA256 != scope {
			return nil, generationInvalid()
		}
	} else {
		u, found, e = g.readOriginalUse(run)
		if e != nil {
			return nil, e
		}
	}
	if found && u.Ref != nil {
		if u.Authority == nil {
			return nil, ErrConfigPreparationStoreUnbound
		}
		copy := *u.Authority
		return &copy, g.validate()
	}
	return nil, g.validate()
}

// This exact request shape matches Coordinator's pre-normalization plan digest.
// Checking it prevents an internal caller from attaching an old pin digest to
// different dependency, budget, completion or artifact constraints.
func ConfigSubmissionPlanDigest(run string, tasks []ConfigGenerationTaskUse) string {
	type task struct {
		Diagnostics            *contract.DiagnosticsPolicy `json:"diagnostics,omitempty"`
		ExecutionPolicy        *contract.ExecutionPolicy   `json:"execution_policy,omitempty"`
		ID                     string                      `json:"id"`
		Dependencies           []string                    `json:"dependencies,omitempty"`
		MaxAttempts            int                         `json:"max_attempts"`
		WorkRevision           int                         `json:"work_revision,omitempty"`
		BudgetGroupID          string                      `json:"budget_group_id,omitempty"`
		MaxActiveMS            int64                       `json:"max_active_ms,omitempty"`
		AdapterPayload         json.RawMessage             `json:"adapter"`
		Fallbacks              []json.RawMessage           `json:"fallbacks,omitempty"`
		CompletionPolicy       string                      `json:"completion_policy,omitempty"`
		ExpectedArtifactSHA256 string                      `json:"expected_artifact_sha256,omitempty"`
	}
	if len(tasks) == 0 {
		return ""
	}
	revision := tasks[0].PlanRevision
	out := make([]task, len(tasks))
	for i, t := range tasks {
		if t.PlanRevision != revision {
			return ""
		}
		out[i] = task{t.Diagnostics, t.ExecutionPolicy, t.TaskID, t.Dependencies, t.MaxAttempts, t.WorkRevision, t.BudgetGroupID, t.MaxActiveMS, t.AdapterPayload, t.Fallbacks, t.CompletionPolicy, t.ExpectedArtifactSHA256}
	}
	raw, _ := json.Marshal(struct {
		RunID        string `json:"run_id"`
		PlanRevision int    `json:"plan_revision"`
		Tasks        []task `json:"tasks"`
	}{run, revision, out})
	return publicationBytesDigest(append([]byte("agent-bird-resolved-plan-v1\x00"), raw...))
}

type configUseAnchor struct {
	directory *os.File
	run, name string
	raw       []byte
	identity  publicationFileIdentity
}

func (g *generationOwner) anchorOriginalUse(u ConfigPreparationUse) (*configUseAnchor, error) {
	f, e := g.useDirectory(u.RunID, false)
	if e != nil {
		return nil, e
	}
	a := &configUseAnchor{directory: f, run: u.RunID, name: publicationBytesDigest([]byte(u.RunID)) + ".json"}
	a.raw, a.identity, e = publicationReadAt(f, a.name, generationRecordLimit)
	if e == nil {
		var got ConfigPreparationUse
		if generationDecode(a.raw, &got, generationRecordLimit) != nil || !reflect.DeepEqual(got, u) {
			e = ErrConfigGenerationConflict
		}
	}
	if e != nil {
		f.Close()
		return nil, e
	}
	if e = a.validate(g); e != nil {
		f.Close()
		return nil, e
	}
	return a, nil
}
func (a *configUseAnchor) validate(g *generationOwner) error {
	if e := g.validateUseDirectory(a.directory, a.run); e != nil {
		return e
	}
	body, id, e := publicationReadAt(a.directory, a.name, generationRecordLimit)
	if e != nil || id != a.identity || !bytes.Equal(body, a.raw) {
		return ErrConfigGenerationConflict
	}
	if _, _, e = publicationReadAt(a.directory, ".pending-"+a.name, generationRecordLimit); !errors.Is(e, os.ErrNotExist) {
		return ErrConfigGenerationPending
	}
	return nil
}
