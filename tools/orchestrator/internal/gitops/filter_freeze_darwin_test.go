//go:build darwin

package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	in.AttemptID = "journal-mutated"
	in.Binding = &CandidateBinding{}
	in.Changes = nil
	in.OrderedInputOIDs = nil
	return j.rawFixtureJournal.PersistCandidateIntent(context.Background(), in)
}
func (j *freezeFixtureJournal) RecordCandidateFreeze(_ context.Context, in CandidateReceipt) error {
	j.receipt = cloneCandidateReceipt(in)
	in.AttemptID = "journal-mutated"
	in.Binding = &CandidateBinding{}
	in.Changes = nil
	in.OrderedInputOIDs = nil
	in.Files = nil
	in.Filter = nil
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
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	f, repo, source, broker, gitPath, base, request, prepared := prepareFreezeFixture(t, ctx, "a", "command", "filter")
	defer source.Close()
	defer prepared.Close()
	desiredR := []byte("provider result R\n")
	payload := filepath.Join(f.root, "provider-rendered")
	if err := os.WriteFile(payload, desiredR, 0600); err != nil {
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
	freezeJournal := &freezeFixtureJournal{rawFixtureJournal: f.journal}
	receipt, err := FreezeFilteredWorkspace(ctx, prepared, freezeJournal)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if receipt.BaseOID != base || receipt.CandidateOID == base || receipt.PrivateRef != "refs/orchestrator/g3/filter-freeze-native" || receipt.Filter == nil || len(receipt.Filter.Views) != 1 || len(receipt.Changes) != 1 {
		t.Fatalf("incomplete frozen receipt: %#v", receipt)
	}
	if freezeJournal.intent.AttemptID != request.AttemptID || len(freezeJournal.intent.Changes) != 1 || freezeJournal.receipt.AttemptID != receipt.AttemptID || freezeJournal.receipt.Filter == nil || freezeJournal.receipt.Files["a.txt"] == (FileIdentity{}) {
		t.Fatalf("journal did not receive immutable candidate copies: intent=%#v receipt=%#v", freezeJournal.intent, freezeJournal.receipt)
	}
	if parent := rawGit(t, f, ctx, gitPath, receipt.Worktree, "rev-parse", receipt.CandidateOID+"^"); parent != base {
		t.Fatalf("candidate parent=%s base=%s", parent, base)
	}
	if tree := rawGit(t, f, ctx, gitPath, receipt.Worktree, "rev-parse", receipt.CandidateOID+"^{tree}"); tree != receipt.TreeOID {
		t.Fatalf("candidate tree=%s receipt=%s", tree, receipt.TreeOID)
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
	validatorRequest.FilterScopeSHA256 = validatorScope
	validatorRequest.FilterReservationID = "validator-filter"
	materialized, err := MaterializeFilteredCandidate(ctx, source, validatorBroker, validatorRequest, receipt, validator.journal)
	if err != nil || materialized == nil {
		t.Fatalf("validator materialize: materialized=%v err=%v", materialized, err)
	}
	defer materialized.Close()
	if got, readErr := os.ReadFile(filepath.Join(validatorRequest.Worktree, "a.txt")); readErr != nil || !bytes.Equal(got, desiredR) {
		t.Fatalf("validator rendered view=%q err=%v", got, readErr)
	}
	if err := materialized.Verify(ctx); err != nil {
		t.Fatalf("validator live verify: %v", err)
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
	_, err := FreezeFilteredWorkspace(ctx, prepared, &memoryJournal{})
	if err == nil {
		t.Fatal("freeze accepted undeclared README")
	}
	if got := rawGit(t, f, ctx, gitPath, request.Worktree, "rev-parse", "HEAD"); got != base {
		t.Fatalf("rejected freeze changed HEAD=%s base=%s", got, base)
	}
	rawGitExit(t, f, ctx, gitPath, repo, 1, "rev-parse", "--verify", "refs/orchestrator/g3/filter-freeze-negative")
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
	if waitErr != nil || treeErr != nil || h.ExitCode() != want {
		if treeErr != nil {
			f.retain = true
		}
		t.Fatalf("git expected exit=%d wait=%v tree=%v got=%d output=%s", want, waitErr, treeErr, h.ExitCode(), h.Output())
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
