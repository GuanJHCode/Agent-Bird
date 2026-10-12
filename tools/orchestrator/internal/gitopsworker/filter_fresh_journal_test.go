package gitopsworker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func TestOpenFreshJournalRefusesAnyExistingLedger(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	first, err := OpenFreshJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "events.jsonl")
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"empty", nil},
		{"partial", []byte(`{"version":1,"kind":"filter_materialized"`)},
		{"filter_materialized_only", []byte(`{"version":1,"kind":"filter_materialized","value":{}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, tc.body, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			journal, err := OpenFreshJournal(root)
			if err == nil {
				_ = journal.Close()
				t.Fatal("existing journal admitted as fresh")
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(after, before) {
				t.Fatalf("ledger changed after=%q readErr=%v", after, readErr)
			}
		})
	}
}

func TestJournalFilterAttachmentStrictness(t *testing.T) {
	t.Run("legacy_nil_filter_records_load", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "journal")
		journal, err := OpenFreshJournal(root)
		if err != nil {
			t.Fatal(err)
		}
		prepared := gitops.PreparedWorkspace{Receipt: gitops.MaterializeReceipt{
			AttemptID: "attempt",
			Files:     map[string]gitops.FileIdentity{"FILTER": {Device: 1, Inode: 2, Mode: 0100644, Size: 3, ModUnixNano: 4}},
		}}
		receipt := gitops.CandidateReceipt{
			AttemptID: "attempt",
			Files:     map[string]gitops.FileIdentity{"FILTER": {Device: 5, Inode: 6, Mode: 0100644, Size: 7, ModUnixNano: 8}},
		}
		if err = journal.RecordWorkspacePrepared(context.Background(), prepared); err != nil {
			t.Fatal(err)
		}
		if err = journal.RecordCandidateFreeze(context.Background(), receipt); err != nil {
			t.Fatal(err)
		}
		if err = journal.Close(); err != nil {
			t.Fatal(err)
		}
		journal, err = OpenJournal(root)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		gotPrepared, err := journal.LoadWorkspacePrepared(context.Background())
		if err != nil || !reflect.DeepEqual(gotPrepared, prepared) {
			t.Fatalf("prepared=%#v want=%#v err=%v", gotPrepared, prepared, err)
		}
		gotReceipt, found, err := journal.LoadCandidateFreeze(context.Background())
		if err != nil || !found || !reflect.DeepEqual(gotReceipt, receipt) {
			t.Fatalf("receipt=%#v want=%#v found=%t err=%v", gotReceipt, receipt, found, err)
		}
	})

	t.Run("duplicate_outer_filter_attachment_rejected", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "journal")
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "events.jsonl")
		record := []byte(`{"version":1,"kind":"candidate_receipt","value":{"attempt_id":"attempt","filter":null,"filter":null}}`)
		if err := os.WriteFile(path, record, 0600); err != nil {
			t.Fatal(err)
		}
		journal, err := OpenJournal(root)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		_, _, err = journal.LoadCandidateFreeze(context.Background())
		if err == nil {
			t.Fatal("duplicate outer filter attachment accepted")
		}
	})
}

func TestCleanupHistoryRequiresOneExactDurableBinding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := OpenFreshJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	intent := gitops.CleanupIntent{Root: "/owned/root", AttemptID: "attempt", ReceiptSHA256: "exact"}
	outcome := gitops.CleanupOutcome{State: gitops.CleanupRemoved, Root: "/owned/root", ReceiptSHA256: "exact"}
	if err = journal.PersistCleanupIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err = journal.RecordCleanup(context.Background(), outcome); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	gotIntent, gotOutcome, found, err := journal.LoadCleanup(context.Background(), "exact")
	if err != nil || !found || !reflect.DeepEqual(gotIntent, intent) || !reflect.DeepEqual(gotOutcome, outcome) {
		t.Fatalf("intent=%+v outcome=%+v found=%t err=%v", gotIntent, gotOutcome, found, err)
	}

	foreignRoot := filepath.Join(t.TempDir(), "foreign-journal")
	foreign, err := OpenFreshJournal(foreignRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err = foreign.PersistCleanupIntent(context.Background(), gitops.CleanupIntent{Root: "/foreign", ReceiptSHA256: "other"}); err != nil {
		t.Fatal(err)
	}
	if err = foreign.Close(); err != nil {
		t.Fatal(err)
	}
	foreign, err = OpenJournal(foreignRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if _, _, _, err = foreign.LoadCleanup(context.Background(), "exact"); err == nil {
		t.Fatal("foreign cleanup journal accepted")
	}
}

func TestCleanupHistoryRefusesConcurrentJournalWriter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	first, err := OpenFreshJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err = first.PersistCleanupIntent(context.Background(), gitops.CleanupIntent{Root: "/owned/root", ReceiptSHA256: "exact"}); err != nil {
		t.Fatal(err)
	}
	second, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err = syscall.Flock(int(first.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(first.lock.Fd()), syscall.LOCK_UN)
	if _, _, _, err = second.LoadCleanup(context.Background(), "exact"); err == nil {
		t.Fatal("read accepted a concurrently writable journal")
	}
}

func TestCleanupRecoveryScansOrderedPlanAndIdempotentSteps(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j, err := OpenFreshJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	plan := gitops.CleanupPlan{Version: 1, Intent: gitops.CleanupIntent{ReceiptSHA256: "exact"}}
	if err = j.PersistCleanupPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	digest := gitops.CleanupPlanDigest(plan)
	for _, index := range []int{0, 0, 1, 2, 3, 4, 5} {
		if err = j.PersistCleanupStep(context.Background(), gitops.CleanupStep{ReceiptSHA256: "exact", PlanSHA256: digest, Index: index}); err != nil {
			t.Fatal(err)
		}
	}
	if err = j.RecordCleanup(context.Background(), gitops.CleanupOutcome{ReceiptSHA256: "exact", State: gitops.CleanupRemoved}); err != nil {
		t.Fatal(err)
	}
	state, err := j.LoadCleanupRecovery(context.Background(), "exact")
	if err != nil || !state.Found || state.Plan == nil || state.Step != 5 || state.Outcome.State != gitops.CleanupRemoved {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
