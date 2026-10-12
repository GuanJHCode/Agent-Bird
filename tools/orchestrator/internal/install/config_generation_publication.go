//go:build !windows

package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"golang.org/x/sys/unix"
)

type configGenerationIntent struct {
	SchemaVersion    int                           `json:"schema_version"`
	OwnerScopeSHA256 string                        `json:"owner_scope_sha256"`
	Package          ConfigGenerationPackage       `json:"package"`
	Request          ConfigGenerationOperation     `json:"request"`
	RequestSHA256    string                        `json:"request_sha256"`
	Manifest         *ConfigGenerationManifest     `json:"manifest,omitempty"`
	Ref              *contract.ConfigGenerationRef `json:"ref,omitempty"`
	PreviousIdentity *publicationFileIdentity      `json:"previous_identity,omitempty"`
	PreviousSHA256   string                        `json:"previous_sha256,omitempty"`
	Next             *ConfigGenerationActive       `json:"next,omitempty"`
}

func configOperationHash(scope string, pkg ConfigGenerationPackage, r ConfigGenerationOperation) string {
	raw, _ := json.Marshal(struct {
		Scope   string
		Package ConfigGenerationPackage
		Request ConfigGenerationOperation
	}{scope, pkg, r})
	return publicationBytesDigest(append([]byte("agent-bird-config-request-v1\x00"), raw...))
}

func validConfigOperation(scope string, r ConfigGenerationOperation) bool {
	if !idPattern.MatchString(r.RequestID) || r.Expected != nil && (r.Expected.Validate() != nil || r.Expected.Ref != nil && r.Expected.Ref.OwnerScopeSHA256 != scope) {
		return false
	}
	switch r.Action {
	case "publish":
		return r.SavedDefaults != nil && r.SavedDefaults.Validate() == nil && r.Ref == nil
	case "deactivate":
		return r.SavedDefaults == nil && r.Ref == nil
	case "retire":
		return r.SavedDefaults == nil && r.Ref != nil && r.Ref.Validate() == nil && r.Ref.OwnerScopeSHA256 == scope
	}
	return false
}

// One immutable pending intent is the entire live publication journal. The
// completed history is not scanned and cannot consume publication capacity.
func (g *generationOwner) pendingIntent() (configGenerationIntent, []byte, bool, error) {
	raw, _, exists, err := generationOriginalRecord(g.owner, "pending.json", generationRecordLimit)
	var intent configGenerationIntent
	if err != nil || !exists {
		return intent, raw, exists, err
	}
	if generationDecode(raw, &intent, generationRecordLimit) != nil {
		return intent, raw, true, generationInvalid()
	}
	key := publicationBytesDigest([]byte(intent.Request.RequestID))
	if g.validIntent(intent, key) != nil {
		return intent, raw, true, generationInvalid()
	}
	return intent, raw, true, nil
}

func (g *generationOwner) quiescent(except string) error {
	intent, _, exists, err := g.pendingIntent()
	if err != nil {
		return err
	}
	key := ""
	if exists {
		key = publicationBytesDigest([]byte(intent.Request.RequestID))
		if key != except {
			return ErrConfigGenerationPending
		}
	}
	entries, err := generationNames(g.owner, 12)
	if err != nil {
		return err
	}
	for _, name := range entries {
		known := name == "active.json"
		for _, d := range generationDirectories {
			known = known || name == d
		}
		if exists {
			known = known || name == "pending.json" || name == ".pending-pending.json"
			if intent.Next != nil {
				known = known || name == ".active-"+key || name == "stage.json" || name == ".pending-stage.json"
			}
		}
		if !known {
			return ErrConfigGenerationPending
		}
	}
	return nil
}

type configGenerationHistory struct {
	SchemaVersion int                     `json:"schema_version"`
	Intent        configGenerationIntent  `json:"intent"`
	Receipt       ConfigGenerationReceipt `json:"receipt"`
	Stage         *configGenerationStage  `json:"stage,omitempty"`
}

type configHistoryAnchor struct {
	directory *os.File
	key, name string
	raw       []byte
	identity  publicationFileIdentity
}

func (a *configHistoryAnchor) validate(g *generationOwner) error {
	if a == nil || g.validate() != nil || publicationSamePath(a.directory, filepath.Join(g.path, "owned-config", g.scope, "requests", a.key[:2])) != nil {
		return ErrConfigGenerationConflict
	}
	if _, _, _, err := generationOriginalRecord(a.directory, a.key+".json", generationRecordLimit); err != nil {
		return err
	}
	raw, id, err := publicationReadAt(a.directory, a.name, generationRecordLimit)
	if err != nil || id != a.identity || !bytes.Equal(raw, a.raw) {
		return ErrConfigGenerationConflict
	}
	return g.validate()
}

func (g *generationOwner) readHistory(key string) (configGenerationHistory, *configHistoryAnchor, bool, error) {
	var history configGenerationHistory
	dir, err := g.historyDirectory(key, false)
	if errors.Is(err, os.ErrNotExist) {
		return history, nil, false, nil
	}
	if err != nil {
		return history, nil, false, err
	}
	raw, id, exists, err := generationOriginalRecord(dir, key+".json", generationRecordLimit)
	if err != nil || !exists {
		dir.Close()
		return history, nil, false, err
	}
	if generationDecode(raw, &history, generationRecordLimit) != nil || history.SchemaVersion != 1 || g.validIntent(history.Intent, key) != nil || !reflect.DeepEqual(history.Receipt, configIntentReceipt(history.Intent)) || (history.Intent.Next == nil) != (history.Stage == nil) || history.Stage != nil && (history.Stage.RequestSHA256 != history.Intent.RequestSHA256 || !validPublicationManifest(history.Stage.Identity)) {
		dir.Close()
		return history, nil, false, generationInvalid()
	}
	name := key + ".json"
	_, _, err = publicationReadAt(dir, name, generationRecordLimit)
	committed := err == nil
	if errors.Is(err, os.ErrNotExist) {
		name = ".pending-" + name
	} else if err != nil {
		dir.Close()
		return history, nil, false, err
	}
	anchor := &configHistoryAnchor{dir, key, name, raw, id}
	if err = anchor.validate(g); err != nil {
		dir.Close()
		return history, nil, false, err
	}
	return history, anchor, committed, nil
}

func configIntentReceipt(i configGenerationIntent) ConfigGenerationReceipt {
	return ConfigGenerationReceipt{SchemaVersion: 1, RequestSHA256: i.RequestSHA256, Action: i.Request.Action, Active: i.Next, Ref: i.Ref}
}

func (g *generationOwner) validIntent(i configGenerationIntent, key string) error {
	if i.SchemaVersion != 1 || i.OwnerScopeSHA256 != g.scope || i.Package.DestinationRoot != g.path || !validConfigOperation(g.scope, i.Request) || publicationBytesDigest([]byte(i.Request.RequestID)) != key || i.RequestSHA256 != configOperationHash(g.scope, i.Package, i.Request) {
		return generationInvalid()
	}
	if (i.Request.Expected == nil) != (i.PreviousIdentity == nil) || i.PreviousIdentity != nil && (!validPublicationManifest(*i.PreviousIdentity) || !generationHash(i.PreviousSHA256)) {
		return generationInvalid()
	}
	if i.Request.Action == "publish" {
		if i.Manifest == nil || i.Ref == nil {
			return generationInvalid()
		}
		_, ref, err := generationManifestBytes(*i.Manifest)
		if err != nil || ref != *i.Ref || i.Manifest.OwnerScopeSHA256 != g.scope || i.Manifest.PackageVersion != i.Package.Version || i.Manifest.InstalledManifestSHA256 != i.Package.InstalledManifestSHA256 || i.Manifest.PayloadManifestSHA256 != i.Package.PayloadManifestSHA256 || !reflect.DeepEqual(i.Manifest.SavedDefaults, *i.Request.SavedDefaults) {
			return generationInvalid()
		}
	} else if i.Manifest != nil {
		return generationInvalid()
	}
	if i.Request.Action == "retire" {
		if i.Next != nil || i.Ref == nil || *i.Ref != *i.Request.Ref {
			return generationInvalid()
		}
		return nil
	}
	want := uint64(1)
	if i.Request.Expected != nil {
		want = i.Request.Expected.Revision + 1
	}
	if i.Next == nil || i.Next.Validate() != nil || i.Next.Revision != want {
		return generationInvalid()
	}
	if i.Request.Action == "publish" && (i.Next.State != "selected" || i.Next.Ref == nil || *i.Next.Ref != *i.Ref) {
		return generationInvalid()
	}
	if i.Request.Action == "deactivate" && (i.Next.State != "inactive" || i.Ref != nil) {
		return generationInvalid()
	}
	return nil
}

func (g *generationOwner) loadManifest(ref contract.ConfigGenerationRef) (ConfigGenerationManifest, error) {
	var value ConfigGenerationManifest
	if ref.Validate() != nil || ref.OwnerScopeSHA256 != g.scope {
		return value, generationInvalid()
	}
	raw, _, err := publicationReadAt(g.dirs["generations"], ref.GenerationID+".json", generationManifestLimit)
	if err != nil || generationDecode(raw, &value, generationManifestLimit) != nil {
		return value, generationInvalid()
	}
	canonical, want, err := generationManifestBytes(value)
	if err != nil || want != ref || !bytes.Equal(raw, canonical) {
		return value, generationInvalid()
	}
	return value, g.validate()
}

func generationCheck(checkpoint func(string) error, point string) error {
	if checkpoint != nil {
		return checkpoint(point)
	}
	return nil
}

// Before a publication has its stage seal, the saved active identity is the
// first mutation barrier. A restarted intent may need to restore its manifest,
// but it must not create that manifest after a foreign replacement of active.
// Once a stage exists, commitActive validates the new sealed identity instead.
func (g *generationOwner) verifyUnstagedPublicationActive(i configGenerationIntent) error {
	if i.Next == nil {
		return nil
	}
	_, _, staged, err := generationOriginalRecord(g.owner, "stage.json", generationRecordLimit)
	if err != nil || staged {
		return err
	}
	body, identity, err := publicationReadAt(g.owner, "active.json", generationRecordLimit)
	if i.PreviousIdentity == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return ErrConfigGenerationConflict
		}
		return g.validate()
	}
	if err != nil || identity != *i.PreviousIdentity || publicationBytesDigest(body) != i.PreviousSHA256 {
		return ErrConfigGenerationConflict
	}
	return g.validate()
}

func updateConfigGenerationImpl(executable, scope string, r ConfigGenerationOperation, checkpoint func(string) error) (ConfigGenerationReceipt, error) {
	if !generationHash(scope) || !validConfigOperation(scope, r) {
		return ConfigGenerationReceipt{}, generationInvalid()
	}
	id, err := runningPackageIdentity(executable)
	if err != nil {
		return ConfigGenerationReceipt{}, err
	}
	lock, err := acquireInstallLock(id.DestinationRoot)
	if err != nil {
		return ConfigGenerationReceipt{}, err
	}
	defer releaseInstallLock(lock)
	pkg, err := verifiedGenerationPackage(executable)
	if err != nil {
		return ConfigGenerationReceipt{}, err
	}
	// An existing owner must be validated without initializing optional
	// directories. In particular, a rejected replay must not turn a legacy
	// owner without uses/abandoned into a different tree before its immutable
	// pending intent and active identity have been checked.
	g, err := openGenerationOwner(pkg.DestinationRoot, scope, false)
	if errors.Is(err, os.ErrNotExist) {
		exists, existsErr := generationOwnerExists(pkg.DestinationRoot, scope)
		if existsErr != nil {
			return ConfigGenerationReceipt{}, existsErr
		}
		if !exists {
			g, err = openGenerationOwner(pkg.DestinationRoot, scope, true)
		}
	}
	if err != nil {
		return ConfigGenerationReceipt{}, err
	}
	defer g.close()
	key := publicationBytesDigest([]byte(r.RequestID))
	// An old committed request returns only its original receipt, even if a
	// different current publication is pending. It does not renew authority.
	history, originalHistory, committed, err := g.readHistory(key)
	exists := originalHistory != nil
	if originalHistory != nil {
		defer originalHistory.directory.Close()
	}
	if err != nil {
		return ConfigGenerationReceipt{}, err
	}
	if exists && history.Intent.RequestSHA256 != configOperationHash(scope, pkg, r) {
		return ConfigGenerationReceipt{}, ErrConfigGenerationConflict
	}
	if committed {
		intent, _, pending, e := g.pendingIntent()
		if e != nil {
			return ConfigGenerationReceipt{}, e
		}
		if pending && publicationBytesDigest([]byte(intent.Request.RequestID)) == key {
			if !reflect.DeepEqual(intent, history.Intent) {
				return ConfigGenerationReceipt{}, ErrConfigGenerationConflict
			}
			if err = g.quiescent(key); err != nil {
				return ConfigGenerationReceipt{}, err
			}
			if err = originalHistory.validate(g); err != nil {
				return ConfigGenerationReceipt{}, err
			}
			if err = g.clearPublication(intent, history.Stage); err != nil {
				return ConfigGenerationReceipt{}, err
			}
		}
		return history.Receipt, originalHistory.validate(g)
	}
	if err = g.quiescent(key); err != nil {
		return ConfigGenerationReceipt{}, err
	}
	intent, raw, pending, err := g.pendingIntent()
	if err != nil {
		return ConfigGenerationReceipt{}, err
	}
	if pending {
		if intent.RequestSHA256 != configOperationHash(scope, pkg, r) {
			return ConfigGenerationReceipt{}, ErrConfigGenerationConflict
		}
	} else {
		if exists {
			return ConfigGenerationReceipt{}, ErrConfigGenerationPending
		}
		current, e := generationReadActive(g.owner, scope)
		if e != nil || !reflect.DeepEqual(current, r.Expected) {
			return ConfigGenerationReceipt{}, ErrConfigGenerationConflict
		}
		intent = configGenerationIntent{SchemaVersion: 1, OwnerScopeSHA256: scope, Package: pkg, Request: r, RequestSHA256: configOperationHash(scope, pkg, r)}
		if current != nil {
			body, identity, e := publicationReadAt(g.owner, "active.json", generationRecordLimit)
			if e != nil {
				return ConfigGenerationReceipt{}, e
			}
			intent.PreviousIdentity = &identity
			intent.PreviousSHA256 = publicationBytesDigest(body)
		}
		if r.Action == "retire" {
			intent.Ref = r.Ref
			if _, err = g.loadManifest(*r.Ref); err != nil {
				return ConfigGenerationReceipt{}, err
			}
			if err = g.checkReferences(r.Ref.GenerationID, ""); err != nil {
				return ConfigGenerationReceipt{}, err
			}
		} else {
			revision := uint64(1)
			if current != nil {
				if current.Revision >= math.MaxInt64 {
					return ConfigGenerationReceipt{}, generationInvalid()
				}
				revision = current.Revision + 1
			}
			intent.Next = &ConfigGenerationActive{SchemaVersion: 1, Revision: revision, State: "inactive"}
			if r.Action == "publish" {
				m := ConfigGenerationManifest{1, scope, pkg.Version, pkg.InstalledManifestSHA256, pkg.PayloadManifestSHA256, *r.SavedDefaults}
				_, ref, e := generationManifestBytes(m)
				if e != nil {
					return ConfigGenerationReceipt{}, e
				}
				intent.Manifest = &m
				intent.Ref = &ref
				intent.Next.State = "selected"
				intent.Next.Ref = &ref
				// Capacity admission is before the new intent, stage, or history shard.
				if err = g.admitGeneration(ref); err != nil {
					return ConfigGenerationReceipt{}, err
				}
			}
		}
		raw, _ = json.Marshal(intent)
		raw = append(raw, '\n')
		if err = g.validIntent(intent, key); err != nil {
			return ConfigGenerationReceipt{}, err
		}
		complete, _ := json.Marshal(configGenerationHistory{SchemaVersion: 1, Intent: intent, Receipt: configIntentReceipt(intent)})
		// Bound the future serialized stage identity before creating any intent.
		extra := 0
		if intent.Next != nil {
			extra = 512
		}
		if len(complete)+extra+1 > generationRecordLimit {
			return ConfigGenerationReceipt{}, generationInvalid()
		}
	}
	if err = g.writeOriginalRecord(g.owner, "pending.json", raw, generationRecordLimit, false); err != nil {
		return ConfigGenerationReceipt{}, err
	}
	if err = generationCheck(checkpoint, "intent"); err != nil {
		return ConfigGenerationReceipt{}, err
	}
	if err = g.verifyUnstagedPublicationActive(intent); err != nil {
		return ConfigGenerationReceipt{}, err
	}
	if r.Action == "publish" {
		body, _, _ := generationManifestBytes(*intent.Manifest)
		if err = g.writeRecord(g.dirs["generations"], intent.Ref.GenerationID+".json", body, generationManifestLimit); err != nil {
			return ConfigGenerationReceipt{}, err
		}
		if err = generationCheck(checkpoint, "manifest"); err != nil {
			return ConfigGenerationReceipt{}, err
		}
	}
	if r.Action == "retire" {
		if err = g.checkReferences(r.Ref.GenerationID, ""); err != nil {
			return ConfigGenerationReceipt{}, err
		}
		body, _, e := publicationReadAt(g.dirs["generations"], r.Ref.GenerationID+".json", generationManifestLimit)
		if e == nil {
			m, e := g.loadManifest(*r.Ref)
			if e != nil {
				return ConfigGenerationReceipt{}, e
			}
			want, _, _ := generationManifestBytes(m)
			if !bytes.Equal(want, body) {
				return ConfigGenerationReceipt{}, generationInvalid()
			}
			if err = g.removeRecord(g.dirs["generations"], r.Ref.GenerationID+".json", body, generationManifestLimit); err != nil {
				return ConfigGenerationReceipt{}, err
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return ConfigGenerationReceipt{}, e
		}
		if err = generationCheck(checkpoint, "retired"); err != nil {
			return ConfigGenerationReceipt{}, err
		}
	} else {
		if err = g.commitActive(intent, key); err != nil {
			return ConfigGenerationReceipt{}, err
		}
		if err = generationCheck(checkpoint, "active"); err != nil {
			return ConfigGenerationReceipt{}, err
		}
	}
	receipt := configIntentReceipt(intent)
	var stage *configGenerationStage
	if intent.Next != nil {
		stageRaw, _, e := publicationReadAt(g.owner, "stage.json", generationRecordLimit)
		stage = &configGenerationStage{}
		if e != nil || generationDecode(stageRaw, stage, generationRecordLimit) != nil || stage.RequestSHA256 != intent.RequestSHA256 || !validPublicationManifest(stage.Identity) {
			return ConfigGenerationReceipt{}, generationInvalid()
		}
	}
	body, _ := json.Marshal(configGenerationHistory{SchemaVersion: 1, Intent: intent, Receipt: receipt, Stage: stage})
	body = append(body, '\n')
	dir, err := g.historyDirectory(key, true)
	if err != nil {
		return ConfigGenerationReceipt{}, err
	}
	defer dir.Close()
	if err = g.writeOriginalRecord(dir, key+".json", body, generationRecordLimit, false); err != nil {
		return ConfigGenerationReceipt{}, err
	}
	_, receiptID, err := publicationReadAt(dir, key+".json", generationRecordLimit)
	if err != nil {
		return ConfigGenerationReceipt{}, err
	}
	anchor := &configHistoryAnchor{dir, key, key + ".json", body, receiptID}
	if err = generationCheck(checkpoint, "receipt"); err != nil {
		return ConfigGenerationReceipt{}, err
	}
	if err = anchor.validate(g); err != nil {
		return ConfigGenerationReceipt{}, err
	}
	if err = g.clearPublication(intent, stage); err != nil {
		return ConfigGenerationReceipt{}, err
	}
	return receipt, anchor.validate(g)
}

func (g *generationOwner) admitGeneration(ref contract.ConfigGenerationRef) error {
	names, err := generationRecordNames(g.dirs["generations"])
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, name := range names {
		base := name
		if len(name) > 9 && name[:9] == ".pending-" {
			base = name[9:]
		}
		if seen[base] {
			return ErrConfigGenerationPending
		}
		seen[base] = true
		if base != name {
			return ErrConfigGenerationPending
		}
	}
	name := ref.GenerationID + ".json"
	if seen[name] {
		_, err = g.loadManifest(ref)
		return err
	}
	if len(seen) >= generationCapacity {
		return errors.New("config_generation_capacity")
	}
	return nil
}

type configGenerationStage struct {
	RequestSHA256 string                  `json:"request_sha256"`
	Identity      publicationFileIdentity `json:"identity"`
}

func (g *generationOwner) commitActive(i configGenerationIntent, key string) error {
	want, _ := json.Marshal(i.Next)
	want = append(want, '\n')
	raw, _, sealed, err := generationOriginalRecord(g.owner, "stage.json", generationRecordLimit)
	if err != nil {
		return err
	}
	var stage configGenerationStage
	if sealed {
		if generationDecode(raw, &stage, generationRecordLimit) != nil || stage.RequestSHA256 != i.RequestSHA256 || !validPublicationManifest(stage.Identity) {
			return generationInvalid()
		}
		if err = g.writeOriginalRecord(g.owner, "stage.json", raw, generationRecordLimit, false); err != nil {
			return err
		}
	}
	body, id, err := publicationReadAt(g.owner, "active.json", generationRecordLimit)
	if sealed && err == nil && id == stage.Identity && bytes.Equal(body, want) {
		return nil
	}
	if i.PreviousIdentity == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return ErrConfigGenerationConflict
		}
	} else if err != nil || id != *i.PreviousIdentity || publicationBytesDigest(body) != i.PreviousSHA256 {
		return ErrConfigGenerationConflict
	}
	name := ".active-" + key
	body, id, err = publicationReadAt(g.owner, name, generationRecordLimit)
	if errors.Is(err, os.ErrNotExist) && !sealed {
		id, err = publicationWriteAt(g.owner, name, want)
		if err == nil {
			err = g.owner.Sync()
		}
		body = want
	}
	if err != nil || !bytes.Equal(body, want) || g.validate() != nil {
		return ErrConfigGenerationConflict
	}
	if !sealed {
		stage = configGenerationStage{i.RequestSHA256, id}
		raw, _ = json.Marshal(stage)
		raw = append(raw, '\n')
		if err = g.writeOriginalRecord(g.owner, "stage.json", raw, generationRecordLimit, false); err != nil {
			return err
		}
	} else if id != stage.Identity {
		return ErrConfigGenerationConflict
	}
	if err = unix.Renameat(int(g.owner.Fd()), name, int(g.owner.Fd()), "active.json"); err != nil {
		return err
	}
	if err = g.owner.Sync(); err != nil {
		return err
	}
	body, id, err = publicationReadAt(g.owner, "active.json", generationRecordLimit)
	if err != nil || id != stage.Identity || !bytes.Equal(body, want) {
		return ErrConfigGenerationConflict
	}
	return g.validate()
}

// Receipt must already be durable. Remove the stage seal first and pending
// intent last; a crash in this cleanup replays the immutable receipt.
func (g *generationOwner) clearPublication(i configGenerationIntent, originalStage *configGenerationStage) error {
	if i.Next != nil {
		want, _ := json.Marshal(i.Next)
		want = append(want, '\n')
		body, id, e := publicationReadAt(g.owner, "active.json", generationRecordLimit)
		if originalStage == nil || e != nil || id != originalStage.Identity || !bytes.Equal(body, want) {
			return ErrConfigGenerationConflict
		}
	} else if originalStage != nil {
		return generationInvalid()
	}
	raw, _, exists, err := generationOriginalRecord(g.owner, "stage.json", generationRecordLimit)
	if err != nil {
		return err
	}
	if exists {
		var stage configGenerationStage
		if generationDecode(raw, &stage, generationRecordLimit) != nil || stage.RequestSHA256 != i.RequestSHA256 || originalStage == nil || stage != *originalStage {
			return generationInvalid()
		}
		if err = g.writeOriginalRecord(g.owner, "stage.json", raw, generationRecordLimit, false); err != nil {
			return err
		}
		if err = g.removeRecord(g.owner, "stage.json", raw, generationRecordLimit); err != nil {
			return err
		}
	}
	raw, _ = json.Marshal(i)
	raw = append(raw, '\n')
	if err = g.writeOriginalRecord(g.owner, "pending.json", raw, generationRecordLimit, false); err != nil {
		return err
	}
	return g.removeRecord(g.owner, "pending.json", raw, generationRecordLimit)
}
