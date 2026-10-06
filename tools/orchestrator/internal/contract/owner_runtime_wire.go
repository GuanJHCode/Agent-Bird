package contract

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
)

// OwnerCommand is a strict discriminated union. The entire launch (including
// all dependency captures) is signed together with its exact control permit.
// A transport forwards this object without acquiring business authority.
type OwnerCommand struct {
	Version    int                `json:"version"`
	Permit     OwnerControlPermit `json:"permit"`
	Kind       string             `json:"kind"`
	RequestID  string             `json:"request_id"`
	BodySHA256 string             `json:"body_sha256"`
	Body       json.RawMessage    `json:"body"`
	Signature  string             `json:"signature"`
}
type OwnerTerminal struct {
	Reason string `json:"reason"`
}

func ownerRawSHA(raw []byte) string { v := sha256.Sum256(raw); return hex.EncodeToString(v[:]) }
func ownerWireSize(v any) bool {
	raw, err := json.Marshal(v)
	return err == nil && len(raw) <= OriginalOwnerEnvelopeLimit-256
}
func ownerPrivateKeyValid(key ed25519.PrivateKey) bool {
	return len(key) == ed25519.PrivateKeySize && bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key)
}
func ownerSignWire(domain string, v any, key ed25519.PrivateKey) (string, error) {
	if !ownerPrivateKeyValid(key) || !ownerWireSize(v) {
		return "", ownerInvalid()
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", ownerInvalid()
	}
	return hex.EncodeToString(ed25519.Sign(key, append([]byte(domain+"\x00"), raw...))), nil
}
func ownerVerifyWire(domain string, v any, signature string, key ed25519.PublicKey) error {
	sig, ok := ownerHex(signature, ed25519.SignatureSize)
	if !ok || len(key) != ed25519.PublicKeySize || !ownerWireSize(v) {
		return ownerInvalid()
	}
	raw, err := json.Marshal(v)
	if err != nil || !ed25519.Verify(key, append([]byte(domain+"\x00"), raw...), sig) {
		return ownerInvalid()
	}
	return nil
}
func ownerBodyValid(body json.RawMessage, hash string) bool {
	return ownerOpaqueBodyValid(body) && ownerDigest(hash) && ownerRawSHA(body) == hash
}

func (c OwnerCommand) Validate() error {
	if c.Version != 1 || c.Permit.Validate() != nil || c.Permit.Signature == "" || !ownerText(c.RequestID, 256) || !ownerBodyValid(c.Body, c.BodySHA256) || !ownerWireSize(c) {
		return ownerInvalid()
	}
	switch c.Kind {
	case "launch":
		var body LaunchCommand
		if DecodeOwnerRuntimeStrict(c.Body, &body) != nil || body.CommandID != c.RequestID || body.ExecutionEpoch != c.Permit.CoordinatorEpoch || body.HostID == "" || body.RunID == "" || body.TaskID == "" || body.AttemptID == "" || body.SegmentID == "" || body.WorkRevision < 1 || body.GrantedActiveMS < 1 || ValidateDependencyInputs(body) != nil {
			return ownerInvalid()
		}
	case "stop":
		var body StopCommand
		if decodeOwner(c.Body, &body) != nil || body.CommandID != c.RequestID || body.SegmentID == "" || body.Reason == "" || body.DeadlineUnixMS <= 0 {
			return ownerInvalid()
		}
	case "terminal":
		var body OwnerTerminal
		if decodeOwner(c.Body, &body) != nil || (body.Reason != "host_retired" && body.Reason != "coordinator_exit") {
			return ownerInvalid()
		}
	default:
		return ownerInvalid()
	}
	return nil
}
func SignOwnerCommand(c OwnerCommand, key ed25519.PrivateKey) (OwnerCommand, error) {
	body, err := ownerCanonicalBody(c.Body)
	if err != nil {
		return c, err
	}
	c.Body = body
	hash := ownerRawSHA(c.Body)
	if c.BodySHA256 != "" && c.BodySHA256 != hash {
		return OwnerCommand{}, ownerInvalid()
	}
	c.BodySHA256 = hash
	c.Signature = ""
	if c.Validate() != nil || !ownerSigningKey(key, c.Permit.CoordinatorKeyID) || VerifyOwnerControlPermit(c.Permit, key.Public().(ed25519.PublicKey)) != nil {
		return OwnerCommand{}, ownerInvalid()
	}
	sig, err := ownerSignWire("original-owner-command-v1", c, key)
	c.Signature = sig
	if err == nil && !ownerWireSize(c) {
		err = ownerInvalid()
	}
	return c, err
}
func VerifyOwnerCommand(c OwnerCommand, key ed25519.PublicKey, expected OwnerControlPermit) error {
	if c.Validate() != nil || ownerJSONHash(c.Permit) != ownerJSONHash(expected) || VerifyOwnerControlPermit(c.Permit, key) != nil {
		return ownerInvalid()
	}
	sig := c.Signature
	c.Signature = ""
	return ownerVerifyWire("original-owner-command-v1", c, sig, key)
}
func DecodeOwnerCommand(raw []byte) (OwnerCommand, error) {
	var c OwnerCommand
	if err := decodeOwnerRuntime(raw, &c); err != nil {
		return c, err
	}
	if _, ok := ownerHex(c.Signature, ed25519.SignatureSize); !ok {
		return c, ownerInvalid()
	}
	return c, c.Validate()
}

// Each owner RPC has its own coordinator nonce and actual kernel peer. A reply
// authenticates the hash of the complete signed request, not just RequestID.
type OwnerChallenge struct {
	Version          int    `json:"version"`
	OwnerID          string `json:"owner_id"`
	CoordinatorEpoch uint64 `json:"coordinator_epoch"`
	CoordinatorKeyID string `json:"coordinator_key_id"`
	Nonce            string `json:"nonce"`
	Signature        string `json:"signature"`
}

func (c OwnerChallenge) Validate() error {
	if c.Version != 1 || !ownerText(c.OwnerID, 256) || c.CoordinatorEpoch == 0 || c.CoordinatorEpoch > math.MaxInt64 || !ownerDigest(c.CoordinatorKeyID) || !ownerDigest(c.Nonce) {
		return ownerInvalid()
	}
	return nil
}
func SignOwnerChallenge(c OwnerChallenge, key ed25519.PrivateKey) (OwnerChallenge, error) {
	c.Signature = ""
	if c.Validate() != nil || !ownerSigningKey(key, c.CoordinatorKeyID) {
		return c, ownerInvalid()
	}
	sig, err := ownerSignWire("original-owner-challenge-v1", c, key)
	c.Signature = sig
	return c, err
}
func VerifyOwnerChallenge(c OwnerChallenge, key ed25519.PublicKey, ownerID string) error {
	id, err := OriginalOwnerKeyID(key)
	if err != nil || c.Validate() != nil || id != c.CoordinatorKeyID || ownerID != c.OwnerID {
		return ownerInvalid()
	}
	sig := c.Signature
	c.Signature = ""
	return ownerVerifyWire("original-owner-challenge-v1", c, sig, key)
}
func DecodeOwnerChallenge(raw []byte) (OwnerChallenge, error) {
	var c OwnerChallenge
	if err := decodeOwnerRuntime(raw, &c); err != nil {
		return c, err
	}
	if _, ok := ownerHex(c.Signature, ed25519.SignatureSize); !ok {
		return c, ownerInvalid()
	}
	return c, c.Validate()
}

type OwnerRPCRequest struct {
	Version             int             `json:"version"`
	OwnerID             string          `json:"owner_id"`
	ScopeSHA256         string          `json:"scope_sha256"`
	OwnerIdentitySHA256 string          `json:"owner_identity_sha256"`
	CoordinatorEpoch    uint64          `json:"coordinator_epoch"`
	ControlRevision     uint64          `json:"control_revision"`
	RequestID           string          `json:"request_id"`
	Kind                string          `json:"kind"`
	Nonce               string          `json:"nonce"`
	BodySHA256          string          `json:"body_sha256"`
	Body                json.RawMessage `json:"body"`
	Signature           string          `json:"signature"`
}

func (r OwnerRPCRequest) Validate() error {
	if r.Version != 1 || !ownerText(r.OwnerID, 256) || !ownerDigest(r.ScopeSHA256) || !ownerDigest(r.OwnerIdentitySHA256) || r.CoordinatorEpoch == 0 || r.CoordinatorEpoch > math.MaxInt64 || r.ControlRevision > math.MaxInt64 || !ownerText(r.RequestID, 256) || !ownerDigest(r.Nonce) || !ownerBodyValid(r.Body, r.BodySHA256) || !ownerWireSize(r) {
		return ownerInvalid()
	}
	switch r.Kind {
	case "bind":
		if r.ControlRevision != 0 {
			return ownerInvalid()
		}
	case "attach", "applied", "activate", "event", "report_capability", "progress", "ticket", "state":
		if r.ControlRevision == 0 {
			return ownerInvalid()
		}
	default:
		return ownerInvalid()
	}
	return nil
}
func SignOwnerRPCRequest(r OwnerRPCRequest, key ed25519.PrivateKey) (OwnerRPCRequest, error) {
	body, err := ownerCanonicalBody(r.Body)
	if err != nil {
		return r, err
	}
	r.Body = body
	hash := ownerRawSHA(r.Body)
	if r.BodySHA256 != "" && r.BodySHA256 != hash {
		return r, ownerInvalid()
	}
	r.BodySHA256 = hash
	r.Signature = ""
	if r.Validate() != nil {
		return r, ownerInvalid()
	}
	sig, err := ownerSignWire("original-owner-rpc-request-v1", r, key)
	r.Signature = sig
	if err == nil && !ownerWireSize(r) {
		err = ownerInvalid()
	}
	return r, err
}
func VerifyOwnerRPCRequest(r OwnerRPCRequest, key ed25519.PublicKey, c OwnerChallenge) error {
	if r.Validate() != nil || c.Validate() != nil || r.Nonce != c.Nonce || r.OwnerID != c.OwnerID || r.CoordinatorEpoch != c.CoordinatorEpoch {
		return ownerInvalid()
	}
	sig := r.Signature
	r.Signature = ""
	return ownerVerifyWire("original-owner-rpc-request-v1", r, sig, key)
}
func DecodeOwnerRPCRequest(raw []byte) (OwnerRPCRequest, error) {
	var r OwnerRPCRequest
	if err := decodeOwnerRuntime(raw, &r); err != nil {
		return r, err
	}
	if _, ok := ownerHex(r.Signature, ed25519.SignatureSize); !ok {
		return r, ownerInvalid()
	}
	return r, r.Validate()
}
func OwnerRPCRequestSHA256(r OwnerRPCRequest) string { return ownerJSONHash(r) }

type OwnerRPCReply struct {
	Version          int             `json:"version"`
	OwnerID          string          `json:"owner_id"`
	CoordinatorEpoch uint64          `json:"coordinator_epoch"`
	RequestID        string          `json:"request_id"`
	RequestSHA256    string          `json:"request_sha256"`
	Body             json.RawMessage `json:"body"`
	Error            string          `json:"error"`
	Signature        string          `json:"signature"`
}

func (r OwnerRPCReply) Validate() error {
	if r.Version != 1 || !ownerText(r.OwnerID, 256) || r.CoordinatorEpoch == 0 || r.CoordinatorEpoch > math.MaxInt64 || !ownerText(r.RequestID, 256) || !ownerDigest(r.RequestSHA256) || !ownerOpaqueBodyValid(r.Body) || (r.Error != "" && !ownerText(r.Error, 256)) || !ownerWireSize(r) {
		return ownerInvalid()
	}
	return nil
}
func SignOwnerRPCReply(r OwnerRPCReply, key ed25519.PrivateKey) (OwnerRPCReply, error) {
	r.Signature = ""
	if r.Validate() != nil {
		return r, ownerInvalid()
	}
	sig, err := ownerSignWire("original-owner-rpc-reply-v1", r, key)
	r.Signature = sig
	if err == nil && !ownerWireSize(r) {
		err = ownerInvalid()
	}
	return r, err
}
func VerifyOwnerRPCReply(r OwnerRPCReply, key ed25519.PublicKey, request OwnerRPCRequest) error {
	if r.Validate() != nil || r.OwnerID != request.OwnerID || r.CoordinatorEpoch != request.CoordinatorEpoch || r.RequestID != request.RequestID || r.RequestSHA256 != OwnerRPCRequestSHA256(request) {
		return ownerInvalid()
	}
	sig := r.Signature
	r.Signature = ""
	return ownerVerifyWire("original-owner-rpc-reply-v1", r, sig, key)
}
func DecodeOwnerRPCReply(raw []byte) (OwnerRPCReply, error) {
	var r OwnerRPCReply
	if err := decodeOwnerRuntime(raw, &r); err != nil {
		return r, err
	}
	if _, ok := ownerHex(r.Signature, ed25519.SignatureSize); !ok {
		return r, ownerInvalid()
	}
	return r, r.Validate()
}
