package contract

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
)

// OwnerPermitApplied is signed only by the original living executor, after it
// has replayed its own spool and report registrations. It never proves Wait on
// behalf of another process. The complete permit fixes the control tuple.
type OwnerPermitApplied struct {
	Permit     OwnerControlPermit `json:"permit"`
	Reconciled HostReconciled     `json:"reconciled"`
	Signature  string             `json:"signature"`
}

func (a OwnerPermitApplied) Validate() error {
	if a.Permit.Validate() != nil || a.Permit.Signature == "" || !ownerText(a.Reconciled.ProducerID, 256) || a.Reconciled.PublishedThrough < 0 {
		return ownerInvalid()
	}
	return nil
}

func SignOwnerPermitApplied(a OwnerPermitApplied, key ed25519.PrivateKey) (OwnerPermitApplied, error) {
	if len(key) != ed25519.PrivateKeySize || a.Validate() != nil {
		return OwnerPermitApplied{}, ownerInvalid()
	}
	a.Signature = ""
	body, _ := json.Marshal(a)
	a.Signature = hex.EncodeToString(ed25519.Sign(key, append([]byte("original-owner-applied-v1\x00"), body...)))
	return a, nil
}

func VerifyOwnerPermitApplied(a OwnerPermitApplied, key ed25519.PublicKey) error {
	sig, ok := ownerHex(a.Signature, ed25519.SignatureSize)
	if !ok || len(key) != ed25519.PublicKeySize || a.Validate() != nil {
		return ownerInvalid()
	}
	a.Signature = ""
	body, _ := json.Marshal(a)
	if !ed25519.Verify(key, append([]byte("original-owner-applied-v1\x00"), body...), sig) {
		return ownerInvalid()
	}
	return nil
}

func DecodeOwnerPermitApplied(raw []byte) (OwnerPermitApplied, error) {
	var a OwnerPermitApplied
	if err := decodeOwner(raw, &a); err != nil {
		return a, err
	}
	if _, ok := ownerHex(a.Signature, ed25519.SignatureSize); !ok {
		return a, ownerInvalid()
	}
	return a, a.Validate()
}
