package contract

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func ownerContractBinding() (OriginalOwnerBinding, ed25519.PrivateKey) {
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("k", 32)))
	pub := key.Public().(ed25519.PublicKey)
	id := sha256.Sum256(pub)
	b := OriginalOwnerBinding{Scope: OriginalOwnerScope{Version: 1, RunID: "run", LaunchID: "launch", ProducerID: "producer", OwnerID: "owner", Incarnation: 1, OriginalOriginContextID: "origin", OriginalOriginPID: 7, OriginalOriginBirth: "origin-birth", HostGeneration: "generation", Executable: "/pinned/orchestrator", ExecutableSHA256: strings.Repeat("a", 64)}, Owner: OriginalOwnerIdentity{PID: 202, Birth: "owner-birth", PublicKey: hex.EncodeToString(pub)}, CoordinatorKeyID: hex.EncodeToString(id[:]), FirstHost: OriginalOwnerTransport{PID: 101, Birth: "host-birth"}, BindingRevision: 1}
	return b, key
}

func TestOriginalOwnerSignatureDomainsAndExactFields(t *testing.T) {
	b, key := ownerContractBinding()
	b, err := SignOriginalOwnerBinding(b, key)
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := OriginalOwnerScopeSHA256(b.Scope)
	identity, _ := OriginalOwnerIdentitySHA256(b.Owner)
	p := OwnerControlPermit{Version: 1, Kind: "attach", OwnerID: b.Scope.OwnerID, ScopeSHA256: scope, OwnerIdentitySHA256: identity, CoordinatorKeyID: b.CoordinatorKeyID, CoordinatorEpoch: 1, ControlRevision: 1, NewHost: b.FirstHost, RequestID: "req", RequestSHA256: strings.Repeat("c", 64), PermitID: "permit"}
	p, err = SignOwnerControlPermit(p, key)
	if err != nil {
		t.Fatal(err)
	}
	if VerifyOwnerControlPermit(p, key.Public().(ed25519.PublicKey)) != nil {
		t.Fatal("valid initial permit rejected")
	}
	for _, name := range []string{"scope", "owner", "key", "epoch", "revision", "old", "new", "request", "digest", "permit", "signature", "domain", "record", "seal"} {
		t.Run(name, func(t *testing.T) {
			v := p
			switch name {
			case "scope":
				v.ScopeSHA256 = strings.Repeat("d", 64)
			case "owner":
				v.OwnerIdentitySHA256 = strings.Repeat("d", 64)
			case "key":
				v.CoordinatorKeyID = strings.Repeat("d", 64)
			case "epoch":
				v.CoordinatorEpoch++
			case "revision":
				v.ControlRevision++
			case "old":
				v.OldHost = &v.NewHost
			case "new":
				v.NewHost.PID++
			case "request":
				v.RequestID = "other"
			case "digest":
				v.RequestSHA256 = strings.Repeat("d", 64)
			case "permit":
				v.PermitID = "other"
			case "signature":
				v.Signature = strings.ToUpper(v.Signature)
			default:
				v.Signature = ""
				raw, _ := json.Marshal(v)
				domain := "original-owner-binding-v1"
				if name == "record" {
					domain = "original-owner-record-v1"
				}
				if name == "seal" {
					domain = "original-owner-seal-v1"
				}
				v.Signature = hex.EncodeToString(ed25519.Sign(key, append([]byte(domain+"\x00"), raw...)))
			}
			if VerifyOwnerControlPermit(v, key.Public().(ed25519.PublicKey)) == nil {
				t.Fatal("changed control or foreign signing domain accepted")
			}
		})
	}
	for _, name := range []string{"scope", "owner", "host", "revision", "digest", "signature", "key"} {
		t.Run("binding_"+name, func(t *testing.T) {
			v := b
			switch name {
			case "scope":
				v.Scope.HostGeneration = "changed"
			case "owner":
				v.Owner.PublicKey = strings.Repeat("a", 64)
			case "host":
				v.FirstHost.Birth = "other"
			case "revision":
				v.BindingRevision++
			case "digest":
				v.BindingSHA256 = strings.Repeat("f", 64)
			case "signature":
				v.Signature = v.Signature[:126]
			case "key":
				v.CoordinatorKeyID = strings.Repeat("f", 64)
			}
			if VerifyOriginalOwnerBinding(v, key.Public().(ed25519.PublicKey)) == nil {
				t.Fatal("modified immutable binding accepted")
			}
		})
	}
	bad := append(ed25519.PrivateKey(nil), key...)
	bad[0] ^= 1
	if _, err := SignOriginalOwnerBinding(b, bad); err == nil {
		t.Fatal("malformed private key accepted")
	}
	if VerifyOriginalOwnerBinding(b, ed25519.PublicKey{1}) == nil {
		t.Fatal("invalid verification key accepted")
	}
}

func TestOriginalOwnerStrictDecodeAndEnvelopeBounds(t *testing.T) {
	b, key := ownerContractBinding()
	b, _ = SignOriginalOwnerBinding(b, key)
	raw, _ := json.Marshal(b)
	for _, name := range []string{"duplicate", "nested_duplicate", "unknown", "alias", "nested_unknown", "null_scope", "null_pid", "missing", "trailing", "float", "upper_key", "upper_signature", "oversize", "invalid_utf8"} {
		t.Run(name, func(t *testing.T) {
			v := append([]byte(nil), raw...)
			switch name {
			case "duplicate":
				v = bytes.Replace(v, []byte(`"binding_revision":1`), []byte(`"binding_revision":1,"binding_revision":1`), 1)
			case "nested_duplicate":
				v = bytes.Replace(v, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
			case "unknown":
				v = append([]byte(`{"unknown":1,`), v[1:]...)
			case "alias":
				v = bytes.Replace(v, []byte(`"scope"`), []byte(`"Scope"`), 1)
			case "nested_unknown":
				v = bytes.Replace(v, []byte(`"incarnation":1`), []byte(`"incarnation":1,"dead":true`), 1)
			case "null_scope":
				var m map[string]json.RawMessage
				json.Unmarshal(v, &m)
				m["scope"] = json.RawMessage("null")
				v, _ = json.Marshal(m)
			case "null_pid":
				v = bytes.Replace(v, []byte(`"pid":202`), []byte(`"pid":null`), 1)
			case "missing":
				v = bytes.Replace(v, []byte(`"binding_revision":1,`), nil, 1)
			case "trailing":
				v = append(v, []byte(` {}`)...)
			case "float":
				v = bytes.Replace(v, []byte(`"pid":202`), []byte(`"pid":202.0`), 1)
			case "upper_key":
				v = bytes.Replace(v, []byte(b.Owner.PublicKey), []byte(strings.ToUpper(b.Owner.PublicKey)), 1)
			case "upper_signature":
				v = bytes.Replace(v, []byte(b.Signature), []byte(strings.ToUpper(b.Signature)), 1)
			case "oversize":
				v = append(v, bytes.Repeat([]byte(" "), OriginalOwnerEnvelopeLimit+1-len(v))...)
			case "invalid_utf8":
				v = append(v, 0xff)
			}
			if _, err := DecodeOriginalOwnerBinding(v); err == nil {
				t.Fatal("non-exact binding accepted")
			}
		})
	}
	atLimit := append(append([]byte(nil), raw...), bytes.Repeat([]byte(" "), OriginalOwnerEnvelopeLimit-len(raw))...)
	if _, err := DecodeOriginalOwnerBinding(atLimit); err != nil {
		t.Fatal("exact 64KiB envelope rejected", err)
	}
	b.Scope.OwnerID = strings.Repeat("x", 257)
	if _, err := SignOriginalOwnerBinding(b, key); err == nil {
		t.Fatal("overlong identity accepted")
	}
	if _, err := OriginalOwnerBootstrapSHA256(strings.Repeat("A", 64)); err == nil {
		t.Fatal("noncanonical bootstrap secret accepted")
	}
}

func TestOriginalOwnerBindingAuthenticatesImmutableIdentity(t *testing.T) {
	b, key := ownerContractBinding()
	signed, err := SignOriginalOwnerBinding(b, key)
	if err != nil {
		t.Fatalf("new binding must have a valid coordinator signature: %v", err)
	}
	if err := VerifyOriginalOwnerBinding(signed, key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	if b.Signature != "" || signed.Signature == "" {
		t.Fatal("signing must return a value without mutating caller")
	}
	signed.Owner.PID++
	if VerifyOriginalOwnerBinding(signed, key.Public().(ed25519.PublicKey)) == nil {
		t.Fatal("changed original owner identity accepted")
	}
}
