package host

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

var errOwnerEnrollment = errors.New("owner_enrollment_rejected")

type ownerEnrollment struct {
	bootstrap contract.OwnerRuntimeBootstrap
	client    ipc.OwnerClient
}

func (e *ownerEnrollment) bind(ctx context.Context) (contract.OwnerBindReply, error) {
	if e == nil {
		return contract.OwnerBindReply{}, errOwnerEnrollment
	}
	return e.bindWith(ctx, e.client.Call)
}

func (e *ownerEnrollment) bindWith(ctx context.Context, rpc ownerExecutionRPC) (contract.OwnerBindReply, error) {
	var zero contract.OwnerBindReply
	if e == nil || rpc == nil || e.bootstrap.Validate() != nil || e.client.Intent != e.bootstrap.Prepared.Intent || e.client.SocketPath != e.bootstrap.Prepared.RPCSocketPath || e.client.Owner.Validate() != nil || hex.EncodeToString(e.client.CoordinatorKey) != e.bootstrap.Prepared.CoordinatorPublicKey || len(e.client.PrivateKey) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(e.client.PrivateKey.Seed()), e.client.PrivateKey) || hex.EncodeToString(e.client.PrivateKey.Public().(ed25519.PublicKey)) != e.client.Owner.PublicKey {
		return zero, errOwnerEnrollment
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	intent := e.bootstrap.Prepared.Intent
	hello := contract.OriginalOwnerHello{Scope: intent.Scope, BootstrapID: intent.BootstrapID, BootstrapToken: e.bootstrap.BootstrapToken, Owner: e.client.Owner, FirstHost: intent.FirstHost, CoordinatorKeyID: intent.CoordinatorKeyID}
	reply, err := rpc(ctx, 0, "bind", intent.RequestID, hello)
	if err != nil {
		return zero, err
	}
	var wire struct {
		Binding json.RawMessage `json:"binding"`
		Permit  json.RawMessage `json:"permit"`
	}
	if contract.DecodeOwnerRuntimeStrict(reply.Body, &wire) != nil {
		return zero, errOwnerEnrollment
	}
	binding, err := contract.DecodeOriginalOwnerBinding(wire.Binding)
	if err != nil {
		return zero, errOwnerEnrollment
	}
	permit, err := contract.DecodeOwnerControlPermit(wire.Permit)
	if err != nil {
		return zero, errOwnerEnrollment
	}
	bound := contract.OwnerBindReply{Binding: binding, Permit: permit}
	if verifyOwnerEnrollment(e.bootstrap, e.client.Owner, e.client.CoordinatorKey, bound) != nil {
		return zero, errOwnerEnrollment
	}
	return bound, nil
}

func verifyOwnerEnrollment(bootstrap contract.OwnerRuntimeBootstrap, owner contract.OriginalOwnerIdentity, key ed25519.PublicKey, result contract.OwnerBindReply) error {
	intent := bootstrap.Prepared.Intent
	expectedAttempt := ""
	if bootstrap.Prepared.LaunchAttempt != nil {
		expectedAttempt = bootstrap.Prepared.LaunchAttempt.AttemptSHA256
	}
	if result.Binding.LaunchAttemptSHA256 != expectedAttempt {
		return errOwnerEnrollment
	}
	if bootstrap.Validate() != nil || hex.EncodeToString(key) != bootstrap.Prepared.CoordinatorPublicKey || contract.VerifyOriginalOwnerBinding(result.Binding, key) != nil || result.Binding.Scope != intent.Scope || result.Binding.Owner != owner || result.Binding.FirstHost != intent.FirstHost || result.Binding.CoordinatorKeyID != intent.CoordinatorKeyID {
		return errOwnerEnrollment
	}
	scopeHash, err := contract.OriginalOwnerScopeSHA256(intent.Scope)
	if err != nil {
		return errOwnerEnrollment
	}
	ownerHash, err := contract.OriginalOwnerIdentitySHA256(owner)
	if err != nil {
		return errOwnerEnrollment
	}
	p := result.Permit
	if contract.VerifyOwnerControlPermit(p, key) != nil || p.OwnerID != intent.Scope.OwnerID || p.ScopeSHA256 != scopeHash || p.OwnerIdentitySHA256 != ownerHash || p.CoordinatorKeyID != intent.CoordinatorKeyID || p.ExpectedControlRevision != 0 || p.ControlRevision != 1 || p.OldHost != nil || p.NewHost != intent.FirstHost || p.RequestID != intent.RequestID || p.RequestSHA256 != intent.RequestSHA256 || p.CoordinatorEpoch < intent.CoordinatorEpoch {
		return errOwnerEnrollment
	}
	return nil
}
