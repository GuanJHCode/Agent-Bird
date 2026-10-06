package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func coreRecoveryScopeFixture(t *testing.T) (*DB, contract.HostHello) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	plan, err := db.SubmitPlan(context.Background(), PlanSpec{
		Run:   RunSpec{ID: "core-scope-run", ControllerThread: "owner", PlanRevision: 1, OriginContextID: "origin", OriginPID: 101, OriginBirth: "old-owner"},
		Host:  HostLaunchSpec{OriginContextID: "origin", HostGeneration: "g0", Executable: "/pinned/orchestrator"},
		Tasks: []TaskSpec{{ID: "core-scope-task", RunID: "core-scope-run", MaxAttempts: 2, AdapterPayload: json.RawMessage(`{"kind":"fake"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := contract.HostHello{LaunchID: plan.LaunchID, LaunchToken: plan.LaunchToken, OriginContextID: "origin", OriginPID: 101, OriginBirth: "old-owner", HostGeneration: "g0", PID: 202, Birth: "old-host", Executable: "/pinned/orchestrator"}
	if _, err := db.RegisterHost(context.Background(), h, 1); err != nil {
		t.Fatal(err)
	}
	return db, h
}

func coreRebindScope(t *testing.T, db *DB, h contract.HostHello, steps int) contract.HostHello {
	t.Helper()
	for n := 1; n <= steps; n++ {
		h.HostGeneration, h.OriginPID, h.OriginBirth = fmt.Sprintf("g%d", n), 303+n, fmt.Sprintf("owner%d", n)
		if err := db.RebindOwner(context.Background(), RebindOwnerSpec{RunID: "core-scope-run", OriginContextID: h.OriginContextID, OriginPID: h.OriginPID, OriginBirth: h.OriginBirth, HostGeneration: h.HostGeneration, AttachmentProofSHA256: strings.Repeat("a", 64)}); err != nil {
			t.Fatal(err)
		}
	}
	h.PID, h.Birth = 909, "new-host"
	return h
}

func TestCoreOwnerRecoveryScopeUsesCommittedBoundedChain(t *testing.T) {
	for _, steps := range []int{1, 2, 64, 65} {
		t.Run(fmt.Sprint(steps), func(t *testing.T) {
			db, old := coreRecoveryScopeFixture(t)
			current := coreRebindScope(t, db, old, steps)
			pin := strings.Repeat("b", 64)
			prior := contract.HostRecoveryScopeSHA256(old, pin)
			p, err := db.AuthorizeRecoveryScope(context.Background(), current, prior, pin)
			if steps == 65 {
				if err == nil || err.Error() != "host_recovery_scope_chain_limit" {
					t.Fatalf("unbounded chain allowed: %+v %v", p, err)
				}
				return
			}
			if err != nil || p.OldPID != old.PID || p.OldBirth != old.Birth || p.ScopeSHA256 != contract.HostRecoveryScopeSHA256(current, pin) {
				t.Fatalf("scope proof=%+v err=%v", p, err)
			}
			var gen, status string
			if err := db.sql.QueryRow(`SELECT host_generation,status FROM host_launches WHERE id=?`, current.LaunchID).Scan(&gen, &status); err != nil || gen != current.HostGeneration || status != "offline" {
				t.Fatalf("proof changed launch: %q %q %v", gen, status, err)
			}
		})
	}
}

func TestCoreOwnerRecoveryScopeRejectsAuthAndHistoryDrift(t *testing.T) {
	for _, name := range []string{"token", "launch", "origin", "owner_pid", "owner_birth", "old_generation", "executable", "binary_digest", "foreign_scope", "uncommitted", "empty_old", "cycle", "cycle_after_match", "foreign_run", "row_owner", "ready_host"} {
		t.Run(name, func(t *testing.T) {
			db, old := coreRecoveryScopeFixture(t)
			h := coreRebindScope(t, db, old, 2)
			pin := strings.Repeat("b", 64)
			prior := contract.HostRecoveryScopeSHA256(old, pin)
			sql := ""
			switch name {
			case "token":
				h.LaunchToken = "foreign"
			case "launch":
				h.LaunchID = "foreign"
			case "origin":
				h.OriginContextID = "foreign"
			case "owner_pid":
				h.OriginPID++
			case "owner_birth":
				h.OriginBirth = "foreign"
			case "old_generation":
				h.HostGeneration = "g1"
			case "executable":
				h.Executable = "/foreign"
			case "binary_digest":
				pin = strings.Repeat("c", 64)
			case "foreign_scope":
				prior = strings.Repeat("c", 64)
			case "uncommitted":
				sql = `DELETE FROM owner_rebinds WHERE host_generation='g2'`
			case "empty_old":
				sql = `UPDATE owner_rebinds SET old_host_generation='' WHERE host_generation='g2'`
			case "cycle":
				sql = `UPDATE owner_rebinds SET old_host_generation='g2' WHERE host_generation='g1'`
			case "cycle_after_match":
				anchor := old
				anchor.HostGeneration = "g1"
				prior = contract.HostRecoveryScopeSHA256(anchor, pin)
				sql = `UPDATE owner_rebinds SET old_host_generation='g2' WHERE host_generation='g1'`
			case "foreign_run":
				if err := db.CreateRun(context.Background(), RunSpec{ID: "foreign-run", ControllerThread: "foreign", PlanRevision: 1, OriginContextID: "foreign", OriginPID: 808, OriginBirth: "foreign"}); err != nil {
					t.Fatal(err)
				}
				if _, err := db.sql.Exec(`INSERT INTO owner_rebinds(run_id,host_generation,old_host_generation,origin_pid,origin_birth,attachment_proof_sha256,created_at) VALUES('foreign-run','g1','g0',304,'owner1',?,'now')`, strings.Repeat("a", 64)); err != nil {
					t.Fatal(err)
				}
				sql = `DELETE FROM owner_rebinds WHERE run_id='core-scope-run' AND host_generation='g1'`
			case "row_owner":
				sql = `UPDATE owner_rebinds SET origin_pid=999 WHERE host_generation='g2'`
			case "ready_host":
				sql = `UPDATE host_launches SET status='ready'`
			}
			if sql != "" {
				if _, err := db.sql.Exec(sql); err != nil {
					t.Fatal(err)
				}
			}
			if p, err := db.AuthorizeRecoveryScope(context.Background(), h, prior, pin); err == nil {
				t.Fatalf("unsafe proof admitted: %+v", p)
			}
		})
	}
}

func TestCoreOwnerRecoveryScopeRejectsUnfinishedSegmentsAndStops(t *testing.T) {
	for _, status := range []string{"launch_requested", "prepared", "spawned", "running", "stopping", "unknown", "pending_stop", "exited", "not_started"} {
		t.Run(status, func(t *testing.T) {
			db, old := coreRecoveryScopeFixture(t)
			g, err := db.ClaimReady(context.Background(), old.LaunchID, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.sql.Exec(`UPDATE segment_runtime SET status='exited'`); err != nil {
				t.Fatal(err)
			}
			h := coreRebindScope(t, db, old, 1)
			if status == "pending_stop" {
				_, err = db.sql.Exec(`INSERT INTO stop_runtime VALUES('stop',?,?, 'controller_stop',1000,'pending','now')`, old.LaunchID, g.SegmentID)
			} else {
				_, err = db.sql.Exec(`UPDATE segment_runtime SET status=?`, status)
			}
			if err != nil {
				t.Fatal(err)
			}
			pin := strings.Repeat("b", 64)
			p, err := db.AuthorizeRecoveryScope(context.Background(), h, contract.HostRecoveryScopeSHA256(old, pin), pin)
			allowed := status == "exited" || status == "not_started"
			if (err == nil) != allowed {
				t.Fatalf("status=%s proof=%+v err=%v", status, p, err)
			}
		})
	}
}

func TestCoreOwnerRebindProofRollsBackAndMigratesLegacyWithoutGuessing(t *testing.T) {
	t.Run("rollback", func(t *testing.T) {
		db, _ := coreRecoveryScopeFixture(t)
		if _, err := db.sql.Exec(`CREATE TRIGGER refuse_owner BEFORE UPDATE ON runs BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
			t.Fatal(err)
		}
		err := db.RebindOwner(context.Background(), RebindOwnerSpec{RunID: "core-scope-run", OriginContextID: "origin", OriginPID: 303, OriginBirth: "new", HostGeneration: "g1", AttachmentProofSHA256: strings.Repeat("a", 64)})
		if err == nil {
			t.Fatal("injected transaction failure ignored")
		}
		var count int
		var gen string
		if err := db.sql.QueryRow(`SELECT COUNT(*) FROM owner_rebinds`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial proof=%d %v", count, err)
		}
		if err := db.sql.QueryRow(`SELECT host_generation FROM host_launches`).Scan(&gen); err != nil || gen != "g0" {
			t.Fatalf("partial generation=%s %v", gen, err)
		}
	})
	t.Run("legacy", func(t *testing.T) {
		db, old := coreRecoveryScopeFixture(t)
		h := coreRebindScope(t, db, old, 1)
		path := db.path
		downgradePolicySchema(t, db)
		if _, err := db.sql.Exec(`DROP TABLE config_preparation_abandons; DROP TABLE decision_attempt_bindings; DROP TABLE owner_control_changes; DROP TABLE execution_owners; ALTER TABLE host_launches DROP COLUMN execution_owner_mode; ALTER TABLE owner_rebinds DROP COLUMN old_host_generation; UPDATE schema_meta SET version=10`); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		migrated, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer migrated.Close()
		var oldGen string
		if err := migrated.sql.QueryRow(`SELECT old_host_generation FROM owner_rebinds WHERE host_generation='g1'`).Scan(&oldGen); err != nil || oldGen != "" {
			t.Fatalf("legacy relation guessed: %q %v", oldGen, err)
		}
		pin := strings.Repeat("b", 64)
		if _, err := migrated.AuthorizeRecoveryScope(context.Background(), h, contract.HostRecoveryScopeSHA256(old, pin), pin); err == nil {
			t.Fatal("legacy missing proof authorized")
		}
	})
}

func TestCoreHostPresenceSnapshotAuthenticatesAndKeepsUnboundDistinct(t *testing.T) {
	db, h := coreRecoveryScopeFixture(t)
	p, err := db.InspectHostPresence(context.Background(), h.LaunchID, h.LaunchToken)
	if err != nil || !p.Registered || p.PID != h.PID || p.Birth != h.Birth || p.Executable != h.Executable {
		t.Fatalf("presence=%+v err=%v", p, err)
	}
	if _, err := db.InspectHostPresence(context.Background(), h.LaunchID, "foreign"); err == nil {
		t.Fatal("foreign token admitted")
	}
}

func TestCoreOwnerRebindRetainsPriorGenerationInItsTransaction(t *testing.T) {
	db, _ := coreRecoveryScopeFixture(t)
	for _, step := range []struct {
		old, next string
		pid       int
		birth     string
	}{{"g0", "g1", 303, "new-owner"}, {"g1", "g2", 404, "newest-owner"}} {
		err := db.RebindOwner(context.Background(), RebindOwnerSpec{RunID: "core-scope-run", OriginContextID: "origin", OriginPID: step.pid, OriginBirth: step.birth, HostGeneration: step.next, AttachmentProofSHA256: strings.Repeat("a", 64)})
		if err != nil {
			t.Fatal(err)
		}
		var old string
		if err := db.sql.QueryRow(`SELECT old_host_generation FROM owner_rebinds WHERE run_id=? AND host_generation=?`, "core-scope-run", step.next).Scan(&old); err != nil || old != step.old {
			t.Fatalf("committed rebind lost exact prior generation: old=%q want=%q err=%v", old, step.old, err)
		}
	}
}
