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

func gitFilterPayload(t *testing.T) json.RawMessage {
	t.Helper()
	h := strings.Repeat("a", 64)
	m := contract.GitFilterManifestV1{Version: 1, DriverID: "stream", Paths: []string{"file"}, Git: contract.GitFilterGitPin{Path: "/usr/bin/git", Version: "git version 2", SHA256: h}, Clean: contract.GitFilterToolPin{Path: "/private/clean", SHA256: h, Args: []string{}}, Smudge: contract.GitFilterToolPin{Path: "/private/smudge", SHA256: h, Args: []string{}}, Attributes: []contract.GitFilterAttributePin{}, ConfigSHA256: h}
	raw, err := json.Marshal(adapter.InvocationPayload{Kind: "provider", Provider: "claude", GitFilter: &m, CandidateWorkspace: &adapter.CandidateWorkspace{Version: 1, AutoDirectory: true, RepoRoot: "/private/repo", BaseOID: strings.Repeat("b", 40), Paths: []string{"file"}}, Profile: &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func gitFilterClaimFixture(t *testing.T) (*DB, contract.LaunchCommand, contract.HostHello) {
	t.Helper()
	db, h := diagnosticsLaunchFixture(t, "omitted", 1)
	// Controlled durable task fixture exercises ClaimReady without opening the
	// explicitly unsupported public admission gate or inventing process proof.
	if _, err := db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id='task'`, string(gitFilterPayload(t))); err != nil {
		t.Fatal(err)
	}
	c, err := db.ClaimReady(context.Background(), h.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return db, c, h
}
func gitFilterCount(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM git_filter_resource_reservations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGitFilterAdmissionRemainsClosed(t *testing.T) {
	var provider adapter.InvocationPayload
	if err := json.Unmarshal(gitFilterPayload(t), &provider); err != nil {
		t.Fatal(err)
	}
	validator := validationPolicyTask()
	var validate adapter.InvocationPayload
	if err := json.Unmarshal(validator.AdapterPayload, &validate); err != nil {
		t.Fatal(err)
	}
	validate.GitFilter = provider.GitFilter
	raw, _ := json.Marshal(validate)
	if _, err := adapter.DecodeInvocationPayload(raw); err != nil {
		t.Fatal("typed validate filter", err)
	}
	if _, err := adapter.ValidationRetryPayload(raw); err == nil {
		t.Fatal("filter auto retry accepted")
	}

	db, _ := diagnosticsLaunchFixture(t, "omitted", 1)
	before := diagnosticsBusinessSnapshot(t, db)
	p := diagnosticsPlan(nil)
	p.Run.ID = "new-run"
	p.Tasks[0].RunID = p.Run.ID
	p.Tasks[0].ID = "new-task"
	p.Tasks[0].AdapterPayload = gitFilterPayload(t)
	if _, err := db.SubmitPlan(context.Background(), p); !errors.Is(err, CodeError("git_filter_pipeline_unsupported")) {
		t.Fatal(err)
	}
	if diagnosticsBusinessSnapshot(t, db) != before || gitFilterCount(t, db) != 0 {
		t.Fatal("refused admission left state")
	}
}

func TestGitFilterReservationQuotaReplayAndCommit(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"raw_exact", "raw_short", "control_exact", "control_short", "commit_failure", "replay_physical"} {
		t.Run(mode, func(t *testing.T) {
			db, h := diagnosticsLaunchFixture(t, "omitted", 1)
			before := diagnosticsBusinessSnapshot(t, db)
			db.artifactLimit = contract.HostArtifactReservedBytes + contract.GitFilterResourceMaximumBytes
			if mode == "raw_short" {
				db.artifactLimit--
			}
			if _, err := db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE task_id='task'`, string(gitFilterPayload(t))); err != nil {
				t.Fatal(err)
			}
			if mode == "control_exact" || mode == "control_short" {
				// Probe the actual SQL accounting in a separate DB, not a hand-coded
				// allowance, then compare same fixed-length dispatch identities.
				probe, _, _ := gitFilterClaimFixture(t)
				var total int64
				if err := probe.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&total); err != nil {
					t.Fatal(err)
				}
				db.userControlLimit = total
				if mode == "control_short" {
					db.userControlLimit--
				}
			}
			if mode == "commit_failure" {
				if _, err := db.sql.Exec(`CREATE TABLE filter_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER filter_commit_trigger AFTER INSERT ON git_filter_resource_reservations BEGIN INSERT INTO filter_commit_fault VALUES('absent'); END`); err != nil {
					t.Fatal(err)
				}
			}
			c, err := db.ClaimReady(ctx, h.LaunchID, 1, 1)
			if mode == "raw_short" || mode == "control_short" || mode == "commit_failure" {
				if err == nil {
					t.Fatal("unfunded/failed COMMIT dispatch accepted")
				}
				if gitFilterCount(t, db) != 0 || diagnosticsBusinessSnapshot(t, db) != before {
					t.Fatal("failed dispatch left obligations")
				}
				external, e := sql.Open("sqlite", db.path)
				if e != nil {
					t.Fatal(e)
				}
				defer external.Close()
				var count int
				if e = external.QueryRow(`SELECT COUNT(*) FROM git_filter_resource_reservations`).Scan(&count); e != nil || count != 0 {
					t.Fatal("external dirty obligation", count, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if contract.RequireGitFilter(c) != nil || contract.ValidateDependencyInputs(c) != nil || gitFilterCount(t, db) != 1 {
				t.Fatal("unbound dispatch")
			}
			var charged, oracle int64
			if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&charged); err != nil {
				t.Fatal(err)
			}
			query := `SELECT 7*8+131074+1024+128+length(CAST(id AS BLOB))+length(CAST(run_id AS BLOB))+length(CAST(task_id AS BLOB))+length(CAST(attempt_id AS BLOB))+length(CAST(segment_id AS BLOB))+length(CAST(producer_id AS BLOB))+length(CAST(command_id AS BLOB))+length(CAST(scope_sha256 AS BLOB))+length(CAST(manifest_sha256 AS BLOB))+length(CAST(clean_tool_sha256 AS BLOB))+length(CAST(smudge_tool_sha256 AS BLOB))+length(CAST(created_at AS BLOB))+length(CAST(state AS BLOB))+length(CAST(host_identity_sha256 AS BLOB)) FROM git_filter_resource_reservations`
			if err = db.sql.QueryRow(query).Scan(&oracle); err != nil || oracle <= contract.GitFilterMetadataBytes || charged < oracle {
				t.Fatal("SQL quota oracle", oracle, charged, err)
			}
			if mode == "replay_physical" {
				path := db.path
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, e := Open(path)
				if e != nil {
					t.Fatal(e)
				}
				defer reopened.Close()
				pending, e := reopened.PendingLaunches(ctx, h.LaunchID, 1)
				if e != nil || len(pending) != 1 || *pending[0].GitFilter != *c.GitFilter || pending[0].LaunchIntentHash != c.LaunchIntentHash {
					t.Fatal("replay identity", pending, e)
				}
				if _, e = reopened.sql.Exec(`UPDATE runtime_hosts SET pid=pid+1,birth='replacement' WHERE id=?`, c.HostID); e != nil {
					t.Fatal(e)
				}
				if _, e = reopened.PendingLaunches(ctx, h.LaunchID, 1); e == nil {
					t.Fatal("physical rebind adopted old resource")
				}
			}
		})
	}
}

func TestGitFilterLifecycleAuthenticatedRelease(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"known", "launch_rejected", "unknown", "unknown_cleanup", "missing_cleanup", "wrong_cleanup", "rebound", "foreign_command"} {
		t.Run(mode, func(t *testing.T) {
			db, c, _ := gitFilterClaimFixture(t)
			g := c.GitFilter
			r := &contract.GitFilterCleanup{Version: 1, ReservationID: g.ReservationID, ScopeSHA256: g.ScopeSHA256, ManifestSHA256: g.ManifestSHA256, CleanToolSHA256: g.CleanToolSHA256, SmudgeToolSHA256: g.SmudgeToolSHA256, JournalSHA256: runtimeHash("removed"), RemovedBytes: contract.GitFilterResourceMaximumBytes}
			e := contract.Event{Version: 1, ProducerID: c.HostID, EventID: "filter-terminal", RunID: c.RunID, TaskID: c.TaskID, AttemptID: c.AttemptID, SegmentID: c.SegmentID, CommandID: c.CommandID, WorkRevision: c.WorkRevision, ExecutionEpoch: 1, Sequence: 1, Kind: contract.EventExited, PayloadHash: runtimeHash("terminal"), ActiveMS: 1, GitFilterCleanup: r}
			switch mode {
			case "launch_rejected":
				e.Kind = contract.EventLaunchRejected
				e.ActiveMS = 0
				e.Artifact = managedHostArtifactForTest(t, db, c, "prelaunch", 0, []byte(`{"status":"launch_rejected","reason":"fixture_rejection"}`))
				e.PayloadHash = e.Artifact.SHA256
			case "unknown_cleanup":
				e.Kind = contract.EventUnknown
			case "unknown":
				e.Kind = contract.EventUnknown
				e.GitFilterCleanup = nil
			case "missing_cleanup":
				e.GitFilterCleanup = nil
			case "wrong_cleanup":
				r.CleanToolSHA256 = strings.Repeat("b", 64)
			case "rebound":
				if _, err := db.sql.Exec(`UPDATE runtime_hosts SET pid=pid+1,birth='replacement' WHERE id=?`, c.HostID); err != nil {
					t.Fatal(err)
				}
			case "foreign_command":
				e.CommandID = "foreign"
			}
			before := diagnosticsBusinessSnapshot(t, db)
			ack, err := db.CommitHostEvent(ctx, e)
			var state string
			if scan := db.sql.QueryRow(`SELECT state FROM git_filter_resource_reservations`).Scan(&state); scan != nil {
				t.Fatal(scan)
			}
			if mode == "unknown" {
				if err != nil || state != "reserved" {
					t.Fatal("UNKNOWN lost obligation", state, err)
				}
				db.artifactLimit = contract.HostArtifactReservedBytes + contract.GitFilterResourceMaximumBytes
				owned, e2 := db.beginOwnerTransaction(ctx)
				if e2 != nil {
					t.Fatal(e2)
				}
				defer owned.close()
				if e2 = db.managedArtifactAdmission(ctx, owned.tx, 1, 0); !errors.Is(e2, CodeError("artifact_spool_full")) {
					t.Fatal("UNKNOWN did not retain full raw", e2)
				}
				return
			}
			if mode != "known" && mode != "launch_rejected" {
				if err == nil || state != "reserved" || diagnosticsBusinessSnapshot(t, db) != before {
					t.Fatal("unauthorized terminal changed state", state, err)
				}
				return
			}
			if err != nil || state != "released" {
				t.Fatal("known release", state, err)
			}
			again, e2 := db.CommitHostEvent(ctx, e)
			if e2 != nil || again != ack {
				t.Fatal("durable replay", e2)
			}

			var outstanding int64
			if err = db.sql.QueryRow(`SELECT COALESCE((SELECT SUM(reserved_bytes-retained_bytes) FROM managed_artifact_reservations WHERE state!='released'),0)+COALESCE((SELECT SUM(m.reserved_bytes-COALESCE((SELECT SUM(f.manifest_bytes) FROM managed_artifact_refs f WHERE f.reservation_id=r.id),0)) FROM managed_host_artifact_metadata m JOIN managed_artifact_reservations r ON r.id=m.reservation_id WHERE r.state!='released'),0)+COALESCE((SELECT SUM(metadata_bytes) FROM git_filter_resource_reservations),0)`).Scan(&outstanding); err != nil {
				t.Fatal(err)
			}
			free := db.minFreeBytes + db.criticalDiskReserve + db.sqliteWriteOverhead + uint64(outstanding)
			db.storageAvailable = func(string) (uint64, error) { return free, nil }
			owned, e2 := db.beginOwnerTransaction(ctx)
			if e2 != nil {
				t.Fatal(e2)
			}
			defer owned.close()
			if err = db.managedArtifactAdmission(ctx, owned.tx, 0, 0); err != nil {
				t.Fatal("released metadata exact", err)
			}
			free--
			if err = db.managedArtifactAdmission(ctx, owned.tx, 0, 0); !errors.Is(err, CodeError("storage_blocked")) {
				t.Fatal("released metadata one short", err)
			}
			historical := c
			historical.GitFilter = nil
			if bindGitFilter(ctx, owned.tx, &historical, false) != nil || *historical.GitFilter != *g {
				t.Fatal("lost release history")
			}
			if bindGitFilter(ctx, owned.tx, &historical, true) == nil {
				t.Fatal("released resources became live")
			}
			if _, e2 = owned.tx.Exec(`DELETE FROM git_filter_resource_reservations`); e2 == nil {
				t.Fatal("history erased")
			}
			owned.close()
		})
	}
}

func TestGitFilterMigration35PreservesAndRollsBack(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(fmt.Sprint("fault_", fault), func(t *testing.T) {
			db, h := diagnosticsLaunchFixture(t, "omitted", 1)
			c, err := db.ClaimReady(context.Background(), h.LaunchID, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			prior := contract.Event{Version: 1, ProducerID: c.HostID, EventID: "legacy-exited", RunID: c.RunID, TaskID: c.TaskID, AttemptID: c.AttemptID, SegmentID: c.SegmentID, CommandID: c.CommandID, WorkRevision: c.WorkRevision, ExecutionEpoch: 1, Sequence: 1, Kind: contract.EventExited, PayloadHash: runtimeHash("legacy"), ActiveMS: 1}
			if _, err = db.CommitHostEvent(context.Background(), prior); err != nil {
				t.Fatal(err)
			}
			before := diagnosticsBusinessSnapshot(t, db)
			if _, err := db.sql.Exec(`DROP TRIGGER quota_git_filter_resource_INSERT;DROP TRIGGER git_filter_resource_identity_immutable;DROP TRIGGER git_filter_resource_retained;DROP TRIGGER git_filter_resource_initial;DROP TRIGGER git_filter_resource_release_proof;DROP TABLE git_filter_resource_reservations;UPDATE schema_meta SET version=35`); err != nil {
				t.Fatal(err)
			}
			if fault {
				if _, err := db.sql.Exec(`CREATE TABLE filter_migration_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED);CREATE TRIGGER filter_migration_trigger AFTER UPDATE OF version ON schema_meta WHEN NEW.version=36 BEGIN INSERT INTO filter_migration_fault VALUES('missing');END`); err != nil {
					t.Fatal(err)
				}
				if err := db.migrateRuntime(); err == nil {
					t.Fatal("deferred COMMIT accepted")
				}
				external, e := sql.Open("sqlite", db.path)
				if e != nil {
					t.Fatal(e)
				}
				var version, count int
				e = external.QueryRow(`SELECT (SELECT version FROM schema_meta),(SELECT COUNT(*) FROM sqlite_master WHERE name='git_filter_resource_reservations')`).Scan(&version, &count)
				external.Close()
				if e != nil || version != 35 || count != 0 {
					t.Fatal("external rollback", version, count, e)
				}
				if _, e = db.sql.Exec(`DROP TRIGGER filter_migration_trigger;DROP TABLE filter_migration_fault`); e != nil {
					t.Fatal(e)
				}
			}
			if err := db.migrateRuntime(); err != nil {
				t.Fatal(err)
			}
			if diagnosticsBusinessSnapshot(t, db) != before || gitFilterCount(t, db) != 0 {
				t.Fatal("migration rewrote prior data")
			}
		})
	}
}
