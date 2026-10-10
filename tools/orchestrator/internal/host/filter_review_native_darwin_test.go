//go:build darwin

package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// TestFilterReviewNativeHostAdmission is the controlled chain's admission
// anchor. The qualified producer/validator fixture supplies the native B/R
// lifecycle; this independent test declares only the fresh, zero-target review
// role so an old Store fails before any reviewer process can acquire authority.
func TestFilterReviewNativeHostAdmission(t *testing.T) {
	root := filepath.Join("/private/tmp", "filter-review-native-admission")
	manifest := contract.GitFilterManifestV1{Version: 1, DriverID: "sealed", Paths: []string{"file"}, Git: contract.GitFilterGitPin{Path: "/usr/bin/git", Version: "fixture", SHA256: strings.Repeat("a", 64)}, Clean: contract.GitFilterToolPin{Path: "/private/tmp/filter", SHA256: strings.Repeat("a", 64), Args: []string{"xor"}}, Smudge: contract.GitFilterToolPin{Path: "/private/tmp/filter", SHA256: strings.Repeat("a", 64), Args: []string{"xor"}}, Attributes: []contract.GitFilterAttributePin{}, ConfigSHA256: strings.Repeat("a", 64)}
	review := adapter.InvocationPayload{Directory: root, Provider: string(adapter.ProviderClaude), GitFilter: &manifest, ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: "/private/tmp/claude-review", Version: "controlled-native", SHA256: strings.Repeat("b", 64)}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Reviewer, Permission: adapter.ReadOnly, TimeoutMS: 300000}, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "review", SourceTask: "validate", DependencyInputVersion: 1, AutoDirectory: true}}
	raw, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.SubmitPlan(t.Context(), store.PlanSpec{Run: store.RunSpec{ID: "filter-review-native", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "fixture"}, Host: store.HostLaunchSpec{OriginContextID: "test", HostGeneration: "fixture", Executable: "/private/host"}, Tasks: []store.TaskSpec{{ID: "review", RunID: "filter-review-native", MaxAttempts: 1, MaxActiveMS: 300000, AdapterPayload: raw}}})
	if err == nil {
		t.Fatal("review without accepted filtered validator unexpectedly admitted")
	}
}
