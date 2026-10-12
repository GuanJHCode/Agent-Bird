package contract

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"
)

const OriginalOwnerEnvelopeLimit = 64 * 1024

// Scope survives transport replacement; incarnation is allocated by Store.
type OriginalOwnerScope struct {
	Version                 int    `json:"version"`
	RunID                   string `json:"run_id"`
	LaunchID                string `json:"launch_id"`
	ProducerID              string `json:"producer_id"`
	OwnerID                 string `json:"owner_id"`
	Incarnation             uint64 `json:"incarnation"`
	OriginalOriginContextID string `json:"original_origin_context_id"`
	OriginalOriginPID       int    `json:"original_origin_pid"`
	OriginalOriginBirth     string `json:"original_origin_birth"`
	HostGeneration          string `json:"host_generation"`
	Executable              string `json:"executable"`
	ExecutableSHA256        string `json:"executable_sha256"`
}

type OriginalOwnerTransport struct {
	PID   int    `json:"pid"`
	Birth string `json:"birth"`
}

type OriginalOwnerIdentity struct {
	PID       int    `json:"pid"`
	Birth     string `json:"birth"`
	PublicKey string `json:"public_key"`
}

type OriginalOwnerIntent struct {
	Scope                 OriginalOwnerScope     `json:"scope"`
	BootstrapID           string                 `json:"bootstrap_id"`
	BootstrapTokenSHA256  string                 `json:"bootstrap_token_sha256"`
	CoordinatorKeyID      string                 `json:"coordinator_key_id"`
	RequestID             string                 `json:"request_id"`
	RequestSHA256         string                 `json:"request_sha256"`
	StartupDeadlineUnixMS int64                  `json:"startup_deadline_unix_ms"`
	FirstHost             OriginalOwnerTransport `json:"first_host"`
	CoordinatorEpoch      uint64                 `json:"coordinator_epoch"`
}

// BootstrapToken is private enrollment material, never a diagnostic field.
type OriginalOwnerHello struct {
	Scope            OriginalOwnerScope     `json:"scope"`
	BootstrapID      string                 `json:"bootstrap_id"`
	BootstrapToken   string                 `json:"bootstrap_token"`
	Owner            OriginalOwnerIdentity  `json:"owner"`
	FirstHost        OriginalOwnerTransport `json:"first_host"`
	CoordinatorKeyID string                 `json:"coordinator_key_id"`
}

type OriginalOwnerBinding struct {
	Scope               OriginalOwnerScope     `json:"scope"`
	Owner               OriginalOwnerIdentity  `json:"owner"`
	CoordinatorKeyID    string                 `json:"coordinator_key_id"`
	FirstHost           OriginalOwnerTransport `json:"first_host"`
	BindingRevision     uint64                 `json:"binding_revision"`
	BindingSHA256       string                 `json:"binding_sha256"`
	LaunchAttemptSHA256 string                 `json:"launch_attempt_sha256,omitempty"`
	Signature           string                 `json:"signature"`
}

type OwnerControlPermit struct {
	Version                 int                     `json:"version"`
	Kind                    string                  `json:"kind"`
	OwnerID                 string                  `json:"owner_id"`
	ScopeSHA256             string                  `json:"scope_sha256"`
	OwnerIdentitySHA256     string                  `json:"owner_identity_sha256"`
	CoordinatorKeyID        string                  `json:"coordinator_key_id"`
	CoordinatorEpoch        uint64                  `json:"coordinator_epoch"`
	ExpectedControlRevision uint64                  `json:"expected_control_revision"`
	ControlRevision         uint64                  `json:"control_revision"`
	OldHost                 *OriginalOwnerTransport `json:"old_host"`
	NewHost                 OriginalOwnerTransport  `json:"new_host"`
	RequestID               string                  `json:"request_id"`
	RequestSHA256           string                  `json:"request_sha256"`
	PermitID                string                  `json:"permit_id"`
	Signature               string                  `json:"signature"`
}

func SignOriginalOwnerBinding(b OriginalOwnerBinding, key ed25519.PrivateKey) (OriginalOwnerBinding, error) {
	b.Signature = ""
	digest := originalBindingDigest(b)
	if b.BindingSHA256 != "" && b.BindingSHA256 != digest {
		return OriginalOwnerBinding{}, ownerInvalid()
	}
	b.BindingSHA256 = digest
	if err := b.Validate(); err != nil {
		return OriginalOwnerBinding{}, err
	}
	if !ownerSigningKey(key, b.CoordinatorKeyID) {
		return OriginalOwnerBinding{}, ownerInvalid()
	}
	raw, _ := json.Marshal(b)
	b.Signature = hex.EncodeToString(ed25519.Sign(key, append([]byte("original-owner-binding-v1\x00"), raw...)))
	return b, nil
}

func VerifyOriginalOwnerBinding(b OriginalOwnerBinding, key ed25519.PublicKey) error {
	if err := b.Validate(); err != nil {
		return err
	}
	sig, ok := ownerHex(b.Signature, ed25519.SignatureSize)
	id, err := OriginalOwnerKeyID(key)
	if !ok || err != nil || id != b.CoordinatorKeyID {
		return ownerInvalid()
	}
	b.Signature = ""
	raw, _ := json.Marshal(b)
	if !ed25519.Verify(key, append([]byte("original-owner-binding-v1\x00"), raw...), sig) {
		return ownerInvalid()
	}
	return nil
}

func SignOwnerControlPermit(p OwnerControlPermit, key ed25519.PrivateKey) (OwnerControlPermit, error) {
	p.Signature = ""
	if err := p.Validate(); err != nil {
		return OwnerControlPermit{}, err
	}
	if !ownerSigningKey(key, p.CoordinatorKeyID) {
		return OwnerControlPermit{}, ownerInvalid()
	}
	raw, _ := json.Marshal(p)
	p.Signature = hex.EncodeToString(ed25519.Sign(key, append([]byte("original-owner-control-v1\x00"), raw...)))
	return p, nil
}

func VerifyOwnerControlPermit(p OwnerControlPermit, key ed25519.PublicKey) error {
	if err := p.Validate(); err != nil {
		return err
	}
	sig, ok := ownerHex(p.Signature, ed25519.SignatureSize)
	id, err := OriginalOwnerKeyID(key)
	if !ok || err != nil || id != p.CoordinatorKeyID {
		return ownerInvalid()
	}
	p.Signature = ""
	raw, _ := json.Marshal(p)
	if !ed25519.Verify(key, append([]byte("original-owner-control-v1\x00"), raw...), sig) {
		return ownerInvalid()
	}
	return nil
}

func ownerInvalid() error { return errors.New("original_owner_invalid") }
func ownerText(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func ownerHex(s string, size int) ([]byte, bool) {
	if len(s) != size*2 {
		return nil, false
	}
	b, err := hex.DecodeString(s)
	return b, err == nil && hex.EncodeToString(b) == s
}
func ownerDigest(s string) bool { _, ok := ownerHex(s, sha256.Size); return ok }
func ownerSigningKey(key ed25519.PrivateKey, id string) bool {
	if len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
		return false
	}
	got, err := OriginalOwnerKeyID(key.Public().(ed25519.PublicKey))
	return err == nil && got == id
}
func ownerJSONHash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func OriginalOwnerKeyID(key ed25519.PublicKey) (string, error) {
	if len(key) != ed25519.PublicKeySize {
		return "", ownerInvalid()
	}
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:]), nil
}
func OriginalOwnerBootstrapSHA256(token string) (string, error) {
	b, ok := ownerHex(token, 32)
	if !ok {
		return "", ownerInvalid()
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func OriginalOwnerScopeSHA256(s OriginalOwnerScope) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	return ownerJSONHash(s), nil
}
func OriginalOwnerIdentitySHA256(i OriginalOwnerIdentity) (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	return ownerJSONHash(i), nil
}
func originalBindingDigest(b OriginalOwnerBinding) string {
	b.Signature, b.BindingSHA256 = "", ""
	return ownerJSONHash(b)
}

func (s OriginalOwnerScope) Validate() error {
	if s.Version != 1 || s.Incarnation == 0 || s.Incarnation > math.MaxInt64 || s.OriginalOriginPID <= 0 || !ownerDigest(s.ExecutableSHA256) || !filepath.IsAbs(s.Executable) || filepath.Clean(s.Executable) != s.Executable || !ownerText(s.Executable, 4096) {
		return ownerInvalid()
	}
	for _, id := range []string{s.RunID, s.LaunchID, s.ProducerID, s.OwnerID, s.OriginalOriginContextID, s.OriginalOriginBirth, s.HostGeneration} {
		if !ownerText(id, 256) {
			return ownerInvalid()
		}
	}
	return nil
}
func (h OriginalOwnerTransport) Validate() error {
	if h.PID <= 0 || !ownerText(h.Birth, 256) {
		return ownerInvalid()
	}
	return nil
}
func (i OriginalOwnerIdentity) Validate() error {
	if _, ok := ownerHex(i.PublicKey, ed25519.PublicKeySize); !ok {
		return ownerInvalid()
	}
	return (OriginalOwnerTransport{PID: i.PID, Birth: i.Birth}).Validate()
}
func (i OriginalOwnerIntent) Validate() error {
	if i.Scope.Validate() != nil || i.FirstHost.Validate() != nil || !ownerText(i.BootstrapID, 256) || !ownerText(i.RequestID, 256) || !ownerDigest(i.BootstrapTokenSHA256) || !ownerDigest(i.CoordinatorKeyID) || !ownerDigest(i.RequestSHA256) || i.StartupDeadlineUnixMS <= 0 || i.CoordinatorEpoch == 0 || i.CoordinatorEpoch > math.MaxInt64 {
		return ownerInvalid()
	}
	return nil
}
func (h OriginalOwnerHello) Validate() error {
	if h.Scope.Validate() != nil || h.Owner.Validate() != nil || h.FirstHost.Validate() != nil || !ownerText(h.BootstrapID, 256) || !ownerDigest(h.CoordinatorKeyID) {
		return ownerInvalid()
	}
	_, err := OriginalOwnerBootstrapSHA256(h.BootstrapToken)
	return err
}
func (b OriginalOwnerBinding) Validate() error {
	if b.Scope.Validate() != nil || b.Owner.Validate() != nil || b.FirstHost.Validate() != nil || !ownerDigest(b.CoordinatorKeyID) || b.BindingRevision != 1 || !ownerDigest(b.BindingSHA256) || (b.LaunchAttemptSHA256 != "" && !ownerDigest(b.LaunchAttemptSHA256)) || b.BindingSHA256 != originalBindingDigest(b) {
		return ownerInvalid()
	}
	if b.Signature != "" {
		if _, ok := ownerHex(b.Signature, ed25519.SignatureSize); !ok {
			return ownerInvalid()
		}
	}
	return nil
}
func (p OwnerControlPermit) Validate() error {
	if p.Version != 1 || p.Kind != "attach" || !ownerText(p.OwnerID, 256) || !ownerText(p.RequestID, 256) || !ownerText(p.PermitID, 256) || !ownerDigest(p.ScopeSHA256) || !ownerDigest(p.OwnerIdentitySHA256) || !ownerDigest(p.CoordinatorKeyID) || !ownerDigest(p.RequestSHA256) || p.CoordinatorEpoch == 0 || p.CoordinatorEpoch > math.MaxInt64 || p.ControlRevision == 0 || p.ControlRevision > math.MaxInt64 || p.ExpectedControlRevision != p.ControlRevision-1 || p.NewHost.Validate() != nil {
		return ownerInvalid()
	}
	if p.ExpectedControlRevision == 0 {
		if p.OldHost != nil {
			return ownerInvalid()
		}
	} else if p.OldHost == nil || p.OldHost.Validate() != nil {
		return ownerInvalid()
	}
	if p.Signature != "" {
		if _, ok := ownerHex(p.Signature, ed25519.SignatureSize); !ok {
			return ownerInvalid()
		}
	}
	return nil
}

// Concrete decoders share exact-field scanning, then require all typed fields.
// The initial permit's explicit null old_host is the sole nullable field.
func decodeOwner(raw []byte, dst any) error {
	if len(raw) == 0 || len(raw) > OriginalOwnerEnvelopeLimit || DecodeBridgeStrict(raw, dst) != nil {
		return ownerInvalid()
	}
	if !ownerRequiredFields(raw, reflect.TypeOf(dst).Elem()) {
		return ownerInvalid()
	}
	return nil
}
func ownerRequiredFields(raw []byte, typ reflect.Type) bool {
	if typ.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return true
		}
		typ = typ.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	if typ.Kind() != reflect.Struct {
		return true
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for n := 0; n < typ.NumField(); n++ {
		f := typ.Field(n)
		tag := f.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if strings.Contains(tag, ",omitempty") {
			continue
		}
		v, ok := fields[name]
		if !ok || !ownerRequiredFields(v, f.Type) {
			return false
		}
	}
	return true
}
func DecodeOriginalOwnerScope(raw []byte) (OriginalOwnerScope, error) {
	var v OriginalOwnerScope
	if err := decodeOwner(raw, &v); err != nil {
		return v, err
	}
	return v, v.Validate()
}
func DecodeOriginalOwnerIntent(raw []byte) (OriginalOwnerIntent, error) {
	var v OriginalOwnerIntent
	if err := decodeOwner(raw, &v); err != nil {
		return v, err
	}
	return v, v.Validate()
}
func DecodeOriginalOwnerHello(raw []byte) (OriginalOwnerHello, error) {
	var v OriginalOwnerHello
	if err := decodeOwner(raw, &v); err != nil {
		return v, err
	}
	return v, v.Validate()
}
func DecodeOriginalOwnerBinding(raw []byte) (OriginalOwnerBinding, error) {
	var v OriginalOwnerBinding
	if err := decodeOwner(raw, &v); err != nil {
		return v, err
	}
	if v.Signature == "" {
		return v, ownerInvalid()
	}
	return v, v.Validate()
}
func DecodeOwnerControlPermit(raw []byte) (OwnerControlPermit, error) {
	var v OwnerControlPermit
	if err := decodeOwner(raw, &v); err != nil {
		return v, err
	}
	if v.Signature == "" {
		return v, ownerInvalid()
	}
	return v, v.Validate()
}
