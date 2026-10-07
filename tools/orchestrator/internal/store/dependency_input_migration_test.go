package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"testing"
)

func downgradeDependencySchema(t *testing.T, db *DB) {
	t.Helper()
	downgradeOwnerAppliedSchema(t, db)
	for _, stmt := range []string{`DROP TRIGGER quota_dependency_inputs_INSERT`, `DROP TRIGGER quota_dependency_inputs_UPDATE`, `DROP TRIGGER quota_dependency_inputs_DELETE`, `DROP TRIGGER dependency_inputs_pair_insert`, `DROP TRIGGER dependency_inputs_pair_update`, `ALTER TABLE segment_runtime DROP COLUMN dependency_inputs_sha256`, `ALTER TABLE segment_runtime DROP COLUMN dependency_inputs_json`, `UPDATE schema_meta SET version=15`} {
		if _, err := db.sql.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDependencyMigrationLegacyPendingAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "rollback"}[fail], func(t *testing.T) {
			db, host := dependencyFixture(t, true)
			ctx := context.Background()
			// A v15 single-source pending grant has the exact historical three-field intent.
			if _, err := db.sql.Exec(`UPDATE tasks SET dependencies_json='["z"]' WHERE id='join'; UPDATE task_runtime SET adapter_payload='{"kind":"candidate","candidate_action":{"version":1,"operation":"validate","command":["/usr/bin/true"],"source_task":"z"}}' WHERE task_id='join'`); err != nil {
				t.Fatal(err)
			}
			c, err := db.ClaimReady(ctx, host, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			var usage int64
			downgradeDependencySchema(t, db)
			db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&usage)
			if fail {
				if _, err = db.sql.Exec(`CREATE TRIGGER dependency_inputs_pair_insert BEFORE INSERT ON segment_runtime BEGIN SELECT 1; END`); err != nil {
					t.Fatal(err)
				}
			}
			path := db.path
			db.Close()
			reopened, err := Open(path)
			if fail {
				if err == nil {
					reopened.Close()
					t.Fatal("broken migration accepted")
				}
				raw, e := sql.Open("sqlite", path)
				if e != nil {
					t.Fatal(e)
				}
				defer raw.Close()
				var version, cols int
				raw.QueryRow(`SELECT version FROM schema_meta`).Scan(&version)
				raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('segment_runtime') WHERE name LIKE 'dependency_inputs_%'`).Scan(&cols)
				if version != 15 || cols != 0 {
					t.Fatalf("partial migration %d/%d", version, cols)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			var raw, hash sql.NullString
			var after int64
			if err = reopened.sql.QueryRow(`SELECT dependency_inputs_json,dependency_inputs_sha256 FROM segment_runtime WHERE segment_id=?`, c.SegmentID).Scan(&raw, &hash); err != nil || raw.Valid || hash.Valid {
				t.Fatalf("legacy capture fabricated %v", err)
			}
			reopened.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&after)
			if after != usage {
				t.Fatal("migration changed usage")
			}
			pending, err := reopened.PendingLaunches(ctx, host, 1)
			if !errors.Is(err, contract.ErrHostArtifactGrant) || len(pending) != 0 {
				t.Fatalf("legacy pending gained live artifact authority %+v %v", pending, err)
			}
			var authority int
			if err = reopened.sql.QueryRow(`SELECT COUNT(*) FROM managed_artifact_reservations`).Scan(&authority); err != nil || authority != 0 {
				t.Fatal("legacy replay manufactured reservations", authority, err)
			}
		})
	}
}
