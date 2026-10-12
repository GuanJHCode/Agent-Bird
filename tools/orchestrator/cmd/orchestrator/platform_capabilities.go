package main

import (
	"encoding/json"
	"io"
	"runtime"
	"runtime/debug"

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
	revision := "unknown"
	modified := true
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				revision = s.Value
			}
			if s.Key == "vcs.modified" {
				modified = s.Value != "false"
			}
		}
	}
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
