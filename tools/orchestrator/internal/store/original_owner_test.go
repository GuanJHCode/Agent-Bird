package store

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type originalOwnerFixture struct {
	db      *DB
	prepare PrepareOriginalOwnerSpec
	key     ed25519.PrivateKey
}

func newOriginalOwnerFixture(t *testing.T) originalOwnerFixture {
	return newOriginalOwnerFixtureWithDiagnostics(t, nil)
}

func newOriginalOwnerFixtureWithDiagnostics(t *testing.T, diagnostics *contract.DiagnosticsPolicy) originalOwnerFixture {
	return newOriginalOwnerFixtureWithPlan(t, diagnostics, nil)
}

func newOriginalOwnerFixtureWithPlan(t *testing.T, diagnostics *contract.DiagnosticsPolicy, configure func(*PlanSpec)) originalOwnerFixture {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.BeginCoordinatorEpoch(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	p := providerTestPlan("owner-run", "controller", "grok-build")
	for i := range p.Tasks {
		p.Tasks[i].Diagnostics = diagnostics
	}
	if configure != nil {
		configure(&p)
	}
	p.Host.ExecutionOwnerMode = "supervised-v1"
	r, err := db.SubmitPlan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	h := contract.HostHello{LaunchID: r.LaunchID, LaunchToken: r.LaunchToken, OriginContextID: p.Run.OriginContextID, OriginPID: p.Run.OriginPID, OriginBirth: p.Run.OriginBirth, HostGeneration: p.Host.HostGeneration, PID: 101, Birth: "host-birth", Executable: p.Host.Executable}
	if _, err := db.RegisterHost(context.Background(), h, 1); err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("c", 32)))
	id, _ := contract.OriginalOwnerKeyID(key.Public().(ed25519.PublicKey))
	tokenHash, _ := contract.OriginalOwnerBootstrapSHA256(strings.Repeat("ab", 32))
	return originalOwnerFixture{db: db, key: key, prepare: PrepareOriginalOwnerSpec{Host: h, Epoch: 1, ExecutableSHA256: strings.Repeat("e", 64), BootstrapID: "bootstrap", BootstrapTokenSHA256: tokenHash, CoordinatorKeyID: id, RequestID: "initial", RequestSHA256: strings.Repeat("f", 64), StartupDeadlineUnixMS: time.Now().Add(5 * time.Second).UnixMilli()}}
}
func (f originalOwnerFixture) bindSpec(i contract.OriginalOwnerIntent) BindOriginalOwnerSpec {
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("o", 32)))
	owner := contract.OriginalOwnerIdentity{PID: 202, Birth: "original-owner-birth", PublicKey: hex.EncodeToString(key.Public().(ed25519.PublicKey))}
	return BindOriginalOwnerSpec{Hello: contract.OriginalOwnerHello{Scope: i.Scope, BootstrapID: i.BootstrapID, BootstrapToken: strings.Repeat("ab", 32), Owner: owner, FirstHost: i.FirstHost, CoordinatorKeyID: i.CoordinatorKeyID}, Host: f.prepare.Host, Epoch: 1, Authenticated: OriginalOwnerAuthentication{Owner: owner, OriginPID: i.Scope.OriginalOriginPID, OriginBirth: i.Scope.OriginalOriginBirth, ExecutableSHA256: i.Scope.ExecutableSHA256}}
}
func TestOriginalOwnerInitialBindingAndPermitCommitTogether(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatalf("immutable initial intent must commit before first owner enrollment: %v", err)
	}
	b, err := f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key)
	if err != nil {
		t.Fatal(err)
	}
	if b.Binding.Scope != i.Scope || b.Permit.ControlRevision != 1 || b.Permit.ExpectedControlRevision != 0 || b.Permit.OldHost != nil || b.Permit.NewHost != i.FirstHost {
		t.Fatal("initial immutable binding/control tuple drifted")
	}
	if contract.VerifyOriginalOwnerBinding(b.Binding, f.key.Public().(ed25519.PublicKey)) != nil || contract.VerifyOwnerControlPermit(b.Permit, f.key.Public().(ed25519.PublicKey)) != nil {
		t.Fatal("unsigned persisted first authorization")
	}
	var phase string
	var revision, count int
	if err := f.db.sql.QueryRow(`SELECT phase,control_revision FROM execution_owners WHERE owner_id=?`, i.Scope.OwnerID).Scan(&phase, &revision); err != nil || phase != "bound" || revision != 1 {
		t.Fatalf("binding missing: %s/%d %v", phase, revision, err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM owner_control_changes WHERE owner_id=? AND revision=1`, i.Scope.OwnerID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("initial journal missing: %d %v", count, err)
	}
	replay, err := f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key)
	if err != nil {
		t.Fatal(err)
	}
	one, _ := json.Marshal(b)
	two, _ := json.Marshal(replay)
	if string(one) != string(two) {
		t.Fatal("lost Bind ACK allocated another binding/permit/signature")
	}
}

func TestOriginalOwnerPredicatesFirstBindAfterSamePeerEpochChange(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.RegisterHost(ctx, f.prepare.Host, 2); err != nil {
		t.Fatal(err)
	}
	spec := f.bindSpec(i)
	spec.Epoch = 2
	b, err := f.db.BindOriginalOwner(ctx, spec, f.key)
	if err != nil {
		t.Fatalf("same original bootstrap/owner/physical peer at actual new epoch must bind once: %v", err)
	}
	state, err := f.db.InspectOriginalOwner(ctx, i.Scope.OwnerID)
	if err != nil || state.Intent != i || state.CoordinatorEpoch != 2 || state.ControlRevision != 1 || b.Permit.CoordinatorEpoch != 2 {
		t.Fatal("first bind rewrote original intent or minted another revision", err)
	}
}

func originalOwnerAttachInput(f originalOwnerFixture, i contract.OriginalOwnerIntent, b OriginalOwnerBindResult, epoch, revision uint64, request string) OriginalOwnerAttachSpec {
	scope, _ := contract.OriginalOwnerScopeSHA256(i.Scope)
	identity, _ := contract.OriginalOwnerIdentitySHA256(b.Binding.Owner)
	return OriginalOwnerAttachSpec{OwnerID: i.Scope.OwnerID, ScopeSHA256: scope, OwnerIdentitySHA256: identity, CoordinatorKeyID: i.CoordinatorKeyID, ExpectedControlRevision: revision, OldHost: i.FirstHost, NewHost: i.FirstHost, Epoch: epoch, RequestID: request, RequestSHA256: strings.Repeat("d", 64), Authentication: OriginalOwnerAttachAuthentication{Owner: f.bindSpec(i).Authenticated, Host: f.prepare.Host, Epoch: epoch}}
}
func TestOriginalOwnerPredicatesBindAckLossThenEpochRenewalAndPeerReplacement(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	first, err := f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.RegisterHost(ctx, f.prepare.Host, 2); err != nil {
		t.Fatal(err)
	}
	spec := f.bindSpec(i)
	spec.Epoch = 2
	replay, err := f.db.BindOriginalOwner(ctx, spec, f.key)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	c, _ := json.Marshal(replay)
	if string(a) != string(c) {
		t.Fatal("lost ACK rewrote binding/initial permit")
	}
	before, _ := f.db.InspectOriginalOwner(ctx, i.Scope.OwnerID)
	if before.CoordinatorEpoch != 1 || before.ControlRevision != 1 {
		t.Fatal("replay advanced lease")
	}
	renew := originalOwnerAttachInput(f, i, first, 2, 1, "renewal")
	permit, err := f.db.CommitOriginalOwnerAttach(ctx, renew, f.key)
	if err != nil {
		t.Fatalf("authenticated same-peer new epoch cannot require dead living peer: %v", err)
	}
	if permit.ControlRevision != 2 || permit.OldHost == nil || *permit.OldHost != permit.NewHost {
		t.Fatal("same-peer renewal tuple drifted")
	}
	again, err := f.db.CommitOriginalOwnerAttach(ctx, renew, f.key)
	if err != nil {
		t.Fatal(err)
	}
	a, _ = json.Marshal(permit)
	c, _ = json.Marshal(again)
	if string(a) != string(c) {
		t.Fatal("attach ACK loss advanced revision")
	}
	noop := renew
	noop.RequestID = "noop"
	noop.ExpectedControlRevision = 2
	if _, err := f.db.CommitOriginalOwnerAttach(ctx, noop, f.key); err == nil {
		t.Fatal("same peer/same epoch minted another permit")
	}
	if err := f.db.BeginCoordinatorEpoch(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.RegisterHost(ctx, f.prepare.Host, 3); err != nil {
		t.Fatal(err)
	}
	replace := originalOwnerAttachInput(f, i, first, 3, 2, "replacement")
	replace.NewHost = contract.OriginalOwnerTransport{PID: 303, Birth: "new-host-birth"}
	replace.Authentication.Host.PID, replace.Authentication.Host.Birth = replace.NewHost.PID, replace.NewHost.Birth
	if _, err := f.db.CommitOriginalOwnerAttach(ctx, replace, f.key); err == nil {
		t.Fatal("live or unobservable old peer replaced")
	}
	wrong := replace.OldHost
	wrong.Birth = "foreign"
	replace.Authentication.ExitedHost = &wrong
	if _, err := f.db.CommitOriginalOwnerAttach(ctx, replace, f.key); err == nil {
		t.Fatal("foreign old-peer death proof accepted")
	}
	replace.Authentication.ExitedHost = &replace.OldHost
	last, err := f.db.CommitOriginalOwnerAttach(ctx, replace, f.key)
	if err != nil {
		t.Fatal(err)
	}
	if last.ControlRevision != 3 || last.NewHost != replace.NewHost {
		t.Fatal("replacement control drifted")
	}
	again, err = f.db.CommitOriginalOwnerAttach(ctx, replace, f.key)
	if err != nil || again.Signature != last.Signature || again.PermitID != last.PermitID {
		t.Fatal("replacement ACK loss allocated fresh control", err)
	}
	state, _ := f.db.InspectOriginalOwner(ctx, i.Scope.OwnerID)
	if state.Intent != i || *state.Binding != first.Binding || state.ControlRevision != 3 || state.CurrentHost == nil || *state.CurrentHost != replace.NewHost {
		t.Fatal("control replacement mutated original owner/scope")
	}
}
