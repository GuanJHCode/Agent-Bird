package install

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestConfigDiagnosticsGuardOwnsConsentSnapshot(t *testing.T) {
	_, exe := ownedConfigPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	published := ownedConfigPublish(t, exe, scope, "a", nil, "a")
	raw, _ := json.Marshal(map[string]any{"kind": "fake", "config_generation": published.Ref, "config_model_source": "not-applicable"})
	policy := contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"}
	tasks := []ConfigGenerationTaskUse{{TaskID: "task", AdapterPayload: raw, PlanRevision: 1, MaxAttempts: 1, Diagnostics: &policy}}
	digest := ConfigSubmissionPlanDigest("run", tasks)
	use, err := ownedConfigBegin(t, exe, scope, "run", []string{"task"}, strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err = PinConfigPreparation(exe, use, digest); err != nil {
		t.Fatal(err)
	}
	guard, err := OpenConfigSubmissionGuard(exe, scope, "run", digest, tasks)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	policy.Mode = "summary"
	called := false
	err = guard.WithSubmission(exe, "run", tasks, func(contract.ConfigPreparationAuthority) error { called = true; return nil })
	if !errors.Is(err, ErrConfigPreparationStoreMismatch) || called {
		t.Fatalf("caller changed frozen consent: called=%v err=%v", called, err)
	}
}
