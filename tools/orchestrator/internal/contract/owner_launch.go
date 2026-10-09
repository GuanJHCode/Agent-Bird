package contract

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"math"
)

const (
	OriginalOwnerLaunchPreparedNotStarted = "prepared_not_started"
	OriginalOwnerLaunchStarting           = "starting"
	OriginalOwnerLaunchStartedWaitOwned   = "started_wait_owned"
	OriginalOwnerLaunchWaited             = "waited"
	OriginalOwnerLaunchNotStartedFinal    = "not_started_final"
)

// OriginalOwnerLaunchSupervisor is the fixed process instance permitted to
// turn one prepared launch attempt into a started or terminal witness.
type OriginalOwnerLaunchSupervisor struct {
	Epoch            uint64 `json:"epoch"`
	PID              int    `json:"pid"`
	Birth            string `json:"birth"`
	Nonce            string `json:"nonce"`
	ExecutableSHA256 string `json:"executable_sha256"`
}

// OriginalOwnerLaunchAttempt keeps the raw base intent immutable. Its
// EffectiveIntent is the only intent a v2 child may consume.
type OriginalOwnerLaunchAttempt struct {
	Version            int                           `json:"version"`
	Attempt            uint8                         `json:"attempt"`
	BaseIntent         OriginalOwnerIntent           `json:"base_intent"`
	EffectiveIntent    OriginalOwnerIntent           `json:"effective_intent"`
	PriorReceiptSHA256 string                        `json:"prior_receipt_sha256,omitempty"`
	Supervisor         OriginalOwnerLaunchSupervisor `json:"supervisor"`
	AttemptSHA256      string                        `json:"attempt_sha256"`
}

type OriginalOwnerLaunchReceipt struct {
	Version            int                           `json:"version"`
	AttemptSHA256      string                        `json:"attempt_sha256"`
	Phase              string                        `json:"phase"`
	Supervisor         OriginalOwnerLaunchSupervisor `json:"supervisor"`
	Child              *OriginalOwnerTransport       `json:"child,omitempty"`
	HandoffClosed      bool                          `json:"handoff_closed"`
	ExitCode           *int                          `json:"exit_code,omitempty"`
	ExitSignal         string                        `json:"exit_signal,omitempty"`
	PriorReceiptSHA256 string                        `json:"prior_receipt_sha256,omitempty"`
	ReceiptSHA256      string                        `json:"receipt_sha256"`
	Signature          string                        `json:"signature"`
}

func (s OriginalOwnerLaunchSupervisor) Validate() error {
	if s.Epoch == 0 || s.Epoch > math.MaxInt64 || s.PID <= 0 || !ownerText(s.Birth, 256) || !ownerDigest(s.Nonce) || !ownerDigest(s.ExecutableSHA256) {
		return ownerInvalid()
	}
	return nil
}

func originalOwnerLaunchAttemptDigest(a OriginalOwnerLaunchAttempt) string {
	a.AttemptSHA256 = ""
	return ownerJSONHash(a)
}

func sameLaunchScope(a, b OriginalOwnerIntent) bool {
	return a.Scope == b.Scope && a.CoordinatorKeyID == b.CoordinatorKeyID
}

func (a OriginalOwnerLaunchAttempt) Validate() error {
	if a.Version != 2 || (a.Attempt != 1 && a.Attempt != 2) || a.BaseIntent.Validate() != nil || a.EffectiveIntent.Validate() != nil || a.Supervisor.Validate() != nil || !sameLaunchScope(a.BaseIntent, a.EffectiveIntent) || !ownerDigest(a.AttemptSHA256) || a.AttemptSHA256 != originalOwnerLaunchAttemptDigest(a) {
		return ownerInvalid()
	}
	if a.Attempt == 1 {
		if a.BaseIntent != a.EffectiveIntent || a.PriorReceiptSHA256 != "" {
			return ownerInvalid()
		}
	} else if !ownerDigest(a.PriorReceiptSHA256) || a.BaseIntent.FirstHost == a.EffectiveIntent.FirstHost {
		return ownerInvalid()
	}
	return nil
}

func SignOriginalOwnerLaunchAttempt(a OriginalOwnerLaunchAttempt) (OriginalOwnerLaunchAttempt, error) {
	a.AttemptSHA256 = ""
	a.AttemptSHA256 = originalOwnerLaunchAttemptDigest(a)
	if err := a.Validate(); err != nil {
		return OriginalOwnerLaunchAttempt{}, err
	}
	return a, nil
}

func VerifyOriginalOwnerLaunchAttempt(a OriginalOwnerLaunchAttempt) error { return a.Validate() }

func originalOwnerLaunchReceiptDigest(r OriginalOwnerLaunchReceipt) string {
	r.ReceiptSHA256, r.Signature = "", ""
	return ownerJSONHash(r)
}

func (r OriginalOwnerLaunchReceipt) Validate() error {
	if r.Version != 2 || !ownerDigest(r.AttemptSHA256) || r.Supervisor.Validate() != nil || !ownerDigest(r.ReceiptSHA256) || r.ReceiptSHA256 != originalOwnerLaunchReceiptDigest(r) {
		return ownerInvalid()
	}
	if r.PriorReceiptSHA256 != "" && !ownerDigest(r.PriorReceiptSHA256) {
		return ownerInvalid()
	}
	if r.Signature != "" {
		if _, ok := ownerHex(r.Signature, ed25519.SignatureSize); !ok {
			return ownerInvalid()
		}
	}
	switch r.Phase {
	case OriginalOwnerLaunchPreparedNotStarted, OriginalOwnerLaunchStarting:
		if r.Child != nil || r.HandoffClosed || r.ExitCode != nil || r.ExitSignal != "" {
			return ownerInvalid()
		}
	case OriginalOwnerLaunchStartedWaitOwned:
		if r.Child == nil || r.Child.Validate() != nil || !r.HandoffClosed || r.ExitCode != nil || r.ExitSignal != "" {
			return ownerInvalid()
		}
	case OriginalOwnerLaunchWaited:
		if r.Child == nil || r.Child.Validate() != nil || !r.HandoffClosed || (r.ExitCode == nil && r.ExitSignal == "") || (r.ExitCode != nil && r.ExitSignal != "") {
			return ownerInvalid()
		}
	case OriginalOwnerLaunchNotStartedFinal:
		if r.Child != nil || r.HandoffClosed || r.ExitCode != nil || r.ExitSignal != "" {
			return ownerInvalid()
		}
	default:
		return ownerInvalid()
	}
	return nil
}

func SignOriginalOwnerLaunchReceipt(r OriginalOwnerLaunchReceipt, key ed25519.PrivateKey) (OriginalOwnerLaunchReceipt, error) {
	r.ReceiptSHA256, r.Signature = "", ""
	r.ReceiptSHA256 = originalOwnerLaunchReceiptDigest(r)
	if err := r.Validate(); err != nil {
		return OriginalOwnerLaunchReceipt{}, err
	}
	if len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
		return OriginalOwnerLaunchReceipt{}, ownerInvalid()
	}
	raw, _ := json.Marshal(r)
	r.Signature = hex.EncodeToString(ed25519.Sign(key, append([]byte("original-owner-launch-v2\x00"), raw...)))
	return r, nil
}

func VerifyOriginalOwnerLaunchReceipt(r OriginalOwnerLaunchReceipt, key ed25519.PublicKey, a OriginalOwnerLaunchAttempt) error {
	if err := a.Validate(); err != nil || r.Validate() != nil || r.AttemptSHA256 != a.AttemptSHA256 {
		return ownerInvalid()
	}
	id, err := OriginalOwnerKeyID(key)
	if err != nil || id != a.EffectiveIntent.CoordinatorKeyID {
		return ownerInvalid()
	}
	sig, ok := ownerHex(r.Signature, ed25519.SignatureSize)
	if !ok {
		return ownerInvalid()
	}
	r.Signature = ""
	raw, _ := json.Marshal(r)
	if !ed25519.Verify(key, append([]byte("original-owner-launch-v2\x00"), raw...), sig) {
		return ownerInvalid()
	}
	return nil
}
