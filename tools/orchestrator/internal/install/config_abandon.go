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

	"golang.org/x/sys/unix"
)

var ErrConfigPreparationAbandoned = errors.New("config_preparation_abandoned")
var ErrConfigAbandonIncomplete = errors.New("config_abandon_incomplete")
var ErrConfigAbandonConflict = errors.New("config_abandon_conflict")

type ConfigAbandonFence struct {
	SchemaVersion int                  `json:"schema_version"`
	Use           ConfigPreparationUse `json:"use"`
	UseSHA256     string               `json:"use_sha256"`
	CreatedAt     string               `json:"created_at"`
	ReceiptSHA256 string               `json:"receipt_sha256"`
}

func ConfigPreparationUseBytes(u ConfigPreparationUse) ([]byte, error) {
	if validatePreparationUse(u) != nil || u.SchemaVersion != 2 || u.Ref == nil || u.Authority == nil {
		return nil, ErrConfigPreparationStoreUnbound
	}
	raw, e := json.Marshal(u)
	if e != nil || len(raw) > generationRecordLimit {
		return nil, generationInvalid()
	}
	return raw, nil
}
func NewConfigAbandonFence(u ConfigPreparationUse, created string) (ConfigAbandonFence, error) {
	raw, e := ConfigPreparationUseBytes(u)
	if e != nil || created == "" || len(created) > 64 {
		return ConfigAbandonFence{}, ErrConfigAbandonConflict
	}
	// Neither current cleanup stage nor a mutable CPP/pin digest enters equality.
	f := ConfigAbandonFence{SchemaVersion: 1, Use: u, UseSHA256: publicationBytesDigest(raw), CreatedAt: created}
	body, _ := json.Marshal(f)
	f.ReceiptSHA256 = publicationBytesDigest(body)
	return f, nil
}
func (f ConfigAbandonFence) Validate() error {
	want, e := NewConfigAbandonFence(f.Use, f.CreatedAt)
	if e != nil || !reflect.DeepEqual(want, f) {
		return ErrConfigAbandonConflict
	}
	return nil
}

type ConfigAbandonReceipt struct {
	SchemaVersion  int    `json:"schema_version"`
	RunID          string `json:"run_id"`
	UseSHA256      string `json:"use_sha256"`
	FenceSHA256    string `json:"fence_sha256"`
	TerminalSHA256 string `json:"terminal_sha256"`
	Status         string `json:"status"`
}

type configAbandonTerminal struct {
	SchemaVersion    int    `json:"schema_version"`
	RunID            string `json:"run_id"`
	OwnerScopeSHA256 string `json:"owner_scope_sha256"`
	UseSHA256        string `json:"use_sha256"`
	FenceSHA256      string `json:"fence_sha256"`
	Decision         string `json:"decision"`
}
type configAbandonFile struct {
	raw []byte
	id  publicationFileIdentity
}

// The guard is private state, not an RPC validation boolean. It owns the install
// flock until Close; Store calls WithOriginalUse only after current inspection.
type ConfigAbandonGuard struct {
	mu               sync.Mutex
	lock             *os.File
	g                *generationOwner
	shard, pins      *os.File
	run, key         string
	use              ConfigPreparationUse
	hold             *configAbandonFile
	files            map[string]configAbandonFile
	ready, completed bool
	original         *ConfigAbandonFence
	receipt          ConfigAbandonReceipt
	history          *configUseAnchor
	records          map[string]configAbandonFile
}

func (a *ConfigAbandonGuard) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.history != nil {
		a.history.directory.Close()
		a.history = nil
	}
	if a.shard != nil {
		a.shard.Close()
		a.shard = nil
	}
	if a.pins != nil {
		a.pins.Close()
		a.pins = nil
	}
	if a.g != nil {
		a.g.close()
		a.g = nil
	}
	if a.lock != nil {
		releaseInstallLock(a.lock)
		a.lock = nil
	}
	a.ready = false
}
func (a *ConfigAbandonGuard) Completed() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.completed }
func (a *ConfigAbandonGuard) Use() ConfigPreparationUse {
	a.mu.Lock()
	defer a.mu.Unlock()
	raw, _ := json.Marshal(a.use)
	var u ConfigPreparationUse
	json.Unmarshal(raw, &u)
	return u
}
func (a *ConfigAbandonGuard) WithOriginalUse(fn func(ConfigPreparationUse) error) error {
	if a == nil {
		return ErrConfigAbandonIncomplete
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.ready || a.g == nil || a.validate() != nil {
		return ErrConfigAbandonIncomplete
	}
	raw, _ := json.Marshal(a.use)
	var copy ConfigPreparationUse
	if json.Unmarshal(raw, &copy) != nil {
		return ErrConfigAbandonConflict
	}
	if !a.completed {
		if e := a.validateWitness(); e != nil {
			return e
		}
	} else {
		terminal, complete := abandonRecords(*a.original)
		if e := a.validateRecords(terminal, complete); e != nil {
			return e
		}
	}
	return fn(copy)
}

func (g *generationOwner) abandonDirectory(run string, create bool) (*os.File, error) {
	if g.dirs["abandoned"] == nil {
		if !create {
			return nil, os.ErrNotExist
		}
		f, e := generationDirectory(g.owner, "abandoned", true)
		if e != nil {
			return nil, e
		}
		g.dirs["abandoned"] = f
	}
	key := publicationBytesDigest([]byte(run))
	f, e := generationDirectory(g.dirs["abandoned"], key[:2], create)
	if e == nil && (g.validate() != nil || publicationSamePath(f, filepath.Join(g.path, "owned-config", g.scope, "abandoned", key[:2])) != nil) {
		f.Close()
		return nil, ErrConfigAbandonConflict
	}
	return f, e
}
func (g *generationOwner) rejectAbandoned(run string) error {
	f, e := g.abandonDirectory(run, false)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	key := publicationBytesDigest([]byte(run))
	for _, name := range []string{key + ".terminal.json", ".pending-" + key + ".terminal.json", key + ".complete.json", ".pending-" + key + ".complete.json"} {
		_, _, e = publicationReadAt(f, name, generationRecordLimit)
		if !errors.Is(e, os.ErrNotExist) {
			return ErrConfigPreparationAbandoned
		}
	}
	return g.validate()
}

// CheckConfigPreparationNotAbandoned never selects active or verifies a retired
// version; it is only the protected terminal lookup used by private CPP entry.
func CheckConfigPreparationNotAbandoned(exe, scope, run string) error {
	if !idPattern.MatchString(run) {
		return generationInvalid()
	}
	id, e := runningPackageIdentity(exe)
	if e != nil {
		return e
	}
	lock, e := acquireInstallLock(id.DestinationRoot)
	if e != nil {
		return e
	}
	defer releaseInstallLock(lock)
	g, e := openGenerationOwner(id.DestinationRoot, scope, false)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	defer g.close()
	return g.rejectAbandoned(run)
}
func OpenConfigAbandonGuard(exe, scope, run, expected string, original *ConfigAbandonFence) (out *ConfigAbandonGuard, err error) {
	if !generationHash(scope) || !idPattern.MatchString(run) || expected != "" && !generationHash(expected) {
		return nil, ErrConfigAbandonConflict
	}
	id, e := runningPackageIdentity(exe)
	if e != nil {
		return nil, e
	}
	lock, e := acquireInstallLock(id.DestinationRoot)
	if e != nil {
		return nil, e
	}
	a := &ConfigAbandonGuard{lock: lock, run: run, key: publicationBytesDigest([]byte(run)), files: map[string]configAbandonFile{}, records: map[string]configAbandonFile{}}
	defer func() {
		if err != nil {
			a.Close()
		}
	}()
	a.g, e = openGenerationOwner(id.DestinationRoot, scope, false)
	if e != nil {
		return nil, e
	}
	if e = a.g.quiescent(""); e != nil {
		return nil, e
	}
	if original != nil {
		if original.Validate() != nil || original.Use.RunID != run || original.Use.OwnerScopeSHA256 != scope || original.Use.Package.DestinationRoot != id.DestinationRoot {
			return nil, ErrConfigAbandonConflict
		}
		copy := *original
		a.original = &copy
		a.use = copy.Use
	} else {
		raw, _, exists, e := generationOriginalRecord(a.g.dirs["preparations"], a.key+".json", generationRecordLimit)
		if e != nil {
			return nil, e
		}
		if exists {
			if generationDecode(raw, &a.use, generationRecordLimit) != nil || validatePreparationUse(a.use) != nil {
				return nil, ErrConfigAbandonConflict
			}
			// A fully verified hold is the only source allowed to recover absent history.
			if a.use.Ref == nil || a.use.Authority == nil {
				return nil, ErrConfigPreparationStoreUnbound
			}
		} else {
			var found bool
			a.use, found, e = a.g.readOriginalUse(run)
			if e != nil {
				return nil, e
			}
			if !found {
				return nil, ErrConfigAbandonIncomplete
			}
		}
	}
	raw, e := ConfigPreparationUseBytes(a.use)
	if e != nil {
		return nil, e
	}
	if a.use.RunID != run || a.use.OwnerScopeSHA256 != scope || a.use.Package.DestinationRoot != id.DestinationRoot || expected != "" && expected != publicationBytesDigest(raw) {
		return nil, ErrConfigAbandonConflict
	}
	a.shard, e = a.g.abandonDirectory(run, false)
	if errors.Is(e, os.ErrNotExist) {
		a.shard = nil
	} else if e != nil {
		return nil, e
	}
	if original != nil && a.shard != nil {
		terminal, want := abandonRecords(*original)
		complete, completeID, exists, e := generationOriginalRecord(a.shard, a.key+".complete.json", generationRecordLimit)
		if e != nil {
			return nil, e
		}
		if exists {
			term, terminalID, ok, e := generationOriginalRecord(a.shard, a.key+".terminal.json", generationRecordLimit)
			if e != nil || !ok || !bytes.Equal(term, terminal) || !bytes.Equal(complete, want) {
				return nil, ErrConfigAbandonConflict
			}
			// Pending-only completion is not yet durable final success.
			if _, _, e = publicationReadAt(a.shard, a.key+".complete.json", generationRecordLimit); e == nil {
				a.records[a.key+".terminal.json"] = configAbandonFile{term, terminalID}
				a.records[a.key+".complete.json"] = configAbandonFile{complete, completeID}
				if e = a.validateRecords(terminal, want); e != nil {
					return nil, e
				}
				json.Unmarshal(complete, &a.receipt)
				a.completed = true
				a.ready = true
				return a, a.validate()
			}
		}
	}
	// First fence admission requires the actual original verified package. A
	// committed fence's recovery uses its immutable proof, never new authority.
	if original == nil {
		pkg, e := verifiedGenerationPackage(exe)
		if e != nil || pkg != a.use.Package {
			return nil, ErrConfigAbandonConflict
		}
	}
	return a, a.validate()
}
func (a *ConfigAbandonGuard) validate() error {
	if a.g == nil || a.g.validate() != nil {
		return ErrConfigAbandonConflict
	}
	if a.shard == nil {
		return nil
	}
	if publicationSamePath(a.shard, filepath.Join(a.g.path, "owned-config", a.g.scope, "abandoned", a.key[:2])) != nil {
		return ErrConfigAbandonConflict
	}
	if info, e := a.shard.Stat(); e != nil {
		return e
	} else if id, e := publicationDirectory(info); e != nil || id.Mode != 0700 {
		return ErrConfigAbandonConflict
	}
	return nil
}

// InspectCleanup reads the current immutable CPP via the authenticated receiver
// callback. It does not freeze that mutable progress in the Store fence.
func (a *ConfigAbandonGuard) InspectCleanup(readCPP func(ConfigPreparationUse) (string, bool, error)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.completed {
		return nil
	}
	if a.validate() != nil {
		return ErrConfigAbandonConflict
	}
	name := a.key + ".json"
	raw, id, held, e := generationOriginalRecord(a.g.dirs["preparations"], name, generationRecordLimit)
	if e != nil {
		return e
	}
	if held {
		var u ConfigPreparationUse
		if generationDecode(raw, &u, generationRecordLimit) != nil || !reflect.DeepEqual(u, a.use) {
			return ErrConfigAbandonConflict
		}
		a.hold = &configAbandonFile{raw, id}
	}
	history, found, e := a.g.readOriginalUse(a.run)
	if e != nil {
		return e
	}
	if !found {
		if !held {
			return ErrConfigAbandonIncomplete
		}
		if e = a.g.persistOriginalUse(a.use); e != nil {
			return e
		}
		history, found, e = a.g.readOriginalUse(a.run)
	}
	if e != nil || !found || !reflect.DeepEqual(history, a.use) {
		return ErrConfigAbandonConflict
	}
	a.history, e = a.g.anchorOriginalUse(a.use)
	if e != nil {
		return e
	}
	if a.shard == nil {
		a.shard, e = a.g.abandonDirectory(a.run, true)
		if e != nil {
			return e
		}
	}

	digest, cpp, e := readCPP(a.use)
	if e != nil {
		return e
	}
	if cpp && !generationHash(digest) {
		return ErrConfigAbandonConflict
	}
	a.pins, e = generationDirectory(a.g.root, "pins", false)
	if e != nil {
		return e
	}
	for _, task := range a.use.TaskIDs {
		name := task + ".json"
		raw, id, e := publicationReadAt(a.pins, name, generationRecordLimit)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		p, e := generationDecodePinRecord(raw)
		if e != nil || !cpp || p.SchemaVersion != 3 || p.TaskID != task || p.RunID != a.run || p.Version != a.use.Package.Version || p.ConfigGeneration == nil || *p.ConfigGeneration != *a.use.Ref || p.Authority == nil || *p.Authority != *a.use.Authority || p.PreparationSHA256 != digest {
			return ErrConfigAbandonConflict
		}
		a.files[name] = configAbandonFile{raw, id}
	}
	// Once terminal is durable, absent references are idempotent deletion, not
	// evidence for the first never-admitted decision.
	_, _, terminal, e := generationOriginalRecord(a.shard, a.key+".terminal.json", generationRecordLimit)
	if e != nil {
		return e
	}
	if !held && !terminal && (!cpp || len(a.files) != len(a.use.TaskIDs)) {
		return ErrConfigAbandonIncomplete
	}
	a.ready = true
	return a.validateWitness()
}
func (a *ConfigAbandonGuard) validateWitness() error {
	if e := a.validate(); e != nil {
		return e
	}
	if a.pins == nil || publicationSamePath(a.pins, filepath.Join(a.g.path, "pins")) != nil {
		return ErrConfigAbandonConflict
	}
	if a.history == nil {
		return ErrConfigAbandonIncomplete
	}
	if e := a.history.validate(a.g); e != nil {
		return e
	}
	if e := a.validateTrackedRecords(); e != nil {
		return e
	}
	if a.hold != nil {
		raw, id, exists, e := generationOriginalRecord(a.g.dirs["preparations"], a.key+".json", generationRecordLimit)
		if e != nil || !exists || id != a.hold.id || !bytes.Equal(raw, a.hold.raw) {
			return ErrConfigAbandonConflict
		}
	}
	for name, original := range a.files {
		raw, id, e := publicationReadAt(a.pins, name, generationRecordLimit)
		if e != nil || id != original.id || !bytes.Equal(raw, original.raw) {
			return ErrConfigAbandonConflict
		}
	}
	return a.history.validate(a.g)
}
func abandonRecords(f ConfigAbandonFence) ([]byte, []byte) {
	terminal, _ := json.Marshal(configAbandonTerminal{1, f.Use.RunID, f.Use.OwnerScopeSHA256, f.UseSHA256, f.ReceiptSHA256, "abandoned"})
	terminal = append(terminal, '\n')
	complete, _ := json.Marshal(ConfigAbandonReceipt{1, f.Use.RunID, f.UseSHA256, f.ReceiptSHA256, publicationBytesDigest(terminal), "abandoned"})
	return terminal, append(complete, '\n')
}
func (a *ConfigAbandonGuard) Finish(f ConfigAbandonFence) (ConfigAbandonReceipt, error) {
	r, e := a.finish(f, nil)
	if e != nil {
		return ConfigAbandonReceipt{}, e
	}
	return r, nil
}
func (a *ConfigAbandonGuard) finish(f ConfigAbandonFence, checkpoint func(string) error) (ConfigAbandonReceipt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var receipt ConfigAbandonReceipt
	if f.Validate() != nil || !reflect.DeepEqual(f.Use, a.use) || !a.ready || a.validate() != nil {
		return receipt, ErrConfigAbandonConflict
	}
	terminal, complete := abandonRecords(f)
	json.Unmarshal(complete, &receipt)
	if a.completed {
		if a.receipt != receipt {
			return receipt, ErrConfigAbandonConflict
		}
		terminal, complete := abandonRecords(f)
		return receipt, a.validateRecords(terminal, complete)
	}
	if e := a.validateWitness(); e != nil {
		return receipt, e
	}
	if e := generationCheck(checkpoint, "before-terminal"); e != nil {
		return receipt, e
	}
	if e := a.g.writeOriginalRecord(a.shard, a.key+".terminal.json", terminal, generationRecordLimit, false); e != nil {
		return receipt, e
	}
	if e := a.trackRecord(a.key+".terminal.json", terminal); e != nil {
		return receipt, e
	}
	if e := generationCheck(checkpoint, "terminal"); e != nil {
		return receipt, e
	}
	if e := a.validateWitness(); e != nil {
		return receipt, e
	}
	if a.hold != nil {
		// Recover an exact complete pending hold, then compare its original identity.
		name := a.key + ".json"
		if e := a.g.writeRecord(a.g.dirs["preparations"], name, a.hold.raw, generationRecordLimit); e != nil {
			return receipt, e
		}
		if e := a.validateWitness(); e != nil {
			return receipt, e
		}
		if e := unix.Unlinkat(int(a.g.dirs["preparations"].Fd()), name, 0); e != nil {
			return receipt, e
		}
		if e := a.g.dirs["preparations"].Sync(); e != nil {
			return receipt, e
		}
		a.hold = nil
		if e := generationCheck(checkpoint, "hold-removed"); e != nil {
			return receipt, e
		}
	}
	for _, task := range a.use.TaskIDs {
		name := task + ".json"
		if _, ok := a.files[name]; !ok {
			continue
		}
		if e := a.validateWitness(); e != nil {
			return receipt, e
		}
		if e := unix.Unlinkat(int(a.pins.Fd()), name, 0); e != nil {
			return receipt, e
		}
		if e := a.pins.Sync(); e != nil {
			return receipt, e
		}
		delete(a.files, name)
		if e := generationCheck(checkpoint, "pin-removed:"+task); e != nil {
			return receipt, e
		}
	}
	// Check every original target again before declaring completion. No writer
	// respecting flock can recreate it after the durable terminal.
	if e := a.validateReferenceAbsence(); e != nil {
		return receipt, e
	}
	if e := a.pins.Sync(); e != nil {
		return receipt, e
	}
	if e := a.g.dirs["preparations"].Sync(); e != nil {
		return receipt, e
	}
	if e := generationCheck(checkpoint, "before-complete"); e != nil {
		return receipt, e
	}
	if e := a.validateReferenceAbsence(); e != nil {
		return receipt, e
	}
	if e := a.g.writeOriginalRecord(a.shard, a.key+".complete.json", complete, generationRecordLimit, false); e != nil {
		return receipt, e
	}
	if e := a.trackRecord(a.key+".complete.json", complete); e != nil {
		return receipt, e
	}
	if e := generationCheck(checkpoint, "complete"); e != nil {
		return receipt, e
	}
	if e := a.validateReferenceAbsence(); e != nil {
		return receipt, e
	}
	if e := a.validateRecords(terminal, complete); e != nil {
		return receipt, e
	}
	a.completed = true
	copy := f
	a.original = &copy
	a.receipt = receipt
	return receipt, nil
}

// Only a new completion depends on the retained reference witness. Exact
// completed replay intentionally does not reopen deleted pins/CPP/history.
func (a *ConfigAbandonGuard) validateReferenceAbsence() error {
	if e := a.validateWitness(); e != nil {
		return e
	}
	if _, _, exists, e := generationOriginalRecord(a.g.dirs["preparations"], a.key+".json", generationRecordLimit); e != nil || exists {
		return ErrConfigAbandonIncomplete
	}
	for _, task := range a.use.TaskIDs {
		if _, _, e := publicationReadAt(a.pins, task+".json", generationRecordLimit); !errors.Is(e, os.ErrNotExist) {
			return ErrConfigAbandonIncomplete
		}
	}
	return a.validateWitness()
}
func (a *ConfigAbandonGuard) validateRecords(terminal, complete []byte) error {
	if e := a.validate(); e != nil {
		return e
	}
	if e := a.validateTrackedRecords(); e != nil {
		return e
	}
	for name, want := range map[string][]byte{a.key + ".terminal.json": terminal, a.key + ".complete.json": complete} {
		raw, _, e := publicationReadAt(a.shard, name, generationRecordLimit)
		if e != nil || !bytes.Equal(raw, want) {
			return ErrConfigAbandonConflict
		}
		// Never conceal a competing/partial same-name publication.
		if _, _, e = publicationReadAt(a.shard, ".pending-"+name, generationRecordLimit); !errors.Is(e, os.ErrNotExist) {
			return ErrConfigAbandonConflict
		}
	}
	return a.validate()
}

func (a *ConfigAbandonGuard) trackRecord(name string, want []byte) error {
	raw, id, e := publicationReadAt(a.shard, name, generationRecordLimit)
	if e != nil || !bytes.Equal(raw, want) {
		return ErrConfigAbandonConflict
	}
	if old, ok := a.records[name]; ok && (old.id != id || !bytes.Equal(old.raw, raw)) {
		return ErrConfigAbandonConflict
	}
	a.records[name] = configAbandonFile{raw, id}
	return a.validate()
}
func (a *ConfigAbandonGuard) validateTrackedRecords() error {
	for name, old := range a.records {
		raw, id, e := publicationReadAt(a.shard, name, generationRecordLimit)
		if e != nil || id != old.id || !bytes.Equal(raw, old.raw) {
			return ErrConfigAbandonConflict
		}
	}
	return a.validate()
}
