package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// These direct SQL fixtures test durable constraints, not kernel liveness or
// the Coordinator's authority to enqueue a live owner's task.
type liveResumeSchemaRow struct {
	command, hash, run, task, launch, owner, last, created string
	revision, control, epoch, ready, count                 int64
}

func liveResumeSchemaFixture(t *testing.T) (originalOwnerFixture, liveResumeSchemaRow) {
	t.Helper()
	f, request, observed := ownerSuccessionResumeFixture(t)
	if err := f.db.QueueResume(context.Background(), request.TaskID, request.WorkRevision); err != nil {
		t.Fatal(err)
	}
	hash, err := originalOwnerResumeHash(request)
	if err != nil {
		t.Fatal(err)
	}
	s := liveResumeSchemaRow{command: request.CommandID, hash: hash, run: "owner-run", task: request.TaskID, launch: f.prepare.Host.LaunchID, owner: observed.OwnerID, created: "2026-10-08T01:00:00Z", revision: 1, control: 1, epoch: 1}
	if err = f.db.sql.QueryRow(`SELECT ready_sequence FROM task_runtime WHERE task_id=?`, s.task).Scan(&s.ready); err != nil {
		t.Fatal(err)
	}
	return f, s
}

func insertLiveResumeSchema(db *sql.DB, s liveResumeSchemaRow) error {
	_, err := db.Exec(`INSERT INTO existing_owner_resume_commands(command_id,request_hash,run_id,task_id,launch_id,owner_id,last_segment_id,created_at,work_revision,control_revision,coordinator_epoch,ready_sequence,segment_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, s.command, s.hash, s.run, s.task, s.launch, s.owner, s.last, s.created, s.revision, s.control, s.epoch, s.ready, s.count)
	return err
}

func TestExistingOwnerResumeSchemaAccountsAndFreezesReceipt(t *testing.T) {
	f, s := liveResumeSchemaFixture(t)
	beforeGlobal := collectionUsage(t, f.db.sql, "global", "")
	beforeRun := collectionUsage(t, f.db.sql, "run", s.run)
	if err := insertLiveResumeSchema(f.db.sql, s); err != nil {
		t.Fatal("live-owner immutable receipt missing", err)
	}
	want := int64(40)
	for _, v := range []string{s.command, s.hash, s.run, s.task, s.launch, s.owner, s.last, s.created} {
		want += int64(len(v))
	}
	if collectionUsage(t, f.db.sql, "global", "")-beforeGlobal != want || collectionUsage(t, f.db.sql, "run", s.run)-beforeRun != want {
		t.Fatal("live receipt UTF8 charge differs from five integers and eight texts")
	}
	before := ownerSuccessionResumeTablesSnapshot(t, f.db, "existing_owner_resume_commands", "storage_usage")
	for _, change := range []string{"command_id='changed'", "request_hash='" + strings.Repeat("f", 64) + "'", "created_at='changed'", "last_segment_id='changed'", "work_revision=2", "control_revision=2", "coordinator_epoch=2", "ready_sequence=ready_sequence+1", "segment_count=1", "DELETE"} {
		t.Run(change, func(t *testing.T) {
			q := "UPDATE existing_owner_resume_commands SET " + change
			if change == "DELETE" {
				q = "DELETE FROM existing_owner_resume_commands"
			}
			if _, err := f.db.sql.Exec(q); err == nil {
				t.Fatal("immutable receipt/history changed")
			}
			if ownerSuccessionResumeTablesSnapshot(t, f.db, "existing_owner_resume_commands", "storage_usage") != before {
				t.Fatal("rejected mutation changed quota or receipt")
			}
		})
	}
}

func TestExistingOwnerResumeSchemaRejectsDrift(t *testing.T) {
	for _, kind := range []string{"task", "run", "launch", "owner", "revision", "control", "epoch", "ready", "count", "last", "unapplied", "retired", "not_queued", "released"} {
		t.Run(kind, func(t *testing.T) {
			f, s := liveResumeSchemaFixture(t)
			var err error
			switch kind {
			case "task":
				s.task = "foreign"
			case "run":
				s.run = "foreign"
			case "launch":
				s.launch = "foreign"
			case "owner":
				s.owner = "foreign"
			case "revision":
				s.revision++
			case "control":
				s.control++
			case "epoch":
				s.epoch++
			case "ready":
				s.ready++
			case "count":
				s.count++
			case "last":
				s.last = "claimed-segment"
			case "unapplied":
				_, err = f.db.sql.Exec(`UPDATE execution_owners SET applied_control_revision=0 WHERE owner_id=?`, s.owner)
			case "retired":
				state, e := f.db.InspectOriginalOwner(context.Background(), s.owner)
				if e != nil {
					t.Fatal(e)
				}
				next := state.Intent
				next.Scope.OwnerID = "next"
				next.Scope.Incarnation++
				if _, e = tryOwnerSuccessionSchemaAuthorization(f.db, state.Intent, next, ""); e != nil {
					t.Fatal(e)
				}
				_, err = f.db.sql.Exec(`UPDATE execution_owners SET phase='retired',is_current=0 WHERE owner_id=?`, s.owner)
			case "not_queued":
				_, err = f.db.sql.Exec(`UPDATE tasks SET status='interrupted' WHERE id=?`, s.task)
			case "released":
				_, err = f.db.sql.Exec(`UPDATE host_launches SET status='released' WHERE id=?`, s.launch)
			}
			if err != nil {
				t.Fatal("invalid drift fixture", err)
			}
			before := collectionUsage(t, f.db.sql, "global", "")
			if err = insertLiveResumeSchema(f.db.sql, s); err == nil {
				t.Fatal("receipt admitted facts absent from current state")
			}
			if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM existing_owner_resume_commands`) != 0 || collectionUsage(t, f.db.sql, "global", "") != before {
				t.Fatal("refused receipt changed accounting")
			}
		})
	}
}

func TestExistingOwnerResumeSchemaCommandUniqueAcrossModes(t *testing.T) {
	for _, first := range []string{"live", "successor"} {
		t.Run(first, func(t *testing.T) {
			f, s := liveResumeSchemaFixture(t)
			s.command = "resume-fixture-command"
			state, err := f.db.InspectOriginalOwner(context.Background(), s.owner)
			if err != nil {
				t.Fatal(err)
			}
			next := state.Intent
			next.Scope.OwnerID = "next"
			next.Scope.Incarnation++
			if first == "live" {
				err = insertLiveResumeSchema(f.db.sql, s)
			} else {
				_, err = tryOwnerSuccessionSchemaAuthorization(f.db, state.Intent, next, "")
			}
			if err != nil {
				t.Fatal("first independent mode receipt", err)
			}
			before := ownerSuccessionResumeTablesSnapshot(t, f.db, "existing_owner_resume_commands", "owner_succession_authorizations", "storage_usage")
			if first == "live" {
				_, err = tryOwnerSuccessionSchemaAuthorization(f.db, state.Intent, next, "")
			} else {
				err = insertLiveResumeSchema(f.db.sql, s)
			}
			if err == nil || !strings.Contains(err.Error(), "owner_resume_command_conflict") {
				t.Fatal("cross-mode command did not fail its own identity gate", err)
			}
			if before != ownerSuccessionResumeTablesSnapshot(t, f.db, "existing_owner_resume_commands", "owner_succession_authorizations", "storage_usage") {
				t.Fatal("collision published second command")
			}
		})
	}
}

func TestExistingOwnerResumeSchemaQueuedReservationIsUnique(t *testing.T) {
	f, s := liveResumeSchemaFixture(t)
	if err := insertLiveResumeSchema(f.db.sql, s); err != nil {
		t.Fatal(err)
	}
	s.command = "another-command"
	if err := insertLiveResumeSchema(f.db.sql, s); err == nil {
		t.Fatal("same queued reservation acquired a second live command")
	}
}

func TestExistingOwnerResumeSchemaRejectsUnboundPhysicalHost(t *testing.T) {
	for _, change := range []string{"launch_host", "host_epoch", "host_released", "host_unknown_status"} {
		t.Run(change, func(t *testing.T) {
			f, s := liveResumeSchemaFixture(t)
			q := map[string]string{"launch_host": `UPDATE host_launches SET host_id='foreign'`, "host_epoch": `UPDATE runtime_hosts SET coordinator_epoch=2`, "host_released": `UPDATE runtime_hosts SET status='released'`, "host_unknown_status": `UPDATE runtime_hosts SET status='unknown-status'`}[change]
			if _, err := f.db.sql.Exec(q); err != nil {
				t.Fatal(err)
			}
			if err := insertLiveResumeSchema(f.db.sql, s); err == nil {
				t.Fatal("stale or foreign physical Host admitted live receipt")
			}
		})
	}
}

func TestExistingOwnerResumeSchemaAcceptsCurrentEpochWithOldAppliedOwner(t *testing.T) {
	f, s := liveResumeSchemaFixture(t)
	if err := f.db.BeginCoordinatorEpoch(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	s.epoch = 2
	if err := insertLiveResumeSchema(f.db.sql, s); err != nil {
		t.Fatal("offline current epoch with old signed owner permit rejected", err)
	}
	var hostEpoch, ownerEpoch uint64
	var hostStatus string
	if err := f.db.sql.QueryRow(`SELECT h.coordinator_epoch,p.coordinator_epoch,h.status FROM execution_owners p JOIN runtime_hosts h ON h.id=p.producer_id WHERE p.owner_id=?`, s.owner).Scan(&hostEpoch, &ownerEpoch, &hostStatus); err != nil || hostEpoch != 2 || ownerEpoch != 1 || hostStatus != "offline" {
		t.Fatal("epoch fixture did not preserve old owner/current offline Host", err)
	}
}

func TestExistingOwnerResumeSchemaRejectsUnprovedLegacyScope(t *testing.T) {
	for _, kind := range []string{"scope_version", "owner_id", "run_id", "launch_id", "producer_id", "incarnation", "intent_key", "control_run", "control_key"} {
		t.Run(kind, func(t *testing.T) {
			f, s := liveResumeSchemaFixture(t)
			if kind == "control_run" {
				if _, err := f.db.SubmitPlan(context.Background(), providerTestPlan("foreign-run", "foreign-controller", "grok-build")); err != nil {
					t.Fatal(err)
				}
			}
			// Schema25 freezes new rows; construct the historically admissible
			// mismatch only in an actual downgraded pre25 database, then migrate.
			downgradeOwnerSuccessionSchema25(t, f.db)
			var err error
			if kind == "control_run" {
				_, err = f.db.sql.Exec(`UPDATE owner_control_changes SET run_id='foreign-run' WHERE owner_id=?`, s.owner)
			} else if kind == "control_key" {
				_, err = f.db.sql.Exec(`UPDATE owner_control_changes SET coordinator_key_id=? WHERE owner_id=?`, strings.Repeat("b", 64), s.owner)
			} else {
				var raw string
				if err = f.db.sql.QueryRow(`SELECT intent_json FROM execution_owners WHERE owner_id=?`, s.owner).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var intent map[string]any
				if err = json.Unmarshal([]byte(raw), &intent); err != nil {
					t.Fatal(err)
				}
				scope := intent["scope"].(map[string]any)
				switch kind {
				case "scope_version":
					scope["version"] = 2
				case "incarnation":
					scope["incarnation"] = 2
				case "intent_key":
					intent["coordinator_key_id"] = strings.Repeat("b", 64)
				default:
					scope[kind] = "foreign"
				}
				body, e := json.Marshal(intent)
				if e != nil {
					t.Fatal(e)
				}
				_, err = f.db.sql.Exec(`UPDATE execution_owners SET intent_json=? WHERE owner_id=?`, string(body), s.owner)
			}
			if err != nil {
				t.Fatal("pre25 mismatch fixture rejected", err)
			}
			path := f.db.path
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(path)
			if err != nil {
				t.Fatal("schema migration changed legacy rejection policy", err)
			}
			defer opened.Close()
			if err = insertLiveResumeSchema(opened.sql, s); err == nil {
				t.Fatal("unproved migrated scope acquired permanent replay authority")
			}
		})
	}
}
