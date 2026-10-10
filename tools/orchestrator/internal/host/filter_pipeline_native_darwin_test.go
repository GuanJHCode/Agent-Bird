//go:build darwin

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

func TestFilterPipelineNativeHostAcceptedValidator(t *testing.T) {
	root, guard := filterPipelineRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	filterActor := filterPipelineBuildActor(t, ctx, root, guard)
	validatorActor, validatorPin := validationNativeActor(t, root, guard)
	gitPath := filterPipelineGitPath(t, ctx, guard)
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	filterPipelineGit(t, ctx, guard, gitPath, repo, "init", "-q", "-b", "main")
	filterPipelineGit(t, ctx, guard, gitPath, repo, "config", "user.name", "fixture")
	filterPipelineGit(t, ctx, guard, gitPath, repo, "config", "user.email", "fixture@example.invalid")
	baseB := []byte("candidate\n")
	if err := os.WriteFile(filepath.Join(repo, "file"), baseB, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filterPipelineManifest(t, ctx, guard, gitPath, filterActor)
	filterPipelineGit(t, ctx, guard, gitPath, repo, "config", "filter.sealed.clean", filterPipelineShell(filterActor, "xor"))
	filterPipelineGit(t, ctx, guard, gitPath, repo, "config", "filter.sealed.smudge", filterPipelineShell(filterActor, "xor"))
	filterPipelineGit(t, ctx, guard, gitPath, repo, "config", "filter.sealed.required", "false")
	attrs := []byte("file filter=sealed\n")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), attrs, 0600); err != nil {
		t.Fatal(err)
	}
	manifest.Attributes = []contract.GitFilterAttributePin{{Path: ".gitattributes", SHA256: filterPipelineSHA(attrs)}}
	manifest.ConfigSHA256 = gitops.FilterConfigurationSHA256(manifest)
	filterPipelineGitNoFilter(t, ctx, guard, gitPath, repo, "add", "file", ".gitattributes")
	filterPipelineGitNoFilter(t, ctx, guard, gitPath, repo, "commit", "-q", "-m", "base")
	base := filterPipelineGitNoFilter(t, ctx, guard, gitPath, repo, "rev-parse", "HEAD")

	sourcePayload := adapter.InvocationPayload{Kind: "provider", Provider: string(adapter.ProviderClaude), Directory: filepath.Join(root, "provider"), ProviderLock: &adapter.ProviderLock{Version: 1, Provider: adapter.ProviderClaude, Protocol: adapter.ProtocolID(adapter.ProviderClaude), Binary: adapter.BinaryPin{Path: validatorActor, SHA256: validatorPin, Version: "controlled-native"}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 300000}, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, RepoRoot: repo, BaseOID: base, Paths: []string{"file"}, AutoDirectory: true}, GitFilter: &manifest}
	validatePayload := adapter.InvocationPayload{Kind: "candidate", Directory: filepath.Join(root, "validate"), CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", SourceTask: "edit", DependencyInputVersion: 1, AutoDirectory: true, Command: []string{validatorActor, "validate"}, CommandSHA256: validatorPin}, GitFilter: &manifest}
	sourceRaw, _ := json.Marshal(sourcePayload)
	validateRaw, _ := json.Marshal(validatePayload)
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	receipt, err := db.SubmitPlan(ctx, store.PlanSpec{Run: store.RunSpec{ID: "filter-native-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "fixture"}, Host: store.HostLaunchSpec{OriginContextID: "test", HostGeneration: "fixture", Executable: "/private/controlled-host"}, Tasks: []store.TaskSpec{{ID: "edit", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 300000, AdapterPayload: sourceRaw}, {ID: "validate", RunID: "filter-native-run", MaxAttempts: 1, MaxActiveMS: 300000, Dependencies: []string{"edit"}, AdapterPayload: validateRaw}}})
	if err != nil {
		t.Fatal(err)
	}
	hostID, err := db.RegisterHost(ctx, contract.HostHello{LaunchID: receipt.LaunchID, LaunchToken: receipt.LaunchToken, OriginContextID: "test", OriginPID: os.Getpid(), OriginBirth: "fixture", HostGeneration: "fixture", PID: os.Getpid(), Birth: "host", Executable: "/private/controlled-host"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(db, filepath.Join(root, "host-spool", hostID))
	if err != nil {
		t.Fatal(err)
	}
	h.producerID = hostID
	sourceGrant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil || sourceGrant.TaskID != "edit" || sourceGrant.GitFilter == nil {
		t.Fatalf("source grant=%+v err=%v", sourceGrant, err)
	}
	guard.pending++
	result, err := h.ExecuteLaunch(ctx, sourceGrant, validationNativeInvocation{launchInvocation: launchInvocation{args: []string{validatorActor, "produce"}, dir: sourcePayload.Directory}, payload: sourcePayload})
	if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
		t.Fatalf("source result=%+v err=%v", result, err)
	}
	sourceBody, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var sourceDelivery CandidateDelivery
	if err = json.Unmarshal(sourceBody, &sourceDelivery); err != nil || sourceDelivery.Candidate.Filter == nil || sourceDelivery.Candidate.Binding == nil || sourceDelivery.Candidate.Binding.AttemptID != sourceGrant.AttemptID {
		t.Fatalf("source delivery=%#v err=%v", sourceDelivery, err)
	}
	desiredR := []byte("candidate\n")
	wantB := filterPipelineXOR(desiredR)
	if views := sourceDelivery.Candidate.Filter.Views; len(views) != 1 || views[0].Path != "file" || views[0].CanonicalSHA256 != filterPipelineSHA(wantB) || views[0].RenderedSHA256 != filterPipelineSHA(desiredR) {
		t.Fatalf("source filter=%#v", sourceDelivery.Candidate.Filter)
	}
	if got := filterPipelineGitBytes(t, ctx, guard, gitPath, repo, []string{"cat-file", "blob", sourceDelivery.Candidate.Changes[0].BlobOID}); !bytes.Equal(got, wantB) {
		t.Fatalf("source canonical=%q want=%q", got, wantB)
	}
	sourceEvents := validationCommitEvents(t, ctx, db, h, sourceGrant)
	if !filterPipelineExited(sourceEvents, sourceGrant) {
		t.Fatal("source original Host Exited proof missing")
	}
	guard.pending--
	validationAccept(t, ctx, db, sourceGrant)
	validatorGrant, err := db.ClaimReady(ctx, hostID, 1, 2)
	if err != nil || validatorGrant.TaskID != "validate" || validatorGrant.GitFilter == nil || validatorGrant.ValidationTool == nil || validatorGrant.GitFilter.ReservationID == sourceGrant.GitFilter.ReservationID || validatorGrant.GitFilter.ScopeSHA256 == sourceGrant.GitFilter.ScopeSHA256 {
		t.Fatalf("validator grant=%+v err=%v", validatorGrant, err)
	}
	guard.pending++
	result, err = h.ExecuteLaunch(ctx, validatorGrant, validationNativeInvocation{launchInvocation: launchInvocation{args: validatePayload.CandidateAction.Command, dir: validatePayload.Directory}, payload: validatePayload})
	if err != nil || result.Status != "result_ready" || result.ExitCode != 0 {
		t.Fatalf("validator result=%+v err=%v", result, err)
	}
	body, err := os.ReadFile(result.ArtifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var delivery CandidateDelivery
	if err = json.Unmarshal(body, &delivery); err != nil || delivery.Stage != "validate" || !reflect.DeepEqual(delivery.Target, gitops.ReviewBinding{}) || delivery.Candidate.Filter == nil || delivery.Validation == nil || !delivery.Validation.Passed || !reflect.DeepEqual(delivery.Candidate.Filter, sourceDelivery.Candidate.Filter) {
		t.Fatalf("validator delivery=%#v err=%v", delivery, err)
	}
	if view := delivery.Candidate.Filter.Views; len(view) != 1 || view[0].Path != "file" || view[0].CanonicalSHA256 != filterPipelineSHA(wantB) || view[0].RenderedSHA256 != filterPipelineSHA(desiredR) {
		t.Fatalf("filter evidence=%#v", delivery.Candidate.Filter)
	}
	validatorEvents := validationCommitEvents(t, ctx, db, h, validatorGrant)
	if !filterPipelineExited(validatorEvents, validatorGrant) {
		t.Fatal("validator original Host Exited proof missing")
	}
	filterPipelineReleasedReservations(t, filepath.Join(root, "state.db"), sourceGrant, filterPipelineExitedEvent(t, sourceEvents, sourceGrant), validatorGrant, filterPipelineExitedEvent(t, validatorEvents, validatorGrant))
	guard.pending--
	validationAccept(t, ctx, db, validatorGrant)
	t.Logf("controlled_filtered_host_validator original_wait_tree=true distinct_filter_grants=true root=%s", root)
}

func filterPipelineBuildActor(t *testing.T, ctx context.Context, root string, guard *validationNativeCleanup) string {
	t.Helper()
	actor := filepath.Join(root, "xor-filter")
	goPath := "/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go"
	guard.pending++
	h, err := process.Start(ctx, process.Command{Path: goPath, PinnedPath: goPath, PinnedSHA256: filterPipelineDigest(t, goPath), Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", actor, "./internal/gitfilter/testdata/actor"}, Dir: filterPipelineModuleRoot(t), ExactEnv: true, Env: []string{"PATH=" + filepath.Dir(goPath) + ":/usr/bin:/bin", "HOME=" + root, "GOCACHE=" + filepath.Join(root, "cache"), "GOPATH=" + filepath.Join(root, "gopath"), "GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "TMPDIR=/private/tmp"}})
	if err != nil || h == nil {
		if h != nil {
			validationNativeStop(h, guard)
		}
		t.Fatal(err)
	}
	if err = h.Wait(ctx); err != nil || h.ConfirmTreeExited() != nil || h.ExitCode() != 0 {
		validationNativeStop(h, guard)
		t.Fatalf("filter actor build err=%v exit=%d", err, h.ExitCode())
	}
	guard.pending--
	if err = os.Chmod(actor, 0500); err != nil {
		t.Fatal(err)
	}
	return actor
}

func filterPipelineManifest(t *testing.T, ctx context.Context, guard *validationNativeCleanup, gitPath, actor string) contract.GitFilterManifestV1 {
	t.Helper()
	version := filterPipelineGit(t, ctx, guard, gitPath, ".", "--version")
	return contract.GitFilterManifestV1{Version: 1, DriverID: "sealed", Paths: []string{"file"}, Git: contract.GitFilterGitPin{Path: gitPath, Version: version, SHA256: filterPipelineDigest(t, gitPath)}, Clean: contract.GitFilterToolPin{Path: actor, SHA256: filterPipelineDigest(t, actor), Args: []string{"xor"}}, Smudge: contract.GitFilterToolPin{Path: actor, SHA256: filterPipelineDigest(t, actor), Args: []string{"xor"}}}
}

func filterPipelineModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(dir, "..", ".."))
}

func filterPipelineRoot(t *testing.T) (string, *validationNativeCleanup) {
	t.Helper()
	parent := filepath.Join(filterPipelineModuleRoot(t), "..", "..", "tasks", "filter-host-native", "tmp")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(parent, "native-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	guard := &validationNativeCleanup{root: root, identity: identity}
	t.Cleanup(func() {
		removed, cleanupErr := guard.cleanup(t.Failed())
		if removed {
			t.Logf("owned_fixture_absent=%s", root)
			return
		}
		t.Logf("RETAINED native fixture root=%s pending=%d failed=%t cleanup_error=%v", root, guard.pending, t.Failed(), cleanupErr)
	})
	return root, guard
}

func filterPipelineGitPath(t *testing.T, ctx context.Context, guard *validationNativeCleanup) string {
	t.Helper()
	p := "/usr/bin/git"
	if runtime.GOOS == "darwin" {
		guard.pending++
		h, err := process.Start(ctx, process.Command{Path: "/usr/bin/xcrun", Args: []string{"--find", "git"}, ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin", "TMPDIR=/private/tmp"}})
		if err != nil || h == nil {
			if h != nil {
				validationNativeStop(h, guard)
			}
			t.Fatal(err)
		}
		if err = h.Wait(ctx); err != nil || h.ConfirmTreeExited() != nil || h.ExitCode() != 0 {
			validationNativeStop(h, guard)
			t.Fatal(err)
		}
		guard.pending--
		p = strings.TrimSpace(h.Output())
	}
	got, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func filterPipelineGit(t *testing.T, ctx context.Context, guard *validationNativeCleanup, gitPath, dir string, args ...string) string {
	return filterPipelineGitRun(t, ctx, guard, gitPath, dir, nil, args)
}
func filterPipelineGitNoFilter(t *testing.T, ctx context.Context, guard *validationNativeCleanup, gitPath, dir string, args ...string) string {
	prefix := []string{"--no-replace-objects", "-c", "filter.sealed.clean=", "-c", "filter.sealed.smudge=", "-c", "filter.sealed.process=", "-c", "filter.sealed.required=false"}
	return filterPipelineGitRun(t, ctx, guard, gitPath, dir, prefix, args)
}
func filterPipelineGitRun(t *testing.T, ctx context.Context, guard *validationNativeCleanup, gitPath, dir string, prefix, args []string) string {
	t.Helper()
	guard.pending++
	all := append(append([]string{}, prefix...), append([]string{"-C", dir}, args...)...)
	h, err := process.Start(ctx, process.Command{Path: gitPath, PinnedPath: gitPath, PinnedSHA256: filterPipelineDigest(t, gitPath), Args: all, ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "TMPDIR=/private/tmp"}})
	if err != nil || h == nil {
		if h != nil {
			validationNativeStop(h, guard)
		}
		t.Fatal(err)
	}
	if err = h.Wait(ctx); err != nil || h.ConfirmTreeExited() != nil || h.ExitCode() != 0 {
		validationNativeStop(h, guard)
		t.Fatalf("git %v err=%v exit=%d output=%s", args, err, h.ExitCode(), h.Output())
	}
	guard.pending--
	return strings.TrimSpace(h.Output())
}
func filterPipelineGitBytes(t *testing.T, ctx context.Context, guard *validationNativeCleanup, gitPath, dir string, args []string) []byte {
	t.Helper()
	guard.pending++
	h, err := process.Start(ctx, process.Command{Path: gitPath, PinnedPath: gitPath, PinnedSHA256: filterPipelineDigest(t, gitPath), Args: append([]string{"--no-replace-objects", "-c", "filter.sealed.clean=", "-c", "filter.sealed.smudge=", "-C", dir}, args...), ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "TMPDIR=/private/tmp"}})
	if err != nil || h == nil {
		if h != nil {
			validationNativeStop(h, guard)
		}
		t.Fatal(err)
	}
	if err = h.Wait(ctx); err != nil || h.ConfirmTreeExited() != nil || h.ExitCode() != 0 {
		validationNativeStop(h, guard)
		t.Fatalf("git bytes %v err=%v exit=%d", args, err, h.ExitCode())
	}
	guard.pending--
	return []byte(h.Output())
}
func filterPipelineDigest(t *testing.T, path string) string {
	t.Helper()
	digest, err := process.ExecutableDigest(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
func filterPipelineSHA(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func filterPipelineXOR(b []byte) []byte {
	out := append([]byte(nil), b...)
	for i := range out {
		out[i] ^= 0x5a
	}
	return out
}
func filterPipelineShell(path string, args ...string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "' " + strings.Join(args, " ")
}
func filterPipelineExited(events []contract.Event, grant contract.LaunchCommand) bool {
	for _, event := range events {
		if event.Kind == contract.EventExited && event.Process != nil && event.ExitCode != nil && *event.ExitCode == 0 && event.RunID == grant.RunID && event.TaskID == grant.TaskID && event.AttemptID == grant.AttemptID && event.SegmentID == grant.SegmentID && event.CommandID == grant.CommandID && event.GitFilterCleanup != nil && event.GitFilterCleanup.Validate() == nil && event.GitFilterCleanup.ReservationID == grant.GitFilter.ReservationID && event.GitFilterCleanup.ScopeSHA256 == grant.GitFilter.ScopeSHA256 && event.GitFilterCleanup.RemovedBytes > 0 {
			if grant.ValidationTool != nil && (event.ValidationToolCleanup == nil || event.ValidationToolCleanup.Validate() != nil || event.ValidationToolCleanup.ReservationID != grant.ValidationTool.ReservationID || event.ValidationToolCleanup.ScopeSHA256 != grant.ValidationTool.ScopeSHA256 || event.ValidationToolCleanup.RemovedBytes <= 0) {
				continue
			}
			return true
		}
	}
	return false
}

func filterPipelineExitedEvent(t *testing.T, events []contract.Event, grant contract.LaunchCommand) string {
	t.Helper()
	for _, event := range events {
		if event.Kind == contract.EventExited && event.EventID != "" && event.RunID == grant.RunID && event.TaskID == grant.TaskID && event.AttemptID == grant.AttemptID && event.SegmentID == grant.SegmentID && event.CommandID == grant.CommandID {
			return event.EventID
		}
	}
	t.Fatal("missing exited event")
	return ""
}

func filterPipelineReleasedReservations(t *testing.T, path string, source contract.LaunchCommand, sourceEvent string, validator contract.LaunchCommand, validatorEvent string) {
	t.Helper()
	readonly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	checkFilter := func(grant contract.LaunchCommand, eventID string) {
		var state, release, cleanup string
		err := readonly.QueryRow(`SELECT state,release_event_id,cleanup_json FROM git_filter_resource_reservations WHERE id=?`, grant.GitFilter.ReservationID).Scan(&state, &release, &cleanup)
		if err != nil || state != "released" || release != eventID || cleanup == "" {
			t.Fatalf("filter reservation=%s state=%s release=%s cleanup=%q err=%v", grant.GitFilter.ReservationID, state, release, cleanup, err)
		}
	}
	checkFilter(source, sourceEvent)
	checkFilter(validator, validatorEvent)
	var state, release, cleanup string
	err = readonly.QueryRow(`SELECT state,release_event_id,cleanup_json FROM validation_tool_reservations WHERE id=?`, validator.ValidationTool.ReservationID).Scan(&state, &release, &cleanup)
	if err != nil || state != "released" || release != validatorEvent || cleanup == "" {
		t.Fatalf("validation reservation=%s state=%s release=%s cleanup=%q err=%v", validator.ValidationTool.ReservationID, state, release, cleanup, err)
	}
}
