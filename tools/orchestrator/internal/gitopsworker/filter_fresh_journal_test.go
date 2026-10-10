package gitopsworker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
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
