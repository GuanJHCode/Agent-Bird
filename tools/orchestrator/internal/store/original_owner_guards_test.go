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

func originalOwnerSnapshot(t *testing.T, f originalOwnerFixture) string {
	t.Helper()
	var bodies []string
	for _, table := range []string{"execution_owners", "owner_control_changes"} {
		rows, err := f.db.sql.Query(`SELECT * FROM ` + table + ` ORDER BY 1,2`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for n := range values {
				ptrs[n] = &values[n]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(values)
			bodies = append(bodies, string(raw))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return strings.Join(bodies, "\n")
}
func originalOwnerBound(t *testing.T) (originalOwnerFixture, contract.OriginalOwnerIntent, OriginalOwnerBindResult) {
	t.Helper()
	f := newOriginalOwnerFixture(t)
	i, err := f.db.PrepareOriginalOwner(context.Background(), f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.db.BindOriginalOwner(context.Background(), f.bindSpec(i), f.key)
	if err != nil {
		t.Fatal(err)
	}
	return f, i, b
}

func TestOriginalOwnerPrepareExactReplayAndCurrentOccupancy(t *testing.T) {
	for _, name := range []string{"request_hash", "bootstrap", "bootstrap_hash", "key", "deadline", "executable_sha", "host_pid", "host_birth", "origin", "origin_pid", "origin_birth", "generation", "launch", "token", "epoch", "different_request"} {
		t.Run(name, func(t *testing.T) {
			f := newOriginalOwnerFixture(t)
			ctx := context.Background()
			i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
			if err != nil {
				t.Fatal(err)
			}
			before := originalOwnerSnapshot(t, f)
			replay, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
			if err != nil || replay != i {
				t.Fatal("lost Prepare ACK changed immutable intent", err)
			}
			s := f.prepare
			switch name {
			case "request_hash":
				s.RequestSHA256 = strings.Repeat("a", 64)
			case "bootstrap":
				s.BootstrapID = "foreign"
			case "bootstrap_hash":
				s.BootstrapTokenSHA256 = strings.Repeat("a", 64)
			case "key":
				s.CoordinatorKeyID = strings.Repeat("a", 64)
			case "deadline":
				s.StartupDeadlineUnixMS++
			case "executable_sha":
				s.ExecutableSHA256 = strings.Repeat("a", 64)
			case "host_pid":
				s.Host.PID++
			case "host_birth":
				s.Host.Birth = "other"
			case "origin":
				s.Host.OriginContextID = "other"
			case "origin_pid":
				s.Host.OriginPID++
			case "origin_birth":
				s.Host.OriginBirth = "other"
			case "generation":
				s.Host.HostGeneration = "other"
			case "launch":
				s.Host.LaunchID = "other"
			case "token":
				s.Host.LaunchToken = "other"
			case "epoch":
				s.Epoch++
			case "different_request":
				s.RequestID = "new-bootstrap"
			}
			if _, err := f.db.PrepareOriginalOwner(ctx, s); err == nil {
				t.Fatal("conflicting or unauthenticated Prepare accepted")
			}
			if before != originalOwnerSnapshot(t, f) {
				t.Fatal("Prepare refusal mutated original occupancy")
			}
		})
	}
}

func TestOriginalOwnerBindRejectsIdentityAndAuthorityDrift(t *testing.T) {
	for _, name := range []string{"scope", "incarnation", "bootstrap", "secret", "owner_pid", "owner_birth", "pubkey", "first_pid", "first_birth", "key_id", "observed_owner", "observed_exe", "ancestor_pid", "ancestor_birth", "host_token", "host_pid", "host_birth", "stale_epoch", "signer", "malformed_signer", "before_worker"} {
		t.Run(name, func(t *testing.T) {
			f := newOriginalOwnerFixture(t)
			ctx := context.Background()
			i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
			if err != nil {
				t.Fatal(err)
			}
			s := f.bindSpec(i)
			key := append(ed25519.PrivateKey(nil), f.key...)
			switch name {
			case "scope":
				s.Hello.Scope.HostGeneration = "other"
			case "incarnation":
				s.Hello.Scope.Incarnation++
			case "bootstrap":
				s.Hello.BootstrapID = "other"
			case "secret":
				s.Hello.BootstrapToken = strings.Repeat("cd", 32)
			case "owner_pid":
				s.Hello.Owner.PID++
			case "owner_birth":
				s.Hello.Owner.Birth = "other"
			case "pubkey":
				s.Hello.Owner.PublicKey = strings.Repeat("a", 64)
			case "first_pid":
				s.Hello.FirstHost.PID++
			case "first_birth":
				s.Hello.FirstHost.Birth = "other"
			case "key_id":
				s.Hello.CoordinatorKeyID = strings.Repeat("a", 64)
			case "observed_owner":
				s.Authenticated.Owner.PID++
			case "observed_exe":
				s.Authenticated.ExecutableSHA256 = strings.Repeat("a", 64)
			case "ancestor_pid":
				s.Authenticated.OriginPID++
			case "ancestor_birth":
				s.Authenticated.OriginBirth = "other"
			case "host_token":
				s.Host.LaunchToken = "other"
			case "host_pid":
				s.Host.PID++
			case "host_birth":
				s.Host.Birth = "other"
			case "stale_epoch":
				s.Epoch = 2
			case "signer":
				key = ed25519.NewKeyFromSeed([]byte(strings.Repeat("x", 32)))
			case "malformed_signer":
				key[0] ^= 1
			case "before_worker":
				if _, err := f.db.ClaimReady(ctx, i.Scope.ProducerID, 1, 1); err != nil {
					t.Fatal(err)
				}
			}
			before := originalOwnerSnapshot(t, f)
			if _, err := f.db.BindOriginalOwner(ctx, s, key); err == nil {
				t.Fatal("unproved original owner binding accepted")
			}
			if before != originalOwnerSnapshot(t, f) {
				t.Fatal("Bind rejection partially committed")
			}
		})
	}
}

func TestOriginalOwnerPredicatesAttachRejectsDriftWithoutMutatingLease(t *testing.T) {
	for _, name := range []string{"owner", "scope", "identity", "key", "revision", "old_pid", "old_birth", "new_pid", "new_birth", "request_hash", "observed_owner", "observed_exe", "ancestor", "token", "auth_epoch", "request_epoch", "signer", "unregistered_peer"} {
		t.Run(name, func(t *testing.T) {
			f, i, b := originalOwnerBound(t)
			ctx := context.Background()
			f.db.BeginCoordinatorEpoch(ctx, 2)
			if _, err := f.db.RegisterHost(ctx, f.prepare.Host, 2); err != nil {
				t.Fatal(err)
			}
			s := originalOwnerAttachInput(f, i, b, 2, 1, "renew")
			key := f.key
			switch name {
			case "owner":
				s.OwnerID = "foreign"
			case "scope":
				s.ScopeSHA256 = strings.Repeat("a", 64)
			case "identity":
				s.OwnerIdentitySHA256 = strings.Repeat("a", 64)
			case "key":
				s.CoordinatorKeyID = strings.Repeat("a", 64)
			case "revision":
				s.ExpectedControlRevision++
			case "old_pid":
				s.OldHost.PID++
			case "old_birth":
				s.OldHost.Birth = "foreign"
			case "new_pid":
				s.NewHost.PID++
			case "new_birth":
				s.NewHost.Birth = "foreign"
			case "request_hash":
				s.RequestSHA256 = "noncanonical"
			case "observed_owner":
				s.Authentication.Owner.Owner.PID++
			case "observed_exe":
				s.Authentication.Owner.ExecutableSHA256 = strings.Repeat("a", 64)
			case "ancestor":
				s.Authentication.Owner.OriginPID++
			case "token":
				s.Authentication.Host.LaunchToken = "foreign"
			case "auth_epoch":
				s.Authentication.Epoch = 1
			case "request_epoch":
				s.Epoch = 1
			case "signer":
				key = ed25519.NewKeyFromSeed([]byte(strings.Repeat("x", 32)))
			case "unregistered_peer":
				if _, err := f.db.sql.Exec(`UPDATE runtime_hosts SET pid=404,birth='foreign'`); err != nil {
					t.Fatal(err)
				}
			}
			before := originalOwnerSnapshot(t, f)
			if _, err := f.db.CommitOriginalOwnerAttach(ctx, s, key); err == nil {
				t.Fatal("drifted lease predicate accepted")
			}
			if before != originalOwnerSnapshot(t, f) {
				t.Fatal("refused attach mutated committed lease")
			}
		})
	}
}

func TestOriginalOwnerHistoricalReplayNeedsSameCurrentAuthenticatedPeer(t *testing.T) {
	f, i, b := originalOwnerBound(t)
	ctx := context.Background()
	f.db.BeginCoordinatorEpoch(ctx, 2)
	f.db.RegisterHost(ctx, f.prepare.Host, 2)
	s := originalOwnerAttachInput(f, i, b, 2, 1, "renew")
	original, err := f.db.CommitOriginalOwnerAttach(ctx, s, f.key)
	if err != nil {
		t.Fatal(err)
	}
	f.db.BeginCoordinatorEpoch(ctx, 3)
	f.db.RegisterHost(ctx, f.prepare.Host, 3)
	s.Authentication.Epoch = 3
	before := originalOwnerSnapshot(t, f)
	replay, err := f.db.CommitOriginalOwnerAttach(ctx, s, f.key)
	if err != nil {
		t.Fatal(err)
	}
	one, _ := json.Marshal(original)
	two, _ := json.Marshal(replay)
	if string(one) != string(two) || before != originalOwnerSnapshot(t, f) {
		t.Fatal("historical replay was upgraded to current epoch")
	}
	for _, name := range []string{"digest", "epoch", "expected_revision", "old_peer", "new_peer", "authenticated_peer"} {
		t.Run(name, func(t *testing.T) {
			v := s
			switch name {
			case "digest":
				v.RequestSHA256 = strings.Repeat("a", 64)
			case "epoch":
				v.Epoch = 3
			case "expected_revision":
				v.ExpectedControlRevision = 2
			case "old_peer":
				v.OldHost.Birth = "other"
			case "new_peer":
				v.NewHost.Birth = "other"
			case "authenticated_peer":
				v.Authentication.Host.Birth = "other"
			}
			if _, err := f.db.CommitOriginalOwnerAttach(ctx, v, f.key); err == nil {
				t.Fatal("historical request or peer drift accepted")
			}
			if before != originalOwnerSnapshot(t, f) {
				t.Fatal("historical conflict mutated lease")
			}
		})
	}
}

func TestOriginalOwnerConcurrentExactRequestsCommitOnce(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ids []string
	var errs []error
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i, e := f.db.PrepareOriginalOwner(ctx, f.prepare)
			mu.Lock()
			defer mu.Unlock()
			ids = append(ids, i.Scope.OwnerID)
			errs = append(errs, e)
		}()
	}
	wg.Wait()
	for n, e := range errs {
		if e != nil || ids[n] != ids[0] {
			t.Fatal("concurrent exact Prepare not idempotent", e)
		}
	}
	i, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
	if err != nil {
		t.Fatal(err)
	}
	errs = nil
	var bindings []string
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, e := f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key)
			raw, _ := json.Marshal(b)
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, e)
			bindings = append(bindings, string(raw))
		}()
	}
	wg.Wait()
	for n, e := range errs {
		if e != nil || bindings[n] != bindings[0] {
			t.Fatal("concurrent exact Bind not idempotent", e)
		}
	}
	b, err := f.db.BindOriginalOwner(ctx, f.bindSpec(i), f.key)
	if err != nil {
		t.Fatal(err)
	}
	f.db.BeginCoordinatorEpoch(ctx, 2)
	f.db.RegisterHost(ctx, f.prepare.Host, 2)
	spec := originalOwnerAttachInput(f, i, b, 2, 1, "renewal")
	errs = nil
	bindings = nil
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := f.db.CommitOriginalOwnerAttach(ctx, spec, f.key)
			raw, _ := json.Marshal(p)
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, e)
			bindings = append(bindings, string(raw))
		}()
	}
	wg.Wait()
	for n, e := range errs {
		if e != nil || bindings[n] != bindings[0] {
			t.Fatal("concurrent exact Attach not idempotent", e)
		}
	}
	var owners, changes int
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM execution_owners`).Scan(&owners)
	f.db.sql.QueryRow(`SELECT COUNT(*) FROM owner_control_changes`).Scan(&changes)
	if owners != 1 || changes != 2 {
		t.Fatal("duplicate concurrent effects")
	}
}
