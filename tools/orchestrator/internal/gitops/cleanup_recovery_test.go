package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type recoveryMemoryJournal struct {
	memoryJournal
	state       CleanupRecoveryState
	steps       []int
	failOutcome bool
}

func (j *recoveryMemoryJournal) LoadCleanupRecovery(_ context.Context, sha string) (CleanupRecoveryState, error) {
	if j.state.Found && j.state.Plan.Intent.ReceiptSHA256 != sha {
		return j.state, errors.New("wrong receipt")
	}
	return j.state, nil
}
func (j *recoveryMemoryJournal) PersistCleanupPlan(_ context.Context, p CleanupPlan) error {
	if j.state.Found {
		return errors.New("duplicate plan")
	}
	j.state = CleanupRecoveryState{Plan: &p, Intent: p.Intent, Step: -1, Found: true}
	return nil
}
func (j *recoveryMemoryJournal) PersistCleanupStep(_ context.Context, s CleanupStep) error {
	if s.Index != j.state.Step+1 {
		return fmt.Errorf("nonsequential %d after %d", s.Index, j.state.Step)
	}
	j.state.Step = s.Index
	j.steps = append(j.steps, s.Index)
	return nil
}
func (j *recoveryMemoryJournal) RecordCleanup(_ context.Context, o CleanupOutcome) error {
	if j.failOutcome {
		j.failOutcome = false
		return errors.New("final journal unavailable")
	}
	j.state.Outcome = o
	return nil
}
func recoveryFixture(t *testing.T) (CleanupRequest, *recoveryMemoryJournal) {
	t.Helper()
	d, base := repo(t)
	oid, _ := candidate(t, d)
	work := filepath.Join(t.TempDir(), "managed")
	r, err := ComposeCandidate(context.Background(), CandidateCompositionRequest{RepoRoot: d, Worktree: work, AttemptID: "recovery", BaseOID: base, OrderedInputOIDs: []string{oid}, PlanRevision: 1, PrivateRef: "refs/orchestrator/g3/recovery"}, &memoryJournal{})
	if err != nil {
		t.Fatal(err)
	}
	j := &recoveryMemoryJournal{}
	return CleanupRequest{Root: work, CandidateReceipt: &r, Journal: j}, j
}

func TestCleanupRecoveryResumesEveryActionAndFinalRecord(t *testing.T) {
	req, j := recoveryFixture(t)
	ctx := context.Background()
	interrupted := map[int]bool{}
	req.BeforeRemove = func() error { return errors.New("stop with plan only") }
	if out, err := Cleanup(ctx, req); err == nil || out.State != CleanupPending || j.state.Step != -1 || j.state.Plan == nil {
		t.Fatalf("plan-only: %+v %v", out, err)
	}
	req.BeforeRemove = nil
	req.AfterCleanupAction = func(index int, _ string) error {
		if !interrupted[index] {
			interrupted[index] = true
			return errors.New("stop after action")
		}
		return nil
	}
	count := CleanupPlanStepCount(*j.state.Plan)
	for i := 0; i < count; i++ {
		out, err := Cleanup(ctx, req)
		if err == nil || out.State != CleanupPending || j.state.Step != i {
			t.Fatalf("action %d state=%d out=%+v err=%v", i, j.state.Step, out, err)
		}
	}
	req.AfterCleanupAction = nil
	j.failOutcome = true
	if out, err := Cleanup(ctx, req); err == nil || out.State != CleanupPending {
		t.Fatalf("final journal: %+v %v", out, err)
	}
	first, err := Cleanup(ctx, req)
	if err != nil || first.State != CleanupRemoved {
		t.Fatalf("finish: %+v %v", first, err)
	}
	second, err := Cleanup(ctx, req)
	if err != nil || second != first {
		t.Fatalf("replay: %+v %v", second, err)
	}
	if len(j.steps) != count || len(interrupted) != count {
		t.Fatalf("steps=%v interrupted=%v", j.steps, interrupted)
	}
}

func TestCleanupRecoveryRetainsForeignObjects(t *testing.T) {
	for _, kind := range []string{"rebuilt_root", "stage_collision", "late_replacement", "parent_replaced"} {
		t.Run(kind, func(t *testing.T) {
			req, j := recoveryFixture(t)
			ctx := context.Background()
			req.AfterQuarantine = func(string) error { return errors.New("pause") }
			if _, err := Cleanup(ctx, req); err == nil {
				t.Fatal("missing pause")
			}
			req.AfterQuarantine = nil
			p := j.state.Plan
			sink := filepath.Join(filepath.Dir(req.Root), p.Root.Quarantine)
			var sentinel string
			switch kind {
			case "rebuilt_root":
				if err := os.Mkdir(req.Root, 0700); err != nil {
					t.Fatal(err)
				}
				sentinel = filepath.Join(req.Root, "foreign")
				if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "stage_collision":
				sentinel = filepath.Join(sink, "entry-00000000")
				if err := os.Mkdir(sentinel, 0700); err != nil {
					t.Fatal(err)
				}
			case "late_replacement":
				a := cleanupActions(*p)[1]
				source := filepath.Join(sink, "root", filepath.FromSlash(a.path))
				if err := os.Rename(source, source+".owned-backup"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(source, []byte("foreign keep"), 0600); err != nil {
					t.Fatal(err)
				}
				sentinel = filepath.Join(sink, "entry-00000000")
			case "parent_replaced":
				parent := filepath.Dir(req.Root)
				if err := os.Rename(parent, parent+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				sentinel = filepath.Join(parent, "foreign")
				if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Remove(sentinel); os.Remove(parent); os.Rename(parent+"-saved", parent) })
			}
			out, err := Cleanup(ctx, req)
			if err == nil || out.State != CleanupPending {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if _, err = os.Lstat(sentinel); err != nil {
				t.Fatalf("foreign object lost: %v", err)
			}
			if got := git(t, req.CandidateReceipt.RepoRoot, "rev-parse", req.CandidateReceipt.PrivateRef); got != req.CandidateReceipt.CandidateOID {
				t.Fatal("private ref lost")
			}
		})
	}
}

func TestCleanupRecoveryRejectsAdminRebindingBeforePlan(t *testing.T) {
	req, j := recoveryFixture(t)
	var sentinel string
	req.AfterCleanupAdminPrepared = func(admin string) error {
		if err := os.Rename(admin, admin+".original"); err != nil {
			return err
		}
		if err := os.Mkdir(admin, 0700); err != nil {
			return err
		}
		sentinel = filepath.Join(admin, "foreign")
		return os.WriteFile(sentinel, []byte("retain"), 0600)
	}
	out, err := Cleanup(context.Background(), req)
	if err == nil || out.State != CleanupPending || j.state.Plan != nil {
		t.Fatalf("foreign authority minted: %+v plan=%+v err=%v", out, j.state.Plan, err)
	}
	if _, err = os.Stat(sentinel); err != nil {
		t.Fatal("foreign admin moved or removed")
	}
	if _, err = os.Stat(req.Root); err != nil {
		t.Fatal("root changed before plan")
	}
}

func TestCleanupRecoveryRetainsChangedRefAfterFilesystemRemoval(t *testing.T) {
	req, j := recoveryFixture(t)
	req.AfterQuarantine = func(string) error { return errors.New("pause") }
	if _, err := Cleanup(context.Background(), req); err == nil {
		t.Fatal("pause missing")
	}
	req.AfterQuarantine = nil
	r := req.CandidateReceipt
	git(t, r.RepoRoot, "update-ref", r.PrivateRef, r.BaseOID, r.CandidateOID)
	out, err := Cleanup(context.Background(), req)
	if err == nil || out.State != CleanupPending || !IsCode(err, CodeTargetDrift) {
		t.Fatalf("changed ref: %+v %v", out, err)
	}
	if git(t, r.RepoRoot, "rev-parse", r.PrivateRef) != r.BaseOID {
		t.Fatal("changed ref removed")
	}
	if j.state.Step != CleanupPlanStepCount(*j.state.Plan)-1 {
		t.Fatal("did not reach ref checkpoint")
	}
}
