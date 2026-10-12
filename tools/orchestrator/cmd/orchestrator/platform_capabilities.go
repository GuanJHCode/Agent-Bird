package main

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"runtime"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// Deliberately does not open state, ensure a Coordinator, launch a probe, or
// read provider configuration. Installed/runtime authority is checked elsewhere.
func platformCapabilities(args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "capabilities" {
		return codeError("invalid_args")
	}
	body, digest, err := contract.CompiledPlatformContract(runtime.GOOS, runtime.GOARCH).CanonicalJSON()
	if err != nil {
		return err
	}
	revision, modified := platformSourceIdentity(platformCodeSHA, platformCodeClean)
	return json.NewEncoder(stdout).Encode(struct {
		Version         int             `json:"version"`
		SchemaVersion   int             `json:"schema_version"`
		CodeSHA         string          `json:"code_sha"`
		CodeModified    bool            `json:"code_modified"`
		RuntimeVerified bool            `json:"runtime_verified"`
		Contract        json.RawMessage `json:"contract"`
		ContractSHA256  string          `json:"contract_sha256"`
	}{1, store.SchemaVersion(), revision, modified, false, body, digest})
}

// Set only by a build that pins the actual product checkout. Go's automatic
// VCS discovery can select an outer repository for a linked worktree (.git is
// a file), so debug.ReadBuildInfo is deliberately not a product identity.
var platformCodeSHA, platformCodeClean string

func platformSourceIdentity(revision, clean string) (string, bool) {
	raw, err := hex.DecodeString(revision)
	if err != nil || len(raw) != 20 || strings.ToLower(revision) != revision || (clean != "true" && clean != "false") {
		return "unknown", true
	}
	return revision, clean != "true"
}
