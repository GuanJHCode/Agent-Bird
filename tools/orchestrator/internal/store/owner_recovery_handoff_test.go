package store

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func recoveryHandoffAPIFixture(t *testing.T) (originalOwnerFixture, OwnerRecoveryHandoffSpec, OwnerRecoveryHandoffFacts, OriginalOwnerBindResult) {
	t.Helper()
	f, p, _ := ownerSuccessionPrepareFixture(t, false)
	i, err := f.db.prepareSuccessorOwner(context.Background(), p, func() (string, string, error) { return "successor-恢复", originalOwnerNow(), nil })
	if err != nil {
		t.Fatal(err)
	}
	bs := successorOwnerBindSpec(f, p, i)
	b, err := f.db.BindOriginalOwner(context.Background(), bs, f.key)
	if err != nil {
		t.Fatal(err)
	}
	s := OwnerRecoveryHandoffSpec{OwnerID: i.Scope.OwnerID, Host: p.Host, Epoch: p.Epoch, Authentication: bs.Authenticated}
	var priorID string
	if err = f.db.sql.QueryRow(`SELECT predecessor_owner_id FROM owner_succession_authorizations WHERE authorization_id=?`, p.AuthorizationID).Scan(&priorID); err != nil {
		t.Fatal(err)
	}
	prior, err := f.db.InspectOriginalOwner(context.Background(), priorID)
	if err != nil {
		t.Fatal(err)
	}
	priorScope, _ := contract.OriginalOwnerRecoveryScopeSHA256(prior.Binding.Scope, prior.Binding.Owner)
	scope, _ := contract.OriginalOwnerRecoveryScopeSHA256(b.Binding.Scope, b.Binding.Owner)
	facts := OwnerRecoveryHandoffFacts{PriorScopeSHA256: priorScope, ScopeSHA256: scope, PriorRecordSHA256: strings.Repeat("a", 64), RecordSHA256: strings.Repeat("b", 64), AuthoritySHA256: strings.Repeat("c", 64)}
	return f, s, facts, b
}
func recoveryHandoffSnapshot(t *testing.T, d *DB) string {
	t.Helper()
	return ownerSuccessionResumeTablesSnapshot(t, d, "execution_owners", "owner_control_changes", "owner_succession_authorizations", "owner_recovery_scope_handoffs", "storage_usage", "tasks", "task_runtime", "runtime_hosts", "host_launches", "runtime_events", "budget_runtime")
}

func TestOwnerRecoveryHandoffDerivesAdjacentBindingsAndReplays(t *testing.T) {
	f, s, facts, b := recoveryHandoffAPIFixture(t)
	ctx := context.Background()
	pub := f.key.Public().(ed25519.PublicKey)
	before := recoveryHandoffSnapshot(t, f.db)
	resolved, err := f.db.ResolveOriginalOwnerRecoveryHandoff(ctx, s, pub)
	if err != nil || resolved.Proof != nil || resolved.Binding != b.Binding || resolved.PriorBinding.Scope.Incarnation+1 != resolved.Binding.Scope.Incarnation {
		t.Fatal("invalid adjacency", err)
	}
	if before != recoveryHandoffSnapshot(t, f.db) {
		t.Fatal("resolve wrote")
	}
	p, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
	if err != nil || p.Applied || p.Facts != facts {
		t.Fatal("prepare", err)
	}
	before = recoveryHandoffSnapshot(t, f.db)
	again, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
	if err != nil || again != p || before != recoveryHandoffSnapshot(t, f.db) {
		t.Fatal("prepare replay mutated", err)
	}
	applied, err := f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
	if err != nil || !applied.Applied || applied.Facts != facts {
		t.Fatal("apply", err)
	}
	before = recoveryHandoffSnapshot(t, f.db)
	for _, call := range []func() (OwnerRecoveryHandoffProof, error){func() (OwnerRecoveryHandoffProof, error) {
		return f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
	}, func() (OwnerRecoveryHandoffProof, error) {
		return f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
	}} {
		got, err := call()
		if err != nil || got != applied || before != recoveryHandoffSnapshot(t, f.db) {
			t.Fatal("applied replay", err)
		}
	}
	resolved, err = f.db.ResolveOriginalOwnerRecoveryHandoff(ctx, s, pub)
	if err != nil || resolved.Proof == nil || *resolved.Proof != applied {
		t.Fatal("lost receipt", err)
	}
	path := f.db.path
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	got, err := f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
	if err != nil || got != applied || before != recoveryHandoffSnapshot(t, f.db) {
		t.Fatal("restart replay", err)
	}
	raw, _ := json.Marshal(facts)
	if string(raw) != "{}" {
		t.Fatal("private facts leaked as public JSON", string(raw))
	}
}
func TestOwnerRecoveryHandoffRejectsChangedFactsAndAuthentication(t *testing.T) {
	for _, stage := range []string{"before", "prepared", "applied"} {
		for _, change := range []string{"scope", "prior_scope", "record", "prior_record", "authority", "uppercase", "owner", "pid", "birth", "public_key", "origin", "digest", "token", "host", "epoch", "key"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				f, s, facts, _ := recoveryHandoffAPIFixture(t)
				ctx := context.Background()
				pub := f.key.Public().(ed25519.PublicKey)
				if stage != "before" {
					if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "applied" {
					if _, err := f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
						t.Fatal(err)
					}
				}
				before := recoveryHandoffSnapshot(t, f.db)
				switch change {
				case "scope":
					facts.ScopeSHA256 = strings.Repeat("d", 64)
				case "prior_scope":
					facts.PriorScopeSHA256 = strings.Repeat("d", 64)
				case "record":
					facts.RecordSHA256 = "bad"
				case "prior_record":
					facts.PriorRecordSHA256 = "bad"
				case "authority":
					facts.AuthoritySHA256 = "bad"
				case "uppercase":
					facts.AuthoritySHA256 = strings.Repeat("A", 64)
				case "owner":
					s.OwnerID = "foreign"
				case "pid":
					s.Authentication.Owner.PID++
				case "birth":
					s.Authentication.Owner.Birth = "foreign"
				case "public_key":
					s.Authentication.Owner.PublicKey = strings.Repeat("a", 64)
				case "origin":
					s.Authentication.OriginBirth = "foreign"
				case "digest":
					s.Authentication.ExecutableSHA256 = strings.Repeat("f", 64)
				case "token":
					s.Host.LaunchToken = "foreign"
				case "host":
					s.Host.PID++
				case "epoch":
					s.Epoch++
				case "key":
					pub = nil
				}
				if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err == nil {
					t.Fatal("changed input accepted")
				}
				if _, err := f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err == nil {
					t.Fatal("changed apply accepted")
				}
				if before != recoveryHandoffSnapshot(t, f.db) {
					t.Fatal("rejection changed state")
				}
			})
		}
	}
}
func TestOwnerRecoveryHandoffConcurrentCommandsHaveOneReceipt(t *testing.T) {
	f, s, facts, _ := recoveryHandoffAPIFixture(t)
	ctx := context.Background()
	pub := f.key.Public().(ed25519.PublicKey)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if originalOwnerSchemaCount(t, f.db.sql, `SELECT COUNT(*) FROM owner_recovery_scope_handoffs`) != 1 {
		t.Fatal("multiple receipts")
	}
}
func TestOwnerRecoveryHandoffGatesOwnerApplied(t *testing.T) {
	f, s, facts, b := recoveryHandoffAPIFixture(t)
	ctx := context.Background()
	pub := f.key.Public().(ed25519.PublicKey)
	a := successorOwnerApplied(t, BindOriginalOwnerSpec{Host: s.Host, Epoch: s.Epoch, Authenticated: s.Authentication}, b, 0)
	for _, stage := range []string{"missing", "prepared"} {
		if stage == "prepared" {
			if _, err := f.db.PrepareOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
				t.Fatal(err)
			}
		}
		before := recoveryHandoffSnapshot(t, f.db)
		if err := f.db.CommitOriginalOwnerApplied(ctx, a, pub); err == nil {
			t.Fatal("owner applied without recovery handoff", stage)
		}
		if err := f.db.ActivateOriginalOwner(ctx, a, pub); err == nil {
			t.Fatal("activated without recovery handoff", stage)
		}
		if before != recoveryHandoffSnapshot(t, f.db) {
			t.Fatal("rejected owner application mutated")
		}
	}
	if _, err := f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
		t.Fatal(err)
	}
	if err := f.db.CommitOriginalOwnerApplied(ctx, a, pub); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ActivateOriginalOwner(ctx, a, pub); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ApplyOriginalOwnerRecoveryHandoff(ctx, s, facts, pub); err != nil {
		t.Fatal("exact replay after activation", err)
	}
}

// Store-only fixtures supply Coordinator-observed hashes. Real filesystem CAS
// is covered separately and must be connected by the signed runtime handler.
func completeOwnerRecoveryHandoffForStoreTest(t *testing.T, db *DB, bs BindOriginalOwnerSpec, b OriginalOwnerBindResult, pub ed25519.PublicKey) {
	t.Helper()
	ctx := context.Background()
	spec := OwnerRecoveryHandoffSpec{OwnerID: b.Binding.Scope.OwnerID, Host: bs.Host, Epoch: bs.Epoch, Authentication: bs.Authenticated}
	resolved, err := db.ResolveOriginalOwnerRecoveryHandoff(ctx, spec, pub)
	if err != nil {
		t.Fatal(err)
	}
	prior, _ := contract.OriginalOwnerRecoveryScopeSHA256(resolved.PriorBinding.Scope, resolved.PriorBinding.Owner)
	next, _ := contract.OriginalOwnerRecoveryScopeSHA256(resolved.Binding.Scope, resolved.Binding.Owner)
	facts := OwnerRecoveryHandoffFacts{PriorScopeSHA256: prior, ScopeSHA256: next, PriorRecordSHA256: strings.Repeat("a", 64), RecordSHA256: strings.Repeat("b", 64), AuthoritySHA256: strings.Repeat("c", 64)}
	if _, err = db.PrepareOriginalOwnerRecoveryHandoff(ctx, spec, facts, pub); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ApplyOriginalOwnerRecoveryHandoff(ctx, spec, facts, pub); err != nil {
		t.Fatal(err)
	}
}
