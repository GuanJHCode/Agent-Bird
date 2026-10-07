package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Build historical fixtures before removing their newer schema. Reconstruct
// each original candidate using its still-present decision binding and verify
// the saved hash first. Never relabel a modern hash as a legacy commitment.
func downgradeHostArtifactGrants(t *testing.T, tx *sql.Tx) {
	t.Helper()
	ctx := context.Background()
	rows, err := tx.Query(`SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,c.work_revision,s.slot_token,s.launch_intent_hash,r.adapter_payload,p.plan_revision,r.work_revision FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime r ON r.task_id=t.id JOIN report_capabilities c ON c.segment_id=s.segment_id JOIN runs p ON p.id=t.run_id`)
	if err != nil {
		t.Fatal(err)
	}
	var commands []contract.LaunchCommand
	for rows.Next() {
		var c contract.LaunchCommand
		var payload string
		var current int
		if err = rows.Scan(&c.CommandID, &c.HostID, &c.RunID, &c.TaskID, &c.AttemptID, &c.SegmentID, &c.WorkRevision, &c.SlotToken, &c.LaunchIntentHash, &payload, &c.PlanRevision, &current); err != nil {
			t.Fatal(err)
		}
		if current != c.WorkRevision {
			t.Fatal("historical fixture needs its original revision payload", c.TaskID, c.WorkRevision, current)
		}
		c.AdapterPayload = json.RawMessage(payload)
		commands = append(commands, c)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	type replacement struct{ segment, hash string }
	var changes []replacement
	for _, c := range commands {
		if err = bindHistoricalCandidateInput(ctx, tx, &c); err != nil {
			t.Fatal("cannot authenticate original fixture grant", c.TaskID, c.SegmentID, err)
		}
		modern, err := contract.CandidateIntentHash(c)
		if err != nil {
			t.Fatal(err)
		}
		// Ordinary launches retain their existing noncandidate hash contract.
		if modern != c.LaunchIntentHash {
			continue
		}
		c.HostArtifacts = nil
		legacy, err := contract.CandidateIntentHash(c)
		if err != nil {
			t.Fatal(err)
		}
		changes = append(changes, replacement{c.SegmentID, legacy})
	}
	for _, c := range changes {
		if _, err = tx.Exec(`UPDATE segment_runtime SET launch_intent_hash=? WHERE segment_id=?`, c.hash, c.segment); err != nil {
			t.Fatal(err)
		}
	}
	// DELETE's existing quota triggers remove exactly the newer metadata.
	// Recreate the immutable guard because v19 itself still owns that trigger.
	for _, stmt := range []string{
		`DROP TRIGGER artifact_reservation_retained`,
		`DELETE FROM managed_artifact_reservations WHERE class IN ('terminal','progress')`,
		`CREATE TRIGGER artifact_reservation_retained BEFORE DELETE ON managed_artifact_reservations BEGIN SELECT RAISE(ABORT,'artifact_reservation_immutable'); END`,
	} {
		if _, err = tx.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

// Indexed review artifacts embed the grant hash. Prepare their historical
// command before constructing that artifact, so no on-disk evidence or accepted
// event is rewritten after the fact by the later schema downgrade.
func legacyCandidateArtifactFixture(t *testing.T, db *DB, c *contract.LaunchCommand) {
	t.Helper()
	modern, err := contract.CandidateIntentHash(*c)
	if err != nil || c.HostArtifacts == nil || modern != c.LaunchIntentHash {
		t.Fatal("invalid original candidate fixture", err)
	}
	legacy := *c
	legacy.HostArtifacts = nil
	legacy.LaunchIntentHash, err = contract.CandidateIntentHash(legacy)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE segment_runtime SET launch_intent_hash=? WHERE segment_id=? AND launch_intent_hash=?`, legacy.LaunchIntentHash, c.SegmentID, modern); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DROP TRIGGER artifact_reservation_retained; DELETE FROM managed_artifact_reservations WHERE scope_key=? AND class IN ('terminal','progress'); CREATE TRIGGER artifact_reservation_retained BEFORE DELETE ON managed_artifact_reservations BEGIN SELECT RAISE(ABORT,'artifact_reservation_immutable'); END`, c.SegmentID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	*c = legacy
}
