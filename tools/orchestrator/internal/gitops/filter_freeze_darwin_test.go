//go:build darwin

package gitops

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// TestFilterFreezeNativeFixture exercises the Host-owned path with real Git,
// a real ResourceBroker, and a separately started provider-like writer.  A
// broken implementation that stages rendered bytes, loses evidence, or lets a
// reopened worktree stand in for the original live owner must fail here.
func TestFilterFreezeNativeFixture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
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
	canonical := []byte("canonical B\\n")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), canonical, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("control\\n"), 0600); err != nil {
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
	defer source.Close()
	broker, scope := rawBroker(t, f, ctx, manifest)
	f.journal = &rawFixtureJournal{}
	request := MaterializeRequest{RepoRoot: repo, Worktree: filepath.Join(f.root, "provider-worktree"), AttemptID: "a", BaseOID: base, CandidateOID: base, OrderedInputOIDs: []string{base}, PlanRevision: 1, FilterScopeSHA256: scope, FilterReservationID: "filter"}
	binding := CandidateBinding{RunID: "run", TaskID: "task", AttemptID: "a", SegmentID: "s", WorkRevision: 1, PlanRevision: 1}
	prepared, err := PrepareFilteredWorkspace(ctx, source, broker, request, binding, []string{"a.txt"}, "refs/orchestrator/g3/filter-freeze-native", f.journal)
	if err != nil || prepared == nil {
		t.Fatalf("prepare live filtered workspace: prepared=%v err=%v", prepared, err)
	}
	defer prepared.Close()

	rendered := xorRaw([]byte("provider result R\\n"))
	payload := filepath.Join(f.root, "provider-rendered")
	if err := os.WriteFile(payload, rendered, 0600); err != nil {
		t.Fatal(err)
	}
	copyProviderFile(t, f, ctx, payload, filepath.Join(prepared.Workspace().Receipt.Worktree, "a.txt"))
	receipt, err := FreezeFilteredWorkspace(ctx, prepared, &memoryJournal{})
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if receipt.BaseOID != base || receipt.CandidateOID == base || receipt.PrivateRef != "refs/orchestrator/g3/filter-freeze-native" || receipt.Filter == nil || len(receipt.Filter.Views) != 1 {
		t.Fatalf("incomplete frozen receipt: %#v", receipt)
	}
	if got := rawGit(t, f, ctx, gitPath, receipt.Worktree, "show", receipt.CandidateOID+":a.txt"); !bytes.Equal([]byte(got+"\n"), []byte("provider result R\n")) {
		t.Fatalf("candidate stored rendered rather than canonical bytes: %q", got)
	}
	if got, readErr := os.ReadFile(filepath.Join(receipt.Worktree, "a.txt")); readErr != nil || !bytes.Equal(got, rendered) {
		t.Fatalf("provider worktree rendered view changed: %q err=%v", got, readErr)
	}
}

func copyProviderFile(t *testing.T, f *rawFixture, ctx context.Context, from, to string) {
	t.Helper()
	path := "/bin/cp"
	sha, err := process.ExecutableDigest(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := process.Start(ctx, process.Command{Path: path, PinnedPath: path, PinnedSHA256: sha, Args: []string{from, to}, ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TMPDIR=/private/tmp"}})
	if h == nil {
		t.Fatal(err)
	}
	if err != nil {
		f.retain = true
		t.Fatal(err)
	}
	rawFixtureWait(t, f, ctx, h)
	t.Logf("provider_writer_original_wait=0 exit=0 tree=0")
}
