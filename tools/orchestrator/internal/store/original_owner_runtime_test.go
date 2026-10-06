package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestOriginalOwnerRuntimeCannotDispatchBeforeApplied(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	if _, err := f.db.ClaimReady(context.Background(), f.prepare.Host.LaunchID, 1, 1); err != ErrHostRejected {
		t.Fatalf("unbound supervised host dispatched: %v", err)
	}
}

func TestOriginalOwnerRuntimeLegacyReconcileCannotActivate(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	if err := f.db.BeginHostReconcile(ctx, f.prepare.Host.LaunchID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.FinishHostReconcile(ctx, f.prepare.Host.LaunchID); err == nil {
		t.Fatal("legacy reconcile bypassed owner applied gate")
	}
}

func TestOriginalOwnerRuntimeAttachMovesHostAtomically(t *testing.T) {
	f, intent, bound := originalOwnerBound(t)
	ctx := context.Background()
	spec := originalOwnerAttachInput(f, intent, bound, 1, 1, "replacement-runtime")
	spec.NewHost = contract.OriginalOwnerTransport{PID: 303, Birth: "replacement-birth"}
	spec.Authentication.Host.PID, spec.Authentication.Host.Birth = spec.NewHost.PID, spec.NewHost.Birth
	spec.Authentication.ExitedHost = &spec.OldHost
	if _, err := f.db.CommitOriginalOwnerAttach(ctx, spec, f.key); err != nil {
		t.Fatal(err)
	}
	var got contract.OriginalOwnerTransport
	var status string
	if err := f.db.sql.QueryRow(`SELECT pid,birth,status FROM runtime_hosts WHERE id=?`, intent.Scope.ProducerID).Scan(&got.PID, &got.Birth, &status); err != nil {
		t.Fatal(err)
	}
	if got != spec.NewHost || status != "reconciling" {
		t.Fatalf("owner/host split: %+v %s", got, status)
	}
}

func appliedFixture(t *testing.T, f originalOwnerFixture, b OriginalOwnerBindResult) OriginalOwnerAppliedSpec {
	t.Helper()
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("o", 32)))
	applied, err := contract.SignOwnerPermitApplied(contract.OwnerPermitApplied{Permit: b.Permit, Reconciled: contract.HostReconciled{ProducerID: b.Binding.Scope.ProducerID}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return OriginalOwnerAppliedSpec{Applied: applied, Host: f.prepare.Host, Epoch: b.Permit.CoordinatorEpoch, Authentication: OriginalOwnerAuthentication{Owner: b.Binding.Owner, OriginPID: b.Binding.Scope.OriginalOriginPID, OriginBirth: b.Binding.Scope.OriginalOriginBirth, ExecutableSHA256: b.Binding.Scope.ExecutableSHA256}}
}

func TestOriginalOwnerRuntimeAppliedActivationAndReplay(t *testing.T) {
	f, intent, b := originalOwnerBound(t)
	ctx := context.Background()
	spec := appliedFixture(t, f, b)
	pub := f.key.Public().(ed25519.PublicKey)
	if err := f.db.ActivateOriginalOwner(ctx, spec, pub); err == nil {
		t.Fatal("activate before applied")
	}
	if _, err := f.db.ClaimReady(ctx, intent.Scope.ProducerID, 1, 1); err != ErrHostRejected {
		t.Fatal("bound dispatched before applied", err)
	}
	used := originalOwnerUsed(t, f)
	for n := 0; n < 2; n++ {
		if err := f.db.CommitOriginalOwnerApplied(ctx, spec, pub); err != nil {
			t.Fatal(err)
		}
	}
	if got := originalOwnerUsed(t, f); got != used+72 {
		t.Fatalf("applied quota/replay=%d, want %d", got, used+72)
	}
	if _, err := f.db.ClaimReady(ctx, intent.Scope.ProducerID, 1, 1); err != ErrHostRejected {
		t.Fatal("applied dispatched before activation", err)
	}
	for n := 0; n < 2; n++ {
		if err := f.db.ActivateOriginalOwner(ctx, spec, pub); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.ClaimReady(ctx, intent.Scope.ProducerID, 1, 1); err != nil {
		t.Fatal("activated did not dispatch", err)
	}
	if err := f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.RegisterHost(ctx, f.prepare.Host, 2); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ActivateOriginalOwner(ctx, spec, pub); err == nil {
		t.Fatal("stale epoch activated")
	}
	renew := originalOwnerAttachInput(f, intent, b, 2, 1, "epoch-2")
	permit, err := f.db.CommitOriginalOwnerAttach(ctx, renew, f.key)
	if err != nil {
		t.Fatal(err)
	}
	next := appliedFixture(t, f, OriginalOwnerBindResult{Binding: b.Binding, Permit: permit})
	next.Epoch = 2
	if err := f.db.ActivateOriginalOwner(ctx, next, pub); err == nil {
		t.Fatal("new permit reused old applied revision")
	}
	if err := f.db.CommitOriginalOwnerApplied(ctx, next, pub); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ActivateOriginalOwner(ctx, next, pub); err != nil {
		t.Fatal(err)
	}
}

func TestOriginalOwnerRuntimeAppliedRejectsDriftAndRollsBack(t *testing.T) {
	for _, name := range []string{"signature", "foreign_owner", "wrong_host", "wrong_key", "cursor", "quota", "ignored_cas"} {
		t.Run(name, func(t *testing.T) {
			f, _, b := originalOwnerBound(t)
			ctx := context.Background()
			spec := appliedFixture(t, f, b)
			pub := f.key.Public().(ed25519.PublicKey)
			before, used := originalOwnerSnapshot(t, f), originalOwnerUsed(t, f)
			switch name {
			case "signature":
				spec.Applied.Signature = strings.Repeat("0", 128)
			case "foreign_owner":
				spec.Authentication.Owner.PID++
			case "wrong_host":
				spec.Host.Birth = "other"
			case "wrong_key":
				pub = ed25519.NewKeyFromSeed([]byte(strings.Repeat("z", 32))).Public().(ed25519.PublicKey)
			case "cursor":
				spec.Applied.Reconciled.PublishedThrough = 1
				spec.Applied, _ = contract.SignOwnerPermitApplied(spec.Applied, ed25519.NewKeyFromSeed([]byte(strings.Repeat("o", 32))))
			case "quota":
				f.db.runControlLimit = used + 71
			case "ignored_cas":
				if _, err := f.db.sql.Exec(`CREATE TRIGGER fixture_owner_ignore_applied BEFORE UPDATE OF applied_control_revision ON execution_owners BEGIN SELECT RAISE(IGNORE);END`); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.db.CommitOriginalOwnerApplied(ctx, spec, pub); err == nil {
				t.Fatal("invalid applied accepted")
			}
			if originalOwnerSnapshot(t, f) != before || originalOwnerUsed(t, f) != used {
				t.Fatal("failed applied partially wrote owner/hash/quota")
			}
		})
	}
}

func TestOriginalOwnerRuntimeAttachRollbackAndOrdinaryRebindRefused(t *testing.T) {
	f, intent, b := originalOwnerBound(t)
	ctx := context.Background()
	spec := originalOwnerAttachInput(f, intent, b, 1, 1, "replacement-failure")
	spec.NewHost = contract.OriginalOwnerTransport{PID: 303, Birth: "next-birth"}
	spec.Authentication.Host.PID, spec.Authentication.Host.Birth = spec.NewHost.PID, spec.NewHost.Birth
	spec.Authentication.ExitedHost = &spec.OldHost
	if _, err := f.db.RegisterHost(ctx, spec.Authentication.Host, 1); err != ErrHostRejected {
		t.Fatal("ordinary registration replaced owner transport", err)
	}
	if _, err := f.db.sql.Exec(`CREATE TRIGGER fixture_owner_host_ignore BEFORE UPDATE ON runtime_hosts BEGIN SELECT RAISE(IGNORE);END`); err != nil {
		t.Fatal(err)
	}
	before, used := originalOwnerSnapshot(t, f), originalOwnerUsed(t, f)
	if _, err := f.db.CommitOriginalOwnerAttach(ctx, spec, f.key); err != ErrConflict {
		t.Fatal("host CAS failure committed owner", err)
	}
	if originalOwnerSnapshot(t, f) != before || originalOwnerUsed(t, f) != used {
		t.Fatal("split attach after failed runtime CAS")
	}
}

// Construct a pre-B2 historical worker row for migration/enrollment-refusal
// tests. Production no longer permits dispatch on an unapplied owner.
func historicalOwnerClaim(t *testing.T, f originalOwnerFixture) {
	t.Helper()
	if _, err := f.db.sql.Exec(`UPDATE host_launches SET execution_owner_mode='legacy'; UPDATE runtime_hosts SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimReady(context.Background(), f.prepare.Host.LaunchID, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec(`UPDATE host_launches SET execution_owner_mode='supervised-v1'; UPDATE runtime_hosts SET status='reconciling'`); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyHostReconcileWaitsForExistingWriter(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	if _, err := f.db.sql.Exec(`UPDATE host_launches SET execution_owner_mode='legacy'`); err != nil {
		t.Fatal(err)
	}
	gate, err := sql.Open("sqlite", f.db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	conn, err := gate.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	done := make(chan error, 1)
	go func() { done <- f.db.FinishHostReconcile(ctx, f.prepare.Host.LaunchID) }()
	select {
	case err := <-done:
		t.Fatalf("legacy reconcile returned before writer released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err = conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("legacy reconcile did not resume after writer released")
	}
}
