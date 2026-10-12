package host

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

// The initial transport already authenticated HostHello and Prepare on this
// connection. Keep that exact session and its credential inside the transport;
// the original owner receives only the public, signed binding on FD4.
type ownerTransportInitialSession struct {
	connection net.Conn
	pending    ipc.Envelope
	token      string
}

func verifyInitialOwnerBound(bootstrap contract.OwnerRuntimeBootstrap, child contract.OriginalOwnerTransport, frame ipc.Envelope) (contract.OwnerBindReply, error) {
	var zero contract.OwnerBindReply
	if child.Validate() != nil || frame.Kind != ipc.KindOriginalOwnerBound || frame.RequestID != bootstrap.Prepared.Intent.RequestID {
		return zero, errOwnerEnrollment
	}
	var wire struct {
		Binding json.RawMessage `json:"binding"`
		Permit  json.RawMessage `json:"permit"`
	}
	if contract.DecodeOwnerRuntimeStrict(frame.Payload, &wire) != nil {
		return zero, errOwnerEnrollment
	}
	binding, err := contract.DecodeOriginalOwnerBinding(wire.Binding)
	if err != nil {
		return zero, errOwnerEnrollment
	}
	permit, err := contract.DecodeOwnerControlPermit(wire.Permit)
	if err != nil || frame.Epoch != permit.CoordinatorEpoch || binding.Owner.PID != child.PID || binding.Owner.Birth != child.Birth {
		return zero, errOwnerEnrollment
	}
	key, err := hex.DecodeString(bootstrap.Prepared.CoordinatorPublicKey)
	bound := contract.OwnerBindReply{Binding: binding, Permit: permit}
	// The child does not choose its public key pin: the Coordinator signature
	// authenticates it, alongside the launcher's original PID and birth proof.
	if err != nil || verifyOwnerEnrollment(bootstrap, binding.Owner, ed25519.PublicKey(key), bound) != nil {
		return zero, errOwnerEnrollment
	}
	return bound, nil
}

func (initial *ownerTransportInitialSession) validate(b contract.OwnerTransportBootstrap, identity contract.OriginalOwnerTransport) error {
	if initial == nil || initial.connection == nil || initial.token == "" || len(initial.token) > 4096 || strings.ContainsRune(initial.token, 0) || identity != b.Intent.FirstHost {
		return errOwnerTransportProtocol
	}
	frame := initial.pending
	pending, err := contract.DecodeOwnerHostPending(frame.Payload)
	scope := b.Intent.Scope
	if err != nil || frame.Kind != ipc.KindOwnerHostPending || frame.RequestID != scope.LaunchID || frame.Epoch != pending.CoordinatorEpoch || frame.Epoch != b.Intent.CoordinatorEpoch || pending.HostID != scope.ProducerID || (pending.OwnerID != "" && pending.OwnerID != scope.OwnerID) || pending.CoordinatorPublicKey != b.CoordinatorPublicKey {
		return errOwnerTransportProtocol
	}
	return nil
}
