//go:build darwin

package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitfilter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

type freezeFixtureJournal struct {
	*rawFixtureJournal
	intent  CandidateIntent
	receipt CandidateReceipt
}

func (j *freezeFixtureJournal) PersistCandidateIntent(_ context.Context, in CandidateIntent) error {
	j.intent = cloneCandidateIntent(in)
	in.Binding.RunID = "journal-mutated"
	in.Changes[0].Path = "journal-mutated"
	in.OrderedInputOIDs[0] = "journal-mutated"
	in.Filter.Views[0].Path = "journal-mutated"
	return j.rawFixtureJournal.PersistCandidateIntent(context.Background(), in)
}
func (j *freezeFixtureJournal) RecordCandidateFreeze(_ context.Context, in CandidateReceipt) error {
	j.receipt = cloneCandidateReceipt(in)
	in.Binding.RunID = "journal-mutated"
	in.Changes[0].Path = "journal-mutated"
	in.OrderedInputOIDs[0] = "journal-mutated"
	in.Files["a.txt"] = FileIdentity{}
	in.Filter.Views[0].Path = "journal-mutated"
	return j.rawFixtureJournal.RecordCandidateFreeze(context.Background(), in)
}
func cloneCandidateIntent(in CandidateIntent) CandidateIntent {
	var out CandidateIntent
	b, _ := json.Marshal(in)
	_ = json.Unmarshal(b, &out)
	return out
}
func cloneCandidateReceipt(in CandidateReceipt) CandidateReceipt {
	var out CandidateReceipt
	b, _ := json.Marshal(in)
	_ = json.Unmarshal(b, &out)
	return out
}

// TestFilterFreezeNativeFixture uses real Git, real ResourceBrokers, and an
// original process writer. It covers the Host protocol after its writer has
// completed, and separately proves an undeclared edit is refused.
func TestFilterFreezeNativeFixture(t *testing.T) {
	t.Run("freeze_canonical_candidate_and_reconstruct_rendered_view", func(t *testing.T) {
		freezeNativeSuccess(t)
	})
	t.Run("undeclared_file_refuses_before_private_ref_publication", func(t *testing.T) {
		freezeNativeUndeclared(t)
	})
}

func freezeNativeSuccess(t *testing.T) {
	// Includes the additional delete/restore source-ref verification cycle.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	f, repo, source, broker, gitPath, base, request, prepared := prepareFreezeFixture(t, ctx, "a", "command", "filter")
	defer source.Close()
	defer prepared.Close()
	desiredR := []byte("provider result R\n")
	payload := filepath.Join(f.root, "provider-rendered")
	if err := os.WriteFile(payload, desiredR, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(payload, 0644); err != nil {
		t.Fatal(err)
	}
	workspace := prepared.Workspace()
	if workspace.Receipt.Filter == nil || len(workspace.Receipt.Filter.Views) != 1 {
		t.Fatalf("prepared filter evidence: %#v", workspace)
	}
	workspace.Paths[0] = "README"
	workspace.Receipt.Files["a.txt"] = FileIdentity{}
	workspace.Receipt.Filter.Views[0].Path = "journal-mutated"
	if again := prepared.Workspace(); again.Paths[0] != "a.txt" || again.Receipt.Files["a.txt"] == (FileIdentity{}) || again.Receipt.Filter.Views[0].Path != "a.txt" {
		t.Fatal("Workspace returned authority rather than a deep clone")
	}
	copyProviderFile(t, f, ctx, payload, filepath.Join(request.Worktree, "a.txt"))
	if info, statErr := os.Stat(filepath.Join(request.Worktree, "a.txt")); statErr != nil {
		t.Fatalf("atomic provider replacement stat: %v", statErr)
	} else if info.Mode().Perm() != 0644 {
		t.Fatalf("atomic provider replacement mode=%v", info.Mode())
	}
	freezeJournal := &freezeFixtureJournal{rawFixtureJournal: f.journal}
	receipt, err := FreezeFilteredWorkspace(ctx, prepared, freezeJournal)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if receipt.BaseOID != base || receipt.CandidateOID == base || receipt.PrivateRef != "refs/orchestrator/g3/filter-freeze-native" || receipt.Filter == nil || len(receipt.Filter.Views) != 1 || len(receipt.Changes) != 1 || receipt.Changes[0].Path != "a.txt" || receipt.Changes[0].Mode != "100644" {
		t.Fatalf("incomplete frozen receipt: %#v", receipt)
	}
	if err := receipt.Filter.ValidateBinding(source.manifest, source.SHA256()); err != nil || receipt.Filter.Views[0].Path != "a.txt" || receipt.Filter.Views[0].CanonicalSHA256 != rawSHA(xorRaw(desiredR)) || receipt.Filter.Views[0].RenderedSHA256 != rawSHA(desiredR) {
		t.Fatalf("frozen filter evidence err=%v evidence=%#v", err, receipt.Filter)
	}
	if freezeJournal.intent.AttemptID != request.AttemptID || !reflect.DeepEqual(freezeJournal.intent.Binding, receipt.Binding) || !reflect.DeepEqual(freezeJournal.intent.Changes, receipt.Changes) || !reflect.DeepEqual(freezeJournal.intent.OrderedInputOIDs, receipt.OrderedInputOIDs) || !reflect.DeepEqual(freezeJournal.intent.Filter, receipt.Filter) || !reflect.DeepEqual(freezeJournal.receipt, receipt) {
		t.Fatalf("journal did not receive immutable candidate copies: intent=%#v receipt=%#v", freezeJournal.intent, freezeJournal.receipt)
	}
	if parent := rawGit(t, f, ctx, gitPath, receipt.Worktree, "rev-parse", receipt.CandidateOID+"^"); parent != base {
		t.Fatalf("candidate parent=%s base=%s", parent, base)
	}
	if tree := rawGit(t, f, ctx, gitPath, receipt.Worktree, "rev-parse", receipt.CandidateOID+"^{tree}"); tree != receipt.TreeOID {
		t.Fatalf("candidate tree=%s receipt=%s", tree, receipt.TreeOID)
	}
	if entry := strings.Fields(rawGit(t, f, ctx, gitPath, receipt.Worktree, "ls-tree", receipt.CandidateOID, "--", "a.txt")); len(entry) != 4 || entry[0] != "100644" || entry[1] != "blob" || entry[2] != receipt.Changes[0].BlobOID || entry[3] != "a.txt" {
		t.Fatalf("candidate tree entry=%q receipt change=%#v", entry, receipt.Changes[0])
	}
	if ref := rawGit(t, f, ctx, gitPath, repo, "rev-parse", receipt.PrivateRef); ref != receipt.CandidateOID {
		t.Fatalf("private ref=%s candidate=%s", ref, receipt.CandidateOID)
	}
	var canonical bytes.Buffer
	gitCtx := source.Context(WithCandidateJournal(ctx, f.journal))
	if err := candidateGitStream(gitCtx, repo, &canonical, "cat-file", "blob", receipt.Changes[0].BlobOID); err != nil {
		t.Fatal(err)
	}
	if want := xorRaw(desiredR); !bytes.Equal(canonical.Bytes(), want) {
		t.Fatalf("canonical blob=%q want=%q", canonical.Bytes(), want)
	}
	if got, readErr := os.ReadFile(filepath.Join(receipt.Worktree, "a.txt")); readErr != nil || !bytes.Equal(got, desiredR) {
		t.Fatalf("provider worktree rendered view=%q err=%v", got, readErr)
	}
	artifact, err := CandidateArtifact("fixture", receipt)
	if err != nil || !bytes.Contains(artifact, []byte("filter")) || !bytes.Contains(artifact, []byte(receipt.Filter.Views[0].CanonicalSHA256)) || !bytes.Contains(artifact, []byte(receipt.Filter.Views[0].RenderedSHA256)) {
		t.Fatalf("candidate artifact evidence err=%v body=%s", err, artifact)
	}
	if broker.State().Unknown {
		f.retain = true
		t.Fatal("provider broker unknown")
	}

	validator := newRawFixture(t)
	validator.journal = &rawFixtureJournal{}
	validatorBroker, validatorScope := freezeBroker(t, validator, ctx, source.manifest, "validator", "validate-command", "validator-filter")
	validatorRequest := request
	validatorRequest.AttemptID = "validator"
	validatorRequest.Worktree = filepath.Join(validator.root, "validator-worktree")
	validatorRequest.BaseOID = receipt.BaseOID
	validatorRequest.CandidateOID = receipt.CandidateOID
	validatorRequest.OrderedInputOIDs = append([]string(nil), receipt.OrderedInputOIDs...)
	validatorRequest.PlanRevision = receipt.PlanRevision
	validatorRequest.FilterScopeSHA256 = validatorScope
	validatorRequest.FilterReservationID = "validator-filter"
	beforeRecords := len(validator.journal.records)
	for _, kind := range []string{"same_attempt", "rework", "new_base", "input_vector", "empty_changes"} {
		badRequest, badReceipt := validatorRequest, cloneCandidateReceipt(receipt)
		switch kind {
		case "same_attempt":
			badRequest.AttemptID = receipt.AttemptID
		case "rework":
			badReceipt.Binding.RevisionSHA256 = strings.Repeat("a", 64)
		case "new_base":
			badReceipt.Binding.NewBaseSHA256 = strings.Repeat("b", 64)
		case "input_vector":
			badReceipt.OrderedInputOIDs = append(badReceipt.OrderedInputOIDs, base)
			badRequest.OrderedInputOIDs = append([]string(nil), badReceipt.OrderedInputOIDs...)
		case "empty_changes":
			badReceipt.Changes = nil
		}
		live, e := MaterializeFilteredCandidate(ctx, source, validatorBroker, badRequest, badReceipt, validator.journal)
		if live != nil {
			live.Close()
			t.Fatalf("%s unexpectedly materialized", kind)
		}
		if !IsCode(e, CodeReviewMismatch) {
			t.Fatalf("%s err=%v", kind, e)
		}
	}
	if len(validator.journal.records) != beforeRecords || validatorBroker.State().EverStarted {
		t.Fatal("invalid validator shape reached Git/filter")
	}
	if _, e := os.Lstat(validatorRequest.Worktree); !os.IsNotExist(e) {
		t.Fatalf("invalid validator created worktree: %v", e)
	}
	materialized, err := MaterializeFilteredCandidate(ctx, source, validatorBroker, validatorRequest, receipt, validator.journal)
	if err != nil || materialized == nil {
		t.Fatalf("validator materialize: materialized=%v err=%v", materialized, err)
	}
	defer materialized.Close()
	if got, readErr := os.ReadFile(filepath.Join(validatorRequest.Worktree, "a.txt")); readErr != nil || !bytes.Equal(got, desiredR) {
		t.Fatalf("validator rendered view=%q err=%v", got, readErr)
	}
	providerCommand(t, validator, ctx, "/usr/bin/cmp", payload, filepath.Join(validatorRequest.Worktree, "a.txt"))
	if err := materialized.Verify(ctx); err != nil {
		t.Fatalf("validator live verify: %v", err)
	}
	rawGit(t, f, ctx, gitPath, repo, "update-ref", "--no-deref", "-d", receipt.PrivateRef, receipt.CandidateOID)
	if err := materialized.Verify(ctx); err == nil {
		t.Fatal("validator accepted deleted source private ref")
	}
	rawGit(t, f, ctx, gitPath, repo, "update-ref", "--no-deref", receipt.PrivateRef, receipt.CandidateOID, strings.Repeat("0", len(receipt.CandidateOID)))
	if ref := rawGit(t, f, ctx, gitPath, repo, "rev-parse", receipt.PrivateRef); ref != receipt.CandidateOID {
		t.Fatalf("restored private ref=%s candidate=%s", ref, receipt.CandidateOID)
	}
	if err := materialized.Verify(ctx); err != nil {
		t.Fatalf("validator live verify after source ref restore: %v", err)
	}
	t.Log("validator live verify rejects deleted source private ref and accepts exact restore")
	if validated := materialized.Receipt(); !reflect.DeepEqual(validated.Filter, receipt.Filter) {
		t.Fatalf("validator filter evidence=%#v accepted=%#v", validated.Filter, receipt.Filter)
	}
	if err := os.WriteFile(filepath.Join(validatorRequest.Worktree, "a.txt"), []byte("later change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := materialized.Verify(ctx); err == nil {
		t.Fatal("validator accepted changed rendered view")
	}
}

func freezeNativeUndeclared(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	f, repo, source, _, gitPath, base, request, prepared := prepareFreezeFixture(t, ctx, "negative", "negative-command", "negative-filter")
	defer source.Close()
	defer prepared.Close()
	if err := os.WriteFile(filepath.Join(request.Worktree, "README"), []byte("undeclared\n"), 0600); err != nil {
		t.Fatal(err)
	}
	freezeJournal := &freezeFixtureJournal{rawFixtureJournal: f.journal}
	_, err := FreezeFilteredWorkspace(ctx, prepared, freezeJournal)
	if !IsCode(err, CodeUnknownObject) && !IsCode(err, CodeIdentityChanged) {
		t.Fatalf("freeze undeclared README err=%v", err)
	}
	if len(freezeJournal.candidateIntents) != 0 || len(freezeJournal.candidateRefIntents) != 0 || len(freezeJournal.candidateReceipts) != 0 {
		t.Fatalf("undeclared freeze persisted candidate records: intents=%d refs=%d receipts=%d", len(freezeJournal.candidateIntents), len(freezeJournal.candidateRefIntents), len(freezeJournal.candidateReceipts))
	}
	if got := rawGit(t, f, ctx, gitPath, request.Worktree, "rev-parse", "HEAD"); got != base {
		t.Fatalf("rejected freeze changed HEAD=%s base=%s", got, base)
	}
	rawGitExit(t, f, ctx, gitPath, repo, 1, "rev-parse", "--verify", "--quiet", "refs/orchestrator/g3/filter-freeze-negative")
}

func prepareFreezeFixture(t *testing.T, ctx context.Context, attempt, command, reservation string) (*rawFixture, string, *FilterGitContext, gitfilter.ResourceBroker, string, string, MaterializeRequest, *FilterPreparedWorkspace) {
	t.Helper()
	f := newRawFixture(t)
	if err := os.MkdirAll(filepath.Join(f.root, "repo"), 0700); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(f.root, "repo")
	tool := buildRawActor(t, f)
	gitPath := filterGitPath(t)
	rawGit(t, f, ctx, gitPath, repo, "init", "-q", "-b", "main")
	rawGit(t, f, ctx, gitPath, repo, "config", "user.name", "fixture")
	rawGit(t, f, ctx, gitPath, repo, "config", "user.email", "fixture@example.invalid")
	canonical := []byte("canonical B\n")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), canonical, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("control\n"), 0600); err != nil {
		t.Fatal(err)
	}
	version := rawGit(t, f, ctx, gitPath, repo, "--version")
	manifest := contract.GitFilterManifestV1{Version: 1, DriverID: "sealed", Paths: []string{"a.txt"}, Git: contract.GitFilterGitPin{Path: gitPath, Version: version, SHA256: filterFileSHA256(t, gitPath)}, Clean: contract.GitFilterToolPin{Path: tool.Path, SHA256: tool.SHA256, Args: []string{"xor"}}, Smudge: contract.GitFilterToolPin{Path: tool.Path, SHA256: tool.SHA256, Args: []string{"xor"}}}
	manifest = rawConfigure(t, f, ctx, gitPath, repo, manifest)
	rawGit(t, f, ctx, gitPath, repo, "add", "a.txt", "README", ".gitattributes")
	rawGit(t, f, ctx, gitPath, repo, "commit", "-q", "-m", "fixture")
	base := rawGit(t, f, ctx, gitPath, repo, "rev-parse", "HEAD")
	manifest = rawConfigure(t, f, ctx, gitPath, repo, manifest)

	source, err := OpenFilterGit(ctx, repo, manifest)
	if err != nil {
		t.Fatal(err)
	}
	broker, scope := freezeBroker(t, f, ctx, manifest, attempt, command, reservation)
	f.journal = &rawFixtureJournal{}
	request := MaterializeRequest{RepoRoot: repo, Worktree: filepath.Join(f.root, "provider-worktree"), AttemptID: attempt, BaseOID: base, CandidateOID: base, OrderedInputOIDs: []string{base}, PlanRevision: 1, FilterScopeSHA256: scope, FilterReservationID: reservation}
	binding := CandidateBinding{RunID: "run", TaskID: "task", AttemptID: attempt, SegmentID: "s", WorkRevision: 1, PlanRevision: 1}
	ref := "refs/orchestrator/g3/filter-freeze-native"
	if attempt != "a" {
		ref = "refs/orchestrator/g3/filter-freeze-negative"
	}
	prepared, err := PrepareFilteredWorkspace(ctx, source, broker, request, binding, []string{"a.txt"}, ref, f.journal)
	if err != nil || prepared == nil {
		_ = source.Close()
		t.Fatalf("prepare live filtered workspace: prepared=%v err=%v", prepared, err)
	}
	return f, repo, source, broker, gitPath, base, request, prepared
}

func freezeBroker(t *testing.T, f *rawFixture, ctx context.Context, manifest contract.GitFilterManifestV1, attempt, command, reservation string) (gitfilter.ResourceBroker, string) {
	t.Helper()
	segment := filepath.Join(f.root, "segments", attempt, "s")
	spool, err := events.Open(segment)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	launch := contract.LaunchCommand{RunID: "run", TaskID: "task", AttemptID: attempt, SegmentID: "s", HostID: "host", CommandID: command, WorkRevision: 1, PlanRevision: 1}
	hostScope, err := contract.HostArtifactScopeSHA256(launch)
	if err != nil {
		t.Fatal(err)
	}
	launch.HostArtifacts = &contract.HostArtifactGrant{Version: 1, TerminalReservationID: "terminal-" + attempt, ProgressReservationID: "progress-" + attempt, ScopeSHA256: hostScope}
	scope, err := contract.GitFilterResourceScopeSHA256(launch, digest)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := gitfilter.OpenBroker(ctx, gitfilter.BrokerRequest{Launch: launch, Grant: contract.GitFilterResourceGrant{Version: 1, ReservationID: reservation, ScopeSHA256: scope, ManifestSHA256: digest, CleanToolSHA256: manifest.Clean.SHA256, SmudgeToolSHA256: manifest.Smudge.SHA256, ToolMaximumBytes: contract.GitFilterToolMaximumBytes, StreamMaximumBytes: contract.GitFilterStreamMaximumBytes, StreamSlots: 3}, Manifest: manifest, SegmentDirectory: segment, Spool: spool})
	if broker != nil {
		f.broker = broker
	}
	if err != nil {
		if broker != nil && broker.State().Unknown {
			f.retain = true
		}
		t.Fatal(err)
	}
	return broker, scope
}

func rawGitExit(t *testing.T, f *rawFixture, ctx context.Context, gitPath, dir string, want int, args ...string) {
	t.Helper()
	sha, err := process.ExecutableDigest(ctx, gitPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := process.Start(ctx, process.Command{Path: gitPath, PinnedPath: gitPath, PinnedSHA256: sha, Args: append(filterGitConfigArgs("sealed"), append([]string{"-C", dir}, args...)...), ExactEnv: true, Env: filterGitEnv()})
	if h == nil {
		t.Fatal(err)
	}
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cleanupErr := process.CleanupFailedStart(cleanupCtx, h, 10*time.Second)
		cancel()
		if cleanupErr != nil {
			f.retain = true
		}
		t.Fatalf("git expected exit start=%v cleanup=%v", err, cleanupErr)
	}
	waitErr := h.Wait(ctx)
	treeErr := h.ConfirmTreeExited()
	if waitErr != nil || treeErr != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		stopErr := h.Stop(stopCtx)
		cancel()
		treeErr = h.ConfirmTreeExited()
		if stopErr != nil || treeErr != nil {
			f.retain = true
		}
		t.Fatalf("git expected exit=%d wait=%v stop=%v tree=%v got=%d output=%s", want, waitErr, stopErr, treeErr, h.ExitCode(), h.Output())
	}
	if h.ExitCode() != want {
		t.Fatalf("git expected exit=%d got=%d output=%s", want, h.ExitCode(), h.Output())
	}
	f.gitWaits++
	t.Logf("expected_git_exit_original_wait=0 exit=%d tree=0", want)
}

func copyProviderFile(t *testing.T, f *rawFixture, ctx context.Context, from, to string) {
	t.Helper()
	temporary := to + ".provider-tmp"
	providerCommand(t, f, ctx, "/bin/cp", from, temporary)
	providerCommand(t, f, ctx, "/bin/mv", temporary, to)
	t.Logf("provider_writer_original_wait=0 exit=0 tree=0")
}

func providerCommand(t *testing.T, f *rawFixture, ctx context.Context, path string, args ...string) {
	t.Helper()
	sha, err := process.ExecutableDigest(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := process.Start(ctx, process.Command{Path: path, PinnedPath: path, PinnedSHA256: sha, Args: args, ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TMPDIR=/private/tmp"}})
	if h == nil {
		t.Fatal(err)
	}
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cleanupErr := process.CleanupFailedStart(cleanupCtx, h, 10*time.Second)
		cancel()
		if cleanupErr != nil {
			f.retain = true
		}
		t.Fatalf("provider writer start=%v cleanup=%v", err, cleanupErr)
	}
	rawFixtureWait(t, f, ctx, h)
}
