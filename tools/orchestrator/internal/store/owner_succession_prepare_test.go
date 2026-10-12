package store

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"sync"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func ownerSuccessionPrepareFixture(t *testing.T, history bool) (originalOwnerFixture, PrepareSuccessorOwnerSpec, contract.LaunchCommand) {
	return ownerSuccessionPrepareFixtureWithPlan(t, history, nil)
}

func ownerSuccessionPrepareFixtureWithPlan(t *testing.T, history bool, configure func(*PlanSpec)) (originalOwnerFixture, PrepareSuccessorOwnerSpec, contract.LaunchCommand) {
	t.Helper()
	f, r, a := ownerSuccessionResumeFixtureWithPlan(t, configure)
	ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
	var grant contract.LaunchCommand
	if history {
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
		grant, err = f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		lifecycle := budgetBatchFixture{db: f.db, host: f.prepare.Host.LaunchID, grant: grant}
		lifecycle.start(t)
		lifecycle.send(t, contract.EventStopped, 0)
		lifecycle.send(t, contract.EventExited, 100)
	}
	authorization, err := f.db.ResumeOriginalOwner(ctx, r, a, pub)
	if err != nil {
		t.Fatal(err)
	}
	prepare := f.prepare
	prepare.Host.PID, prepare.Host.Birth = 303, "successor-host-birth"
	prepare.BootstrapID, prepare.RequestID = "successor-bootstrap", "successor-prepare"
	prepare.RequestSHA256 = strings.Repeat("d", 64)
	prepare.BootstrapTokenSHA256, _ = contract.OriginalOwnerBootstrapSHA256(strings.Repeat("cd", 32))
	return f, PrepareSuccessorOwnerSpec{PrepareOriginalOwnerSpec: prepare, AuthorizationID: authorization.AuthorizationID}, grant
}

func successorOwnerBindSpec(f originalOwnerFixture, spec PrepareSuccessorOwnerSpec, intent contract.OriginalOwnerIntent) BindOriginalOwnerSpec {
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("n", 32)))
	owner := contract.OriginalOwnerIdentity{PID: 404, Birth: "successor-owner-birth", PublicKey: hex.EncodeToString(key.Public().(ed25519.PublicKey))}
	return BindOriginalOwnerSpec{Hello: contract.OriginalOwnerHello{Scope: intent.Scope, BootstrapID: intent.BootstrapID, BootstrapToken: strings.Repeat("cd", 32), Owner: owner, FirstHost: intent.FirstHost, CoordinatorKeyID: intent.CoordinatorKeyID}, Host: spec.Host, Epoch: spec.Epoch, Authenticated: OriginalOwnerAuthentication{Owner: owner, OriginPID: intent.Scope.OriginalOriginPID, OriginBirth: intent.Scope.OriginalOriginBirth, ExecutableSHA256: intent.Scope.ExecutableSHA256}}
}

func successorOwnerApplied(t *testing.T, spec BindOriginalOwnerSpec, b OriginalOwnerBindResult, published int64) OriginalOwnerAppliedSpec {
	t.Helper()
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("n", 32)))
	applied, err := contract.SignOwnerPermitApplied(contract.OwnerPermitApplied{Permit: b.Permit, Reconciled: contract.HostReconciled{ProducerID: b.Binding.Scope.ProducerID, PublishedThrough: published}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return OriginalOwnerAppliedSpec{Applied: applied, Host: spec.Host, Epoch: spec.Epoch, Authentication: spec.Authenticated}
}

func TestOwnerSuccessionPreparePublishesOnceAndPreservesHistory(t *testing.T) {
	f, spec, previousGrant := ownerSuccessionPrepareFixture(t, true)
	ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
	before := ownerSuccessionResumeSnapshot(t, f.db)
	scope, err := f.db.PreviewSuccessorOwnerScope(ctx, spec.Host, spec.Epoch, spec.ExecutableSHA256, spec.AuthorizationID)
	if err != nil || scope.Incarnation != 2 || scope.ProducerID != f.prepare.Host.LaunchID {
		t.Fatal("successor scope preview unavailable", err)
	}
	if before != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("preview published physical Host or owner")
	}
	history := ownerSuccessionResumeTablesSnapshot(t, f.db, "runtime_events", "attempts", "segments", "segment_runtime", "budget_runtime")
	i, err := f.db.PrepareSuccessorOwner(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if i.Scope.Incarnation != 2 || i.FirstHost.PID != spec.Host.PID || i.FirstHost.Birth != spec.Host.Birth || i.Scope.OwnerID == "" {
		t.Fatal("missing adjacent new identity")
	}
	state, err := f.db.InspectOriginalOwner(ctx, i.Scope.OwnerID)
	if err != nil || state.Phase != "intended" || !state.IsCurrent || state.SuccessionAuthorizationID != spec.AuthorizationID {
		t.Fatal("successor detached from exact authorization", err)
	}
	var hostPID int
	var hostBirth, status string
	if err = f.db.sql.QueryRow(`SELECT pid,birth,status FROM runtime_hosts WHERE launch_id=?`, spec.Host.LaunchID).Scan(&hostPID, &hostBirth, &status); err != nil || hostPID != spec.Host.PID || hostBirth != spec.Host.Birth || status != "reconciling" {
		t.Fatal("owner/physical Host publication split", err)
	}
	snapshot := ownerSuccessionResumeSnapshot(t, f.db)
	again, err := f.db.PrepareSuccessorOwner(ctx, spec)
	if err != nil || again != i || snapshot != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("Prepare ACK loss consumed authorization twice", err)
	}
	bindSpec := successorOwnerBindSpec(f, spec, i)
	b, err := f.db.BindOriginalOwner(ctx, bindSpec, f.key)
	if err != nil {
		t.Fatal("retained terminal history prevented authorized successor Bind", err)
	}
	if b.Permit.ControlRevision != 1 || b.Binding.Scope.Incarnation != 2 {
		t.Fatal("new owner inherited old control revision")
	}
	completeOwnerRecoveryHandoffForStoreTest(t, f.db, bindSpec, b, pub)
	var last int64
	if err = f.db.sql.QueryRow(`SELECT MAX(sequence) FROM runtime_events WHERE producer_id=?`, i.Scope.ProducerID).Scan(&last); err != nil || last == 0 {
		t.Fatal("fixture omitted prior producer history", err)
	}
	before = ownerSuccessionResumeSnapshot(t, f.db)
	for _, published := range []int64{0, last - 1, last + 1} {
		if err = f.db.CommitOriginalOwnerApplied(ctx, successorOwnerApplied(t, bindSpec, b, published), pub); err == nil {
			t.Fatal("successor accepted a nonexact producer cursor", published)
		}
		if before != ownerSuccessionResumeSnapshot(t, f.db) {
			t.Fatal("rejected reconciliation changed state")
		}
	}
	applied := successorOwnerApplied(t, bindSpec, b, last)
	if err = f.db.CommitOriginalOwnerApplied(ctx, applied, pub); err != nil {
		t.Fatal(err)
	}
	if err = f.db.ActivateOriginalOwner(ctx, applied, pub); err != nil {
		t.Fatal(err)
	}
	if history != ownerSuccessionResumeTablesSnapshot(t, f.db, "runtime_events", "attempts", "segments", "segment_runtime", "budget_runtime") {
		t.Fatal("succession erased or rewrote predecessor execution history")
	}
	grant, err := f.db.ClaimReady(ctx, spec.Host.LaunchID, spec.Epoch, 1)
	if err != nil || grant.AttemptID != previousGrant.AttemptID || grant.SegmentID == previousGrant.SegmentID {
		t.Fatal("resume lost original attempt", err)
	}
	// A known Applied receipt remains an exact read-only ACK-loss replay after
	// the successor has begun publishing the next event in the retained stream.
	lifecycle := budgetBatchFixture{db: f.db, host: i.Scope.ProducerID, grant: grant, sequence: last}
	lifecycle.send(t, contract.EventPrepared, 0)
	snapshot = ownerSuccessionResumeSnapshot(t, f.db)
	if err = f.db.CommitOriginalOwnerApplied(ctx, applied, pub); err != nil {
		t.Fatal("durable Applied replay rejected after stream advanced", err)
	}
	if snapshot != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("Applied replay rewrote progress")
	}
	again, err = f.db.PrepareSuccessorOwner(ctx, spec)
	if err != nil || again != i || snapshot != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("exact Prepare replay depended on later task progress", err)
	}
}

func TestOwnerSuccessionPrepareRejectsDriftAndCompetingHost(t *testing.T) {
	for _, field := range []string{"authorization", "host_pid", "host_birth", "token", "origin", "origin_pid", "origin_birth", "generation", "executable", "digest", "epoch", "queued_task", "ready_sequence"} {
		t.Run(field, func(t *testing.T) {
			f, spec, _ := ownerSuccessionPrepareFixture(t, false)
			switch field {
			case "authorization":
				spec.AuthorizationID = "foreign"
			case "host_pid":
				spec.Host.PID = f.prepare.Host.PID
				spec.Host.Birth = f.prepare.Host.Birth
			case "host_birth":
				spec.Host.Birth = ""
			case "token":
				spec.Host.LaunchToken = "foreign"
			case "origin":
				spec.Host.OriginContextID = "foreign"
			case "origin_pid":
				spec.Host.OriginPID++
			case "origin_birth":
				spec.Host.OriginBirth = "foreign"
			case "generation":
				spec.Host.HostGeneration = "foreign"
			case "executable":
				spec.Host.Executable = "/foreign"
			case "digest":
				spec.ExecutableSHA256 = strings.Repeat("b", 64)
			case "epoch":
				spec.Epoch++
			case "queued_task":
				if _, err := f.db.sql.Exec(`UPDATE tasks SET status='interrupted' WHERE id='owner-run-task'`); err != nil {
					t.Fatal(err)
				}
			case "ready_sequence":
				if _, err := f.db.sql.Exec(`UPDATE task_runtime SET ready_sequence=ready_sequence+1 WHERE task_id='owner-run-task'`); err != nil {
					t.Fatal(err)
				}
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			if _, err := f.db.PrepareSuccessorOwner(context.Background(), spec); err == nil {
				t.Fatal("drifted successor admission published")
			}
			if before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("rejected prepare changed owner, Host, quota or authorization")
			}
		})
	}
}

func TestOwnerSuccessionPrepareConcurrentAuthorizationConsumption(t *testing.T) {
	f, spec, _ := ownerSuccessionPrepareFixture(t, false)
	inputs := []PrepareSuccessorOwnerSpec{spec, spec}
	inputs[1].Host.PID++
	inputs[1].Host.Birth = "competing-host"
	inputs[1].RequestID = "competing-prepare"
	inputs[1].BootstrapID = "competing-bootstrap"
	var wg sync.WaitGroup
	errList := make([]error, 2)
	for n := range inputs {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, errList[n] = f.db.PrepareSuccessorOwner(context.Background(), inputs[n])
		}(n)
	}
	wg.Wait()
	if (errList[0] == nil) == (errList[1] == nil) {
		t.Fatal("one authorization did not choose exactly one physical owner", errList)
	}
	if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM execution_owners`) != 2 || originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM execution_owners WHERE is_current=1`) != 1 {
		t.Fatal("concurrent prepare forked owner history")
	}
}
