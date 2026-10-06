package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
)

func TestExecutionPolicyCommitmentPreservesLegacyAndRejectsDrift(t *testing.T) {
	tasks := []TaskRequest{{ID: "task", MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}}
	literal := []byte(`{"run_id":"run","plan_revision":1,"tasks":[{"id":"task","max_attempts":3,"max_active_ms":1000,"adapter":{"kind":"fake"}}]}`)
	digest := sha256.Sum256(append([]byte("agent-bird-resolved-plan-v1\x00"), literal...))
	legacy := hex.EncodeToString(digest[:])
	if got := ConfigTaskPlanDigest("run", 1, tasks); got != legacy {
		t.Fatalf("legacy commitment bytes changed %s/%s", got, legacy)
	}
	uses := []install.ConfigGenerationTaskUse{{TaskID: "task", PlanRevision: 1, MaxAttempts: 3, MaxActiveMS: 1000, AdapterPayload: tasks[0].AdapterPayload}}
	if got := install.ConfigSubmissionPlanDigest("run", uses); got != legacy {
		t.Fatalf("legacy receiver bytes changed %s/%s", got, legacy)
	}
	tasks[0].ExecutionPolicy = &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 3, GroupMaxActiveMS: 2000, TaskMaxActiveMS: 1000, SegmentMaxActiveMS: 250}
	uses[0].ExecutionPolicy = tasks[0].ExecutionPolicy
	original := ConfigTaskPlanDigest("run", 1, tasks)
	if original == legacy || install.ConfigSubmissionPlanDigest("run", uses) != original {
		t.Fatal("policy missing from common commitment")
	}
	changed := *tasks[0].ExecutionPolicy
	changed.SegmentMaxActiveMS = 251
	tasks[0].ExecutionPolicy = &changed
	if ConfigTaskPlanDigest("run", 1, tasks) == original {
		t.Fatal("policy drift reused original commitment")
	}
	for _, raw := range []string{`{"id":"task","execution_policy":{"version":1},"execution_policy":{"version":1}}`, `{"id":"task","execution_policy":{"version":1,"retry":{"codes":["probe_start_eagain"],"codes":["probe_start_eintr"]}}}`} {
		var task TaskRequest
		if json.Unmarshal([]byte(raw), &task) == nil {
			t.Fatal("duplicate public policy accepted", raw)
		}
	}
}
