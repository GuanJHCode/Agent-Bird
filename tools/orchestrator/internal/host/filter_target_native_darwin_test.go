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
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestFilterTargetNativeHostAcceptedReview(t *testing.T) {
	filterReviewNativeChain(t, true)
}

type filterTargetHostFileIdentity struct {
	Device, Inode int64
	Mode          os.FileMode
	Size          int64
	ModUnixNano   int64
	UID           uint32
	Links         uint64
}

type filterTargetHostBaseline struct {
	files      map[string][]byte
	modes      map[string]os.FileMode
	identities map[string]filterTargetHostFileIdentity
}

func filterTargetHostCapture(t *testing.T, repo string) filterTargetHostBaseline {
	t.Helper()
	out, err := filterTargetHostInventory(repo)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func filterTargetHostIdentity(info os.FileInfo) (filterTargetHostFileIdentity, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return filterTargetHostFileIdentity{}, fmt.Errorf("native stat unavailable")
	}
	return filterTargetHostFileIdentity{Device: int64(st.Dev), Inode: int64(st.Ino), Mode: info.Mode(), Size: info.Size(), ModUnixNano: info.ModTime().UnixNano(), UID: st.Uid, Links: uint64(st.Nlink)}, nil
}

// Capture every named worktree and .git entry, including absence of sidecars
// via the complete entry set. Never follow symlinks to compare an old inode.
func filterTargetHostInventory(repo string) (filterTargetHostBaseline, error) {
	out := filterTargetHostBaseline{files: map[string][]byte{}, modes: map[string]os.FileMode{}, identities: map[string]filterTargetHostFileIdentity{}}
	entries, pathBytes := 0, 0
	total := int64(0)
	err := filepath.WalkDir(repo, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, e := filepath.Rel(repo, path)
		if e != nil {
			return e
		}
		entries++
		pathBytes += len(rel)
		if entries > 4096 || pathBytes > 256<<10 || len(rel) > 512 {
			return fmt.Errorf("target inventory bounds")
		}
		before, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if !before.IsDir() && !before.Mode().IsRegular() {
			return fmt.Errorf("target special named entry %s", rel)
		}
		namedID, e := filterTargetHostIdentity(before)
		if e != nil {
			return e
		}
		flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		if before.IsDir() {
			flags |= syscall.O_DIRECTORY
		}
		f, e := os.OpenFile(path, flags, 0)
		if e != nil {
			return e
		}
		defer f.Close()
		fi, e := f.Stat()
		if e != nil {
			return e
		}
		heldID, e := filterTargetHostIdentity(fi)
		if e != nil || heldID != namedID {
			return fmt.Errorf("target opened identity %s: %v", rel, e)
		}
		if before.Mode().IsRegular() {
			if before.Size() > 16<<20 || namedID.Links != 1 {
				return fmt.Errorf("target file bounds/links %s", rel)
			}
			total += before.Size()
			if total > 256<<20 {
				return fmt.Errorf("target aggregate bounds")
			}
			raw, e := io.ReadAll(io.LimitReader(f, 16<<20+1))
			if e != nil || int64(len(raw)) != before.Size() {
				return fmt.Errorf("target read %s: %v", rel, e)
			}
			out.files[rel] = raw
		}
		after, e := os.Lstat(path)
		if e != nil {
			return e
		}
		afterID, e := filterTargetHostIdentity(after)
		if e != nil || afterID != heldID {
			return fmt.Errorf("target final identity %s: %v", rel, e)
		}
		out.modes[rel], out.identities[rel] = before.Mode(), heldID
		return f.Close()
	})
	return out, err
}

func TestFilterTargetInventoryOracleDetectsNamedChanges(t *testing.T) {
	for _, mode := range []string{"symlink_to_old_inode", "new_file", "git_lock"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "file")
			if err := os.WriteFile(file, []byte("content"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := filterTargetHostInventory(root)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "symlink_to_old_inode":
				if err = os.Rename(file, file+"-old"); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(file+"-old", file); err != nil {
					t.Fatal(err)
				}
			case "new_file":
				err = os.WriteFile(filepath.Join(root, "extra"), nil, 0600)
			case "git_lock":
				err = os.WriteFile(filepath.Join(root, ".git", "index.lock"), nil, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := filterTargetHostInventory(root)
			if err == nil && reflect.DeepEqual(before, after) {
				t.Fatal("named inventory change hidden")
			}
		})
	}
}

func filterTargetHostAssertPreserved(t *testing.T, repo string, want filterTargetHostBaseline) {
	t.Helper()
	got := filterTargetHostCapture(t, repo)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("target changed by host observation: before=%#v after=%#v", want, got)
	}
}

func filterTargetNativeAcceptedReview(t *testing.T, ctx context.Context, db *store.DB, h *Host, hostID, root, repo string, baseB []byte, guard *validationNativeCleanup, reviewGrant contract.LaunchCommand, reviewed CandidateDelivery, reviewEvents []contract.Event) (contract.LaunchCommand, FilteredTargetPreflightDeliveryV1, []contract.Event) {
	t.Helper()
	baseR := filterPipelineXOR(baseB)
	if err := os.WriteFile(filepath.Join(repo, "file"), baseR, 0600); err != nil {
		t.Fatalf("render canonical target base: %v", err)
	}
	baseline := filterTargetHostCapture(t, repo)

	targetGrant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil || targetGrant.TaskID != "preflight" || targetGrant.GitFilter == nil || targetGrant.ValidationTool != nil || targetGrant.Input == nil || targetGrant.Input.Event.Artifact == nil || targetGrant.Input.Event.Artifact.HostArtifact == nil || targetGrant.Input.Event.TaskID != "review" || targetGrant.DependencyInputs == nil || targetGrant.DependencyInputs.Version != 1 || len(targetGrant.DependencyInputs.Inputs) != 1 {
		t.Fatalf("target grant=%+v err=%v", targetGrant, err)
	}
	for _, prior := range []contract.LaunchCommand{reviewGrant} {
		if targetGrant.AttemptID == prior.AttemptID || targetGrant.GitFilter.ReservationID == prior.GitFilter.ReservationID || targetGrant.GitFilter.ScopeSHA256 == prior.GitFilter.ScopeSHA256 {
			t.Fatal("target observation reused review authority")
		}
	}
	var payload adapter.InvocationPayload
	manifestSHA := ""
	if err = json.Unmarshal(targetGrant.AdapterPayload, &payload); err == nil && payload.GitFilter != nil {
		manifestSHA, err = payload.GitFilter.Digest()
	}
	if err != nil || !adapter.IsFilterTargetPreflight(payload) || payload.Kind != "candidate" || payload.Provider != "" || payload.ProviderLock != nil || payload.Profile != nil || payload.Directory != filepath.Join(root, "preflight") || payload.CandidateAction.SourceTask != "review" || payload.CandidateAction.DependencyInputVersion != 1 || !payload.CandidateAction.AutoDirectory || payload.CandidateAction.Target != (adapter.CandidateTarget{Worktree: repo, Ref: "refs/heads/main", BaseOID: reviewed.Candidate.BaseOID}) || manifestSHA != targetGrant.GitFilter.ManifestSHA256 {
		t.Fatalf("target payload=%#v err=%v", payload, err)
	}
	if len(targetGrant.Input.AdapterPayload) == 0 || !reflect.DeepEqual(*targetGrant.Input, targetGrant.DependencyInputs.Inputs[0]) {
		t.Fatalf("target accepted input incomplete: input=%#v dependencies=%#v", targetGrant.Input, targetGrant.DependencyInputs)
	}

	guard.pending++
	invocation := filterInvocation{payload: payload}
	if len(invocation.Args()) != 0 || len(invocation.Stdin()) != 0 || len(invocation.Environment()) != 0 {
		t.Fatalf("target invocation gained external input: %#v", invocation)
	}
	path, pin := invocation.ExecutablePin()
	if path != "" || pin != "" {
		t.Fatalf("target invocation gained executable pin: path=%q pin=%q", path, pin)
	}
	result, err := h.ExecuteLaunch(ctx, targetGrant, invocation)
	if err != nil || result.Status != "result_ready" || result.ExitCode != -1 {
		t.Fatalf("target result=%+v err=%v", result, err)
	}
	filterTargetHostAssertPreserved(t, repo, baseline)
	body, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var delivered FilteredTargetPreflightDeliveryV1
	if err = contract.DecodeBridgeStrict(body, &delivered); err != nil {
		t.Fatalf("strict decode target delivery: %v", err)
	}
	reviewResult := filterPipelineResultEvent(t, reviewEvents)
	if delivered.Version != 1 || delivered.Stage != "filter_target_preflight" || delivered.Binding != filterReviewBinding(targetGrant) || delivered.InputEventID != reviewResult.EventID || delivered.InputSHA256 != reviewResult.Artifact.SHA256 || delivered.OwnerDecisionID != "accept-review" || !reflect.DeepEqual(delivered.Review, reviewed) || delivered.Target != payload.CandidateAction.Target {
		t.Fatalf("target delivery binding=%#v", delivered)
	}
	if delivered.Review.Binding != reviewed.Binding || delivered.Review.CandidateReceiptSHA256 != reviewed.CandidateReceiptSHA256 || delivered.Review.Candidate.Binding == nil || reviewed.Candidate.Binding == nil || delivered.Review.Candidate.Binding.TaskID != reviewed.Candidate.Binding.TaskID || delivered.Review.Validation == nil || reviewed.Validation == nil || delivered.Review.Validation.Binding != reviewed.Validation.Binding {
		t.Fatalf("target delivery altered accepted review provenance=%#v", delivered.Review)
	}
	observation := delivered.Observation
	if observation.Version != 1 || observation.AttemptID != targetGrant.AttemptID || observation.PlanRevision != targetGrant.PlanRevision || observation.TargetWorktree != repo || observation.TargetRef != "refs/heads/main" || observation.BaseOID != reviewed.Candidate.BaseOID || observation.CandidateOID != reviewed.Candidate.CandidateOID || observation.CandidateTreeOID != reviewed.Candidate.TreeOID || observation.FilterScopeSHA256 != targetGrant.GitFilter.ScopeSHA256 || observation.FilterReservationID != targetGrant.GitFilter.ReservationID || len(observation.BaseFilter.Views) != 1 || observation.BaseFilter.Views[0].Path != "file" || observation.BaseFilter.Views[0].CanonicalSHA256 != filterPipelineSHA(baseB) || observation.BaseFilter.Views[0].RenderedSHA256 != filterPipelineSHA(baseR) {
		t.Fatalf("target observation=%#v", observation)
	}

	events := validationCommitEvents(t, ctx, db, h, targetGrant)
	var prepared, resultEvent, exited *contract.Event
	for i := range events {
		event := &events[i]
		switch event.Kind {
		case contract.EventPrepared:
			prepared = event
		case contract.EventResult:
			resultEvent = event
		case contract.EventExited:
			exited = event
		case contract.EventSpawned, contract.EventRunning, contract.EventSession:
			t.Fatalf("target observation published process event: %#v", event)
		default:
			t.Fatalf("target observation published unexpected event: %#v", event)
		}
		if event.Process != nil || event.ExitCode != nil || event.SessionID != "" || event.SessionKind != "" {
			t.Fatalf("target observation retained process/session state: %#v", event)
		}
	}
	if prepared == nil || resultEvent == nil || exited == nil || len(events) != 3 || resultEvent.Artifact == nil || exited.ActiveMS <= 0 || exited.GitFilterCleanup == nil || exited.GitFilterCleanup.Validate() != nil || exited.GitFilterCleanup.ReservationID != targetGrant.GitFilter.ReservationID || exited.GitFilterCleanup.ScopeSHA256 != targetGrant.GitFilter.ScopeSHA256 || exited.GitFilterCleanup.RemovedBytes <= 0 {
		t.Fatalf("target event lifecycle=%#v", events)
	}
	guard.pending--

	readonly, err := sql.Open("sqlite", "file:"+filepath.Join(root, "state.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var state, release, cleanup string
	err = readonly.QueryRow("SELECT state,release_event_id,cleanup_json FROM git_filter_resource_reservations WHERE id=?", targetGrant.GitFilter.ReservationID).Scan(&state, &release, &cleanup)
	closeErr := readonly.Close()
	if err != nil || closeErr != nil || state != "released" || release != exited.EventID || cleanup == "" {
		t.Fatalf("target reservation state=%s release=%s err=%v close=%v", state, release, err, closeErr)
	}
	validationAccept(t, ctx, db, targetGrant)
	filterTargetHostAssertPreserved(t, repo, baseline)
	t.Logf("controlled_filtered_target_preflight original_host_callback=true provider_started=false complete_target_inventory_unchanged=true accepted_review_preserved=true active_ms=%d cleanup_bytes=%d root=%s", exited.ActiveMS, exited.GitFilterCleanup.RemovedBytes, root)
	if bytes.Equal(baseB, baseR) {
		t.Fatal("target filter fixture did not distinguish canonical and rendered base")
	}
	return targetGrant, delivered, events
}
