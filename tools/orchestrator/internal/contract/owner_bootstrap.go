package contract

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/json"
	"path/filepath"
	"strings"
)

// OwnerRuntimeBootstrap is enrollment material delivered once on an inherited
// pipe. Prepared is not signed: runtime must independently obtain and verify
// Binding/Permit before admitting work. The owner generates its own private key.
type OwnerRuntimeBootstrap struct {
	Version        int           `json:"version"`
	StateDir       string        `json:"state_dir"`
	SpoolRoot      string        `json:"spool_root"`
	Prepared       OwnerPrepared `json:"prepared"`
	BootstrapToken string        `json:"bootstrap_token"`
}

// OwnerTransportBootstrap contains public scope only. A replacement child must
// exchange its exact short-lived ticket itself; launch/control credentials and
// enrollment/private signing material have no place in this object.
type OwnerTransportBootstrap struct {
	Version              int                   `json:"version"`
	StateDir             string                `json:"state_dir"`
	Intent               OriginalOwnerIntent   `json:"intent"`
	Owner                OriginalOwnerIdentity `json:"owner"`
	CoordinatorPublicKey string                `json:"coordinator_public_key"`
}

// Filesystem ownership, live PID/birth, deadline and the initial transport's
// authenticated public-key pin are checked by the runtime before enrollment.
// This validation is deliberately pure: path equality is not an inode proof.
func (b OwnerRuntimeBootstrap) Validate() error {
	if b.Version != 1 || ownerBootstrapPublic(b.StateDir, b.Prepared.Intent, b.Prepared.CoordinatorPublicKey) != nil || b.SpoolRoot != filepath.Join(b.StateDir, "host-spool", b.Prepared.Intent.Scope.LaunchID) || b.Prepared.RPCSocketPath != filepath.Join(b.StateDir, "owner.sock") || !ownerBootstrapSize(b) {
		return ownerInvalid()
	}
	hash, err := OriginalOwnerBootstrapSHA256(b.BootstrapToken)
	if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(b.Prepared.Intent.BootstrapTokenSHA256)) != 1 {
		return ownerInvalid()
	}
	return nil
}

func (b OwnerTransportBootstrap) Validate() error {
	if b.Version != 1 || ownerBootstrapPublic(b.StateDir, b.Intent, b.CoordinatorPublicKey) != nil || b.Owner.Validate() != nil || !ownerBootstrapSize(b) {
		return ownerInvalid()
	}
	return nil
}

func ownerBootstrapPublic(state string, intent OriginalOwnerIntent, publicKey string) error {
	if !ownerText(state, 4096) || !filepath.IsAbs(state) || filepath.Clean(state) != state || filepath.Dir(state) == state || intent.Validate() != nil {
		return ownerInvalid()
	}
	launch := intent.Scope.LaunchID
	if launch == "." || launch == ".." || filepath.Base(launch) != launch || strings.ContainsAny(launch, `/\`) || intent.Scope.ProducerID != launch {
		return ownerInvalid()
	}
	key, ok := ownerHex(publicKey, ed25519.PublicKeySize)
	if !ok {
		return ownerInvalid()
	}
	id, err := OriginalOwnerKeyID(ed25519.PublicKey(key))
	if err != nil || id != intent.CoordinatorKeyID {
		return ownerInvalid()
	}
	return nil
}

func ownerBootstrapSize(value any) bool {
	raw, err := json.Marshal(value)
	return err == nil && len(raw) <= OriginalOwnerEnvelopeLimit
}

func DecodeOwnerRuntimeBootstrap(raw []byte) (OwnerRuntimeBootstrap, error) {
	var b OwnerRuntimeBootstrap
	if err := decodeOwnerRuntime(raw, &b); err != nil {
		return b, err
	}
	return b, b.Validate()
}

func DecodeOwnerTransportBootstrap(raw []byte) (OwnerTransportBootstrap, error) {
	var b OwnerTransportBootstrap
	if err := decodeOwnerRuntime(raw, &b); err != nil {
		return b, err
	}
	return b, b.Validate()
}
