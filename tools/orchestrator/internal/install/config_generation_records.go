package install

import (
	"errors"
	"math"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const generationManifestLimit = 16 << 10
const generationRecordLimit = 8 << 10
const generationCapacity = 64

type ConfigSavedDefaults struct {
	AGY    string `json:"antigravity-cli"`
	Claude string `json:"claude-code"`
	Codex  string `json:"codex-cli"`
}

func (d ConfigSavedDefaults) Validate() error {
	for _, model := range []string{d.AGY, d.Claude, d.Codex} {
		if !adapter.ValidModelID(model) {
			return errors.New("config_model_invalid")
		}
	}
	return nil
}

func (d ConfigSavedDefaults) Model(provider string) (string, error) {
	switch adapter.Provider(provider) {
	case adapter.ProviderAGY:
		return d.AGY, nil
	case adapter.ProviderClaude:
		return d.Claude, nil
	case adapter.ProviderCodex:
		return d.Codex, nil
	default:
		return "", errors.New("config_provider_unsupported")
	}
}

type ConfigGenerationManifest struct {
	SchemaVersion           int                 `json:"schema_version"`
	OwnerScopeSHA256        string              `json:"owner_scope_sha256"`
	PackageVersion          string              `json:"package_version"`
	InstalledManifestSHA256 string              `json:"installed_manifest_sha256"`
	PayloadManifestSHA256   string              `json:"payload_manifest_sha256"`
	SavedDefaults           ConfigSavedDefaults `json:"saved_defaults"`
}

type ConfigGenerationActive struct {
	SchemaVersion int                           `json:"schema_version"`
	Revision      uint64                        `json:"revision"`
	State         string                        `json:"state"`
	Ref           *contract.ConfigGenerationRef `json:"ref,omitempty"`
}

func (a ConfigGenerationActive) Validate() error {
	if a.SchemaVersion != 1 || a.Revision == 0 || a.Revision > math.MaxInt64 {
		return generationInvalid()
	}
	if a.State == "selected" && a.Ref != nil {
		return a.Ref.Validate()
	}
	if a.State == "inactive" && a.Ref == nil {
		return nil
	}
	return generationInvalid()
}

// Nil Expected is first-ever absence, distinct from an inactive tombstone.
type ConfigGenerationOperation struct {
	RequestID     string                        `json:"request_id"`
	Action        string                        `json:"action"`
	Expected      *ConfigGenerationActive       `json:"expected,omitempty"`
	SavedDefaults *ConfigSavedDefaults          `json:"saved_defaults,omitempty"`
	Ref           *contract.ConfigGenerationRef `json:"ref,omitempty"`
}

type ConfigGenerationPackage struct {
	DestinationRoot         string `json:"destination_root"`
	Version                 string `json:"version"`
	InstalledManifestSHA256 string `json:"installed_manifest_sha256"`
	PayloadManifestSHA256   string `json:"payload_manifest_sha256"`
	ExecutableSHA256        string `json:"executable_sha256"`
}

type ConfigPreparationUse struct {
	SchemaVersion    int                                  `json:"schema_version"`
	OwnerScopeSHA256 string                               `json:"owner_scope_sha256"`
	RunID            string                               `json:"run_id"`
	TaskIDs          []string                             `json:"task_ids"`
	RequestSHA256    string                               `json:"request_sha256"`
	Mode             string                               `json:"mode"`
	Package          ConfigGenerationPackage              `json:"package"`
	Ref              *contract.ConfigGenerationRef        `json:"ref,omitempty"`
	Authority        *contract.ConfigPreparationAuthority `json:"authority,omitempty"`
}

func generationInvalid() error { return errors.New("config_generation_invalid") }
func generationHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validateGenerationManifest(m ConfigGenerationManifest) error {
	if m.SchemaVersion != 1 || !generationHash(m.OwnerScopeSHA256) || !versionPattern.MatchString(m.PackageVersion) || !generationHash(m.InstalledManifestSHA256) || !generationHash(m.PayloadManifestSHA256) || m.SavedDefaults.Validate() != nil {
		return generationInvalid()
	}
	return nil
}

func validatePreparationUse(u ConfigPreparationUse) error {
	if (u.SchemaVersion != 1 && u.SchemaVersion != 2) || !generationHash(u.OwnerScopeSHA256) || !idPattern.MatchString(u.RunID) || !generationHash(u.RequestSHA256) || len(u.TaskIDs) == 0 || len(u.TaskIDs) > 64 || u.Package.DestinationRoot == "" || !versionPattern.MatchString(u.Package.Version) || !generationHash(u.Package.InstalledManifestSHA256) || u.Package.PayloadManifestSHA256 != "" && !generationHash(u.Package.PayloadManifestSHA256) || !generationHash(u.Package.ExecutableSHA256) {
		return generationInvalid()
	}
	if u.SchemaVersion == 1 && u.Authority != nil || u.SchemaVersion == 2 && (u.Authority == nil || u.Authority.Validate() != nil || u.Mode != "generation") {
		return generationInvalid()
	}
	previous := ""
	for _, id := range u.TaskIDs {
		if !idPattern.MatchString(id) || strings.Compare(id, previous) <= 0 {
			return generationInvalid()
		}
		previous = id
	}
	if u.Mode == "legacy" && u.Ref == nil {
		return nil
	}
	if u.Mode == "generation" && generationHash(u.Package.PayloadManifestSHA256) && u.Ref != nil && u.Ref.Validate() == nil && u.Ref.OwnerScopeSHA256 == u.OwnerScopeSHA256 && u.Ref.PackageVersion == u.Package.Version {
		return nil
	}
	return generationInvalid()
}
