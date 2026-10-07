package store

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestExistingOwnerResumeCanAuthorizeSuccessorForUnclaimedQueue(t *testing.T) {
	f, request, observed := ownerSuccessionResumeFixture(t)
	ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
	live, err := f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(observed), pub)
	if err != nil {
		t.Fatal(err)
	}
	queue := ownerSuccessionResumeTablesSnapshot(t, f.db, "tasks", "task_runtime", "runtime_meta", "attempts", "segments", "budget_runtime")
	newRequest := request
	newRequest.CommandID = "successor-after-live-queue"
	a, err := f.db.ResumeOriginalOwner(ctx, newRequest, observed, pub)
	if err != nil {
		t.Fatal("proved-dead owner left an unclaimable live queue", err)
	}
	if a.ReadySequence != live.ReadySequence || a.PredecessorOwnerID != live.OwnerID {
		t.Fatal("upgrade changed the reserved queue", a)
	}
	if queue != ownerSuccessionResumeTablesSnapshot(t, f.db, "tasks", "task_runtime", "runtime_meta", "attempts", "segments", "budget_runtime") {
		t.Fatal("upgrade allocated queue sequence or changed budget/history")
	}
	before := liveOwnerResumeSnapshot(t, f.db)
	old, found, err := f.db.LookupOriginalOwnerResume(ctx, request)
	if err != nil || !found || old != live {
		t.Fatal("old command changed mode after upgrade", old, err)
	}
	if again, err := f.db.ResumeOriginalOwner(ctx, newRequest, OriginalOwnerSuccessionObservation{}, nil); err != nil || again != a {
		t.Fatal("upgrade receipt replay changed", err)
	}
	if liveOwnerResumeSnapshot(t, f.db) != before {
		t.Fatal("upgrade replay wrote state")
	}
}

func TestExistingOwnerResumeUpgradeUsesLatestControlAndOrigin(t *testing.T) {
	for _, mode := range []string{"epoch", "control", "control_unapplied", "old_host", "rebind", "broken_rebind"} {
		t.Run(mode, func(t *testing.T) {
			f, request, dead := ownerSuccessionResumeFixture(t)
			ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
			live, err := f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(dead), pub)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "rebind" || mode == "broken_rebind" {
				dead.Origin.OriginPID++
				dead.Origin.OriginBirth = "new-origin"
				spec := RebindOwnerSpec{RunID: "owner-run", OriginContextID: dead.Origin.OriginContextID, OriginPID: dead.Origin.OriginPID, OriginBirth: dead.Origin.OriginBirth, HostGeneration: "next-generation", AttachmentProofSHA256: strings.Repeat("a", 64)}
				if err = f.db.RebindOwner(ctx, spec); err != nil {
					t.Fatal(err)
				}
				if mode == "broken_rebind" {
					if _, err = f.db.sql.Exec(`DELETE FROM owner_rebinds`); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				state, err := f.db.InspectOriginalOwner(ctx, dead.OwnerID)
				if err != nil {
					t.Fatal(err)
				}
				control, err := f.db.OriginalOwnerControl(ctx, dead.OwnerID, pub)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
					t.Fatal(err)
				}
				dead.Epoch = 2
				if mode != "epoch" {
					spec := originalOwnerAttachInput(f, state.Intent, OriginalOwnerBindResult{Binding: control.Binding, Permit: control.Permit}, 2, 1, "queued-owner-new-host")
					spec.NewHost = contract.OriginalOwnerTransport{PID: 303, Birth: "replaced-host"}
					spec.Authentication.Host.PID, spec.Authentication.Host.Birth = spec.NewHost.PID, spec.NewHost.Birth
					spec.Authentication.ExitedHost = &spec.OldHost
					permit, err := f.db.CommitOriginalOwnerAttach(ctx, spec, f.key)
					if err != nil {
						t.Fatal(err)
					}
					f.prepare.Host = spec.Authentication.Host
					if mode != "control_unapplied" {
						if err = f.db.CommitOriginalOwnerApplied(ctx, appliedFixture(t, f, OriginalOwnerBindResult{Binding: control.Binding, Permit: permit}), pub); err != nil {
							t.Fatal(err)
						}
					}
					if mode != "old_host" {
						dead.Host = spec.NewHost
					}
				}
			}
			request.CommandID = "new-command-latest-control"
			before := liveOwnerResumeSnapshot(t, f.db)
			a, err := f.db.ResumeOriginalOwner(ctx, request, dead, pub)
			if mode == "broken_rebind" || mode == "control_unapplied" || mode == "old_host" {
				if err == nil || before != liveOwnerResumeSnapshot(t, f.db) {
					t.Fatal("unproved latest control/origin admitted upgrade", err)
				}
				return
			}
			if err != nil || a.ReadySequence != live.ReadySequence || a.SuccessorOrigin != dead.Origin || a.CoordinatorEpoch != dead.Epoch {
				t.Fatal("latest valid state could not preserve live queue", a, err)
			}
			if mode == "control" && (live.ControlRevision != 1 || a.PredecessorControlRevision != 2 || a.Host != dead.Host) {
				t.Fatal("old receipt overrode latest physical control", a)
			}
		})
	}
}

func TestExistingOwnerResumeUpgradeOnlyOneNewCommandWins(t *testing.T) {
	f, request, dead := ownerSuccessionResumeFixture(t)
	ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
	live, err := f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(dead), pub)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			r := request
			r.CommandID = fmt.Sprintf("competing-successor-%d", n)
			a, e := f.db.ResumeOriginalOwner(ctx, r, dead, pub)
			if e == nil && a.ReadySequence != live.ReadySequence {
				e = fmt.Errorf("changed ready sequence")
			}
			results <- e
		}(n)
	}
	close(start)
	wg.Wait()
	close(results)
	success, refused := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else {
			refused++
		}
	}
	if success != 1 || refused != 1 || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM owner_succession_authorizations`) != 1 {
		t.Fatal("competing commands created divergent succession", success, refused)
	}
}

func TestExistingOwnerResumeUpgradeRejectsSameCountDifferentLastSegment(t *testing.T) {
	f, request, dead, first := existingOwnerStoppedFixture(t, 50)
	ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
	if err := f.db.QueueResume(ctx, request.TaskID, request.WorkRevision); err != nil {
		t.Fatal(err)
	}
	g, err := f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := budgetBatchFixture{db: f.db, host: f.prepare.Host.LaunchID, grant: g, sequence: int64(originalOwnerSchemaCount(t, f.db.sql, `SELECT MAX(sequence) FROM runtime_events`))}
	lifecycle.start(t)
	lifecycle.send(t, contract.EventStopped, 0)
	lifecycle.send(t, contract.EventExited, 50)
	if _, err = f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(dead), pub); err != nil {
		t.Fatal(err)
	}
	// Isolate last-segment ordering drift while leaving row count, task status,
	// ready sequence, terminal lifecycle and owner identities unchanged.
	if _, err = f.db.sql.Exec(`UPDATE segments SET segment_no=99 WHERE id=?`, first.SegmentID); err != nil {
		t.Fatal(err)
	}
	if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM segments`) != 2 {
		t.Fatal("snapshot fixture did not preserve segment count")
	}
	request.CommandID = "new-command-after-ordering-drift"
	before := liveOwnerResumeSnapshot(t, f.db)
	if _, err = f.db.ResumeOriginalOwner(ctx, request, dead, pub); err == nil || before != liveOwnerResumeSnapshot(t, f.db) {
		t.Fatal("changed last segment admitted upgrade", err)
	}
}

func existingOwnerStoppedFixture(t *testing.T, used int64) (originalOwnerFixture, OriginalOwnerResumeRequest, OriginalOwnerSuccessionObservation, contract.LaunchCommand) {
	t.Helper()
	f, r, o := ownerSuccessionResumeFixtureWithPlan(t, func(p *PlanSpec) {
		p.Tasks[0].MaxActiveMS = 200
		p.Tasks[0].ExecutionPolicy = &contract.ExecutionPolicy{Version: 1, GroupMaxAttempts: 2, GroupMaxActiveMS: 200, TaskMaxActiveMS: 200, SegmentMaxActiveMS: 200}
	})
	ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
	control, err := f.db.OriginalOwnerControl(ctx, o.OwnerID, pub)
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
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := budgetBatchFixture{db: f.db, host: f.prepare.Host.LaunchID, grant: grant}
	lifecycle.start(t)
	lifecycle.send(t, contract.EventStopped, 0)
	lifecycle.send(t, contract.EventExited, used)
	return f, r, o, grant
}

func TestExistingOwnerResumePreservesConsumedExecutionBudget(t *testing.T) {
	for _, used := range []int64{150, 200} {
		t.Run(fmt.Sprint(used), func(t *testing.T) {
			f, request, dead, _ := existingOwnerStoppedFixture(t, used)
			before := liveOwnerResumeSnapshot(t, f.db)
			budget := ownerSuccessionResumeTablesSnapshot(t, f.db, "budget_runtime", "budget_revisions", "decision_attempt_bindings", "task_execution_policies", "attempts", "segments")
			live, err := f.db.ResumeExistingOriginalOwner(context.Background(), request, liveOwnerObservation(dead), f.key.Public().(ed25519.PublicKey))
			if used == 200 {
				if err != CodeError("budget_exhausted") || before != liveOwnerResumeSnapshot(t, f.db) {
					t.Fatal("exhausted live resume changed state", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				request.CommandID = "new-death-after-live"
				a, err := f.db.ResumeOriginalOwner(context.Background(), request, dead, f.key.Public().(ed25519.PublicKey))
				if err != nil || a.ReadySequence != live.ReadySequence {
					t.Fatal("unclaimed queue with real settled history rejected", err)
				}
			}
			if budget != ownerSuccessionResumeTablesSnapshot(t, f.db, "budget_runtime", "budget_revisions", "decision_attempt_bindings", "task_execution_policies", "attempts", "segments") {
				t.Fatal("live queue/upgrade changed consumed allowance")
			}
		})
	}
}

func TestExistingOwnerResumeUpgradeRejectsChangedReservation(t *testing.T) {
	for _, change := range []string{"plain_queue", "revision", "sequence", "active", "claimed_then_stopped", "budget_exhausted", "pending_stop", "open_question", "other_ready"} {
		t.Run(change, func(t *testing.T) {
			f, request, dead, grant := existingOwnerStoppedFixture(t, 50)
			ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
			if change == "plain_queue" {
				if err := f.db.QueueResume(ctx, request.TaskID, request.WorkRevision); err != nil {
					t.Fatal(err)
				}
			} else {
				live, err := f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(dead), pub)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "revision":
					_, err = f.db.sql.Exec(`UPDATE task_runtime SET work_revision=work_revision+1`)
				case "sequence":
					_, err = f.db.sql.Exec(`UPDATE task_runtime SET ready_sequence=ready_sequence+1`)
				case "active":
					_, err = f.db.sql.Exec(`UPDATE segment_runtime SET status='unknown' WHERE segment_id=?`, grant.SegmentID)
				case "claimed_then_stopped":
					// Claim is genuine; restore only the queue metadata to isolate
					// the immutable segment-snapshot fence from status/sequence.
					g, e := f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1)
					if e != nil {
						t.Fatal(e)
					}
					lifecycle := budgetBatchFixture{db: f.db, host: f.prepare.Host.LaunchID, grant: g, sequence: int64(originalOwnerSchemaCount(t, f.db.sql, `SELECT MAX(sequence) FROM runtime_events`))}
					lifecycle.start(t)
					lifecycle.send(t, contract.EventStopped, 0)
					lifecycle.send(t, contract.EventExited, 50)
					_, err = f.db.sql.Exec(`UPDATE tasks SET status='resume_queued' WHERE id=?; UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, request.TaskID, live.ReadySequence, request.TaskID)
				case "budget_exhausted":
					_, err = f.db.sql.Exec(`UPDATE budget_runtime SET used_ms=200 WHERE segment_id=?`, grant.SegmentID)
				case "pending_stop":
					_, err = f.db.sql.Exec(`INSERT INTO stop_runtime(command_id,host_id,segment_id,reason,deadline_unix_ms,status,created_at) VALUES('pending-stop',?,?,'fixture',1,'pending','now')`, f.prepare.Host.LaunchID, grant.SegmentID)
				case "open_question":
					_, err = f.db.sql.Exec(`INSERT INTO runtime_questions(question_id,task_id,attempt_id,segment_id,question_revision,work_revision,session_kind,session_id,status,created_at) VALUES('question',?,?,?,1,1,'fixture','session','open','now')`, grant.TaskID, grant.AttemptID, grant.SegmentID)
				case "other_ready":
					// Same-run task is inserted with the same immutable plan shape.
					_, err = f.db.sql.Exec(`INSERT INTO tasks(id,run_id,status,dependencies_json,max_attempts,created_at) SELECT 'other-ready',run_id,'ready',dependencies_json,max_attempts,created_at FROM tasks WHERE id=?`, request.TaskID)
				}
				if err != nil {
					t.Fatal("fixture change", err)
				}
			}
			request.CommandID = "new-command-after-change"
			before := liveOwnerResumeSnapshot(t, f.db)
			if _, err := f.db.ResumeOriginalOwner(ctx, request, dead, pub); err == nil {
				t.Fatal("changed live reservation admitted succession")
			}
			if before != liveOwnerResumeSnapshot(t, f.db) {
				t.Fatal("rejected upgrade changed queue/owner/budget")
			}
		})
	}
}

func TestExistingOwnerResumeUpgradeRemainsUsableBySuccessor(t *testing.T) {
	f, request, observed, _ := existingOwnerStoppedFixture(t, 50)
	ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
	if _, err := f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(observed), pub); err != nil {
		t.Fatal(err)
	}
	request.CommandID = "new-successor-reservation"
	a, err := f.db.ResumeOriginalOwner(ctx, request, observed, pub)
	if err != nil {
		t.Fatal(err)
	}
	p := f.prepare
	p.Host.PID, p.Host.Birth = 303, "successor-host-birth"
	p.BootstrapID, p.RequestID = "successor-bootstrap", "successor-prepare"
	p.RequestSHA256 = strings.Repeat("d", 64)
	p.BootstrapTokenSHA256, _ = contract.OriginalOwnerBootstrapSHA256(strings.Repeat("cd", 32))
	spec := PrepareSuccessorOwnerSpec{PrepareOriginalOwnerSpec: p, AuthorizationID: a.AuthorizationID}
	i, err := f.db.PrepareSuccessorOwner(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	bindSpec := successorOwnerBindSpec(f, spec, i)
	b, err := f.db.BindOriginalOwner(ctx, bindSpec, f.key)
	if err != nil {
		t.Fatal(err)
	}
	max := int64(originalOwnerSchemaCount(t, f.db.sql, `SELECT MAX(sequence) FROM runtime_events`))
	applied := successorOwnerApplied(t, bindSpec, b, max)
	if err = f.db.CommitOriginalOwnerApplied(ctx, applied, pub); err != nil {
		t.Fatal(err)
	}
	if err = f.db.ActivateOriginalOwner(ctx, applied, pub); err != nil {
		t.Fatal(err)
	}
	g, err := f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1)
	if err != nil || g.TaskID != request.TaskID || g.GrantedActiveMS != 150 {
		t.Fatal("successor could not claim original live reservation with remaining allowance", g, err)
	}
}
