package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Positive fixtures materialize the actual bytes under a Store-issued grant.
// This never repairs malformed claims supplied by negative tests.
func managedHostArtifactForTest(t testing.TB, db *DB, g contract.LaunchCommand, kind string, index int, body []byte) *contract.ArtifactRef {
	t.Helper()
	if err := contract.RequireHostArtifacts(g); err != nil {
		t.Fatal("fixture lacks Store-issued artifact permission", err)
	}
	class, reservation, id := "terminal", g.HostArtifacts.TerminalReservationID, "result-output.bin"
	if kind == "prelaunch" {
		id = "launch-failure"
	} else if kind == "progress" {
		class, reservation, id = "progress", g.HostArtifacts.ProgressReservationID, "provider-progress"
	}
	d := artifacts.HostSlotDescriptor{HostSlotBinding: artifacts.HostSlotBinding{Version: 1, ReservationID: reservation, ScopeSHA256: g.HostArtifacts.ScopeSHA256, Class: class, Index: index}, Kind: kind, Size: int64(len(body)), SHA256: runtimeHash(string(body))}
	root := filepath.Join(filepath.Dir(db.path), "host-spool", g.HostID)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := artifacts.MaterializeHostSlot(context.Background(), root, d, body)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := d.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return &contract.ArtifactRef{ID: id, Path: p.Path, Size: p.Size, SHA256: p.SHA256, HostArtifact: &contract.HostArtifactReceipt{Version: 1, Descriptor: descriptor, Proof: proof}}
}

// Historical fixtures remove modern authority before constructing any artifact
// or accepted input evidence. This does not relabel an already-published ref.
func historicalHostGrantForTest(t *testing.T, db *DB, g *contract.LaunchCommand) {
	t.Helper()
	if err := contract.RequireHostArtifacts(*g); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	modern, err := contract.CandidateIntentHash(*g)
	if err != nil {
		t.Fatal(err)
	}
	if modern == g.LaunchIntentHash {
		legacy := *g
		legacy.HostArtifacts = nil
		hash, err := contract.CandidateIntentHash(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`UPDATE segment_runtime SET launch_intent_hash=? WHERE segment_id=? AND launch_intent_hash=?`, hash, g.SegmentID, g.LaunchIntentHash); err != nil {
			t.Fatal(err)
		}
		g.LaunchIntentHash = hash
	}
	removeHostArtifactFixtureRows(t, tx, g.SegmentID)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	g.HostArtifacts = nil
}

// Independent durable ref row cost; the slot manifest was prepaid at dispatch.
func hostArtifactRefCostForTest(t *testing.T, e contract.Event) int64 {
	t.Helper()
	var descriptor artifacts.HostSlotDescriptor
	var proof artifacts.Proof
	if e.Artifact == nil || e.Artifact.HostArtifact == nil {
		t.Fatal("missing managed fixture")
	}
	if err := json.Unmarshal(e.Artifact.HostArtifact.Descriptor, &descriptor); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(e.Artifact.HostArtifact.Proof, &proof); err != nil {
		t.Fatal(err)
	}
	receipt, err := json.Marshal(e.Artifact.HostArtifact)
	if err != nil {
		t.Fatal(err)
	}
	return int64(64 + len(proof.IntakeID) + len(descriptor.ReservationID) + len(e.RunID) + 1 + len(receipt))
}
