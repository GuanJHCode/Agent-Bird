package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func validationToolQuotaFixture(t *testing.T) (*DB, contract.LaunchCommand) {
	t.Helper()
	db, hello := diagnosticsLaunchFixture(t, "omitted", 1)
	c, err := db.ClaimReady(context.Background(), hello.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.AdapterPayload, err = json.Marshal(adapter.InvocationPayload{Kind: "candidate", Directory: "/private/validation", CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "validate", AutoDirectory: true, SourceTask: "source", Command: []string{"/usr/bin/true"}, CommandSHA256: strings.Repeat("a", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	return db, c
}

func TestValidationToolReservationQuotaAndReplay(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"one_byte_short", "exact", "control_short", "control_exact", "control_one_short", "commit_failure"} {
		t.Run(mode, func(t *testing.T) {
			db, c := validationToolQuotaFixture(t)
			before := continuationQuotaState(t, db)
			db.artifactLimit = contract.HostArtifactReservedBytes + contract.ValidationToolMaximumBytes
			if mode == "one_byte_short" {
				db.artifactLimit--
			}
			if mode == "control_short" {
				db.userControlLimit = 1
			}
			if mode == "control_exact" || mode == "control_one_short" {
				probe, err := db.beginOwnerTransaction(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var beforeBytes, afterBytes int64
				if err = probe.tx.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&beforeBytes); err != nil {
					t.Fatal(err)
				}
				candidate := c
				if err = db.reserveValidationTool(ctx, probe.tx, &candidate); err != nil {
					t.Fatal(err)
				}
				if err = probe.tx.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&afterBytes); err != nil {
					t.Fatal(err)
				}
				probe.close()
				db.userControlLimit = afterBytes
				if mode == "control_one_short" {
					db.userControlLimit--
				}
				if afterBytes-beforeBytes <= contract.ValidationToolMetadataBytes {
					t.Fatal("missing logical metadata charge")
				}
			}
			owned, err := db.beginOwnerTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = db.reserveValidationTool(ctx, owned.tx, &c)
			if mode == "one_byte_short" || mode == "control_short" || mode == "control_one_short" {
				owned.close()
				if err == nil {
					t.Fatal("unfunded physical grant accepted")
				}
				if continuationQuotaState(t, db) != before {
					t.Fatal("failed reservation changed durable state")
				}
				return
			}
			if err != nil {
				owned.close()
				t.Fatal(err)
			}
			if mode == "commit_failure" {
				if _, err = owned.tx.Exec(`CREATE TABLE tool_commit_parent(id TEXT PRIMARY KEY); CREATE TABLE tool_commit_child(id TEXT REFERENCES tool_commit_parent(id) DEFERRABLE INITIALLY DEFERRED); INSERT INTO tool_commit_child VALUES('absent')`); err != nil {
					owned.close()
					t.Fatal(err)
				}
				if err = owned.commit(); err == nil {
					owned.close()
					t.Fatal("real deferred FK COMMIT accepted")
				}
				owned.close()
				if continuationQuotaState(t, db) != before {
					t.Fatal("failed COMMIT retained tool obligation")
				}
				return
			}
			if err = owned.commit(); err != nil {
				owned.close()
				t.Fatal(err)
			}
			owned.close()
			if contract.RequireValidationTool(c, strings.Repeat("a", 64)) != nil {
				t.Fatal("grant unbound")
			}
			after := continuationQuotaState(t, db)
			owned, err = db.beginOwnerTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.close()
			replay := c
			replay.ValidationTool = nil
			if err = bindValidationTool(ctx, owned.tx, &replay, true); err != nil {
				t.Fatal(err)
			}
			if *replay.ValidationTool != *c.ValidationTool {
				t.Fatal("replay changed physical authority")
			}
			if err = db.managedArtifactAdmission(ctx, owned.tx, 1, 0); !errors.Is(err, CodeError("artifact_spool_full")) {
				t.Fatalf("tool raw bytes excluded from shared artifact limit: %v", err)
			}
			owned.close()
			if continuationQuotaState(t, db) != after {
				t.Fatal("read-only replay mutated quota")
			}
		})
	}
}

// Primitive proof checks use explicitly synthetic lifecycle rows. The vertical
// Host/Store tests separately verify authentication and atomic release in the
// real CommitHostEvent path; these seeded facts do not stand in for that.
func TestValidationToolReleaseRequiresOriginalHostAndTerminal(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"original", "same_id_rebound", "unknown", "missing_cleanup", "foreign_receipt", "orphan_ticket"} {
		t.Run(mode, func(t *testing.T) {
			db, c := validationToolQuotaFixture(t)
			owned, err := db.beginOwnerTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.reserveValidationTool(ctx, owned.tx, &c); err != nil {
				owned.close()
				t.Fatal(err)
			}
			if err = owned.commit(); err != nil {
				owned.close()
				t.Fatal(err)
			}
			owned.close()
			g := c.ValidationTool
			e := contract.Event{Version: 1, ProducerID: c.HostID, EventID: "tool-exited", RunID: c.RunID, TaskID: c.TaskID, AttemptID: c.AttemptID, SegmentID: c.SegmentID, WorkRevision: c.WorkRevision, ExecutionEpoch: 1, CommandID: c.CommandID, Sequence: 1, Kind: contract.EventExited, PayloadHash: runtimeHash("original-exit"), ActiveMS: 1, ValidationToolCleanup: &contract.ValidationToolCleanup{Version: 1, ReservationID: g.ReservationID, ScopeSHA256: g.ScopeSHA256, ToolSHA256: g.ToolSHA256, JournalSHA256: runtimeHash("removed-journal"), RemovedBytes: 8192}}
			if mode == "unknown" {
				e.Kind = contract.EventUnknown
			}
			if mode == "missing_cleanup" {
				e.ValidationToolCleanup = nil
			}
			if mode == "foreign_receipt" {
				e.ValidationToolCleanup.ToolSHA256 = strings.Repeat("b", 64)
			}
			seed, err := db.beginOwnerTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			_, err = seed.tx.Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at,host_artifact_verified,legacy_artifact_claimed) VALUES(?,?,?,?,?,?,?,?,1,'fixture','internal','2026-10-09T00:00:00Z',0,0)`, e.EventID, e.ProducerID, e.Sequence, e.TaskID, e.AttemptID, e.SegmentID, e.PayloadHash, string(body))
			if err == nil {
				err = applyEvent(ctx, seed.tx, e)
			}
			if err == nil {
				err = seed.commit()
			}
			seed.close()
			if err != nil {
				t.Fatal("synthetic lifecycle setup", err)
			}
			if mode == "same_id_rebound" {
				if _, err = db.sql.Exec(`UPDATE runtime_hosts SET pid=pid+1,birth='successor' WHERE id=?`, c.HostID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "orphan_ticket" {
				c.AdapterPayload = json.RawMessage(`{"kind":"fixture"}`)
			}
			owned, err = db.beginOwnerTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.close()
			if mode == "orphan_ticket" {
				if bindValidationTool(ctx, owned.tx, &c, true) == nil {
					t.Fatal("nonpinned command borrowed tool ticket")
				}
				return
			}
			err = applyValidationToolRelease(ctx, owned.tx, e)
			if mode != "original" {
				if err == nil {
					t.Fatal("released without original terminal authority")
				}
				owned.close()
				var state string
				if err = db.sql.QueryRow(`SELECT state FROM validation_tool_reservations WHERE id=?`, g.ReservationID).Scan(&state); err != nil || state != "reserved" {
					t.Fatal("refusal refunded raw bytes", state, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = applyValidationToolRelease(ctx, owned.tx, e); err != nil {
				t.Fatal("exact release replay", err)
			}
			historical := c
			historical.ValidationTool = nil
			if err = bindValidationTool(ctx, owned.tx, &historical, false); err != nil || *historical.ValidationTool != *g {
				t.Fatal("lost historical grant identity", err)
			}
			if bindValidationTool(ctx, owned.tx, &historical, true) == nil {
				t.Fatal("released ticket revived live authority")
			}
			if err = owned.commit(); err != nil {
				t.Fatal(err)
			}
			owned.close()
			owned, err = db.beginOwnerTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			db.artifactLimit = contract.HostArtifactReservedBytes
			if err = db.managedArtifactAdmission(ctx, owned.tx, 0, 0); err != nil {
				t.Fatal("removed tool still charged raw allowance", err)
			}
			// The binary is gone, but both bounded journal versions still own
			// their physical escrow. Verify the exact free-space boundary.
			required := db.minFreeBytes + db.criticalDiskReserve + db.sqliteWriteOverhead + uint64(contract.HostArtifactReservedBytes+hostArtifactManifestReservationBytes+contract.ValidationToolMetadataBytes)
			db.storageAvailable = func(string) (uint64, error) { return required, nil }
			if err = db.managedArtifactAdmission(ctx, owned.tx, 0, 0); err != nil {
				t.Fatal("exact remaining journal capacity", err)
			}
			db.storageAvailable = func(string) (uint64, error) { return required - 1, nil }
			if err = db.managedArtifactAdmission(ctx, owned.tx, 0, 0); !errors.Is(err, CodeError("storage_blocked")) {
				t.Fatal("released grant lost physical journal escrow", err)
			}
			owned.close()
		})
	}
}

// Historical test snapshots may remove only empty additive DDL. Never erase a
// charged or released tool record to invent a pre34 database.
func removeValidationToolSchema34Tx(tx *sql.Tx) error {
	var version, n int
	if err := tx.QueryRow(`SELECT version FROM schema_meta`).Scan(&version); err != nil {
		return err
	}
	if version < 34 {
		return nil
	}
	if version != 34 {
		return fmt.Errorf("unsupported tool schema %d", version)
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM validation_tool_reservations`).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return errors.New("cannot erase validation tool history")
	}
	_, err := tx.Exec(`DROP TABLE validation_tool_reservations; UPDATE schema_meta SET version=33`)
	return err
}

func TestValidationToolMigrationPreservesAndRollsBack(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(fmt.Sprint(fault), func(t *testing.T) {
			db, _ := validationToolQuotaFixture(t)
			before := continuationQuotaState(t, db)
			tx, err := db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = removeValidationToolSchema34Tx(tx); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if fault {
				if _, err = db.sql.Exec(`CREATE TABLE tool_migration_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER tool_migration_commit AFTER UPDATE OF version ON schema_meta WHEN NEW.version=34 BEGIN INSERT INTO tool_migration_fault VALUES('missing'); END`); err != nil {
					t.Fatal(err)
				}
				snapshot := continuationQuotaState(t, db)
				if err = db.migrateRuntime(); err == nil {
					t.Fatal("deferred FK COMMIT succeeded")
				}
				if continuationQuotaState(t, db) != snapshot {
					t.Fatal("failed migration leaked pooled transaction")
				}
				external, err := sql.Open("sqlite", db.path)
				if err != nil {
					t.Fatal(err)
				}
				var version, objects int
				err = external.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE name='validation_tool_reservations')`).Scan(&version, &objects)
				external.Close()
				if err != nil || version != 33 || objects != 0 {
					t.Fatal("external rollback", version, objects, err)
				}
				if _, err = db.sql.Exec(`DROP TRIGGER tool_migration_commit; DROP TABLE tool_migration_fault`); err != nil {
					t.Fatal(err)
				}
			}
			if err = db.migrateRuntime(); err != nil {
				t.Fatal(err)
			}
			if continuationQuotaState(t, db) != before {
				t.Fatal("migration changed prior state or invented grant")
			}
		})
	}
}

func TestValidationToolBindingAndHistoryCannotBeErased(t *testing.T) {
	ctx := context.Background()
	db, c := validationToolQuotaFixture(t)
	owned, err := db.beginOwnerTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bindValidationTool(ctx, owned.tx, &c, true) == nil {
		t.Fatal("pin without stored grant admitted")
	}
	if err = db.reserveValidationTool(ctx, owned.tx, &c); err != nil {
		t.Fatal(err)
	}
	if err = owned.commit(); err != nil {
		t.Fatal(err)
	}
	owned.close()
	before := continuationQuotaState(t, db)
	owned, err = db.beginOwnerTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.close()
	if err = removeValidationToolSchema34Tx(owned.tx); err == nil {
		t.Fatal("erased charged tool history")
	}
	if _, err = owned.tx.Exec(`DELETE FROM validation_tool_reservations`); err == nil {
		t.Fatal("deleted durable grant")
	}
	if _, err = owned.tx.Exec(`UPDATE validation_tool_reservations SET host_identity_sha256=?`, strings.Repeat("b", 64)); err == nil {
		t.Fatal("rewrote physical identity")
	}
	wrong := c
	g := *c.ValidationTool
	g.ReservationID = "validation-tool-foreign"
	wrong.ValidationTool = &g
	if bindValidationTool(ctx, owned.tx, &wrong, true) == nil {
		t.Fatal("foreign grant rebound")
	}
	if _, err = owned.tx.Exec(`ALTER TABLE validation_tool_reservations RENAME TO hidden_tool_reservations`); err != nil {
		t.Fatal(err)
	}
	for _, live := range []bool{false, true} {
		if bindValidationTool(ctx, owned.tx, &c, live) == nil {
			t.Fatal("schema34 missing table interpreted as legacy", live)
		}
		legacy := c
		legacy.ValidationTool = nil
		legacy.AdapterPayload = json.RawMessage(`{"kind":"fixture"}`)
		if bindValidationTool(ctx, owned.tx, &legacy, live) == nil {
			t.Fatal("nil authority bypassed schema34 corruption", live)
		}
	}
	owned.close()
	if continuationQuotaState(t, db) != before {
		t.Fatal("refusal altered durable authority")
	}
}
