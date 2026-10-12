package host

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

func TestCodexPluginOriginBindsNewBackendWithoutUpgradingLegacy(t *testing.T) {
	h, g, old, env := codexOriginFixture(t)
	payload, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
	if err != nil {
		t.Fatal(err)
	}
	payload.Profile.CodexHostToolsVersion = 1
	inv, err := adapter.BuildCodexPluginInvocation(adapter.Request{Provider: adapter.ProviderCodex, Binary: payload.ProviderLock.Binary, Lock: payload.ProviderLock, Profile: payload.Profile, CWD: payload.Directory, Prompt: payload.Prompt})
	if err != nil {
		t.Fatal(err)
	}
	g.AdapterPayload, _ = json.Marshal(payload)
	prepared, err := h.prepareManagedCodexState(context.Background(), g, inv, env, codexPreparationControl{})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Origin.Backend != adapter.CodexPluginBackend || prepared.Origin.Profile.CodexHostToolsVersion != 1 {
		t.Fatal("origin did not freeze plugin mode")
	}
	if err := codexValidateOriginRecord(prepared.Origin); err != nil {
		t.Fatal(err)
	}
	changed := prepared.Origin
	changed.Backend = adapter.ManagedCodexBackend
	if codexValidateOriginRecord(changed) == nil {
		t.Fatal("legacy origin gained plugin tools")
	}
	if old.ExecutionProfile().CodexHostToolsVersion != 0 {
		t.Fatal("legacy invocation mutated")
	}
}
