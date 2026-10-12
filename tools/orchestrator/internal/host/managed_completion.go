package host

import (
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Called only on an admitted immutable mode, after the original process tree
// proof and protocol.Finish's exact init/result-session validation. The native
// structured channel is mandatory; result prose is never a codec fallback.
func managedTerminalOutcome(provider adapter.Provider, terminal adapter.Event, exit int, protocolErr error) (string, string, string, error) {
	failed := func(err error) (string, string, string, error) {
		reason := "managed_native_completion_failed"
		if err != nil && (err.Error() == "managed_completion_invalid" || err.Error() == "managed_completion_too_large") {
			reason = err.Error()
		}
		return contract.EventFailed, "failed", `{"status":"incomplete","reason":"` + reason + `"}`, err
	}
	if protocolErr != nil {
		return failed(protocolErr)
	}
	if exit != 0 || terminal.Kind != adapter.EventResult || !providerSucceeded(provider, terminal) || terminal.NativeFailure || terminal.NativePermissionDenied || terminal.NativeMetadataInvalid {
		return failed(errors.New("managed_native_completion_failed"))
	}
	completion, err := adapter.DecodeTaskCompletion(terminal.StructuredText, providerEventLimit)
	if err != nil {
		return failed(err)
	}
	if completion.Kind == "technical_question" {
		return contract.EventQuestion, "waiting_user", completion.Text, nil
	}
	return contract.EventResult, "result_ready", completion.Text, nil
}
