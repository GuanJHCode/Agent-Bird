//go:build darwin

package host

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestFilterTargetReviewNativeHostAcceptedPreflight(t *testing.T) {
	filterReviewNativeChain(t, true, true)
}

func filterTargetReviewNativeAcceptedPreflight(t *testing.T, ctx context.Context, db *store.DB, h *Host, hostID, root, repo string, guard *validationNativeCleanup, prior contract.LaunchCommand, in FilteredTargetPreflightDeliveryV1, priorEvents []contract.Event) {
	t.Helper()
	baseline := filterTargetHostCapture(t, repo)
	g, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil || g.TaskID != "target-review" || g.GitFilter == nil || g.ValidationTool != nil || g.Input == nil || g.DependencyInputs == nil || len(g.DependencyInputs.Inputs) != 1 || !reflect.DeepEqual(*g.Input, g.DependencyInputs.Inputs[0]) || g.Input.Event.TaskID != "preflight" {
		t.Fatalf("approval grant=%+v err=%v", g, err)
	}
	if g.AttemptID == prior.AttemptID || g.GitFilter.ScopeSHA256 == prior.GitFilter.ScopeSHA256 || g.GitFilter.ReservationID == prior.GitFilter.ReservationID {
		t.Fatal("approval reused preflight authority")
	}
	p, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
	if err != nil || !adapter.IsFilterTargetReview(p) || p.Prompt != "" || p.CandidateAction.Target != in.Target {
		t.Fatalf("approval payload: %v", err)
	}
	priorEvent := filterPipelineResultEvent(t, priorEvents)
	if g.Input.Event.EventID != priorEvent.EventID || g.Input.Event.Artifact.SHA256 != priorEvent.Artifact.SHA256 || g.Input.DecisionCommandID != "accept-preflight" {
		t.Fatal("accepted preflight binding lost")
	}
	o := in.Observation
	bound := gitops.ReviewBinding{TargetWorktreeIdentity: repo, TargetDirectoryIdentity: o.TargetIdentity, TargetRef: o.TargetRef, TargetBaseOID: o.BaseOID, FinalCandidateOID: o.CandidateOID, OrderedInputOIDs: append([]string(nil), o.OrderedInputOIDs...), ValidationDigest: in.Review.ValidationDigest, PlanRevision: int64(g.PlanRevision), ReviewRevision: int64(g.WorkRevision)}
	oraw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	expectedContext := map[string]any{"preflight_sha256": priorEvent.Artifact.SHA256, "observation_sha256": hashBytes(oraw), "target": bound, "index_sha256": o.IndexSHA256, "inventory_sha256": o.InventorySHA256, "manifest_sha256": o.BaseFilter.ManifestSHA256, "filter_context_sha256": o.BaseFilter.FilterContextSHA256, "candidate_receipt_sha256": in.Review.CandidateReceiptSHA256}
	expectedRaw, err := json.Marshal(expectedContext)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := FilteredTargetReviewPrompt(g, filepath.Join(root, "host-spool"))
	if err != nil {
		t.Fatal(err)
	}
	guard.pending++
	result, err := h.ExecuteLaunch(ctx, g, filterReviewNativeInvocation{validationNativeInvocation: validationNativeInvocation{launchInvocation: launchInvocation{args: []string{p.ProviderLock.Binary.Path, string(expectedRaw)}, dir: p.Directory}, payload: p}, prompt: prompt})
	if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
		t.Fatalf("target-review result=%+v err=%v", result, err)
	}
	body, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var out FilteredTargetReviewDeliveryV1
	if err = contract.DecodeBridgeStrict(body, &out); err != nil {
		t.Fatal(err)
	}
	fresh := o
	fresh.AttemptID = g.AttemptID
	fresh.FilterScopeSHA256 = g.GitFilter.ScopeSHA256
	fresh.FilterReservationID = g.GitFilter.ReservationID
	expected := FilteredTargetReviewDeliveryV1{Version: 1, Stage: "filter_target_review", Binding: filterReviewBinding(g), InputEventID: priorEvent.EventID, InputSHA256: priorEvent.Artifact.SHA256, OwnerDecisionID: "accept-preflight", PreflightBinding: in.Binding, ProducerBinding: *in.Review.Candidate.Binding, ValidatorBinding: in.Review.Validation.Binding, ContentReviewBinding: in.Review.Binding, CandidateReceiptSHA256: in.Review.CandidateReceiptSHA256, ValidationDigest: in.Review.ValidationDigest, ManifestSHA256: o.BaseFilter.ManifestSHA256, FilterContextSHA256: o.BaseFilter.FilterContextSHA256, PreflightObservationSHA256: hashBytes(oraw), ProviderLock: p.ProviderLock, Review: &ReviewEvidence{Decision: "approve", Summary: "read candidate R and exact target context; candidate and target writes denied"}, Target: bound, Observation: fresh}
	if !reflect.DeepEqual(out, expected) {
		t.Fatalf("approval envelope/provenance changed: got=%#v want=%#v", out, expected)
	}
	dir := candidateDirectory(p.Directory, g, true)
	b, err := os.ReadFile(filepath.Join(dir, "file"))
	if err != nil || !bytes.Equal(b, []byte("candidate\n")) {
		t.Fatalf("fresh review R=%q err=%v", b, err)
	}
	admin := filterTargetReviewAdminPath(t, repo, dir, baseline)
	if err = filterTargetReviewInventoryPreserved(baseline, filterTargetHostCapture(t, repo), admin); err != nil {
		t.Fatal(err)
	}
	original, err := readManagedCandidateArtifact(g.Input.Event, filepath.Join(root, "host-spool"))
	if err != nil || hashBytes(original) != priorEvent.Artifact.SHA256 {
		t.Fatal("original full preflight artifact changed")
	}
	events := validationCommitEvents(t, ctx, db, h, g)
	if !filterPipelineExited(events, g) {
		t.Fatal("target-review original process/Wait/tree/cleanup proof missing")
	}
	exitID := filterPipelineExitedEvent(t, events, g)
	guard.pending--
	readonly, err := sql.Open("sqlite", "file:"+filepath.Join(root, "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var state, release, cleanup string
	err = readonly.QueryRow("SELECT state,release_event_id,cleanup_json FROM git_filter_resource_reservations WHERE id=?", g.GitFilter.ReservationID).Scan(&state, &release, &cleanup)
	closeErr := readonly.Close()
	if err != nil || closeErr != nil || state != "released" || release != exitID || cleanup == "" {
		t.Fatalf("approval resource=%s release=%s err=%v close=%v", state, release, err, closeErr)
	}
	validationAccept(t, ctx, db, g)
	if err = filterTargetReviewInventoryPreserved(baseline, filterTargetHostCapture(t, repo), admin); err != nil {
		t.Fatal(err)
	}
	t.Logf("controlled_filtered_target_review original_wait_tree=true native_structured=true exact_target_context=true actual_candidate_read=true candidate_target_write_denied=true original_preflight_preserved=true original_target_entries_unchanged=true new_owned_admin=%s root=%s", admin, root)
}

func filterTargetReviewReadNamed(t *testing.T, path string) []byte {
	t.Helper()
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := filterTargetHostIdentity(before)
	if err != nil || !before.Mode().IsRegular() || before.Size() > 4096 {
		t.Fatal("invalid worktree link")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	oid, err := filterTargetHostIdentity(opened)
	if err != nil || oid != id {
		f.Close()
		t.Fatal("worktree link changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	closeErr := f.Close()
	after, statErr := os.Lstat(path)
	if err != nil || closeErr != nil || statErr != nil {
		t.Fatal("worktree link read failed")
	}
	aid, err := filterTargetHostIdentity(after)
	if err != nil || aid != id || int64(len(raw)) != id.Size {
		t.Fatal("worktree link changed")
	}
	return raw
}
func filterTargetReviewAdminPath(t *testing.T, repo, dir string, before filterTargetHostBaseline) string {
	t.Helper()
	link := string(filterTargetReviewReadNamed(t, filepath.Join(dir, ".git")))
	if !strings.HasPrefix(link, "gitdir: ") || !strings.HasSuffix(link, "\n") {
		t.Fatal("missing fresh worktree registration")
	}
	path := strings.TrimSuffix(strings.TrimPrefix(link, "gitdir: "), "\n")
	if filepath.Clean(path) != path || filepath.Dir(path) != filepath.Join(repo, ".git", "worktrees") {
		t.Fatal("foreign admin path")
	}
	relative, err := filepath.Rel(repo, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := before.identities[relative]; exists {
		t.Fatal("reused prior admin subtree")
	}
	if string(filterTargetReviewReadNamed(t, filepath.Join(path, "gitdir"))) != filepath.Join(dir, ".git")+"\n" {
		t.Fatal("admin backlink changed")
	}
	return relative
}

// Materializing a fresh candidate registers exactly one owned child under the
// shared worktrees directory. All pre-existing files and other directories
// remain exact; no target index/ref/config exception is allowed.
func filterTargetReviewInventoryPreserved(before, after filterTargetHostBaseline, admin string) error {
	if filepath.Dir(admin) != filepath.Join(".git", "worktrees") || before.identities[admin].Inode != 0 || after.identities[admin].Inode == 0 {
		return fmt.Errorf("invalid fresh admin subtree")
	}
	for name, want := range before.identities {
		got, ok := after.identities[name]
		if !ok {
			return fmt.Errorf("target entry removed: %s", name)
		}
		if name == filepath.Join(".git", "worktrees") {
			if got.Links != want.Links && got.Links != want.Links+1 {
				return fmt.Errorf("unexpected worktrees link count")
			}
			got.Size, got.ModUnixNano, got.Links = want.Size, want.ModUnixNano, want.Links
		}
		if got != want || before.modes[name] != after.modes[name] || !bytes.Equal(before.files[name], after.files[name]) {
			return fmt.Errorf("original target entry changed: %s", name)
		}
	}
	for name := range after.identities {
		if _, exists := before.identities[name]; !exists && name != admin && !strings.HasPrefix(name, admin+string(filepath.Separator)) {
			return fmt.Errorf("unexpected target entry: %s", name)
		}
	}
	return nil
}

func TestFilterTargetReviewInventoryOracle(t *testing.T) {
	for _, mode := range []string{"owned_admin_only", "changed_index", "unowned_admin", "target_extra"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".git", "worktrees"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".git", "index"), []byte("index"), 0600); err != nil {
				t.Fatal(err)
			}
			before := filterTargetHostCapture(t, root)
			admin := filepath.Join(".git", "worktrees", "fresh")
			if err := os.Mkdir(filepath.Join(root, admin), 0700); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "changed_index":
				if err := os.WriteFile(filepath.Join(root, ".git", "index"), []byte("drift"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unowned_admin":
				if err := os.Mkdir(filepath.Join(root, ".git", "worktrees", "other"), 0700); err != nil {
					t.Fatal(err)
				}
			case "target_extra":
				if err := os.WriteFile(filepath.Join(root, "extra"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := filterTargetReviewInventoryPreserved(before, filterTargetHostCapture(t, root), admin)
			if (err == nil) != (mode == "owned_admin_only") {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}
