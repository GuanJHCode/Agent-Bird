//go:build darwin

package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestFilterIntegrationNativePublicChain(t *testing.T) {
	filterReviewNativeChain(t, true, true, true)
}

func filterIntegrationBindNativeHelper(t *testing.T, ctx context.Context, h *Host, root string, guard *validationNativeCleanup) {
	t.Helper()
	binary := filepath.Join(root, "orchestrator-carrier")
	goPath := "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go"
	guard.pending++
	p, err := process.Start(ctx, process.Command{Path: goPath, PinnedPath: goPath, PinnedSHA256: filterPipelineDigest(t, goPath), Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", binary, "./cmd/orchestrator"}, Dir: filterPipelineModuleRoot(t)})
	if err != nil || p == nil {
		t.Fatalf("helper build start: %v", err)
	}
	we, te := p.Wait(ctx), p.ConfirmTreeExited()
	if we != nil || te != nil || p.ExitCode() != 0 {
		validationNativeStop(p, guard)
		t.Fatalf("helper build %v %v exit=%d %s", we, te, p.ExitCode(), p.Output())
	}
	guard.pending--
	if err = os.Chmod(binary, 0500); err != nil {
		t.Fatal(err)
	}
	// This controlled fixture does not claim a real owner handshake. Production
	// calls this same binder only after VerifyOriginalOwnerBinding succeeds.
	binding := contract.OriginalOwnerBinding{Scope: contract.OriginalOwnerScope{Version: 1, RunID: "filter-native-run", LaunchID: "fixture-launch", ProducerID: h.producerID, OwnerID: "fixture-owner", Incarnation: 1, OriginalOriginContextID: "fixture", OriginalOriginPID: os.Getpid(), OriginalOriginBirth: "fixture-birth", HostGeneration: "fixture", Executable: binary, ExecutableSHA256: filterPipelineDigest(t, binary)}}
	if err = h.bindFilterHelper(binding); err != nil {
		t.Fatal(err)
	}
}
func filterIntegrationNativeFinal(t *testing.T, ctx context.Context, db *store.DB, h *Host, hostID, root, repo string, guard *validationNativeCleanup) {
	t.Helper()
	var g contract.LaunchCommand
	pendingBefore, err := db.PendingLaunches(ctx, hostID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingBefore) == 1 && pendingBefore[0].TaskID == "integrate" {
		g = pendingBefore[0]
		if _, e := os.Lstat(filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID)); !os.IsNotExist(e) {
			t.Fatal("continuation diagnostic refuses any prior final-stage spool")
		}
	} else {
		g, err = db.ClaimReady(ctx, hostID, 1, 2)
	}
	if err != nil || g.TaskID != "integrate" || g.GitFilterCarrier == nil || g.Input == nil || g.Input.Event.TaskID != "target-review" || g.DependencyInputs == nil || len(g.DependencyInputs.Inputs) != 2 {
		t.Fatalf("integration grant=%+v err=%v", g, err)
	}
	pending, err := db.PendingLaunches(ctx, hostID, 1)
	if err != nil || len(pending) != 1 || pending[0].LaunchIntentHash != g.LaunchIntentHash || *pending[0].GitFilterCarrier != *g.GitFilterCarrier {
		t.Fatalf("replay=%+v err=%v", pending, err)
	}
	payload, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
	if err != nil {
		t.Fatal(err)
	}
	guard.pending++
	result, err := h.ExecuteLaunch(ctx, g, filterInvocation{payload: payload})
	if err != nil || result.Status != "result_ready" {
		t.Fatalf("integration result=%+v err=%v", result, err)
	}
	body, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var out FilteredIntegrationDeliveryV1
	if err = contract.DecodeBridgeStrict(body, &out); err != nil || out.Stage != "filter_integrate" || out.Integration == nil || out.Integration.State != gitops.IntegrationIntegrated || out.Integration.GitExitCode != 0 || out.Integration.ProcessPID < 1 || out.Integration.TargetHEAD != out.Target.FinalCandidateOID {
		t.Fatalf("delivery=%+v err=%v", out, err)
	}
	rendered, err := os.ReadFile(filepath.Join(repo, "file"))
	if err != nil || string(rendered) != "candidate\n" {
		t.Fatalf("rendered=%q err=%v", rendered, err)
	}
	head, err := os.ReadFile(filepath.Join(repo, ".git", "refs", "heads", "main"))
	if err != nil || strings.TrimSpace(string(head)) != out.Target.FinalCandidateOID {
		t.Fatalf("HEAD=%q err=%v", head, err)
	}
	events := validationCommitEvents(t, ctx, db, h, g)
	exitID := ""
	for _, e := range events {
		if e.Kind == contract.EventExited {
			if e.Process != nil || e.ExitCode != nil || e.ActiveMS < 1 || e.GitFilterCleanup == nil || e.GitFilterCarrierCleanup == nil || e.GitFilterCarrierCleanup.Outcome != "removed" || e.GitFilterCarrierCleanup.Validate() != nil {
				t.Fatalf("terminal=%+v", e)
			}
			exitID = e.EventID
		}
	}
	if exitID == "" {
		t.Fatal("original terminal proof missing")
	}
	guard.pending--
	ro, err := sql.Open("sqlite", "file:"+filepath.Join(root, "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	for _, table := range []string{"git_filter_resource_reservations", "git_filter_carrier_reservations"} {
		var state, release string
		id := g.GitFilter.ReservationID
		if table == "git_filter_carrier_reservations" {
			id = g.GitFilterCarrier.ReservationID
		}
		if err = ro.QueryRow("SELECT state,release_event_id FROM "+table+" WHERE id=?", id).Scan(&state, &release); err != nil || state != "released" || release != exitID {
			t.Fatalf("%s state=%s release=%s err=%v", table, state, release, err)
		}
	}
	validationAccept(t, ctx, db, g)
	raw, _ := json.Marshal(out.Integration)
	t.Logf("public_filtered_merge admission_pending_replay=true accepted_managed_inputs=2 original_git_wait_tree=true both_reservations_released=true outcome=%s", raw)
}

// A failed pre-claim join leaves these five accepted managed artifacts reusable.
// This diagnostic fixture creates fresh final-stage live owners, never restores
// a carrier/Broker/transition from saved observations. Opt-in only, not CI proof.
func TestFilterIntegrationNativeAcceptedContinuation(t *testing.T) {
	root := os.Getenv("ORCHESTRATOR_TEST_ACCEPTED_FILTER_ROOT")
	if root == "" {
		t.Skip("requires retained pending-zero controlled fixture")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Base(filepath.Dir(root)) != "tmp" {
		t.Fatal("invalid controlled fixture root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, err := os.ReadDir(filepath.Join(root, "host-spool"))
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatal("fixture host missing")
	}
	hostID := entries[0].Name()
	h, err := New(db, filepath.Join(root, "host-spool", hostID))
	if err != nil {
		t.Fatal(err)
	}
	h.producerID = hostID
	id, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	guard := &validationNativeCleanup{root: root, identity: id}
	// No automatic deletion of resumed input evidence, including failure cases.
	filterIntegrationBindNativeHelper(t, ctx, h, root, guard)
	filterIntegrationNativeFinal(t, ctx, db, h, hostID, root, filepath.Join(root, "repo"), guard)
	if guard.pending != 0 {
		t.Fatal("unproved original process remains")
	}
}
