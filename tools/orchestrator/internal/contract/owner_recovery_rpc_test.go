package contract

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func TestOwnerRecoveryRPCIdentityAndEnvelope(t *testing.T) {
	b, key := ownerContractBinding()
	id, err := OwnerRecoveryScopeRequestID(b.Scope, b.Owner)
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := OriginalOwnerScopeSHA256(b.Scope)
	owner, _ := OriginalOwnerIdentitySHA256(b.Owner)
	r, err := SignOwnerRPCRequest(OwnerRPCRequest{Version: 1, OwnerID: b.Scope.OwnerID, ScopeSHA256: scope, OwnerIdentitySHA256: owner, CoordinatorEpoch: 1, ControlRevision: 1, RequestID: id, Kind: "recovery_scope", Nonce: strings.Repeat("a", 64), Body: json.RawMessage(`{"version":1}`)}, key)
	if err != nil {
		t.Fatal("private handoff kind rejected", err)
	}
	raw, _ := json.Marshal(r)
	decoded, err := DecodeOwnerRPCRequest(raw)
	if err != nil || OwnerRPCRequestSHA256(decoded) != OwnerRPCRequestSHA256(r) {
		t.Fatal("lost signed request", err)
	}
	reply, err := SignOwnerRPCReply(OwnerRPCReply{Version: 1, OwnerID: r.OwnerID, CoordinatorEpoch: 1, RequestID: id, RequestSHA256: OwnerRPCRequestSHA256(r), Body: json.RawMessage(`{"version":1,"status":"recovery_scope_applied"}`)}, key)
	if err != nil || VerifyOwnerRPCReply(reply, key.Public().(ed25519.PublicKey), r) != nil {
		t.Fatal("reply not bound", err)
	}
	r.ControlRevision = 0
	r.BodySHA256 = ""
	r.Signature = ""
	if _, err = SignOwnerRPCRequest(r, key); err == nil {
		t.Fatal("unbound handoff request accepted")
	}
	changed := b.Owner
	changed.Birth += "-other"
	other, err := OwnerRecoveryScopeRequestID(b.Scope, changed)
	if err != nil || other == id {
		t.Fatal("ID omitted physical owner")
	}
	if len(id) > 256 || len(id) == 0 {
		t.Fatal("invalid bounded RequestID")
	}
}
