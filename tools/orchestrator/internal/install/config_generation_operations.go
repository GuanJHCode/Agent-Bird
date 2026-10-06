//go:build !windows

package install

import "github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"

type ConfigGenerationReceipt struct {
	SchemaVersion int                           `json:"schema_version"`
	RequestSHA256 string                        `json:"request_sha256"`
	Action        string                        `json:"action"`
	Active        *ConfigGenerationActive       `json:"active,omitempty"`
	Ref           *contract.ConfigGenerationRef `json:"ref,omitempty"`
}

// Public callers provide authenticated scope and their actual executable.
func UpdateConfigGeneration(executable, scope string, request ConfigGenerationOperation) (ConfigGenerationReceipt, error) {
	return updateConfigGeneration(executable, scope, request, nil)
}

func updateConfigGeneration(executable, scope string, request ConfigGenerationOperation, checkpoint func(string) error) (ConfigGenerationReceipt, error) {
	return updateConfigGenerationImpl(executable, scope, request, checkpoint)
}

func BeginConfigPreparation(executable, scope, runID string, taskIDs []string, requestSHA256 string, authorities ...*contract.ConfigPreparationAuthority) (ConfigPreparationUse, error) {
	return beginConfigPreparationForTasks(executable, scope, runID, taskIDs, requestSHA256, nil, authorities...)
}

func PinConfigPreparation(executable string, use ConfigPreparationUse, preparationSHA256 string) error {
	return pinConfigPreparation(executable, use, preparationSHA256, nil)
}

func pinConfigPreparation(executable string, use ConfigPreparationUse, preparationSHA256 string, checkpoint func(string) error) error {
	return pinConfigPreparationImpl(executable, use, preparationSHA256, checkpoint)
}

func ReadConfigGeneration(executable string, ref contract.ConfigGenerationRef) (ConfigGenerationManifest, error) {
	return readConfigGeneration(executable, ref)
}
