package store

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestManagedCandidateReadersRequireOriginalObject(t *testing.T) {
	readers := map[string]func(context.Context, *sql.Tx, contract.Event) error{
		"rework": func(ctx context.Context, tx *sql.Tx, e contract.Event) error {
			_, err := readReworkArtifact(ctx, tx, e)
			return err
		},
		"indexed": func(ctx context.Context, tx *sql.Tx, e contract.Event) error {
			_, err := readIndexedResultArtifact(ctx, tx, e)
			return err
		},
	}
	for name, read := range readers {
		t.Run(name, func(t *testing.T) {
			db, g := quotaFixture(t)
			ctx := context.Background()
			body := []byte(`{"version":1}`)
			ref := managedHostArtifactForTest(t, db, g, "terminal", 0, body)
			e := contract.Event{Version: 1, ProducerID: g.HostID, EventID: "candidate", Sequence: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, Kind: contract.EventResult, PayloadHash: ref.SHA256, Artifact: ref}
			if _, err := db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			check := func() error {
				tx, err := db.sql.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				return read(ctx, tx, e)
			}
			if err := check(); err != nil {
				t.Fatal("original managed candidate rejected", err)
			}
			// Keep the original inode alive so replacement cannot accidentally reuse it.
			if err := os.Rename(ref.Path, ref.Path+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ref.Path, body, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(ref.Path + ".original"); err != nil {
				t.Fatal(err)
			}
			if err := check(); err == nil {
				t.Fatal("same-content replacement impersonated original Host object")
			}
		})
	}
}
