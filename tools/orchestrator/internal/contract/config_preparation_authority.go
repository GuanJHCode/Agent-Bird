package contract

import (
	"encoding/json"
	"errors"
	"path/filepath"
)

// This is a nonsecret reference. Only the receiving Store and its persisted
// owner grants can establish authority; decoding this value does not do so.
type ConfigPreparationAuthority struct {
	SchemaVersion  int    `json:"schema_version"`
	OwnerGrantID   string `json:"owner_grant_id"`
	StateRoot      string `json:"state_root"`
	StateDevice    uint64 `json:"state_device"`
	StateInode     uint64 `json:"state_inode"`
	DatabaseDevice uint64 `json:"database_device"`
	DatabaseInode  uint64 `json:"database_inode"`
}

func (a ConfigPreparationAuthority) Validate() error {
	if a.SchemaVersion != 1 || !ownerText(a.OwnerGrantID, 256) || !ownerText(a.StateRoot, 4096) || !filepath.IsAbs(a.StateRoot) || filepath.Clean(a.StateRoot) != a.StateRoot || a.StateInode == 0 || a.DatabaseInode == 0 {
		return errors.New("config_preparation_store_mismatch")
	}
	return nil
}

func (a *ConfigPreparationAuthority) UnmarshalJSON(raw []byte) error {
	type plain ConfigPreparationAuthority
	var p plain
	if len(raw) > ConfigGenerationRecordLimit || decodeOwner(raw, &p) != nil {
		return errors.New("config_preparation_store_mismatch")
	}
	value := ConfigPreparationAuthority(p)
	if err := value.Validate(); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 7 {
		return errors.New("config_preparation_store_mismatch")
	}
	for _, key := range []string{"schema_version", "owner_grant_id", "state_root", "state_device", "state_inode", "database_device", "database_inode"} {
		if len(fields[key]) == 0 || string(fields[key]) == "null" {
			return errors.New("config_preparation_store_mismatch")
		}
	}
	*a = value
	return nil
}
