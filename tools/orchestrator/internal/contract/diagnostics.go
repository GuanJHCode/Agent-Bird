package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// DiagnosticsPolicy is separate from invocation and execution-budget policy.
// An omitted policy authorizes only existing summary diagnostics. Only an
// explicit version-1 detailed policy may receive a raw-output reservation.
type DiagnosticsPolicy struct {
	Version int    `json:"version"`
	Mode    string `json:"mode"`
}

func (p DiagnosticsPolicy) Validate() error {
	if p.Version != 1 || (p.Mode != "summary" && p.Mode != "detailed") {
		return errors.New("diagnostics_policy_invalid")
	}
	return nil
}

func (p *DiagnosticsPolicy) UnmarshalJSON(raw []byte) error {
	type plain DiagnosticsPolicy
	var value plain
	if err := DecodeBridgeStrict(raw, &value); err != nil {
		return errors.New("diagnostics_policy_invalid")
	}
	next := DiagnosticsPolicy(value)
	if err := next.Validate(); err != nil {
		return err
	}
	*p = next
	return nil
}

func (p DiagnosticsPolicy) CanonicalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

// DiagnosticsGrant binds explicit capture consent to the original durable
// artifact reservation. It never carries a caller-selected filesystem path.
// Validate checks format and policy consistency only. The signed launch and
// Store must separately prove reservation class, scope and durable admission.
type DiagnosticsGrant struct {
	Policy        DiagnosticsPolicy `json:"policy"`
	PolicySHA256  string            `json:"policy_sha256"`
	ReservationID string            `json:"reservation_id"`
}

func (g DiagnosticsGrant) Validate() error {
	body, err := g.Policy.CanonicalJSON()
	if err != nil || g.Policy.Mode != "detailed" || len(g.ReservationID) == 0 || len(g.ReservationID) > 128 {
		return errors.New("diagnostics_grant_invalid")
	}
	for _, r := range g.ReservationID {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return errors.New("diagnostics_grant_invalid")
		}
	}
	digest := sha256.Sum256(body)
	if g.PolicySHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("diagnostics_grant_invalid")
	}
	return nil
}

func (g *DiagnosticsGrant) UnmarshalJSON(raw []byte) error {
	type plain DiagnosticsGrant
	var value plain
	if err := DecodeBridgeStrict(raw, &value); err != nil {
		return errors.New("diagnostics_grant_invalid")
	}
	next := DiagnosticsGrant(value)
	if err := next.Validate(); err != nil {
		return err
	}
	*g = next
	return nil
}
