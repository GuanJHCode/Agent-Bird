package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func ownerEnrollmentFixture(t *testing.T) (*ownerEnrollment, contract.OwnerBindReply, ed25519.PrivateKey) {
	t.Helper()
	cfg, permit, key := ownerExecutionFixture(t)
	binding := cfg.binding
	binding.Scope.LaunchID = binding.Scope.ProducerID
	binding.BindingSHA256 = ""
	binding, err := contract.SignOriginalOwnerBinding(binding, key)
	if err != nil {
		t.Fatal(err)
	}
	scopeHash, _ := contract.OriginalOwnerScopeSHA256(binding.Scope)
	permit.ScopeSHA256 = scopeHash
	permit, err = contract.SignOwnerControlPermit(permit, key)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("d", 64)
	hash, err := contract.OriginalOwnerBootstrapSHA256(token)
	if err != nil {
		t.Fatal(err)
	}
	intent := contract.OriginalOwnerIntent{Scope: binding.Scope, BootstrapID: "bootstrap", BootstrapTokenSHA256: hash, CoordinatorKeyID: binding.CoordinatorKeyID, RequestID: permit.RequestID, RequestSHA256: permit.RequestSHA256, StartupDeadlineUnixMS: time.Now().Add(time.Minute).UnixMilli(), FirstHost: binding.FirstHost, CoordinatorEpoch: permit.CoordinatorEpoch}
	state := t.TempDir()
	b := contract.OwnerRuntimeBootstrap{Version: 1, StateDir: state, SpoolRoot: filepath.Join(state, "host-spool", binding.Scope.LaunchID), Prepared: contract.OwnerPrepared{Intent: intent, CoordinatorPublicKey: hex.EncodeToString(cfg.coordinatorKey), RPCSocketPath: filepath.Join(state, "owner.sock")}, BootstrapToken: token}
	if err = b.Validate(); err != nil {
		t.Fatal(err)
	}
	return &ownerEnrollment{bootstrap: b, client: ipc.OwnerClient{SocketPath: b.Prepared.RPCSocketPath, Intent: intent, Owner: binding.Owner, PrivateKey: cfg.privateKey, CoordinatorKey: cfg.coordinatorKey}}, contract.OwnerBindReply{Binding: binding, Permit: permit}, key
}

func TestOwnerEnrollmentReplaysExactHelloAfterLostACK(t *testing.T) {
	e, bound, _ := ownerEnrollmentFixture(t)
	expiry := time.Now().Add(50 * time.Millisecond)
	e.bootstrap.Prepared.Intent.StartupDeadlineUnixMS = expiry.UnixMilli()
	e.client.Intent = e.bootstrap.Prepared.Intent
	var original []byte
	calls := 0
	rpc := func(_ context.Context, revision uint64, kind, id string, body any) (contract.OwnerRPCReply, error) {
		calls++
		if revision != 0 || kind != "bind" || id != e.bootstrap.Prepared.Intent.RequestID {
			t.Fatal("binding used mutable request authority")
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var hello contract.OriginalOwnerHello
		if err = contract.DecodeOwnerRuntimeStrict(raw, &hello); err != nil || hello.Scope != e.bootstrap.Prepared.Intent.Scope || hello.Owner != e.client.Owner || hello.FirstHost != e.bootstrap.Prepared.Intent.FirstHost || hello.BootstrapToken != e.bootstrap.BootstrapToken || hello.BootstrapID != e.bootstrap.Prepared.Intent.BootstrapID || hello.CoordinatorKeyID != e.bootstrap.Prepared.Intent.CoordinatorKeyID {
			t.Fatal("binding dropped exact bootstrap identity", err)
		}
		if calls == 1 {
			original = raw
			return contract.OwnerRPCReply{}, context.DeadlineExceeded
		}
		if !bytes.Equal(original, raw) {
			t.Fatal("lost ACK changed Hello")
		}
		reply, _ := json.Marshal(bound)
		return contract.OwnerRPCReply{Version: 1, CoordinatorEpoch: bound.Permit.CoordinatorEpoch + 1, Body: reply}, nil
	}
	if _, err := e.bindWith(context.Background(), rpc); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lost ACK was not retained", err)
	}
	// A committed enrollment remains exactly replayable after its startup window.
	time.Sleep(time.Until(expiry) + time.Millisecond)
	got, err := e.bindWith(context.Background(), rpc)
	if err != nil || got.Binding != bound.Binding || got.Permit.PermitID != bound.Permit.PermitID || calls != 2 {
		t.Fatal("exact old permit replay was lost", err)
	}
}

func TestOwnerEnrollmentRejectsSignedForeignBindingAndPermit(t *testing.T) {
	for _, kind := range []string{"binding_scope", "binding_owner", "binding_host", "permit_scope", "permit_owner", "permit_host", "permit_revision", "permit_epoch", "permit_id", "permit_body_hash", "signature", "foreign_key"} {
		t.Run(kind, func(t *testing.T) {
			e, bound, key := ownerEnrollmentFixture(t)
			wrong := bound
			switch kind {
			case "binding_scope":
				wrong.Binding.Scope.Incarnation++
			case "binding_owner":
				wrong.Binding.Owner.PID++
			case "binding_host":
				wrong.Binding.FirstHost.PID++
			case "permit_scope":
				wrong.Permit.ScopeSHA256 = strings.Repeat("e", 64)
			case "permit_owner":
				wrong.Permit.OwnerIdentitySHA256 = strings.Repeat("e", 64)
			case "permit_host":
				wrong.Permit.NewHost.PID++
			case "permit_revision":
				wrong.Permit.ControlRevision++
				wrong.Permit.ExpectedControlRevision++
				old := bound.Permit.NewHost
				wrong.Permit.OldHost = &old
			case "permit_epoch":
				wrong.Permit.CoordinatorEpoch--
			case "permit_id":
				wrong.Permit.RequestID = "different-business-binding"
			case "permit_body_hash":
				wrong.Permit.RequestSHA256 = strings.Repeat("e", 64)
			case "foreign_key":
				e.client.CoordinatorKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32)).Public().(ed25519.PublicKey)
			}
			var err error
			wrong.Binding.BindingSHA256 = ""
			wrong.Binding, err = contract.SignOriginalOwnerBinding(wrong.Binding, key)
			if err != nil {
				t.Fatal(err)
			}
			wrong.Permit, err = contract.SignOwnerControlPermit(wrong.Permit, key)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "signature" {
				wrong.Binding.Signature = strings.Repeat("0", 128)
			}
			raw, _ := json.Marshal(wrong)
			_, err = e.bindWith(context.Background(), func(context.Context, uint64, string, string, any) (contract.OwnerRPCReply, error) {
				return contract.OwnerRPCReply{Version: 1, Body: raw}, nil
			})
			if !errors.Is(err, errOwnerEnrollment) {
				t.Fatal("foreign authority reached owner engine", err)
			}
		})
	}
}

func TestOwnerEnrollmentRequiresCompleteSignedWire(t *testing.T) {
	e, bound, _ := ownerEnrollmentFixture(t)
	raw, _ := json.Marshal(bound)
	changed := bytes.Replace(raw, []byte(`"old_host":null,`), nil, 1)
	if bytes.Equal(raw, changed) {
		t.Fatal("fixture did not omit required nullable field")
	}
	_, err := e.bindWith(context.Background(), func(context.Context, uint64, string, string, any) (contract.OwnerRPCReply, error) {
		return contract.OwnerRPCReply{Version: 1, Body: changed}, nil
	})
	if !errors.Is(err, errOwnerEnrollment) {
		t.Fatal("signature reconstruction hid missing required permit field", err)
	}
}
