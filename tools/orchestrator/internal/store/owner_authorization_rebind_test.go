package store

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// Store observations are synthetic; public tests must prove original child
// lifetime and authenticated origin attachment separately.
func pendingAuthorizationRebindFixture(t *testing.T, initial bool) (originalOwnerFixture, PrepareSuccessorOwnerSpec, OriginalOwnerResumeRequest) {
	t.Helper()
	ctx := context.Background()
	if !initial {
		f, ps, _ := ownerSuccessionPrepareFixture(t, true)
		req := OriginalOwnerResumeRequest{TaskID: "owner-run-task", ControllerThread: "controller", ControlToken: "resume-test-control-token", WorkRevision: 1, CommandID: "resume-command-界"}
		return f, ps, req
	}
	var f originalOwnerFixture
	var req OriginalOwnerResumeRequest
	var obs OriginalOwnerSuccessionObservation
	var a OriginalOwnerSuccessionAuthorization
	var err error
	var facts InitialOwnerRecoveryFacts
	f, req, obs, facts = initialOwnerStoreFixture(t)
	pub := f.key.Public().(ed25519.PublicKey)
	if _, err = f.db.PrepareInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
		t.Fatal(err)
	}
	a, err = f.db.FinalizeInitialOwnerRecovery(ctx, req, obs, facts, pub)
	if err != nil {
		t.Fatal(err)
	}
	spec := f.prepare
	spec.Host.PID, spec.Host.Birth = 303, "successor-host-birth"
	spec.BootstrapID, spec.RequestID = "successor-bootstrap", "successor-prepare"
	spec.RequestSHA256 = strings.Repeat("d", 64)
	spec.BootstrapTokenSHA256, _ = contract.OriginalOwnerBootstrapSHA256(strings.Repeat("cd", 32))
	return f, PrepareSuccessorOwnerSpec{PrepareOriginalOwnerSpec: spec, AuthorizationID: a.AuthorizationID}, req
}

func authorizationRebindSpec(f originalOwnerFixture, hop int) RebindOwnerSpec {
	return RebindOwnerSpec{RunID: "owner-run", OriginContextID: f.prepare.Host.OriginContextID,
		OriginPID: 700 + hop, OriginBirth: fmt.Sprintf("replacement-origin-%d", hop),
		HostGeneration: fmt.Sprintf("replacement-generation-%d", hop), AttachmentProofSHA256: strings.Repeat("a", 64)}
}

func pendingAuthorizationReceipt(t *testing.T, f originalOwnerFixture, req OriginalOwnerResumeRequest) string {
	t.Helper()
	r, found, err := f.db.LookupOriginalOwnerResume(context.Background(), req)
	if err != nil || !found {
		t.Fatal("lost original Resume receipt", found, err)
	}
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestPendingAuthorizationRebindPreservesReceiptAndInitialReservation(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprintf("initial_%v", initial), func(t *testing.T) {
			f, ps, req := pendingAuthorizationRebindFixture(t, initial)
			ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
			receipt, queue := pendingAuthorizationReceipt(t, f, req), initialOwnerStableQueue(t, f.db)
			auth := ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_succession_authorizations")
			for hop := 1; hop <= 2; hop++ {
				rb := authorizationRebindSpec(f, hop)
				if err := f.db.RebindOwner(ctx, rb); err != nil {
					t.Fatal("pending authorization rebind", hop, err)
				}
				snapshot := ownerSuccessionResumeSnapshot(t, f.db)
				if err := f.db.RebindOwner(ctx, rb); err != nil || snapshot != ownerSuccessionResumeSnapshot(t, f.db) {
					t.Fatal("rebind ACK replay changed state", err)
				}
				if receipt != pendingAuthorizationReceipt(t, f, req) || auth != ownerSuccessionResumeTablesSnapshot(t, f.db, "owner_succession_authorizations") || queue != initialOwnerStableQueue(t, f.db) {
					t.Fatal("rebind rewrote immutable authorization, receipt, FIFO or budget")
				}
				ps.Host.OriginPID, ps.Host.OriginBirth, ps.Host.HostGeneration = rb.OriginPID, rb.OriginBirth, rb.HostGeneration
				scope, err := f.db.PreviewSuccessorOwnerScope(ctx, ps.Host, ps.Epoch, ps.ExecutableSHA256, ps.AuthorizationID)
				if err != nil || scope.OriginalOriginPID != rb.OriginPID || scope.HostGeneration != rb.HostGeneration {
					t.Fatal("proven replacement origin cannot admit pending successor", err)
				}
			}
			i, err := f.db.PrepareSuccessorOwner(ctx, ps)
			if err != nil {
				t.Fatal(err)
			}
			bs := successorOwnerBindSpec(f, ps, i)
			b, err := f.db.BindOriginalOwner(ctx, bs, f.key)
			if err != nil {
				t.Fatal(err)
			}
			completeOwnerRecoveryHandoffForStoreTest(t, f.db, bs, b, pub)
			var published int64
			if err = f.db.sql.QueryRow(`SELECT COALESCE(MAX(sequence),0) FROM runtime_events WHERE producer_id=?`, i.Scope.ProducerID).Scan(&published); err != nil {
				t.Fatal(err)
			}
			applied := successorOwnerApplied(t, bs, b, published)
			if err = f.db.CommitOriginalOwnerApplied(ctx, applied, pub); err != nil {
				t.Fatal(err)
			}
			if err = f.db.ActivateOriginalOwner(ctx, applied, pub); err != nil {
				t.Fatal(err)
			}
			if receipt != pendingAuthorizationReceipt(t, f, req) || queue != initialOwnerStableQueue(t, f.db) {
				t.Fatal("successor consumed reservation before ClaimReady")
			}
			if initial {
				var phase string
				if err = f.db.sql.QueryRow(`SELECT phase FROM owner_initial_recoveries`).Scan(&phase); err != nil || phase != "activated" {
					t.Fatal("original initial proof did not activate", phase, err)
				}
			}
			grant, err := f.db.ClaimReady(ctx, ps.Host.LaunchID, ps.Epoch, 1)
			if err != nil || grant.TaskID != req.TaskID {
				t.Fatal("original reservation not claimable", err)
			}
		})
	}
}

func TestPendingAuthorizationRebindRejectsInexactReplayAndConsumedTarget(t *testing.T) {
	for _, mode := range []string{"origin_pid", "origin_birth", "old_generation", "intended", "bound"} {
		t.Run(mode, func(t *testing.T) {
			f, ps, _ := pendingAuthorizationRebindFixture(t, false)
			ctx := context.Background()
			rb := authorizationRebindSpec(f, 1)
			if mode == "intended" || mode == "bound" {
				i, err := f.db.PrepareSuccessorOwner(ctx, ps)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "bound" {
					if _, err = f.db.BindOriginalOwner(ctx, successorOwnerBindSpec(f, ps, i), f.key); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if err := f.db.RebindOwner(ctx, rb); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "origin_pid":
					rb.OriginPID++
				case "origin_birth":
					rb.OriginBirth = "foreign"
				case "old_generation":
					if err := f.db.RebindOwner(ctx, authorizationRebindSpec(f, 2)); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			if err := f.db.RebindOwner(ctx, rb); err == nil {
				t.Fatal("unsafe rebind accepted", mode)
			}
			if before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("rejected rebind mutated state")
			}
		})
	}
}

func TestPendingAuthorizationRebindPinsInitialRootAndPreparedSource(t *testing.T) {
	for _, mode := range []string{"prepared", "replaced_root"} {
		t.Run(mode, func(t *testing.T) {
			f, req, obs, facts := initialOwnerStoreFixture(t)
			ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
			if _, err := f.db.PrepareInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
				t.Fatal(err)
			}
			if mode == "replaced_root" {
				if _, err := f.db.FinalizeInitialOwnerRecovery(ctx, req, obs, facts, pub); err != nil {
					t.Fatal(err)
				}
				root := filepath.Dir(f.db.path)
				moved := root + "-original"
				if err := os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = os.Remove(root)
					_ = os.Rename(moved, root)
				})
			}
			before := ownerSuccessionResumeSnapshot(t, f.db)
			if err := f.db.RebindOwner(ctx, authorizationRebindSpec(f, 1)); err == nil {
				t.Fatal("initial source/root authority drift accepted", mode)
			}
			if before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("rejected initial rebind mutated state")
			}
		})
	}
}
