//go:build darwin || linux

package candidatemaintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type nativeStore struct {
	marked, finished bool
	reject           bool
	outcome          store.CandidateMaintenanceOutcome
}

type nativePreparationJournal struct{}

func (nativePreparationJournal) PersistIntegrationIntent(context.Context, gitops.IntegrationIntent) error {
	return nil
}
func (nativePreparationJournal) RecordIntegration(context.Context, gitops.IntegrationOutcome) error {
	return nil
}
func (nativePreparationJournal) PersistCleanupIntent(context.Context, gitops.CleanupIntent) error {
	return nil
}
func (nativePreparationJournal) RecordCleanup(context.Context, gitops.CleanupOutcome) error {
	return nil
}

func (s *nativeStore) MarkCandidateMaintenanceRunning(_ context.Context, _ string, _ string, _ store.CandidateMaintenanceGrant, _ process.Identity) (store.CandidateMaintenanceState, error) {
	s.marked = true
	if s.reject {
		return store.CandidateMaintenanceState{}, errors.New("mark refused")
	}
	return store.CandidateMaintenanceState{State: "running"}, nil
}
func (s *nativeStore) FinishCandidateMaintenance(_ context.Context, _ store.CandidateMaintenanceGrant, _ process.Identity, out store.CandidateMaintenanceOutcome) (store.CandidateMaintenanceState, error) {
	s.finished, s.outcome = true, out
	return store.CandidateMaintenanceState{State: out.State, Outcome: &out}, nil
}

func TestCandidateMaintenanceNativeActorRemovesFrozenWorktree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "state.db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	nativeGit(t, root, "init", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, "owned.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeGit(t, root, "-C", repo, "add", "owned.txt")
	nativeGit(t, root, "-C", repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
	base := nativeGit(t, root, "-C", repo, "rev-parse", "HEAD")
	binding := gitops.CandidateBinding{RunID: "run", TaskID: "task", AttemptID: "attempt", SegmentID: "segment", WorkRevision: 1, PlanRevision: 1}
	key := nativeHash(binding.RunID + "\x00" + binding.TaskID + "\x00" + binding.AttemptID + "\x00" + binding.SegmentID)
	spool := filepath.Join(state, "host-spool", "host")
	if err := os.MkdirAll(filepath.Join(spool, ".gitops-journal"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(state, "host-spool"), spool, filepath.Join(spool, ".gitops-journal")} {
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	sourceRoot := filepath.Join(spool, ".gitops-journal", key)
	journal, err := gitopsworker.OpenJournal(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "candidate")
	prepared, err := gitops.PrepareWorkspace(ctx, gitops.MaterializeRequest{RepoRoot: repo, Worktree: worktree, AttemptID: binding.AttemptID, BaseOID: base, CandidateOID: base, PlanRevision: 1}, binding, []string{"owned.txt"}, "refs/orchestrator/g3/candidate-"+key, nativePreparationJournal{})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(worktree, "owned.txt"), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := gitops.FreezeWorkspace(gitops.WithCandidateJournal(ctx, journal), prepared, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	source := gitops.CandidateResourceSource{Kind: "producer", HostID: "host", CommandID: "command", Binding: binding, JournalRelative: ".gitops-journal/" + key, ProjectionSHA256: gitops.CandidateResourceReceiptDigest(receipt)}
	header, err := gitopsworker.SealCandidateJournal(ctx, spool, source)
	if err != nil {
		body, _ := os.ReadFile(filepath.Join(sourceRoot, "events.jsonl"))
		t.Logf("source journal: %s", body)
		t.Fatal(err)
	}
	operation := filepath.Join(state, "candidate-maintenance-"+strings.Repeat("4", 64))
	identity := nativeMaintenanceJournal(t, operation)
	exe := filepath.Join(root, "orchestrator")
	module, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", exe, "./cmd/orchestrator")
	build.Dir = module
	if output, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build worker: %v %s", e, output)
	}
	digest, err := process.ExecutableDigest(ctx, exe)
	if err != nil {
		t.Fatal(err)
	}
	g := store.CandidateMaintenanceGrant{Version: 1, OperationID: strings.Repeat("1", 64), RunID: binding.RunID, ResourceID: header.ResourceID, Generation: 1, OwnerBinding: strings.Repeat("2", 64), FactsSHA256: strings.Repeat("3", 64), Header: header, SpoolRoot: spool, Root: nativeRoot(t, state), JournalIdentity: identity, OperationJournal: operation, Executable: exe, ExecutableSHA256: digest}
	raw, _ := json.Marshal(g)
	g.GrantSHA256 = nativeHash(string(raw))
	if err = g.Validate(); err != nil {
		t.Fatal(err)
	}
	adminRoot := filepath.Join(repo, ".git", "worktrees")
	admins, err := os.ReadDir(adminRoot)
	if err != nil || len(admins) != 1 {
		t.Fatalf("candidate admin before cleanup=%d err=%v", len(admins), err)
	}
	denied := &nativeStore{reject: true}
	if _, e := Run(ctx, denied, "controller", "token", g); e == nil || !denied.marked || denied.finished {
		t.Fatalf("denied grant lifecycle=%+v err=%v", denied, e)
	}
	if _, e := os.Stat(worktree); e != nil {
		t.Fatal("ungranted child deleted worktree", e)
	}
	if b, e := os.ReadFile(filepath.Join(operation, "events.jsonl")); e != nil || len(b) != 0 {
		t.Fatal("ungranted child wrote operation journal", e)
	}
	store := &nativeStore{}
	out, err := Run(ctx, store, "controller", "token", g)
	if err != nil || out.State != "removed" || !store.marked || !store.finished || store.outcome.State != "removed" {
		t.Fatalf("actor=%+v store=%+v err=%v", out, store, err)
	}
	if _, err = os.Lstat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree retained: %v", err)
	}
	if err = exec.Command("/usr/bin/git", "-C", repo, "show-ref", "--verify", receipt.PrivateRef).Run(); err == nil {
		t.Fatal("private ref retained")
	}
	admins, err = os.ReadDir(adminRoot)
	if err != nil || len(admins) != 0 {
		t.Fatalf("candidate admin retained=%d err=%v", len(admins), err)
	}
	for _, path := range []string{sourceRoot, operation} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("durable recovery journal lost %s: %v", path, err)
		}
	}
}

func nativeHash(v string) string { s := sha256.Sum256([]byte(v)); return hex.EncodeToString(s[:]) }
func nativeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "/usr/bin/git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func nativeRoot(t *testing.T, state string) store.StoreRootNodes {
	t.Helper()
	a, _ := os.Stat(state)
	b, _ := os.Stat(filepath.Join(state, "state.db"))
	as := a.Sys().(*syscall.Stat_t)
	bs := b.Sys().(*syscall.Stat_t)
	return store.StoreRootNodes{StateRoot: state, StateDevice: uint64(as.Dev), StateInode: as.Ino, DatabaseDevice: uint64(bs.Dev), DatabaseInode: bs.Ino}
}
func nativeMaintenanceJournal(t *testing.T, root string) gitopsworker.MaintenanceJournalIdentity {
	t.Helper()
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"events.jsonl", "events.lock"} {
		if err := os.WriteFile(filepath.Join(root, n), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	d, _ := os.Stat(root)
	ds := d.Sys().(*syscall.Stat_t)
	out := gitopsworker.MaintenanceJournalIdentity{Directory: gitops.DirectoryIdentity{Device: int64(ds.Dev), Inode: int64(ds.Ino), Mode: int64(d.Mode()), UID: int64(ds.Uid)}}
	for _, x := range []struct {
		n string
		p *gitops.FileIdentity
	}{{"events.jsonl", &out.Events}, {"events.lock", &out.Lock}} {
		f, _ := os.Stat(filepath.Join(root, x.n))
		fs := f.Sys().(*syscall.Stat_t)
		*x.p = gitops.FileIdentity{Device: int64(fs.Dev), Inode: int64(fs.Ino), Mode: int64(f.Mode()), Size: f.Size(), ModUnixNano: f.ModTime().UnixNano()}
	}
	return out
}
