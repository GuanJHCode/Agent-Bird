package gitopsworker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

func TestOpenFreshJournalRefusesAnyExistingLedger(t *testing.T) {
	root := t.TempDir()
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
		root := t.TempDir()
		journal, err := OpenFreshJournal(root)
		if err != nil {
			t.Fatal(err)
		}
		prepared := gitops.PreparedWorkspace{Receipt: gitops.MaterializeReceipt{AttemptID: "attempt"}}
		receipt := gitops.CandidateReceipt{AttemptID: "attempt"}
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
		if err != nil || gotPrepared.Receipt.Filter != nil {
			t.Fatalf("prepared=%#v err=%v", gotPrepared, err)
		}
		gotReceipt, found, err := journal.LoadCandidateFreeze(context.Background())
		if err != nil || !found || gotReceipt.Filter != nil {
			t.Fatalf("receipt=%#v found=%t err=%v", gotReceipt, found, err)
		}
	})

	t.Run("duplicate_outer_filter_attachment_rejected", func(t *testing.T) {
		root := t.TempDir()
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
