package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestOwnerSuccessionPrepareAndBindRecheckReservation(t *testing.T) {
	for _, stage := range []string{"prepare", "bind"} {
		for _, change := range []string{"unknown", "running", "pending_stop", "question_open", "question_answer_committed", "question_resume_queued", "question_resume_started", "task_status", "revision", "ready_sequence", "other_ready", "other_launch"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				f, spec, grant := ownerSuccessionPrepareFixtureWithPlan(t, true, func(p *PlanSpec) {
					other := p.Tasks[0]
					other.ID = "other-task"
					other.Dependencies = []string{p.Tasks[0].ID}
					p.Tasks = append(p.Tasks, other)
				})
				ctx := context.Background()
				var intent contract.OriginalOwnerIntent
				var err error
				if stage == "bind" {
					intent, err = f.db.PrepareSuccessorOwner(ctx, spec)
					if err != nil {
						t.Fatal(err)
					}
				}
				switch change {
				case "unknown", "running":
					_, err = f.db.sql.Exec(`UPDATE segment_runtime SET status=? WHERE segment_id=?`, change, grant.SegmentID)
				case "pending_stop":
					_, err = f.db.sql.Exec(`INSERT INTO stop_runtime(command_id,host_id,segment_id,reason,deadline_unix_ms,status,created_at) VALUES('late-stop',?,?,'fixture',1,'pending','now')`, f.prepare.Host.LaunchID, grant.SegmentID)
				case "task_status":
					_, err = f.db.sql.Exec(`UPDATE tasks SET status='interrupted' WHERE id='owner-run-task'`)
				case "revision":
					_, err = f.db.sql.Exec(`UPDATE task_runtime SET work_revision=work_revision+1 WHERE task_id='owner-run-task'`)
				case "ready_sequence":
					_, err = f.db.sql.Exec(`UPDATE task_runtime SET ready_sequence=ready_sequence+1 WHERE task_id='owner-run-task'`)
				case "other_ready":
					_, err = f.db.sql.Exec(`UPDATE tasks SET status='ready' WHERE id='other-task'`)
				case "other_launch":
					external, e := f.db.SubmitPlan(ctx, providerTestPlan("foreign-run", "foreign-controller", "grok-build"))
					if e != nil {
						t.Fatal(e)
					}
					_, err = f.db.sql.Exec(`UPDATE task_runtime SET host_launch_id=? WHERE task_id='other-task'`, external.LaunchID)
				default:
					_, err = f.db.sql.Exec(`INSERT INTO runtime_questions(question_id,task_id,attempt_id,segment_id,question_revision,work_revision,session_kind,session_id,status,created_at) VALUES('late-question',?,?,?,1,1,'fixture','session',?,'now')`, grant.TaskID, grant.AttemptID, grant.SegmentID, strings.TrimPrefix(change, "question_"))
				}
				if err != nil {
					t.Fatal(err)
				}
				before := ownerSuccessionResumeSnapshot(t, f.db)
				if stage == "prepare" {
					_, err = f.db.PrepareSuccessorOwner(ctx, spec)
				} else {
					_, err = f.db.BindOriginalOwner(ctx, successorOwnerBindSpec(f, spec, intent), f.key)
				}
				if err == nil || before != ownerSuccessionResumeSnapshot(t, f.db) {
					t.Fatal("late reservation/active state escaped atomic gate", err)
				}
			})
		}
	}
}

func TestOwnerSuccessionPrepareExactQuotaIncludesAuthorizationReference(t *testing.T) {
	for _, limit := range []string{"run", "global"} {
		for _, deficit := range []int64{0, 1} {
			t.Run(fmt.Sprintf("%s-minus-%d", limit, deficit), func(t *testing.T) {
				f, spec, _ := ownerSuccessionPrepareFixture(t, false)
				ctx := context.Background()
				scope, err := f.db.PreviewSuccessorOwnerScope(ctx, spec.Host, spec.Epoch, spec.ExecutableSHA256, spec.AuthorizationID)
				if err != nil {
					t.Fatal(err)
				}
				const id = "successor-owner-额度"
				const created = "2026-10-08T00:00:00.123456789Z"
				scope.OwnerID = id
				intent := contract.OriginalOwnerIntent{Scope: scope, BootstrapID: spec.BootstrapID, BootstrapTokenSHA256: spec.BootstrapTokenSHA256, CoordinatorKeyID: spec.CoordinatorKeyID, RequestID: spec.RequestID, RequestSHA256: spec.RequestSHA256, StartupDeadlineUnixMS: spec.StartupDeadlineUnixMS, FirstHost: contract.OriginalOwnerTransport{PID: spec.Host.PID, Birth: spec.Host.Birth}, CoordinatorEpoch: spec.Epoch}
				body, err := json.Marshal(intent)
				if err != nil {
					t.Fatal(err)
				}
				// Independent fixed 72-byte base, 18 TEXT columns (five
				// binding/physical texts are NULL), plus nullable-reference charge.
				incoming := int64(9*8 + 8 + len([]byte(spec.AuthorizationID)))
				for _, value := range []string{id, scope.LaunchID, scope.RunID, scope.ProducerID, spec.BootstrapID, spec.BootstrapTokenSHA256, spec.CoordinatorKeyID, spec.RequestID, spec.RequestSHA256, string(body), "intended", created, created} {
					incoming += int64(len([]byte(value)))
				}
				global, run := collectionUsage(t, f.db.sql, "global", ""), collectionUsage(t, f.db.sql, "run", scope.RunID)
				if limit == "run" {
					f.db.runControlLimit = run + incoming - deficit
				} else {
					f.db.userControlLimit = global + incoming - deficit
				}
				before := ownerSuccessionResumeSnapshot(t, f.db)
				got, err := f.db.prepareSuccessorOwner(ctx, spec, func() (string, string, error) { return id, created, nil })
				if deficit != 0 {
					if err != CodeError("control_spool_full") || before != ownerSuccessionResumeSnapshot(t, f.db) {
						t.Fatal("one byte below full owner/reference cost admitted", err)
					}
				} else if err != nil || got != intent || collectionUsage(t, f.db.sql, "global", "") != global+incoming || collectionUsage(t, f.db.sql, "run", scope.RunID) != run+incoming {
					t.Fatal("exact owner/reference accounting wrong", err)
				}
			})
		}
	}
}

func TestOwnerSuccessionDoesNotRelaxInitialBindHistoryGate(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	// Insert a valid legacy Store execution after initial Prepare to exercise
	// this defensive race gate. This mode change is a synthetic test setup.
	if _, err = f.db.sql.Exec(`UPDATE host_launches SET execution_owner_mode='legacy',status='ready'; UPDATE runtime_hosts SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	grant, err := f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := budgetBatchFixture{db: f.db, host: f.prepare.Host.LaunchID, grant: grant}
	lifecycle.start(t)
	lifecycle.send(t, contract.EventStopped, 0)
	lifecycle.send(t, contract.EventExited, 1)
	if _, err = f.db.sql.Exec(`UPDATE host_launches SET execution_owner_mode='supervised-v1'`); err != nil {
		t.Fatal(err)
	}
	before := ownerSuccessionResumeSnapshot(t, f.db)
	if _, err = f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key); err == nil {
		t.Fatal("initial owner acquired authority over preexisting execution")
	}
	if before != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("initial Bind rejection altered history")
	}
}

func TestOwnerSuccessionBindQuotaFailureRetainsPreparedSlot(t *testing.T) {
	f, spec, _ := ownerSuccessionPrepareFixture(t, true)
	ctx := context.Background()
	i, err := f.db.PrepareSuccessorOwner(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	bind := successorOwnerBindSpec(f, spec, i)
	limit := f.db.runControlLimit
	f.db.runControlLimit = originalOwnerUsed(t, f)
	before := ownerSuccessionResumeSnapshot(t, f.db)
	if _, err = f.db.BindOriginalOwner(ctx, bind, f.key); err != CodeError("control_spool_full") {
		t.Fatal("binding/permit bypassed quota admission", err)
	}
	if before != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("failed binding consumed prepared identity or control revision")
	}
	f.db.runControlLimit = limit
	if _, err = f.db.BindOriginalOwner(ctx, bind, f.key); err != nil {
		t.Fatal("quota refusal prevented retry of original intent", err)
	}
}
