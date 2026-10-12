package install

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Real owned-file/flock and descriptor lifecycle; no Store, peer or process
// authentication is claimed by this FS-only fixture.
func TestOwnedConfigSubmissionGuardOwnsOriginalLock(t *testing.T) {
	_, exe := ownedConfigPackage(t)
	scope, _ := contract.ConfigGenerationOwnerScope("controller", "birth")
	published := ownedConfigPublish(t, exe, scope, "a", nil, "a")
	raw, _ := json.Marshal(map[string]any{"kind": "fake", "config_generation": published.Ref, "config_model_source": "not-applicable"})
	tasks := []ConfigGenerationTaskUse{{TaskID: "task", AdapterPayload: raw, PlanRevision: 1, MaxAttempts: 1}}
	digest := ConfigSubmissionPlanDigest("run", tasks)
	use, e := ownedConfigBegin(t, exe, scope, "run", []string{"task"}, strings.Repeat("1", 64))
	if e != nil {
		t.Fatal(e)
	}
	if e = PinConfigPreparation(exe, use, digest); e != nil {
		t.Fatal(e)
	}
	guard, e := OpenConfigSubmissionGuard(exe, scope, "run", digest, tasks)
	if e != nil {
		t.Fatal(e)
	}
	originalFile := guard.lock
	if _, e = ReadConfigGenerationActive(exe, scope); e == nil || e.Error() != "install_locked" {
		guard.Close()
		t.Fatal("guard dropped lock before Store transaction", e)
	}
	guard.Close()
	guard.Close()
	if _, e = originalFile.Stat(); !errors.Is(e, os.ErrClosed) {
		t.Fatal("Close did not close its original descriptor", e)
	}
	if _, e = ReadConfigGenerationActive(exe, scope); e != nil {
		t.Fatal("Close retained install lock", e)
	}
	// This request has a correct descriptor digest, acquires the lock, then fails
	// because no original use exists. Constructor must release all ownership.
	if failed, e := OpenConfigSubmissionGuard(exe, scope, "missing", ConfigSubmissionPlanDigest("missing", tasks), tasks); e == nil || failed != nil {
		if failed != nil {
			failed.Close()
		}
		t.Fatal("missing original use accepted", e)
	}
	if _, e = ReadConfigGenerationActive(exe, scope); e != nil {
		t.Fatal("failed constructor leaked install lock", e)
	}
}
