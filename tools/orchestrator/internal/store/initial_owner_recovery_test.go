package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Synthetic trusted observations isolate durable Store contracts. Actual
// filesystem publication and original-process Wait use separate Host/CLI tests.
func initialOwnerStoreFixture(t *testing.T) (originalOwnerFixture, OriginalOwnerResumeRequest, OriginalOwnerSuccessionObservation, InitialOwnerRecoveryFacts) {
	t.Helper()
	f := newOriginalOwnerFixtureWithPlan(t, nil, func(p *PlanSpec) {
		other := p.Tasks[0]
		other.ID = "other-ready"
		p.Tasks = append(p.Tasks, other)
		dependent := p.Tasks[0]
		dependent.ID = "dependent"
		dependent.Dependencies = []string{p.Tasks[0].ID}
		p.Tasks = append(p.Tasks, dependent)
	})
	ctx := context.Background()
	pub := f.key.Public().(ed25519.PublicKey)
	i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key)
	if err != nil {
		t.Fatal(err)
	}
	req := OriginalOwnerResumeRequest{TaskID: "owner-run-task", ControllerThread: "controller", ControlToken: "initial-control", WorkRevision: 1, CommandID: "initial-recovery-command"}
	if _, err = f.db.sql.Exec(`UPDATE run_control SET token_hash=? WHERE run_id=?`, runtimeHash(req.ControlToken), i.Scope.RunID); err != nil {
		t.Fatal(err)
	}
	obs := OriginalOwnerSuccessionObservation{OwnerID: i.Scope.OwnerID, Owner: contract.OriginalOwnerTransport{PID: b.Binding.Owner.PID, Birth: b.Binding.Owner.Birth}, Host: i.FirstHost, Origin: RunOwnerBinding{OriginContextID: i.Scope.OriginalOriginContextID, OriginPID: i.Scope.OriginalOriginPID, OriginBirth: i.Scope.OriginalOriginBirth}, Epoch: 1, ExecutableSHA256: i.Scope.ExecutableSHA256, OwnerExitedAt: "2026-10-08T00:00:00Z", HostExitedAt: "2026-10-08T00:00:01Z"}
	r, err := f.db.ResolveInitialOwnerRecovery(ctx, req, obs, pub)
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(i.Scope, b.Binding.Owner)
	facts := InitialOwnerRecoveryFacts{Root: r.RootAuthority.Nodes(), SpoolDevice: 1, SpoolInode: 2, DirectoryDevice: 1, DirectoryInode: 3, LockDevice: 1, LockInode: 4, ScopeSHA256: scope}
	facts.AuthoritySHA256 = originalOwnerHash([]byte(fmt.Sprintf("original-owner-recovery-authority-v2\x00%d:%d:%d:%d", facts.DirectoryDevice, facts.DirectoryInode, facts.LockDevice, facts.LockInode)))
	facts.RecordSHA256 = initialOwnerZeroRecordHash(scope, facts.AuthoritySHA256)
	return f, req, obs, facts
}

func initialOwnerStableQueue(t *testing.T, db *DB) string {
	return ownerSuccessionResumeTablesSnapshot(t, db, "tasks", "task_runtime", "runtime_meta", "attempts", "segments", "budget_runtime", "budget_reservations", "task_execution_policies", "budget_revisions")
}
func initialOwnerUsage(t *testing.T, db *DB) (int64, int64) {
	t.Helper()
	var run, global int64
	if err := db.sql.QueryRow(`SELECT (SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='owner-run'),(SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id='')`).Scan(&run, &global); err != nil {
		t.Fatal(err)
	}
	return run, global
}

func TestInitialOwnerRecoveryPreservesQueueThroughNeverAppliedChain(t *testing.T) {
	f, req, obs, facts := initialOwnerStoreFixture(t)
	ctx := context.Background()
	pub := f.key.Public().(ed25519.PublicKey)
	queue := initialOwnerStableQueue(t, f.db)
	p, err := f.db.PrepareInitialOwnerRecovery(ctx, req, obs, facts, pub)
	if err != nil {
		t.Fatal(err)
	}
	if p.Phase != "prepared" || p.Facts != facts {
		t.Fatal("missing durable preparation")
	}
	for _, stage := range []string{"prepared", "finalized"} {
		if _, err = f.db.RequestStop(ctx, req.TaskID, 1, "owner_stop_requested"); err != CodeError("owner_initial_recovery_pending") {
			t.Fatal(stage, "run fence", err)
		}
		if stage == "prepared" {
			obs.Epoch = 2
			obs.OwnerExitedAt = "2026-10-08T00:01:00Z"
			obs.HostExitedAt = obs.OwnerExitedAt
			if err = f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
				t.Fatal(err)
			}
			again, e := f.db.PrepareInitialOwnerRecovery(ctx, req, obs, facts, pub)
			if e != nil || again != p {
				t.Fatal("prepared epoch replay", e)
			}
			if _, err = f.db.FinalizeInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
				t.Fatal(err)
			}
		}
	}
	a, err := f.db.FinalizeInitialOwnerRecovery(ctx, req, obs, facts, pub)
	if err != nil {
		t.Fatal(err)
	}
	if a.CoordinatorEpoch != 2 || a.OwnerExitedAt != "2026-10-08T00:00:00Z" || a.InitialProofID != obs.OwnerID {
		t.Fatal("immutable proof/epoch drift", a)
	}
	r, found, err := f.db.LookupOriginalOwnerResume(ctx, req)
	if err != nil || !found || r.Mode != "initial_ready" {
		t.Fatal("wrong public receipt", r, err)
	}
	if queue != initialOwnerStableQueue(t, f.db) {
		t.Fatal("initial recovery changed FIFO or budget")
	}
	for incarnation := uint64(2); incarnation <= 3; incarnation++ {
		spec := f.prepare
		spec.Epoch = 2
		spec.Host.PID = 300 + int(incarnation)
		spec.Host.Birth = fmt.Sprint("host-", incarnation)
		spec.BootstrapID = fmt.Sprint("bootstrap-", incarnation)
		spec.RequestID = fmt.Sprint("prepare-", incarnation)
		spec.BootstrapTokenSHA256, _ = contract.OriginalOwnerBootstrapSHA256(strings.Repeat("cd", 32))
		ps := PrepareSuccessorOwnerSpec{PrepareOriginalOwnerSpec: spec, AuthorizationID: a.AuthorizationID}
		i, e := f.db.PrepareSuccessorOwner(ctx, ps)
		if e != nil || i.Scope.Incarnation != incarnation {
			t.Fatal("adjacent initial successor", e)
		}
		bs := successorOwnerBindSpec(f, ps, i)
		bs.Hello.Owner.PID = 400 + int(incarnation)
		bs.Hello.Owner.Birth = fmt.Sprint("owner-", incarnation)
		bs.Authenticated.Owner = bs.Hello.Owner
		b, e := f.db.BindOriginalOwner(ctx, bs, f.key)
		if e != nil {
			t.Fatal(e)
		}
		completeOwnerRecoveryHandoffForStoreTest(t, f.db, bs, b, pub)
		if incarnation == 2 {
			next := req
			next.CommandID = "initial-bound-abandonment"
			death := obs
			death.OwnerID = i.Scope.OwnerID
			death.Owner = contract.OriginalOwnerTransport{PID: b.Binding.Owner.PID, Birth: b.Binding.Owner.Birth}
			death.Host = i.FirstHost
			resolved, e := f.db.ResolveBoundOwnerAbandonment(ctx, next, death, pub)
			if e != nil || resolved.Proof == nil {
				t.Fatal(e)
			}
			fs := resolved.Proof.Facts
			if _, e = f.db.PrepareBoundOwnerAbandonment(ctx, next, death, fs, pub); e != nil {
				t.Fatal(e)
			}
			if _, e = f.db.ApplyBoundOwnerAbandonment(ctx, next, death, fs, pub); e != nil {
				t.Fatal(e)
			}
			a, e = f.db.ResumeBoundOwnerAbandonment(ctx, next, death, fs, pub)
			if e != nil || a.InitialProofID != obs.OwnerID {
				t.Fatal("initial marker lost", e)
			}
			if queue != initialOwnerStableQueue(t, f.db) {
				t.Fatal("bound recovery changed initial reservation")
			}
		} else {
			applied := successorOwnerApplied(t, bs, b, 0)
			if e = f.db.CommitOriginalOwnerApplied(ctx, applied, pub); e != nil {
				t.Fatal(e)
			}
			var phase string
			if e = f.db.sql.QueryRow(`SELECT phase FROM owner_initial_recoveries`).Scan(&phase); e != nil || phase != "activated" {
				t.Fatal("Applied failed to release fence", e)
			}
			if e = f.db.ActivateOriginalOwner(ctx, applied, pub); e != nil {
				t.Fatal(e)
			}
			if queue != initialOwnerStableQueue(t, f.db) {
				t.Fatal("activation consumed execution")
			}
			grant, e := f.db.ClaimReady(ctx, spec.Host.LaunchID, 2, 1)
			if e != nil || grant.TaskID != req.TaskID {
				t.Fatal("original FIFO not claimable", grant.TaskID, e)
			}
			if _, e = f.db.RequestStop(ctx, "other-ready", 1, "owner_stop_requested"); e != nil {
				t.Fatal("activated fence remained", e)
			}
		}
	}
}

func TestInitialOwnerRecoveryEscrowAndCommitFailure(t *testing.T) {
	for _, stage := range []string{"prepare", "finalize"} {
		t.Run(stage, func(t *testing.T) {
			f, req, obs, facts := initialOwnerStoreFixture(t)
			ctx := context.Background()
			pub := f.key.Public().(ed25519.PublicKey)
			if stage == "finalize" {
				if _, err := f.db.PrepareInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
					t.Fatal(err)
				}
			}
			var tableTrigger string
			if stage == "prepare" {
				tableTrigger = "AFTER INSERT ON owner_initial_recoveries"
			} else {
				tableTrigger = "AFTER UPDATE OF phase ON owner_initial_recoveries WHEN NEW.phase='finalized'"
			}
			if _, err := f.db.sql.Exec(`CREATE TABLE initial_commit_fault(parent TEXT REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail_initial_commit ` + tableTrigger + ` BEGIN INSERT INTO initial_commit_fault VALUES('missing'); END`); err != nil {
				t.Fatal(err)
			}
			operation := func() error {
				if stage == "prepare" {
					_, e := f.db.PrepareInitialOwnerRecovery(ctx, req, obs, facts, pub)
					return e
				}
				_, e := f.db.FinalizeInitialOwnerRecovery(ctx, req, obs, facts, pub)
				return e
			}
			before := ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_initial_recoveries", "execution_owners", "owner_succession_authorizations", "storage_usage")
			if err := operation(); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
				t.Fatal("actual COMMIT did not fail", err)
			}
			external, err := sql.Open("sqlite", f.db.path)
			if err != nil {
				t.Fatal(err)
			}
			defer external.Close()
			for _, db := range []*DB{f.db, {sql: external}} {
				if before != ownerSuccessionResumeTablesSnapshot(t, db, "owner_initial_recoveries", "execution_owners", "owner_succession_authorizations", "storage_usage") {
					t.Fatal("failed COMMIT leaked proof or escrow")
				}
			}
			if _, err = f.db.sql.Exec(`DROP TRIGGER fail_initial_commit`); err != nil {
				t.Fatal(err)
			}
			if err = operation(); err != nil {
				t.Fatal("same request retry", err)
			}
			if stage == "prepare" {
				run, global := initialOwnerUsage(t, f.db)
				f.db.runControlLimit = run
				f.db.userControlLimit = global
				if _, err = f.db.FinalizeInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
					t.Fatal("durable escrow unavailable at exact quota", err)
				}
				nextRun, nextGlobal := initialOwnerUsage(t, f.db)
				if nextRun > run || nextGlobal > global {
					t.Fatal("finalize double-charged escrow")
				}
			}
		})
	}
}

func TestInitialOwnerRecoveryRejectsPreparedDriftAndCollision(t *testing.T) {
	for _, field := range []string{"command", "task", "work_revision", "token", "snapshot", "facts", "phase_and_command", "authority_case"} {
		t.Run(field, func(t *testing.T) {
			f, req, obs, facts := initialOwnerStoreFixture(t)
			ctx := context.Background()
			pub := f.key.Public().(ed25519.PublicKey)
			if _, err := f.db.PrepareInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "command":
				req.CommandID = "another"
			case "task":
				req.TaskID = "other-ready"
			case "work_revision":
				req.WorkRevision++
			case "token":
				req.ControlToken = "foreign"
			case "snapshot":
				if _, err := f.db.sql.Exec(`UPDATE task_runtime SET completion_policy='changed' WHERE task_id='other-ready'`); err != nil {
					t.Fatal(err)
				}
			case "facts":
				facts.LockInode++
			case "authority_case":
				facts.AuthoritySHA256 = strings.ToUpper(facts.AuthoritySHA256)
			case "phase_and_command":
				if _, err := f.db.sql.Exec(`UPDATE owner_initial_recoveries SET phase='finalized',command_id='changed'`); err == nil {
					t.Fatal("phase change bypassed immutable fields")
				}
				return
			}
			before := ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_initial_recoveries", "execution_owners", "owner_succession_authorizations", "storage_usage")
			if _, err := f.db.FinalizeInitialOwnerRecovery(ctx, req, obs, facts, pub); err == nil {
				t.Fatal("admitted", field)
			}
			if before != ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_initial_recoveries", "execution_owners", "owner_succession_authorizations", "storage_usage") {
				t.Fatal("rejection mutated state")
			}
		})
	}
}

func TestInitialOwnerRecoveryRejectsReplacedDatabaseAndPoolRecreation(t *testing.T) {
	f, req, obs, facts := initialOwnerStoreFixture(t)
	ctx := context.Background()
	pub := f.key.Public().(ed25519.PublicKey)
	if _, err := f.db.PrepareInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.FinalizeInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.db.path, f.db.path+".saved"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.db.LookupOriginalOwnerResume(ctx, req); err == nil {
		t.Fatal("receipt launch authority survived missing original db")
	}
	conn, err := f.db.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
	if _, err = f.db.sql.Exec(`SELECT 1`); err == nil {
		t.Fatal("pool recreated missing database")
	}
	if _, err = os.Lstat(f.db.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new database created", err)
	}
}

func TestInitialOwnerRecoveryReservesCommandAcrossRunsAndModes(t *testing.T) {
	for _, mode := range []string{"successor", "existing_owner"} {
		t.Run(mode, func(t *testing.T) {
			f, req, obs, facts := initialOwnerStoreFixture(t)
			ctx := context.Background()
			pub := f.key.Public().(ed25519.PublicKey)
			if _, err := f.db.PrepareInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
				t.Fatal(err)
			}
			plan := providerTestPlan("other-run", "other-controller", "grok-build")
			plan.Host.ExecutionOwnerMode = "supervised-v1"
			submitted, err := f.db.SubmitPlan(ctx, plan)
			if err != nil {
				t.Fatal(err)
			}
			other := f
			other.prepare.Host = contract.HostHello{LaunchID: submitted.LaunchID, LaunchToken: submitted.LaunchToken, OriginContextID: plan.Run.OriginContextID, OriginPID: plan.Run.OriginPID, OriginBirth: plan.Run.OriginBirth, HostGeneration: plan.Host.HostGeneration, PID: 707, Birth: "other-host", Executable: plan.Host.Executable}
			other.prepare.RequestID = "other-prepare"
			other.prepare.BootstrapID = "other-bootstrap"
			if _, err = f.db.RegisterHost(ctx, other.prepare.Host, 1); err != nil {
				t.Fatal(err)
			}
			i, err := f.db.PrepareOriginalOwner(ctx, other.prepare)
			if err != nil {
				t.Fatal(err)
			}
			b, err := f.db.BindOriginalOwner(ctx, other.bindSpec(i), f.key)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.CommitOriginalOwnerApplied(ctx, appliedFixture(t, other, b), pub); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.sql.Exec(`UPDATE tasks SET status='interrupted' WHERE run_id='other-run'`); err != nil {
				t.Fatal(err)
			}
			otherReq := OriginalOwnerResumeRequest{TaskID: plan.Tasks[0].ID, ControllerThread: plan.Run.ControllerThread, ControlToken: submitted.ControlToken, WorkRevision: 1, CommandID: req.CommandID}
			otherObs := obs
			otherObs.OwnerID = i.Scope.OwnerID
			otherObs.Host = i.FirstHost
			otherObs.Owner = contract.OriginalOwnerTransport{PID: b.Binding.Owner.PID, Birth: b.Binding.Owner.Birth}
			otherObs.Origin = RunOwnerBinding{OriginContextID: i.Scope.OriginalOriginContextID, OriginPID: i.Scope.OriginalOriginPID, OriginBirth: i.Scope.OriginalOriginBirth}
			operation := func() error {
				if mode == "successor" {
					_, e := f.db.ResumeOriginalOwner(ctx, otherReq, otherObs, pub)
					return e
				}
				_, e := f.db.ResumeExistingOriginalOwner(ctx, otherReq, OriginalOwnerLiveObservation{OwnerID: otherObs.OwnerID, Owner: otherObs.Owner, Origin: otherObs.Origin, Epoch: otherObs.Epoch, ExecutableSHA256: otherObs.ExecutableSHA256, OwnerAliveAt: otherObs.OwnerExitedAt}, pub)
				return e
			}
			if err = operation(); err != ErrConflict {
				t.Fatal("reserved command stolen", mode, err)
			}
			otherReq.CommandID = "unrelated-command"
			if err = operation(); err != nil {
				t.Fatal("unrelated recovery over-fenced", mode, err)
			}
			if _, err = f.db.FinalizeInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
				t.Fatal("original prepared command lost", mode, err)
			}
		})
	}
}
