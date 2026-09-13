package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type memoryJournal struct {
	intents           []IntegrationIntent
	outcomes          []IntegrationOutcome
	cleanupIntents    []CleanupIntent
	cleanupOutcomes   []CleanupOutcome
	candidateIntents  []CandidateIntent
	candidateReceipts []CandidateReceipt
}

func (m *memoryJournal) PersistCandidateIntent(_ context.Context, in CandidateIntent) error {
	m.candidateIntents = append(m.candidateIntents, in)
	return nil
}
func (m *memoryJournal) RecordCandidateFreeze(_ context.Context, in CandidateReceipt) error {
	m.candidateReceipts = append(m.candidateReceipts, in)
	return nil
}

func (m *memoryJournal) PersistIntegrationIntent(_ context.Context, in IntegrationIntent) error {
	m.intents = append(m.intents, in)
	return nil
}
func (m *memoryJournal) RecordIntegration(_ context.Context, out IntegrationOutcome) error {
	m.outcomes = append(m.outcomes, out)
	return nil
}
func (m *memoryJournal) PersistCleanupIntent(_ context.Context, in CleanupIntent) error {
	m.cleanupIntents = append(m.cleanupIntents, in)
	return nil
}
func (m *memoryJournal) RecordCleanup(_ context.Context, out CleanupOutcome) error {
	m.cleanupOutcomes = append(m.cleanupOutcomes, out)
	return nil
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func gitQuiet(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
func repo(t *testing.T) (string, string) {
	t.Helper()
	d := t.TempDir()
	git(t, d, "init", "-q", "-b", "main")
	git(t, d, "config", "user.email", "test@example.invalid")
	git(t, d, "config", "user.name", "G3 Test")
	if err := os.WriteFile(filepath.Join(d, "README"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, d, "add", "README")
	git(t, d, "commit", "-qm", "base")
	return d, git(t, d, "rev-parse", "HEAD")
}
func candidate(t *testing.T, d string) (string, string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(d, "candidate.txt"), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, d, "add", "candidate.txt")
	git(t, d, "commit", "-qm", "candidate")
	oid := git(t, d, "rev-parse", "HEAD")
	return oid, git(t, d, "rev-parse", oid+"^{tree}")
}
func reviewFor(t *testing.T, d, base, candidate string) ReviewBinding {
	t.Helper()
	identity, err := directoryIdentity(d)
	if err != nil {
		t.Fatal(err)
	}
	return ReviewBinding{TargetWorktreeIdentity: CanonicalPath(d), TargetRef: "refs/heads/main", TargetBaseOID: base,
		TargetDirectoryIdentity: identity, FinalCandidateOID: candidate, OrderedInputOIDs: []string{candidate}, ValidationDigest: "validation-sha", PlanRevision: 4, ReviewRevision: 9}
}

func mustDirectoryIdentity(t *testing.T, d string) DirectoryIdentity {
	t.Helper()
	identity, err := directoryIdentity(d)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestMaterializeCandidateBindsOIDTreeAndDependencies(t *testing.T) {
	d, base := repo(t)
	candidateOID, tree := candidate(t, d)
	work := filepath.Join(t.TempDir(), "candidate-worktree")
	j := &memoryJournal{}
	got, err := Materialize(context.Background(), MaterializeRequest{RepoRoot: d, Worktree: work, AttemptID: "a-1", BaseOID: base, CandidateOID: candidateOID, OrderedInputOIDs: []string{candidateOID}, PlanRevision: 4}, j)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitOID != candidateOID || got.TreeOID != tree || got.AttemptID != "a-1" {
		t.Fatalf("receipt=%+v", got)
	}
	if got.CommonDir == "" || len(got.WorktreeIdentity) == 0 || len(j.intents) != 1 {
		t.Fatalf("materialization journal missing: %+v", j)
	}
	if git(t, work, "rev-parse", "HEAD") != candidateOID {
		t.Fatal("materialized worktree is not candidate")
	}
}

func TestIntegrateRequiresReviewBindingAndUsesProtectedFastForward(t *testing.T) {
	d, base := repo(t)
	candidateOID, _ := candidate(t, d)
	git(t, d, "reset", "--hard", base)
	j := &memoryJournal{}
	_, err := Integrate(context.Background(), IntegrationRequest{RepoRoot: d, TargetWorktree: d, TargetRef: "refs/heads/main", TargetBaseOID: base,
		CandidateOID: candidateOID, Review: ReviewBinding{TargetWorktreeIdentity: CanonicalPath(d), TargetRef: "refs/heads/main", TargetBaseOID: base, TargetDirectoryIdentity: mustDirectoryIdentity(t, d), FinalCandidateOID: "wrong", PlanRevision: 4, ReviewRevision: 9}, Journal: j})
	if err == nil || !IsCode(err, CodeReviewMismatch) {
		t.Fatalf("err=%v", err)
	}
	if len(j.intents) != 0 {
		t.Fatal("review mismatch persisted integration intent")
	}

	j = &memoryJournal{}
	out, err := Integrate(context.Background(), IntegrationRequest{RepoRoot: d, TargetWorktree: d, TargetRef: "refs/heads/main", TargetBaseOID: base,
		CandidateOID: candidateOID, Review: reviewFor(t, d, base, candidateOID), Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	if out.State != IntegrationIntegrated || git(t, d, "rev-parse", "HEAD") != candidateOID || len(j.outcomes) != 1 {
		t.Fatalf("out=%+v journal=%+v", out, j)
	}
}

func TestIntegrateRejectsTrackedDirtyTreeAndIgnoredCollisionWithoutChangingTarget(t *testing.T) {
	d, base := repo(t)
	candidateOID, _ := candidate(t, d)
	git(t, d, "reset", "--hard", base)
	// Move target back to base in a second worktree so candidate remains reachable.
	target := filepath.Join(t.TempDir(), "target")
	git(t, d, "worktree", "add", "-q", "-b", "target-dirty", target, base)
	defer git(t, d, "worktree", "remove", "--force", target)
	if err := os.WriteFile(filepath.Join(target, "README"), []byte("local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	j := &memoryJournal{}
	out, err := Integrate(context.Background(), IntegrationRequest{RepoRoot: d, TargetWorktree: target, TargetRef: "refs/heads/target-dirty", TargetBaseOID: base,
		CandidateOID: candidateOID, Review: ReviewBinding{TargetWorktreeIdentity: CanonicalPath(target), TargetRef: "refs/heads/target-dirty", TargetBaseOID: base, TargetDirectoryIdentity: mustDirectoryIdentity(t, target), FinalCandidateOID: candidateOID, OrderedInputOIDs: []string{candidateOID}, ValidationDigest: "validation-sha", PlanRevision: 4, ReviewRevision: 9}, Journal: j})
	if err == nil || !IsCode(err, CodeTargetDirty) || out.State != IntegrationRejected {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if len(j.intents) != 0 {
		t.Fatal("dirty target persisted integration intent")
	}
	if got := git(t, target, "rev-parse", "HEAD"); got != base {
		t.Fatalf("target changed: %s", got)
	}

	// A candidate path colliding with an ignored file is rejected before merge.
	clean := filepath.Join(t.TempDir(), "clean")
	git(t, d, "worktree", "add", "-q", "-b", "target-ignore", clean, base)
	defer git(t, d, "worktree", "remove", "--force", clean)
	if err := os.WriteFile(filepath.Join(clean, ".gitignore"), []byte("candidate.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, clean, "add", ".gitignore")
	git(t, clean, "commit", "-qm", "ignore")
	base2 := git(t, clean, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(clean, "candidate.txt"), []byte("sentinel\n"), 0600); err != nil {
		t.Fatal(err)
	}
	j = &memoryJournal{}
	out, err = Integrate(context.Background(), IntegrationRequest{RepoRoot: d, TargetWorktree: clean, TargetRef: "refs/heads/target-ignore", TargetBaseOID: base2,
		CandidateOID: candidateOID, Review: ReviewBinding{TargetWorktreeIdentity: CanonicalPath(clean), TargetRef: "refs/heads/target-ignore", TargetBaseOID: base2, TargetDirectoryIdentity: mustDirectoryIdentity(t, clean), FinalCandidateOID: candidateOID, OrderedInputOIDs: []string{candidateOID}, ValidationDigest: "validation-sha", PlanRevision: 4, ReviewRevision: 9}, Journal: j})
	if err == nil || !IsCode(err, CodeCollision) || out.State != IntegrationRejected {
		t.Fatalf("ignored collision err=%v out=%+v", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(clean, "candidate.txt")); string(got) != "sentinel\n" {
		t.Fatal("ignored sentinel changed")
	}
}

func TestIntegrateTargetDriftIsUnknownAndDoesNotRetry(t *testing.T) {
	d, base := repo(t)
	candidateOID, _ := candidate(t, d)
	git(t, d, "reset", "--hard", base)
	j := &memoryJournal{}
	before := func() error { git(t, d, "switch", "-c", "drift"); return nil }
	out, err := Integrate(context.Background(), IntegrationRequest{RepoRoot: d, TargetWorktree: d, TargetRef: "refs/heads/main", TargetBaseOID: base,
		CandidateOID: candidateOID, Review: reviewFor(t, d, base, candidateOID), Journal: j, BeforeMerge: before})
	if err == nil || !IsCode(err, CodeTargetDrift) || out.State != IntegrationUncertain {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if len(j.outcomes) != 1 || j.outcomes[0].State != IntegrationUncertain {
		t.Fatalf("journal=%+v", j.outcomes)
	}
}

func TestCleanupRetainsExternalSentinelAndRemovesOwnedWorktreeWhenClean(t *testing.T) {
	d, base := repo(t)
	candidateOID, _ := candidate(t, d)
	work := filepath.Join(t.TempDir(), "worktree")
	receipt, err := Materialize(context.Background(), MaterializeRequest{RepoRoot: d, Worktree: work, AttemptID: "a-2", BaseOID: base, CandidateOID: candidateOID, OrderedInputOIDs: []string{candidateOID}, PlanRevision: 4}, &memoryJournal{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "external-sentinel"), []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	j := &memoryJournal{}
	out, err := Cleanup(context.Background(), CleanupRequest{Root: work, Receipt: receipt, Journal: j})
	if err == nil || !IsCode(err, CodeUnknownObject) || out.State != CleanupPending {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if _, err := os.Stat(filepath.Join(work, "external-sentinel")); err != nil {
		t.Fatal("sentinel was removed")
	}
	if len(j.cleanupIntents) != 1 || len(j.cleanupOutcomes) != 1 {
		t.Fatalf("cleanup journal=%+v %+v", j.cleanupIntents, j.cleanupOutcomes)
	}

	if err := os.Remove(filepath.Join(work, "external-sentinel")); err != nil {
		t.Fatal(err)
	}
	j = &memoryJournal{}
	out, err = Cleanup(context.Background(), CleanupRequest{Root: work, Receipt: receipt, Journal: j})
	if err != nil || out.State != CleanupRemoved || len(j.cleanupOutcomes) != 1 {
		t.Fatalf("clean err=%v out=%+v", err, out)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
}

func TestComposeCandidateFreezesOrderedDependencyComposition(t *testing.T) {
	d, base := repo(t)
	first, _ := candidate(t, d)
	if err := os.WriteFile(filepath.Join(d, "second.txt"), []byte("second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, d, "add", "second.txt")
	git(t, d, "commit", "-qm", "second")
	second := git(t, d, "rev-parse", "HEAD")
	work := filepath.Join(t.TempDir(), "composed")
	j := &memoryJournal{}
	got, err := ComposeCandidate(context.Background(), CandidateCompositionRequest{RepoRoot: d, Worktree: work, AttemptID: "compose-1", BaseOID: base, OrderedInputOIDs: []string{first, second}, PlanRevision: 7}, j)
	if err != nil {
		t.Fatal(err)
	}
	if got.CandidateOID != second || got.BaseOID != base || len(j.candidateIntents) != 1 || len(j.candidateReceipts) != 1 {
		t.Fatalf("receipt=%+v journal=%+v", got, j)
	}
	if git(t, work, "rev-parse", "HEAD") != second {
		t.Fatal("composition did not freeze final dependency")
	}
}

func TestPrivateRefRequiresManagedNamespaceAndCompareAndSwap(t *testing.T) {
	d, base := repo(t)
	candidateOID, _ := candidate(t, d)
	managed := "refs/orchestrator/g3/attempt-1"
	if err := PrivateRef(context.Background(), d, "refs/heads/unsafe", "", candidateOID); err == nil || !IsCode(err, CodeInvalidInput) {
		t.Fatalf("unsafe ref accepted: %v", err)
	}
	if err := PrivateRef(context.Background(), d, managed, "", candidateOID); err != nil {
		t.Fatal(err)
	}
	if err := PrivateRef(context.Background(), d, managed, base, candidateOID); err == nil || !IsCode(err, CodeTargetDrift) {
		t.Fatalf("CAS mismatch accepted: %v", err)
	}
	if err := DeletePrivateRef(context.Background(), d, managed, candidateOID); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupDetectsRootReplacementBeforeGitRemoval(t *testing.T) {
	d, base := repo(t)
	candidateOID, _ := candidate(t, d)
	work := filepath.Join(t.TempDir(), "worktree")
	receipt, err := Materialize(context.Background(), MaterializeRequest{RepoRoot: d, Worktree: work, AttemptID: "replace-1", BaseOID: base, CandidateOID: candidateOID, OrderedInputOIDs: []string{candidateOID}, PlanRevision: 1}, &memoryJournal{})
	if err != nil {
		t.Fatal(err)
	}
	replaced := work + ".moved"
	j := &memoryJournal{}
	out, err := Cleanup(context.Background(), CleanupRequest{Root: work, Receipt: receipt, Journal: j, BeforeRemove: func() error {
		if err := os.Rename(work, replaced); err != nil {
			return err
		}
		return os.Mkdir(work, 0700)
	}})
	if err == nil || !IsCode(err, CodeIdentityChanged) || out.State != CleanupPending {
		t.Fatalf("replacement not rejected: err=%v out=%+v", err, out)
	}
	if _, err := os.Stat(replaced); err != nil {
		t.Fatal("original root was not retained")
	}
}

func TestIntegrationUsesCommonDirectoryOSLock(t *testing.T) {
	d, _ := repo(t)
	common, err := commonDir(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	first, err := acquireIntegrationLock(common)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseIntegrationLock(first)
	second, err := acquireIntegrationLock(common)
	if err == nil || second != nil {
		if second != nil {
			releaseIntegrationLock(second)
		}
		t.Fatalf("concurrent lock acquired: file=%v err=%v", second, err)
	}
}
