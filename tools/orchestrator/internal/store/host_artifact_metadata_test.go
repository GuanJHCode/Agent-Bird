package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestHostArtifactDispatchPrechargesOfflineManifestBytes(t *testing.T) {
	db, host := diagnosticsLaunchFixture(t, "omitted", 1)
	var before, globalBefore int64
	if err := db.sql.QueryRow(`SELECT COALESCE((SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'),0),COALESCE((SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''),0)`).Scan(&before, &globalBefore); err != nil {
		t.Fatal(err)
	}
	g, err := db.ClaimReady(context.Background(), host.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	after, globalAfter := continuationQuotaUsage(t, db)
	const manifests = int64(22 * 1024)
	if after-before < manifests || globalAfter-globalBefore < manifests {
		t.Fatal("offline manifest capacity was not charged before dispatch", after-before, globalAfter-globalBefore)
	}
	var count int
	var reserved int64
	err = db.sql.QueryRow(`SELECT COUNT(*),SUM(m.reserved_bytes) FROM managed_host_artifact_metadata m JOIN managed_artifact_reservations r ON r.id=m.reservation_id WHERE r.scope_key=? AND r.run_id=?`, g.SegmentID, g.RunID).Scan(&count, &reserved)
	if err != nil || count != 2 || reserved != manifests {
		t.Fatal("dispatch lacks its exact physical metadata reservation", count, reserved, err)
	}
}

// This downgrade keeps the v20 Host grants and legacy refs. It removes only
// schema21's exact metadata charge; it does not rewrite candidate intent hashes.
func downgradeHostMetadataSchema(t *testing.T, db *DB) {
	t.Helper()
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	downgradeHostArtifactAccounting(t, tx)
	if _, err = tx.Exec(`UPDATE storage_usage SET total_bytes=total_bytes-COALESCE((SELECT SUM(64+length(CAST(m.reservation_id AS BLOB))+length(CAST(m.run_id AS BLOB))+m.reserved_bytes) FROM managed_host_artifact_metadata m WHERE storage_usage.scope='global' OR (storage_usage.scope='run' AND storage_usage.scope_id=m.run_id)),0);
DROP TRIGGER artifact_host_ref_binding;
DROP TABLE managed_host_artifact_metadata;
ALTER TABLE managed_artifact_refs DROP COLUMN manifest_bytes;
UPDATE schema_meta SET version=20;`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func downgradeHostArtifactAccounting(t *testing.T, tx *sql.Tx) {
	t.Helper()
	for _, name := range []string{"quota_runtime_events_INSERT", "quota_runtime_events_UPDATE", "quota_runtime_events_DELETE", "quota_managed_artifact_refs_INSERT"} {
		var ddl string
		if err := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		for _, ref := range []string{"NEW", "OLD"} {
			ddl = strings.ReplaceAll(ddl, managedHostRuntimeBytes(ref+".body_json"), runtimeStoredBytes(ref+".body_json"))
		}
		if name == "quota_managed_artifact_refs_INSERT" {
			// The old ref counter charged total control bytes only.
			start := strings.Index(ddl, ",progress_bytes=progress_bytes+(")
			for start >= 0 {
				end := strings.Index(ddl[start:], " WHERE scope=")
				if end < 0 {
					t.Fatal("unexpected ref quota DDL")
				}
				ddl = ddl[:start] + ddl[start+end:]
				start = strings.Index(ddl, ",progress_bytes=progress_bytes+(")
			}
		}
		if strings.Contains(ddl, "manifest_bytes") {
			t.Fatal("historical accounting retained modern ref semantics", name)
		}
		if _, err := tx.Exec("DROP TRIGGER " + name); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DROP TRIGGER runtime_host_artifact_charge_binding; DROP TRIGGER runtime_host_artifact_charge_immutable; ALTER TABLE runtime_events DROP COLUMN host_artifact_verified;`); err != nil {
		t.Fatal(err)
	}
}

func TestHostArtifactMetadataMigrationChargesOnceWithoutClaimingFiles(t *testing.T) {
	for _, state := range []string{"reserved", "active", "released"} {
		t.Run(state, func(t *testing.T) {
			db, g := quotaFixture(t)
			downgradeHostMetadataSchema(t, db)
			if _, err := db.sql.Exec(`UPDATE managed_artifact_reservations SET state=? WHERE id=?`, state, g.HostArtifacts.TerminalReservationID); err != nil {
				t.Fatal(err)
			}
			legacy := `{"path":"/unverified/old","size":999}`
			if _, err := db.sql.Exec(`INSERT INTO managed_artifact_refs(id,reservation_id,run_id,slot,intake_id,proof_json) VALUES('legacy',?,?, 'old-claim',NULL,?)`, g.HostArtifacts.TerminalReservationID, g.RunID, legacy); err != nil {
				t.Fatal(err)
			}
			before, globalBefore := continuationQuotaUsage(t, db)
			var expected int64
			if err := db.sql.QueryRow(`SELECT SUM(64+length(CAST(id AS BLOB))+length(CAST(run_id AS BLOB))+CASE class WHEN 'terminal' THEN 1024 ELSE 21504 END) FROM managed_artifact_reservations WHERE class IN ('terminal','progress')`).Scan(&expected); err != nil {
				t.Fatal(err)
			}
			path := db.path
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				opened, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				after, globalAfter := continuationQuotaUsage(t, opened)
				var version, count, credited int
				var saved string
				err = opened.sql.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM managed_host_artifact_metadata),(SELECT manifest_bytes FROM managed_artifact_refs WHERE id='legacy'),(SELECT proof_json FROM managed_artifact_refs WHERE id='legacy')`).Scan(&version, &count, &credited, &saved)
				if err != nil || version != currentSchemaVersion || count != 2 || credited != 0 || saved != legacy || after != before+expected || globalAfter != globalBefore+expected {
					t.Fatal("migration changed ownership or repeated metadata charge", version, count, credited, saved, after, globalAfter, expected, err)
				}
				if err = opened.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Lstat(filepath.Join(filepath.Dir(path), "host-spool")); !os.IsNotExist(err) {
				t.Fatal("migration created Host files", err)
			}
		})
	}
}

func TestHostArtifactMetadataMigrationRollsBackBackfillAndDDL(t *testing.T) {
	db, _ := quotaFixture(t)
	downgradeHostMetadataSchema(t, db)
	before, globalBefore := continuationQuotaUsage(t, db)
	if _, err := db.sql.Exec(`CREATE TRIGGER fail_metadata_backfill BEFORE UPDATE ON storage_usage WHEN NEW.total_bytes>OLD.total_bytes BEGIN SELECT RAISE(ABORT,'fixture_backfill_failure'); END`); err != nil {
		t.Fatal(err)
	}
	path := db.path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(path); err == nil {
		reopened.Close()
		t.Fatal("failed backfill committed")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var version, tables, columns int
	var after, globalAfter int64
	err = raw.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_host_artifact_metadata'),(SELECT COUNT(*) FROM pragma_table_info('managed_artifact_refs') WHERE name='manifest_bytes'),(SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'),(SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id='')`).Scan(&version, &tables, &columns, &after, &globalAfter)
	if err != nil || version != 20 || tables != 0 || columns != 0 || after != before || globalAfter != globalBefore {
		t.Fatal("migration escaped transaction rollback", version, tables, columns, after, globalAfter, err)
	}
}

// SQL tests exercise the durable shape guard only. A syntactically bound proof
// is not filesystem authority; Store must verify it under the original object
// lock before inserting this row in a real event transaction.
func TestHostArtifactMetadataSQLRefBindings(t *testing.T) {
	db, g := quotaFixture(t)
	insert := func(tx *sql.Tx, class, kind string, index int, slot string, manifestAdjust int, run string) error {
		reservation := g.HostArtifacts.TerminalReservationID
		if class == "progress" {
			reservation = g.HostArtifacts.ProgressReservationID
		}
		d := artifacts.HostSlotDescriptor{HostSlotBinding: artifacts.HostSlotBinding{Version: 1, ReservationID: reservation, ScopeSHA256: g.HostArtifacts.ScopeSHA256, Class: class, Index: index}, Kind: kind, Size: 1, SHA256: runtimeHash("x")}
		raw, _ := json.Marshal(d)
		id := fmt.Sprintf("fixture-%s-%s-%d", class, kind, index)
		proof, _ := json.Marshal(artifacts.Proof{IntakeID: id, Size: 1, SHA256: d.SHA256})
		receipt, _ := json.Marshal(contract.HostArtifactReceipt{Version: 1, Descriptor: raw, Proof: proof})
		_, err := tx.Exec(`INSERT INTO managed_artifact_refs(id,reservation_id,run_id,slot,intake_id,proof_json,manifest_bytes) VALUES(?,?,?,?,NULL,?,?)`, id, reservation, run, slot, string(receipt), len(raw)+manifestAdjust)
		return err
	}
	for _, tc := range []struct {
		name, class, kind, slot, run string
		index, adjust                int
		valid                        bool
	}{
		{"terminal", "terminal", "terminal", "0", g.RunID, 0, 0, true},
		{"prelaunch", "terminal", "prelaunch", "0", g.RunID, 0, 0, true},
		{"terminal_second_slot", "terminal", "prelaunch", "1", g.RunID, 1, 0, false},
		{"progress_last", "progress", "progress", "20", g.RunID, 20, 0, true},
		{"progress_overflow", "progress", "progress", "21", g.RunID, 21, 0, false},
		{"slot_alias", "progress", "progress", "00", g.RunID, 0, 0, false},
		{"wrong_kind", "progress", "terminal", "0", g.RunID, 0, 0, false},
		{"wrong_run", "terminal", "terminal", "0", "foreign-run", 0, 0, false},
		{"false_length", "terminal", "terminal", "0", g.RunID, 0, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = insert(tx, tc.class, tc.kind, tc.index, tc.slot, tc.adjust, tc.run)
			if tc.valid && err != nil || !tc.valid && err == nil {
				t.Fatal("unexpected SQL binding", err)
			}
		})
	}
	t.Run("progress_unique_slots", func(t *testing.T) {
		tx, err := db.sql.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		for i := 0; i < 21; i++ {
			if err = insert(tx, "progress", "progress", i, strconv.Itoa(i), 0, g.RunID); err != nil {
				t.Fatal(i, err)
			}
		}
		if err = insert(tx, "progress", "progress", 0, "0", 0, g.RunID); err == nil {
			t.Fatal("duplicate physical slot was charged twice")
		}
	})
}

func TestHostArtifactOfflineMetadataProtectsOtherAdmission(t *testing.T) {
	db, _ := quotaFixture(t)
	const manifests = uint64(22 * 1024)
	base := db.minFreeBytes + db.criticalDiskReserve + db.sqliteWriteOverhead + uint64(contract.HostArtifactReservedBytes)
	for _, tc := range []struct {
		name      string
		available uint64
		blocked   bool
	}{{"one_byte_short", base + manifests - 1, true}, {"exact", base + manifests, false}} {
		t.Run(tc.name, func(t *testing.T) {
			db.storageAvailable = func(string) (uint64, error) { return tc.available, nil }
			tx, err := db.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = db.managedArtifactAdmission(context.Background(), tx, 0, 0)
			if tc.blocked && !errors.Is(err, CodeError("storage_blocked")) || !tc.blocked && err != nil {
				t.Fatal("outstanding offline metadata was omitted or counted twice", err)
			}
		})
	}
}
