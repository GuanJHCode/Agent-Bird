package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
)

const ConfigGenerationRecordLimit = 8 * 1024

// ConfigGenerationRef names immutable data, never a path or authority.
type ConfigGenerationRef struct {
	SchemaVersion    int    `json:"schema_version"`
	OwnerScopeSHA256 string `json:"owner_scope_sha256"`
	GenerationID     string `json:"generation_id"`
	ManifestSHA256   string `json:"manifest_sha256"`
	PackageVersion   string `json:"package_version"`
}

var configPackageVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

func (r ConfigGenerationRef) Validate() error {
	if r.SchemaVersion != 1 || !ownerDigest(r.OwnerScopeSHA256) || !ownerDigest(r.GenerationID) || !ownerDigest(r.ManifestSHA256) || len(r.PackageVersion) > 128 || !configPackageVersion.MatchString(r.PackageVersion) {
		return errors.New("config_generation_ref_invalid")
	}
	return nil
}

func DecodeConfigGenerationRef(raw []byte) (ConfigGenerationRef, error) {
	type plain ConfigGenerationRef
	var value plain
	if len(raw) > ConfigGenerationRecordLimit || decodeOwner(raw, &value) != nil {
		return ConfigGenerationRef{}, errors.New("config_generation_ref_invalid")
	}
	r := ConfigGenerationRef(value)
	return r, r.Validate()
}

func (r *ConfigGenerationRef) UnmarshalJSON(raw []byte) error {
	value, err := DecodeConfigGenerationRef(raw)
	if err == nil {
		*r = value
	}
	return err
}

// Inputs must come from an authenticated original owner, not request JSON.
func ConfigGenerationOwnerScope(controllerThread, originBirth string) (string, error) {
	if !ownerText(controllerThread, 256) || !ownerText(originBirth, 256) {
		return "", errors.New("config_owner_invalid")
	}
	raw, _ := json.Marshal([]string{controllerThread, originBirth})
	sum := sha256.Sum256(append([]byte("agent-bird-config-owner-v1\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}
