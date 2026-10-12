package gitopsworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

type finalFaultJournal struct{ *durableJournal }

func (j finalFaultJournal) RecordCleanup(context.Context, gitops.CleanupOutcome) error {
	return errors.New("final record interrupted")
}

func TestCleanupRecoveryDurableReopenAtEveryAction(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Recovery Test")
	git("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("owned\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "file")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	journalPath := filepath.Join(root, "journal")
	j, err := OpenFreshJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { j.Close() }()
	work := filepath.Join(root, "work")
	receipt, err := gitops.ComposeCandidate(ctx, gitops.CandidateCompositionRequest{RepoRoot: repo, Worktree: work, AttemptID: "recovery", BaseOID: base, OrderedInputOIDs: []string{base}, PlanRevision: 1, PrivateRef: "refs/orchestrator/g3/recovery"}, j)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "parent-alias")
	if err = os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	req := gitops.CleanupRequest{Root: filepath.Join(alias, "work"), CandidateReceipt: &receipt, Journal: j, BeforeRemove: func() error { return errors.New("plan-only") }}
	if _, err = gitops.Cleanup(ctx, req); err == nil {
		t.Fatal("plan fault missing")
	}
	var plan gitops.CleanupPlan
	if found, e := j.readBoundedRecord(ctx, "cleanup_plan", &plan); e != nil || !found {
		t.Fatalf("plan missing %v", e)
	}
	reopen := func() {
		t.Helper()
		if e := j.Close(); e != nil {
			t.Fatal(e)
		}
		j, err = OpenJournal(journalPath)
		if err != nil {
			t.Fatal(err)
		}
		req.Journal = j
	}
	req.BeforeRemove = nil
	stopped := map[int]bool{}
	req.AfterCleanupAction = func(i int, _ string) error {
		if !stopped[i] {
			stopped[i] = true
			return errors.New("action interrupted")
		}
		return nil
	}
	for i := 0; i < gitops.CleanupPlanStepCount(plan); i++ {
		reopen()
		out, e := gitops.Cleanup(ctx, req)
		if e == nil || out.State != gitops.CleanupPending {
			t.Fatalf("action %d: %+v %v", i, out, e)
		}
		state, e := j.LoadCleanupRecovery(ctx, plan.Intent.ReceiptSHA256)
		if e != nil || state.Step != i {
			t.Fatalf("action %d state=%+v err=%v", i, state, e)
		}
	}
	reopen()
	req.AfterCleanupAction = nil
	req.Journal = finalFaultJournal{j}
	if out, e := gitops.Cleanup(ctx, req); e == nil || out.State != gitops.CleanupPending {
		t.Fatalf("final fault %+v %v", out, e)
	}
	reopen()
	first, err := gitops.Cleanup(ctx, req)
	if err != nil || first.State != gitops.CleanupRemoved || first.Root != receipt.WorktreeIdentity {
		t.Fatalf("finish %+v %v", first, err)
	}
	reopen()
	second, err := gitops.Cleanup(ctx, req)
	if err != nil || second != first {
		t.Fatalf("third replay %+v %v", second, err)
	}
}

func TestCleanupJournalReservationProtectsRemainingRecords(t *testing.T) {
	ctx := context.Background()
	plan := gitops.CleanupPlan{Version: 1, Intent: gitops.CleanupIntent{ReceiptSHA256: "exact"}}
	for _, admit := range []bool{false, true} {
		t.Run(map[bool]string{false: "reject_before_plan", true: "preserve_future"}[admit], func(t *testing.T) {
			j, err := OpenFreshJournal(filepath.Join(t.TempDir(), "journal"))
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if admit {
				if err = j.PersistCleanupPlan(ctx, plan); err != nil {
					t.Fatal(err)
				}
			}
			// Construct a legal near-capacity history once. The actual boundary
			// append below still goes through production admission.
			state, e := j.LoadCleanupRecovery(ctx, "exact")
			if e != nil {
				t.Fatal(e)
			}
			st, _ := j.file.Stat()
			target := int64(journalLimit) - cleanupReservedBytes(state) - 512
			paddingHeader, _ := json.Marshal(map[string]any{"version": 1, "kind": "padding", "value": ""})
			left := target - st.Size()
			for left > 0 {
				size := min(left, int64(900*1024))
				if size < int64(len(paddingHeader)+1) {
					break
				}
				raw, _ := json.Marshal(map[string]any{"version": 1, "kind": "padding", "value": strings.Repeat("p", int(size)-len(paddingHeader)-1)})
				if _, e = j.file.Write(append(raw, '\n')); e != nil {
					t.Fatal(e)
				}
				left -= int64(len(raw) + 1)
			}
			if e = j.file.Sync(); e != nil {
				t.Fatal(e)
			}
			if e = j.append("padding", strings.Repeat("x", 1024)); e == nil {
				t.Fatal("padding consumed reserved space")
			}

			before, _ := j.file.Stat()
			if !admit {
				if err = j.PersistCleanupPlan(ctx, plan); err == nil {
					t.Fatal("unfunded plan admitted")
				}
				after, _ := j.file.Stat()
				if after.Size() != before.Size() {
					t.Fatal("rejected plan appended")
				}
				return
			}
			for i := 0; i < gitops.CleanupPlanStepCount(plan); i++ {
				step := gitops.CleanupStep{ReceiptSHA256: "exact", PlanSHA256: gitops.CleanupPlanDigest(plan), Index: i}
				if err = j.PersistCleanupStep(ctx, step); err != nil {
					t.Fatal(err)
				}
				first, _ := j.file.Stat()
				if err = j.PersistCleanupStep(ctx, step); err != nil {
					t.Fatal(err)
				}
				second, _ := j.file.Stat()
				if first.Size() != second.Size() {
					t.Fatal("duplicate consumed capacity")
				}
			}
			if err = j.RecordCleanup(ctx, gitops.CleanupOutcome{ReceiptSHA256: "exact", State: gitops.CleanupRemoved}); err != nil {
				t.Fatal(err)
			}
			state, err = j.LoadCleanupRecovery(ctx, "exact")
			if err != nil || state.Outcome.State != gitops.CleanupRemoved {
				t.Fatalf("final state %+v %v", state, err)
			}
		})
	}
}

func TestCleanupJournalRejectsUnsafeAuthority(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"orphan", "legacy_upgrade", "skipped_step", "replaced", "symlink", "hardlink", "oversized_other"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			j, err := OpenFreshJournal(root)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			plan := gitops.CleanupPlan{Version: 1, Intent: gitops.CleanupIntent{ReceiptSHA256: "exact"}}
			switch kind {
			case "orphan":
				err = j.RecordCleanup(ctx, gitops.CleanupOutcome{ReceiptSHA256: "exact", State: gitops.CleanupRemoved})
			case "legacy_upgrade":
				if e := j.PersistCleanupIntent(ctx, plan.Intent); e != nil {
					t.Fatal(e)
				}
				err = j.PersistCleanupPlan(ctx, plan)
			case "skipped_step":
				if e := j.PersistCleanupPlan(ctx, plan); e != nil {
					t.Fatal(e)
				}
				err = j.PersistCleanupStep(ctx, gitops.CleanupStep{ReceiptSHA256: "exact", PlanSHA256: gitops.CleanupPlanDigest(plan), Index: 1})
			case "replaced", "symlink":
				name := j.file.Name()
				if e := os.Rename(name, name+".original"); e != nil {
					t.Fatal(e)
				}
				if kind == "symlink" {
					if e := os.Symlink(name+".original", name); e != nil {
						t.Fatal(e)
					}
				} else {
					if e := os.WriteFile(name, nil, 0600); e != nil {
						t.Fatal(e)
					}
				}
				err = j.PersistCleanupPlan(ctx, plan)
				if _, e := j.LoadCleanupRecovery(ctx, "exact"); e == nil {
					t.Fatal("unsafe read")
				}
			case "hardlink":
				if e := os.Link(j.file.Name(), j.file.Name()+".link"); e != nil {
					t.Fatal(e)
				}
				err = j.PersistCleanupPlan(ctx, plan)
			case "oversized_other":
				raw, _ := json.Marshal(map[string]any{"version": 1, "kind": "other", "value": strings.Repeat("x", journalRecordLimit)})
				if _, e := j.file.Write(append(raw, '\n')); e != nil {
					t.Fatal(e)
				}
				_, err = j.LoadCleanupRecovery(ctx, "exact")
			}
			if err == nil {
				t.Fatal("unsafe authority accepted")
			}
		})
	}
}
