package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestHistoricalHostGrantFixtureRemovesOnlyModernMetadata(t *testing.T) {
	db, _ := quotaFixture(t)
	before, globalBefore := continuationQuotaUsage(t, db)
	modern := hostArtifactControlBytes(t, db, "")
	if modern == 0 || before < modern || globalBefore < modern {
		t.Fatal("fixture lacks modern metadata", before, globalBefore, modern)
	}
	downgradeReportIntakeSchema(t, db)
	after, globalAfter := continuationQuotaUsage(t, db)
	if after != before-modern || globalAfter != globalBefore-modern {
		t.Fatal("historical fixture retained nonexistent modern metadata", before, after, globalBefore, globalAfter, modern)
	}
}

// Build historical fixtures before removing their newer schema. Reconstruct
// each original candidate using its still-present decision binding and verify
// the saved hash first. Never relabel a modern hash as a legacy commitment.
func downgradeHostArtifactGrants(t *testing.T, tx *sql.Tx) {
	t.Helper()
	ctx := context.Background()
	rows, err := tx.Query(`SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,c.work_revision,s.slot_token,s.launch_intent_hash,r.adapter_payload,p.plan_revision,r.work_revision FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime r ON r.task_id=t.id JOIN report_capabilities c ON c.segment_id=s.segment_id JOIN runs p ON p.id=t.run_id WHERE EXISTS(SELECT 1 FROM managed_artifact_reservations m WHERE m.scope_key=s.segment_id AND m.class IN ('terminal','progress'))`)
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
	removeHostArtifactFixtureRows(t, tx, "")
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
	removeHostArtifactFixtureRows(t, tx, c.SegmentID)

	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	*c = legacy
}

// Production forbids deleting reservations and therefore has no DELETE quota
// trigger. Historical fixtures must explicitly remove only the newer fields'
// exact metadata cost, before stripping those rows/schema.
func removeHostArtifactFixtureRows(t *testing.T, tx *sql.Tx, segment string) {
	t.Helper()
	rows, err := tx.Query(`SELECT run_id,COUNT(*),SUM(64+COALESCE((SELECT 64+length(CAST(m.reservation_id AS BLOB))+length(CAST(m.run_id AS BLOB))+m.reserved_bytes FROM managed_host_artifact_metadata m WHERE m.reservation_id=managed_artifact_reservations.id),0)+length(CAST(id AS BLOB))+length(CAST(run_id AS BLOB))+length(CAST(task_id AS BLOB))+length(CAST(attempt_id AS BLOB))+length(CAST(producer_id AS BLOB))+length(CAST(class AS BLOB))+length(CAST(scope_key AS BLOB))+length(CAST(policy_sha256 AS BLOB))+length(CAST(namespace_sha256 AS BLOB))+length(CAST(state AS BLOB))+length(CAST(created_at AS BLOB))) FROM managed_artifact_reservations WHERE class IN ('terminal','progress') AND (?='' OR scope_key=?) GROUP BY run_id`, segment, segment)
	if err != nil {
		t.Fatal(err)
	}
	type refund struct {
		run        string
		rows, cost int64
	}
	var refunds []refund
	var count int64
	for rows.Next() {
		var r refund
		if err = rows.Scan(&r.run, &r.rows, &r.cost); err != nil {
			t.Fatal(err)
		}
		count += r.rows
		refunds = append(refunds, r)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, r := range refunds {
		result, err := tx.Exec(`UPDATE storage_usage SET total_bytes=total_bytes-? WHERE ((scope='global' AND scope_id='') OR (scope='run' AND scope_id=?)) AND total_bytes>=?`, r.cost, r.run, r.cost)
		if err != nil {
			t.Fatal(err)
		}
		n, err := result.RowsAffected()
		if err != nil || n != 2 {
			t.Fatal("fixture metadata refund did not update both scopes", n, err)
		}
	}
	if _, err = tx.Exec(`DROP TRIGGER host_artifact_metadata_retained; DELETE FROM managed_host_artifact_metadata WHERE reservation_id IN (SELECT id FROM managed_artifact_reservations WHERE class IN ('terminal','progress') AND (?='' OR scope_key=?)); CREATE TRIGGER host_artifact_metadata_retained BEFORE DELETE ON managed_host_artifact_metadata BEGIN SELECT RAISE(ABORT,'host_artifact_metadata_immutable'); END;`, segment, segment); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DROP TRIGGER artifact_reservation_retained`); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Exec(`DELETE FROM managed_artifact_reservations WHERE class IN ('terminal','progress') AND (?='' OR scope_key=?)`, segment, segment)
	if err != nil {
		t.Fatal(err)
	}
	n, err := result.RowsAffected()
	if err != nil || n != count {
		t.Fatal("fixture removed unexpected rows", n, count, err)
	}
	if _, err = tx.Exec(`CREATE TRIGGER artifact_reservation_retained BEFORE DELETE ON managed_artifact_reservations BEGIN SELECT RAISE(ABORT,'artifact_reservation_immutable'); END`); err != nil {
		t.Fatal(err)
	}
}
