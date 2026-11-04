package main

import (
	"context"
	"os"

	"codex-cli-orchestration-design/tools/orchestrator/internal/adapter"
	"codex-cli-orchestration-design/tools/orchestrator/internal/install"
)

// Metadata uses the same pinned executable boundary as execution. In particular,
// Claude probes cannot inherit caller credentials or configuration.
func providerMetadata(ctx context.Context, provider adapter.Provider, pin adapter.BinaryPin, args ...string) (string, error) {
	return adapter.ProbePinnedOutput(ctx, provider, pin, args...)
}

func providerHelp(ctx context.Context, provider adapter.Provider, pin adapter.BinaryPin) (string, error) {
	help, err := providerMetadata(ctx, provider, pin, "--help")
	if err != nil || provider != adapter.ProviderCodex {
		return help, err
	}
	execHelp, err := providerMetadata(ctx, provider, pin, "exec", "--help")
	if err != nil {
		return "", err
	}
	return help + "\n" + execHelp, nil
}

// A version catalog is not execution authority. Codex requires its exact native
// pin and a receipt-bound installation containing the complete native runtime.
func nativeExecutionAdmission(provider adapter.Provider, pin adapter.BinaryPin) error {
	if err := adapter.LaunchAdmission(provider, pin.Version, pin.SHA256); err != nil {
		return err
	}
	if provider != adapter.ProviderCodex {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return codeError("codex_guard_package_untrusted")
	}
	_, _, err = install.CodexGuardRuntime(executable)
	return err
}
