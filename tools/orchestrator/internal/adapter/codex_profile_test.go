package adapter

import (
	"strings"
	"testing"
)

func typedCodexRequest(t *testing.T) Request {
	t.Helper()
	req := profileRequest(t, `{"version":1,"role":"reviewer","permission":"read-only","timeout_ms":9000}`)
	req.Provider = ProviderCodex
	req.Binary = codexPinForTest()
	req.Lock = &ProviderLock{Version: 1, Provider: ProviderCodex, Protocol: ProtocolID(ProviderCodex), Binary: req.Binary}
	return req
}

// The worker must honor the source CLI's default model and approval configuration.
// Hardcoding a model or adding approval bypasses violates the public contract.
func TestCodexTypedWorkerPreservesModelAndApprovalDefaults(t *testing.T) {
	inv, err := BuildInvocation(typedCodexRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	args := inv.Args()
	for _, arg := range args {
		if arg == "--model" || arg == "--full-auto" || strings.Contains(arg, "approval_policy") || strings.Contains(arg, "bypass") {
			t.Fatalf("worker overrides source decision: %q", arg)
		}
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "exec --json") || !strings.Contains(joined, "--ephemeral") || !strings.Contains(joined, "--sandbox read-only") {
		t.Fatalf("missing bounded native contract: %v", args)
	}
	if string(inv.Stdin()) != typedCodexRequest(t).Prompt {
		t.Fatal("prompt not passed over stdin")
	}
}

func TestCodexTypedWorkerUsesRequestedModel(t *testing.T) {
	req := typedCodexRequest(t)
	req.Profile.Model = "configured-provider-model"
	inv, err := BuildInvocation(req)
	if err != nil {
		t.Fatal(err)
	}
	args := inv.Args()
	count := 0
	for i, arg := range args {
		if arg == "--model" {
			count++
			if i+1 >= len(args) || args[i+1] != "configured-provider-model" {
				t.Fatal("wrong requested model")
			}
		}
	}
	if count != 1 {
		t.Fatal("requested model missing or duplicated")
	}
}

func TestCodexCapabilityPreflightRejectsUntilNativeAdmissionVerified(t *testing.T) {
	help := "--json --ephemeral --sandbox --config --output-last-message --disable --output-schema --model"
	if err := CheckCapabilities(typedCodexRequest(t), help); err == nil || err.Error() != CodexWorkerAdmissionReason {
		t.Fatalf("help flags cannot establish native auth admission: %v", err)
	}
}
