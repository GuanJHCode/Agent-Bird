package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// These are trusted synthetic Store observations, never native death proofs.
// Coordinator acceptance must exercise the real kernel and locked recovery file.
func ownerSuccessionResumeFixture(t *testing.T) (originalOwnerFixture, OriginalOwnerResumeRequest, OriginalOwnerSuccessionObservation) {
	return ownerSuccessionResumeFixtureWithPlan(t, nil)
}

func ownerSuccessionResumeFixtureWithPlan(t *testing.T, configure func(*PlanSpec)) (originalOwnerFixture, OriginalOwnerResumeRequest, OriginalOwnerSuccessionObservation) {
	t.Helper()
	f := newOriginalOwnerFixtureWithPlan(t, nil, configure)
	i, err := f.db.PrepareOriginalOwner(context.Background(), f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.db.BindOriginalOwner(context.Background(), f.bindSpec(i), f.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.CommitOriginalOwnerApplied(context.Background(), appliedFixture(t, f, b), f.key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	request := OriginalOwnerResumeRequest{TaskID: "owner-run-task", ControllerThread: "controller", ControlToken: "resume-test-control-token", WorkRevision: 1, CommandID: "resume-command-界"}
	if _, err := f.db.sql.Exec(`UPDATE run_control SET token_hash=? WHERE run_id=?`, runtimeHash(request.ControlToken), i.Scope.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec(`UPDATE tasks SET status='interrupted' WHERE id=?`, request.TaskID); err != nil {
		t.Fatal(err)
	}
	observed := OriginalOwnerSuccessionObservation{OwnerID: i.Scope.OwnerID, Owner: contract.OriginalOwnerTransport{PID: b.Binding.Owner.PID, Birth: b.Binding.Owner.Birth}, Host: i.FirstHost, Origin: RunOwnerBinding{OriginContextID: i.Scope.OriginalOriginContextID, OriginPID: i.Scope.OriginalOriginPID, OriginBirth: i.Scope.OriginalOriginBirth}, Epoch: 1, ExecutableSHA256: i.Scope.ExecutableSHA256, OwnerExitedAt: "2026-10-08T00:00:00Z", HostExitedAt: "2026-10-08T00:00:00Z"}
	return f, request, observed
}

func ownerSuccessionResumeSnapshot(t *testing.T, db *DB) string {
	return ownerSuccessionResumeTablesSnapshot(t, db, "runs", "run_control", "owner_rebinds", "execution_owners", "owner_control_changes", "owner_succession_authorizations", "tasks", "task_runtime", "runtime_meta", "runtime_hosts", "host_launches", "attempts", "segments", "segment_runtime", "stop_runtime", "runtime_questions", "budget_runtime", "task_execution_policies", "storage_usage")
}

func ownerSuccessionResumeTablesSnapshot(t *testing.T, db *DB, tables ...string) string {
	t.Helper()
	var out []string
	for _, table := range tables {
		rows, err := db.sql.Query("SELECT * FROM " + table + " ORDER BY 1,2")
		if err != nil {
			t.Fatal(table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		out = append(out, table)
		for rows.Next() {
			values, dest := make([]any, len(columns)), make([]any, len(columns))
			for j := range values {
				dest[j] = &values[j]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			// Schema28 adds only a NULL reference to old rows; ignore that absent
			// evidence when comparing historical migration snapshots.
			for j := len(columns) - 1; j >= 0; j-- {
				if (columns[j] == "abandoned_owner_id" || columns[j] == "initial_proof_id") && values[j] == nil {
					values = append(values[:j], values[j+1:]...)
				}
			}
			body, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out = append(out, string(body))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return strings.Join(out, "\n")
}

func TestOwnerSuccessionResumeRejectsRunWideActivityAndQuestions(t *testing.T) {
	for _, mode := range []string{"launch_requested", "prepared", "spawned", "running", "stopping", "unknown", "pending_stop", "open", "answer_committed", "resume_queued", "resume_started", "other_ready", "other_resume_queued", "other_running", "other_stopping", "other_waiting_question", "other_launch"} {
		t.Run(mode, func(t *testing.T) {
			// The unsafe execution belongs to another task in the same run. The
			// target remains interrupted, so task-local queue checks cannot suffice.
			f, request, observed := ownerSuccessionResumeFixtureWithPlan(t, func(p *PlanSpec) {
				other := p.Tasks[0]
				other.ID = "other-task"
				p.Tasks = append(p.Tasks, other)
			})
			ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
			control, err := f.db.OriginalOwnerControl(ctx, observed.OwnerID, pub)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.ActivateOriginalOwner(ctx, appliedFixture(t, f, OriginalOwnerBindResult{Binding: control.Binding, Permit: control.Permit}), pub); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "other_") {
				status := strings.TrimPrefix(mode, "other_")
				if mode == "other_launch" {
					status = "interrupted"
					external, err := f.db.SubmitPlan(ctx, providerTestPlan("other-run", "other-controller", "grok-build"))
					if err != nil {
						t.Fatal(err)
					}
					if _, err = f.db.sql.Exec(`UPDATE task_runtime SET host_launch_id=? WHERE task_id='other-task'`, external.LaunchID); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.db.sql.Exec(`UPDATE tasks SET status=? WHERE id='other-task'`, status); err != nil {
					t.Fatal(err)
				}
			} else {
				grant, err := f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1)
				if err != nil || grant.TaskID != "other-task" {
					t.Fatal("missing other-task grant", err)
				}
				// Synthetic persisted fence states isolate each refusal. They are
				// not receipts from a real worker or native process lifecycle.
				if _, err = f.db.sql.Exec(`UPDATE tasks SET status='interrupted' WHERE id='other-task'`); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "pending_stop", "open", "answer_committed", "resume_queued", "resume_started":
					if _, err = f.db.sql.Exec(`UPDATE segment_runtime SET status='exited' WHERE segment_id=?`, grant.SegmentID); err != nil {
						t.Fatal(err)
					}
					if mode == "pending_stop" {
						_, err = f.db.sql.Exec(`INSERT INTO stop_runtime(command_id,host_id,segment_id,reason,deadline_unix_ms,status,created_at) VALUES('stop',?,?,'fixture',1,'pending','now')`, f.prepare.Host.LaunchID, grant.SegmentID)
					} else {
						_, err = f.db.sql.Exec(`INSERT INTO runtime_questions(question_id,task_id,attempt_id,segment_id,question_revision,work_revision,session_kind,session_id,status,created_at) VALUES('question',?,?,?,1,1,'fixture','session',?,'now')`, grant.TaskID, grant.AttemptID, grant.SegmentID, mode)
					}
				default:
					_, err = f.db.sql.Exec(`UPDATE segment_runtime SET status=? WHERE segment_id=?`, mode, grant.SegmentID)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			if _, err := f.db.ResumeOriginalOwner(ctx, request, observed, pub); err != CodeError("owner_succession_not_quiescent") {
				t.Fatal("run-wide fence not enforced", err)
			}
			if before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("unsafe run mutated during refusal")
			}
		})
	}
}

func TestOwnerSuccessionResumePreservesEpochAndAdjacentRebind(t *testing.T) {
	for _, mode := range []string{"epoch", "adjacent", "missing_link", "linked_generations", "same_generation_foreign_origin"} {
		t.Run(mode, func(t *testing.T) {
			f, request, observed := ownerSuccessionResumeFixture(t)
			ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
			if mode == "epoch" {
				if err := f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
					t.Fatal(err)
				}
				observed.Epoch = 2
			} else {
				observed.Origin.OriginPID++
				observed.Origin.OriginBirth = "new-origin"
				spec := RebindOwnerSpec{RunID: "owner-run", OriginContextID: observed.Origin.OriginContextID, OriginPID: observed.Origin.OriginPID, OriginBirth: observed.Origin.OriginBirth, HostGeneration: "next-generation", AttachmentProofSHA256: strings.Repeat("a", 64)}
				if mode == "same_generation_foreign_origin" {
					if _, err := f.db.sql.Exec(`UPDATE runs SET origin_pid=?,origin_birth=? WHERE id='owner-run'`, spec.OriginPID, spec.OriginBirth); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := f.db.RebindOwner(ctx, spec); err != nil {
						t.Fatal(err)
					}
					if mode == "missing_link" {
						if _, err := f.db.sql.Exec(`DELETE FROM owner_rebinds`); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "linked_generations" {
						spec.HostGeneration = "third-generation"
						if err := f.db.RebindOwner(ctx, spec); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			result, err := f.db.ResumeOriginalOwner(ctx, request, observed, pub)
			if mode == "epoch" || mode == "adjacent" || mode == "linked_generations" {
				if err != nil || result.SuccessorOrigin != observed.Origin || result.CoordinatorEpoch != observed.Epoch {
					t.Fatal("valid adjacent scope rejected", err)
				}
				state, err := f.db.InspectOriginalOwner(ctx, observed.OwnerID)
				if err != nil || state.Intent.Scope.OriginalOriginPID != 7 || state.Intent.Scope.HostGeneration != "owner-run" || state.CoordinatorEpoch != 1 {
					t.Fatal("succession rewrote predecessor scope/epoch", err)
				}
			} else if err == nil || before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("unproved rebind admitted or changed history", err)
			}
		})
	}
}

func TestOwnerSuccessionResumeRejectsChangedAuthenticatedCapability(t *testing.T) {
	for _, field := range []string{"controller", "token"} {
		t.Run(field, func(t *testing.T) {
			f, r, a := ownerSuccessionResumeFixture(t)
			pub := f.key.Public().(ed25519.PublicKey)
			if _, err := f.db.ResumeOriginalOwner(context.Background(), r, a, pub); err != nil {
				t.Fatal(err)
			}
			if field == "controller" {
				r.ControllerThread = "rotated-controller"
				if _, err := f.db.sql.Exec(`UPDATE runs SET controller_thread=? WHERE id='owner-run'`, r.ControllerThread); err != nil {
					t.Fatal(err)
				}
			} else {
				r.ControlToken = "rotated-token"
				if _, err := f.db.sql.Exec(`UPDATE run_control SET token_hash=? WHERE run_id='owner-run'`, runtimeHash(r.ControlToken)); err != nil {
					t.Fatal(err)
				}
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			if _, err := f.db.ResumeOriginalOwner(context.Background(), r, a, pub); err != ErrConflict {
				t.Fatal("changed but currently authenticated request replayed old command", err)
			}
			if before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("conflicting authenticated replay mutated state")
			}
		})
	}
}

func TestOwnerSuccessionResumeCommitsOnceAndReplaysAfterRestart(t *testing.T) {
	f, request, observed := ownerSuccessionResumeFixture(t)
	ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
	before := originalOwnerUsed(t, f)
	result, err := f.db.ResumeOriginalOwner(ctx, request, observed, pub)
	if err != nil {
		t.Fatal(err)
	}
	if result.AuthorizationID == "" || result.CommandID != request.CommandID || result.ReadySequence < 1 || result.PredecessorOwnerID != observed.OwnerID || result.CoordinatorEpoch != 1 {
		t.Fatal("missing original succession receipt")
	}
	state, err := f.db.InspectOriginalOwner(ctx, observed.OwnerID)
	if err != nil || state.Phase != "retired" || state.IsCurrent {
		t.Fatal("resume did not retire predecessor atomically", err)
	}
	var status, hostStatus, launchStatus string
	var sequence int64
	if err = f.db.sql.QueryRow(`SELECT t.status,tr.ready_sequence,h.status,l.status FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id JOIN host_launches l ON l.id=tr.host_launch_id JOIN runtime_hosts h ON h.id=l.host_id WHERE t.id=?`, request.TaskID).Scan(&status, &sequence, &hostStatus, &launchStatus); err != nil || status != "resume_queued" || sequence != result.ReadySequence || hostStatus != "offline" || launchStatus != "offline" {
		t.Fatal("task/transport fence split from authorization", err)
	}
	if originalOwnerUsed(t, f) <= before || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM owner_succession_authorizations`) != 1 {
		t.Fatal("authorization not accounted exactly once")
	}
	path := f.db.path
	f.db.Close()
	f.db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	if err = f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	snapshot := ownerSuccessionResumeSnapshot(t, f.db)
	// Mutable task/owner/epoch/quota no longer permit a fresh resume. Replay is
	// read-only and does not need to manufacture fresh predecessor observations.
	f.db.runControlLimit = 0
	again, err := f.db.ResumeOriginalOwner(ctx, request, OriginalOwnerSuccessionObservation{}, pub)
	if err != nil || again != result || snapshot != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("lost ACK replay changed receipt, state, budget or sequence", err)
	}
	if strings.Contains(snapshot, request.ControlToken) {
		t.Fatal("plaintext control capability entered durable authorization")
	}
}

func TestOwnerSuccessionResumeRejectsUnprovedOrDriftedFacts(t *testing.T) {
	for _, field := range []string{"owner_id", "owner_pid", "owner_birth", "host_pid", "host_birth", "origin", "origin_pid", "origin_birth", "epoch", "digest", "owner_exit", "host_exit", "bad_time", "unapplied", "revision", "controller", "token", "empty_command", "long_command", "invalid_utf8", "key"} {
		t.Run(field, func(t *testing.T) {
			f, r, a := ownerSuccessionResumeFixture(t)
			pub := f.key.Public().(ed25519.PublicKey)
			switch field {
			case "owner_id":
				a.OwnerID = "foreign"
			case "owner_pid":
				a.Owner.PID++
			case "owner_birth":
				a.Owner.Birth = "foreign"
			case "host_pid":
				a.Host.PID++
			case "host_birth":
				a.Host.Birth = "foreign"
			case "origin":
				a.Origin.OriginContextID = "foreign"
			case "origin_pid":
				a.Origin.OriginPID++
			case "origin_birth":
				a.Origin.OriginBirth = "foreign"
			case "epoch":
				a.Epoch++
			case "digest":
				a.ExecutableSHA256 = strings.Repeat("a", 64)
			case "owner_exit":
				a.OwnerExitedAt = ""
			case "host_exit":
				a.HostExitedAt = ""
			case "bad_time":
				a.OwnerExitedAt = "not-an-observation-time"
			case "unapplied":
				if _, err := f.db.sql.Exec(`UPDATE execution_owners SET applied_control_revision=0 WHERE owner_id=?`, a.OwnerID); err != nil {
					t.Fatal(err)
				}
			case "revision":
				r.WorkRevision++
			case "controller":
				r.ControllerThread = "foreign"
			case "token":
				r.ControlToken = "foreign"
			case "empty_command":
				r.CommandID = ""
			case "long_command":
				r.CommandID = strings.Repeat("界", 43)
			case "invalid_utf8":
				r.CommandID = string([]byte{255})
			case "key":
				pub = ed25519.NewKeyFromSeed([]byte(strings.Repeat("z", 32))).Public().(ed25519.PublicKey)
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			if _, err := f.db.ResumeOriginalOwner(context.Background(), r, a, pub); err == nil {
				t.Fatal("unproved or drifted resume authorized retirement")
			}
			if before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("rejected resume changed durable state")
			}
		})
	}
}

func TestOwnerSuccessionResumeConcurrentReplayAndChangedRequest(t *testing.T) {
	f, request, observed := ownerSuccessionResumeFixture(t)
	pub := f.key.Public().(ed25519.PublicKey)
	results := make([]OriginalOwnerSuccessionAuthorization, 8)
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for n := range results {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			results[n], errs[n] = f.db.ResumeOriginalOwner(context.Background(), request, observed, pub)
		}(n)
	}
	wg.Wait()
	for n := range results {
		if errs[n] != nil || results[n] != results[0] {
			t.Fatal("concurrent replay allocated divergent authorization", errs[n])
		}
	}
	before := ownerSuccessionResumeSnapshot(t, f.db)
	for _, change := range []string{"revision", "controller", "token", "task", "command"} {
		r := request
		switch change {
		case "revision":
			r.WorkRevision++
		case "controller":
			r.ControllerThread += "-changed"
		case "token":
			r.ControlToken += "-changed"
		case "task":
			r.TaskID += "-changed"
		case "command":
			r.CommandID += "-changed"
		}
		if _, err := f.db.ResumeOriginalOwner(context.Background(), r, observed, pub); err == nil {
			t.Fatal("changed resume reused a committed authorization", change)
		}
		if before != ownerSuccessionResumeSnapshot(t, f.db) {
			t.Fatal("changed request mutated state", change)
		}
	}
}

func TestOwnerSuccessionResumeQuotaAndLateSQLFailureRollback(t *testing.T) {
	for _, failure := range []string{"quota", "retirement", "host", "launch", "commit"} {
		t.Run(failure, func(t *testing.T) {
			f, request, observed := ownerSuccessionResumeFixture(t)
			if failure == "quota" {
				f.db.runControlLimit = originalOwnerUsed(t, f)
			} else {
				query := `CREATE TRIGGER reject_resume_retirement BEFORE UPDATE OF phase ON execution_owners WHEN NEW.phase='retired' BEGIN SELECT RAISE(ABORT,'injected_retirement_failure'); END`
				switch failure {
				case "host":
					query = `CREATE TRIGGER reject_resume_fence BEFORE UPDATE OF status ON runtime_hosts WHEN NEW.status='offline' BEGIN SELECT RAISE(ABORT,'injected_host_fence'); END`
				case "launch":
					query = `CREATE TRIGGER reject_resume_fence BEFORE UPDATE OF status ON host_launches WHEN NEW.status='offline' BEGIN SELECT RAISE(ABORT,'injected_launch_fence'); END`
				case "commit":
					query = `CREATE TABLE resume_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER reject_resume_commit AFTER UPDATE OF status ON host_launches WHEN NEW.status='offline' BEGIN INSERT INTO resume_commit_fault VALUES('nonexistent-parent'); END`
				}
				if _, err := f.db.sql.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			_, resumeErr := f.db.ResumeOriginalOwner(context.Background(), request, observed, f.key.Public().(ed25519.PublicKey))
			if resumeErr == nil {
				t.Fatal("injected failure did not reject resume")
			}
			if failure == "commit" {
				external, err := sql.Open("sqlite", f.db.path)
				if err != nil {
					t.Fatal(err)
				}
				defer external.Close()
				visible := originalOwnerSchemaCount(t, external, `SELECT COUNT(*) FROM owner_succession_authorizations`)
				pooled := originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM owner_succession_authorizations`)
				t.Logf("actual commit error=%v; external committed authorizations=%d; pooled visible authorizations=%d", resumeErr, visible, pooled)
				if visible != 0 {
					t.Fatal("failed deferred constraint committed authorization")
				}
			}
			if before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("failed transaction leaked queue, sequence, authorization or retirement")
			}
			if failure == "commit" && originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM resume_commit_fault`) != 0 {
				t.Fatal("failed commit retained deferred FK row")
			}
			if failure == "commit" {
				if originalOwnerSchemaCount(t, f.db.sql, `PRAGMA foreign_keys`) != 1 {
					t.Fatal("replacement connection lost FK enforcement")
				}
				if _, err := f.db.sql.Exec(`DROP TRIGGER reject_resume_commit`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.ResumeOriginalOwner(context.Background(), request, observed, f.key.Public().(ed25519.PublicKey)); err != nil {
					t.Fatal("failed commit poisoned later resume", err)
				}
			}
		})
	}
}

func TestOwnerSuccessionResumeExactUTF8QuotaBoundary(t *testing.T) {
	for _, limit := range []string{"run", "global"} {
		for _, deficit := range []int64{0, 1} {
			t.Run(fmt.Sprintf("%s-minus-%d", limit, deficit), func(t *testing.T) {
				f, request, observed := ownerSuccessionResumeFixture(t)
				state, err := f.db.InspectOriginalOwner(context.Background(), observed.OwnerID)
				if err != nil {
					t.Fatal(err)
				}
				s := state.Intent.Scope
				const id = "owner-succession-额度"
				const created = "2026-10-08T00:00:00.123456789Z"
				var oldUpdated string
				if err := f.db.sql.QueryRow(`SELECT updated_at FROM execution_owners WHERE owner_id=?`, observed.OwnerID).Scan(&oldUpdated); err != nil {
					t.Fatal(err)
				}
				// Independently enumerate 15 variable TEXT values, three SHA-256
				// texts and two fixed proof texts plus eight numeric cells.
				incoming := int64(8*8 + 3*64 + 2*len("identity_exited_v1"))
				for _, value := range []string{id, request.CommandID, s.RunID, request.TaskID, s.LaunchID, s.OwnerID, observed.Owner.Birth, observed.OwnerExitedAt, observed.Host.Birth, observed.HostExitedAt, s.OriginalOriginContextID, s.OriginalOriginBirth, s.HostGeneration, s.Executable, created} {
					incoming += int64(len([]byte(value)))
				}
				incoming += int64(len("retired") - len("bound") + len(created) - len(oldUpdated))
				global, run := collectionUsage(t, f.db.sql, "global", ""), collectionUsage(t, f.db.sql, "run", s.RunID)
				if limit == "run" {
					f.db.runControlLimit = run + incoming - deficit
				} else {
					f.db.userControlLimit = global + incoming - deficit
				}
				before := ownerSuccessionResumeSnapshot(t, f.db)
				_, err = f.db.resumeOriginalOwner(context.Background(), request, observed, f.key.Public().(ed25519.PublicKey), func() (string, string, error) { return id, created, nil })
				if deficit != 0 {
					if err != CodeError("control_spool_full") || before != ownerSuccessionResumeSnapshot(t, f.db) {
						t.Fatal("one-byte deficit admitted or mutated state", err)
					}
				} else if err != nil || collectionUsage(t, f.db.sql, "global", "") != global+incoming || collectionUsage(t, f.db.sql, "run", s.RunID) != run+incoming {
					t.Fatal("exact UTF-8 admission boundary or accounting wrong", err)
				}
			})
		}
	}
}

func TestOwnerSuccessionResumePreservesBudgetAfterStoreLifecycle(t *testing.T) {
	for _, used := range []int64{150, 200} {
		t.Run(fmt.Sprint(used), func(t *testing.T) {
			f, r, a := ownerSuccessionResumeFixtureWithPlan(t, func(p *PlanSpec) {
				p.Tasks[0].MaxActiveMS = 200
				p.Tasks[0].ExecutionPolicy = &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 2, GroupMaxActiveMS: 200, TaskMaxActiveMS: 200, SegmentMaxActiveMS: 200}
			})
			ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
			control, err := f.db.OriginalOwnerControl(ctx, a.OwnerID, pub)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.ActivateOriginalOwner(ctx, appliedFixture(t, f, OriginalOwnerBindResult{Binding: control.Binding, Permit: control.Permit}), pub); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.sql.Exec(`UPDATE tasks SET status='ready' WHERE id=?`, r.TaskID); err != nil {
				t.Fatal(err)
			}
			grant, err := f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1)
			if err != nil || grant.GrantedActiveMS != 200 {
				t.Fatal("missing policy grant", err)
			}
			// Real Store transition methods; Coordinator signature/kernel peer
			// verification and native child ownership are separate acceptance.
			lifecycle := budgetBatchFixture{db: f.db, host: f.prepare.Host.LaunchID, grant: grant}
			lifecycle.start(t)
			lifecycle.send(t, contract.EventStopped, 0)
			lifecycle.send(t, contract.EventExited, used)
			var status, budgetStatus string
			var charged int64
			if err = f.db.sql.QueryRow(`SELECT t.status,b.status,b.used_ms FROM tasks t JOIN attempts a ON a.task_id=t.id JOIN segments s ON s.attempt_id=a.id JOIN budget_runtime b ON b.segment_id=s.id WHERE t.id=?`, r.TaskID).Scan(&status, &budgetStatus, &charged); err != nil || status != "interrupted" || budgetStatus != "settled" || charged != used {
				t.Fatal("missing interrupted/settled lifecycle", status, budgetStatus, charged, err)
			}
			budget := ownerSuccessionResumeTablesSnapshot(t, f.db, "budget_runtime", "budget_revisions", "decision_attempt_bindings", "task_execution_policies", "attempts", "segments")
			before := ownerSuccessionResumeSnapshot(t, f.db)
			_, err = f.db.ResumeOriginalOwner(ctx, r, a, pub)
			if used == 200 {
				if err != CodeError("budget_exhausted") || before != ownerSuccessionResumeSnapshot(t, f.db) {
					t.Fatal("exhausted budget resumed or changed state", err)
				}
			} else if err != nil {
				t.Fatal("remaining budget rejected", err)
			}
			if budget != ownerSuccessionResumeTablesSnapshot(t, f.db, "budget_runtime", "budget_revisions", "decision_attempt_bindings", "task_execution_policies", "attempts", "segments") {
				t.Fatal("succession reset or consumed execution budget")
			}
		})
	}
}

func TestOwnerSuccessionResumeNeverRetiresIntendedOwner(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	i, err := f.db.PrepareOriginalOwner(context.Background(), f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	r := OriginalOwnerResumeRequest{TaskID: "owner-run-task", ControllerThread: "controller", ControlToken: "test-control", WorkRevision: 1, CommandID: "intended-command"}
	if _, err = f.db.sql.Exec(`UPDATE run_control SET token_hash=? WHERE run_id='owner-run'; UPDATE tasks SET status='interrupted' WHERE id='owner-run-task'`, runtimeHash(r.ControlToken)); err != nil {
		t.Fatal(err)
	}
	wouldBeOwner := f.bindSpec(i).Hello.Owner
	a := OriginalOwnerSuccessionObservation{OwnerID: i.Scope.OwnerID, Owner: contract.OriginalOwnerTransport{PID: wouldBeOwner.PID, Birth: wouldBeOwner.Birth}, Host: i.FirstHost, Origin: RunOwnerBinding{OriginContextID: i.Scope.OriginalOriginContextID, OriginPID: i.Scope.OriginalOriginPID, OriginBirth: i.Scope.OriginalOriginBirth}, Epoch: 1, ExecutableSHA256: i.Scope.ExecutableSHA256, OwnerExitedAt: "2026-10-08T00:00:00Z", HostExitedAt: "2026-10-08T00:00:00Z"}
	before := ownerSuccessionResumeSnapshot(t, f.db)
	if _, err = f.db.ResumeOriginalOwner(context.Background(), r, a, f.key.Public().(ed25519.PublicKey)); err != originalOwnerRejected() {
		t.Fatal("unproved intended retired", err)
	}
	if before != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("intended reservation or history changed")
	}
}

func TestOwnerSuccessionResumeCancellationCannotCommitLate(t *testing.T) {
	f, r, a := ownerSuccessionResumeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := ownerSuccessionResumeSnapshot(t, f.db)
	_, err := f.db.resumeOriginalOwner(ctx, r, a, f.key.Public().(ed25519.PublicKey), func() (string, string, error) {
		cancel()
		return "cancelled-authorization", "2026-10-08T00:00:00Z", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled admission continued", err)
	}
	if before != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("cancelled admission committed late")
	}
	if _, err = f.db.ResumeOriginalOwner(context.Background(), r, a, f.key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal("cancelled lease poisoned later resume", err)
	}
}

func TestOwnerSuccessionResumeRejectsBrokenOrForeignRebindChains(t *testing.T) {
	for _, mode := range []string{"broken", "cycle", "current_origin", "missing_proof", "wrong_run", "limit", "limit64"} {
		t.Run(mode, func(t *testing.T) {
			f, r, a := ownerSuccessionResumeFixture(t)
			ctx := context.Background()
			count := 2
			if mode == "limit" {
				count = 65
			}
			if mode == "limit64" {
				count = 64
			}
			for n := 1; n <= count; n++ {
				a.Origin.OriginPID = 1000 + n
				a.Origin.OriginBirth = fmt.Sprintf("origin-%d", n)
				if err := f.db.RebindOwner(ctx, RebindOwnerSpec{RunID: "owner-run", OriginContextID: a.Origin.OriginContextID, OriginPID: a.Origin.OriginPID, OriginBirth: a.Origin.OriginBirth, HostGeneration: fmt.Sprintf("generation-%d", n), AttachmentProofSHA256: strings.Repeat("a", 64)}); err != nil {
					t.Fatal(err)
				}
			}
			queries := map[string]string{
				"broken":         `DELETE FROM owner_rebinds WHERE host_generation='generation-1'`,
				"cycle":          `UPDATE owner_rebinds SET old_host_generation='generation-2' WHERE host_generation='generation-1'`,
				"current_origin": `UPDATE owner_rebinds SET origin_birth='foreign' WHERE host_generation='generation-2'`,
				"missing_proof":  `UPDATE owner_rebinds SET attachment_proof_sha256='' WHERE host_generation='generation-1'`,
				"wrong_run":      `DELETE FROM owner_rebinds WHERE host_generation='generation-1'`,
			}
			if q := queries[mode]; q != "" {
				if _, err := f.db.sql.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "wrong_run" {
				if _, err := f.db.SubmitPlan(ctx, providerTestPlan("foreign-run", "controller", "grok-build")); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.sql.Exec(`INSERT INTO owner_rebinds(run_id,host_generation,old_host_generation,origin_pid,origin_birth,attachment_proof_sha256,created_at) VALUES('foreign-run','generation-1','owner-run',1001,'origin-1',?,'now')`, strings.Repeat("a", 64)); err != nil {
					t.Fatal(err)
				}
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			_, err := f.db.ResumeOriginalOwner(ctx, r, a, f.key.Public().(ed25519.PublicKey))
			if mode == "limit64" {
				if err != nil {
					t.Fatal("complete chain at the supported bound rejected", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unproved chain authorized succession")
			}
			if before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("rejected chain changed durable state")
			}
		})
	}
}
