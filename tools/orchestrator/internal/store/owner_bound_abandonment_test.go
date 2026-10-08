package store

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func boundAbandonmentFixture(t *testing.T) (originalOwnerFixture, OriginalOwnerResumeRequest, OriginalOwnerSuccessionObservation, OwnerRecoveryHandoffSpec, OwnerRecoveryHandoffFacts) {
	t.Helper()
	f, s, facts, b := recoveryHandoffAPIFixture(t)
	scope := b.Binding.Scope
	req := OriginalOwnerResumeRequest{TaskID: "owner-run-task", ControllerThread: "controller", ControlToken: "resume-test-control-token", WorkRevision: 1, CommandID: "abandon-界"}
	obs := OriginalOwnerSuccessionObservation{OwnerID: s.OwnerID, Owner: contract.OriginalOwnerTransport{PID: b.Binding.Owner.PID, Birth: b.Binding.Owner.Birth}, Host: b.Binding.FirstHost, Origin: RunOwnerBinding{OriginContextID: scope.OriginalOriginContextID, OriginPID: scope.OriginalOriginPID, OriginBirth: scope.OriginalOriginBirth}, Epoch: s.Epoch, ExecutableSHA256: scope.ExecutableSHA256, OwnerExitedAt: "2026-10-08T01:00:00Z", HostExitedAt: "2026-10-08T01:00:00Z"}
	return f, req, obs, s, facts
}

func TestBoundOwnerAbandonmentPreservesReservationAndReplays(t *testing.T) {
	for _, phase := range []string{"no_intent", "prepared", "applied"} {
		t.Run(phase, func(t *testing.T) {
			f, req, obs, s, facts := boundAbandonmentFixture(t)
			ctx := context.Background()
			key := f.key.Public().(ed25519.PublicKey)
			if phase != "no_intent" {
				if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, key); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "applied" {
				if _, err := f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, key); err != nil {
					t.Fatal(err)
				}
			}
			stable := ownerSuccessionResumeTablesSnapshot(t, f.db, "tasks", "task_runtime", "runtime_meta", "attempts", "segments", "budget_runtime")
			if _, err := f.db.ResumeOriginalOwner(ctx, req, obs, key); err == nil {
				t.Fatal("ordinary Applied gate relaxed")
			}
			resolved, err := f.db.ResolveBoundOwnerAbandonment(ctx, req, obs, key)
			if err != nil || resolved.Binding.Scope.OwnerID != obs.OwnerID {
				t.Fatal("resolve", err)
			}
			p, err := f.db.PrepareBoundOwnerAbandonment(ctx, req, obs, facts, key)
			if err != nil || p.Facts != facts {
				t.Fatal("prepare", err)
			}
			before := ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_bound_abandonments", "owner_recovery_scope_handoffs", "storage_usage")
			if _, err = f.db.PrepareBoundOwnerAbandonment(ctx, req, obs, facts, key); err != nil {
				t.Fatal("prepare replay", err)
			}
			if before != ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_bound_abandonments", "owner_recovery_scope_handoffs", "storage_usage") {
				t.Fatal("prepare replay wrote")
			}
			if phase != "applied" {
				if _, err = f.db.ResumeBoundOwnerAbandonment(ctx, req, obs, facts, key); err == nil {
					t.Fatal("retired before FS apply")
				}
			}
			p, err = f.db.ApplyBoundOwnerAbandonment(ctx, req, obs, facts, key)
			if err != nil || !p.Applied {
				t.Fatal("apply", err)
			}
			a, err := f.db.ResumeBoundOwnerAbandonment(ctx, req, obs, facts, key)
			if err != nil {
				t.Fatal("resume", err)
			}
			if a.PredecessorOwnerID != obs.OwnerID || a.AbandonedOwnerID != obs.OwnerID || a.PredecessorIncarnation != 2 {
				t.Fatal("wrong lineage", a)
			}
			before = ownerSuccessionResumeSnapshot(t, f.db)
			again, err := f.db.ResumeBoundOwnerAbandonment(ctx, req, obs, facts, key)
			if err != nil || again != a || before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("final replay wrote", err)
			}
			if stable != ownerSuccessionResumeTablesSnapshot(t, f.db, "tasks", "task_runtime", "runtime_meta", "attempts", "segments", "budget_runtime") {
				t.Fatal("reservation or budget changed")
			}
			// Consuming the resulting authorization must admit only the adjacent successor.
			next := f.prepare
			next.Host = s.Host
			next.Host.PID += 10
			next.Host.Birth = "next-host"
			next.RequestID = "next-request"
			next.BootstrapID = "next-bootstrap"
			i, err := f.db.PrepareSuccessorOwner(ctx, PrepareSuccessorOwnerSpec{PrepareOriginalOwnerSpec: next, AuthorizationID: a.AuthorizationID})
			if err != nil || i.Scope.Incarnation != 3 {
				t.Fatal("next admission", err)
			}
		})
	}
}
