package install

import (
	"encoding/json"
	"errors"
)

// This file is a distributable declaration of mandatory runtime policy, not a
// permission override. Legacy bundles omit it and retain the same hard floor.
func validateDefaultPolicy(raw []byte) error {
	var p struct {
		Version          int    `json:"version"`
		WriteWorkspace   string `json:"write_workspace"`
		AllowSharedWrite bool   `json:"allow_shared_write"`
		ProviderScope    string `json:"provider_scope"`
		Model            string `json:"model"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Version != 1 || p.WriteWorkspace != "isolated-worktree" || p.AllowSharedWrite || p.ProviderScope != "codex-session" || p.Model != "cli-default" {
		return errors.New("default_policy_invalid")
	}
	return nil
}
