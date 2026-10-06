package contract

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func TestOriginalOwnerTransportTicketBindsPendingAndExpiry(t *testing.T) {
	b, p, key := ownerWirePermit(t)
	ticket := OwnerTransportTicket{Version: 1, OwnerID: b.Scope.OwnerID, ScopeSHA256: p.ScopeSHA256, OwnerIdentitySHA256: p.OwnerIdentitySHA256, CoordinatorKeyID: p.CoordinatorKeyID, CoordinatorEpoch: p.CoordinatorEpoch, Pending: OwnerTransportPending{Version: 1, RecoverySlot: 2, RequestID: "replacement", ExpectedControlRevision: p.ControlRevision, OldHost: p.NewHost, NewHost: OriginalOwnerTransport{PID: 303, Birth: "child-birth"}}, Nonce: strings.Repeat("a", 64), ExpiresUnixMS: 20_000}
	signed, err := SignOwnerTransportTicket(ticket, key)
	if err != nil {
		t.Fatal("valid ticket rejected", err)
	}
	if VerifyOwnerTransportTicket(signed, key.Public().(ed25519.PublicKey), p.CoordinatorEpoch, 10_000) != nil {
		t.Fatal("valid ticket failed verification")
	}
	mutations := map[string]func(*OwnerTransportTicket){
		"slot":     func(v *OwnerTransportTicket) { v.Pending.RecoverySlot++ },
		"request":  func(v *OwnerTransportTicket) { v.Pending.RequestID += "x" },
		"revision": func(v *OwnerTransportTicket) { v.Pending.ExpectedControlRevision++ },
		"old":      func(v *OwnerTransportTicket) { v.Pending.OldHost.Birth += "x" },
		"new":      func(v *OwnerTransportTicket) { v.Pending.NewHost.PID++ },
		"scope":    func(v *OwnerTransportTicket) { v.ScopeSHA256 = strings.Repeat("d", 64) },
		"identity": func(v *OwnerTransportTicket) { v.OwnerIdentitySHA256 = strings.Repeat("e", 64) },
		"epoch":    func(v *OwnerTransportTicket) { v.CoordinatorEpoch++ },
		"nonce":    func(v *OwnerTransportTicket) { v.Nonce = strings.Repeat("f", 64) },
		"expiry":   func(v *OwnerTransportTicket) { v.ExpiresUnixMS++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			v := signed
			mutate(&v)
			if VerifyOwnerTransportTicket(v, key.Public().(ed25519.PublicKey), p.CoordinatorEpoch, 10_000) == nil {
				t.Fatal("mutation accepted")
			}
		})
	}
	for _, now := range []int64{-1, 20_000, 20_001, 1} {
		if VerifyOwnerTransportTicket(signed, key.Public().(ed25519.PublicKey), p.CoordinatorEpoch, now) == nil {
			t.Fatalf("unbounded/expired ticket accepted at %d", now)
		}
	}
	for _, slot := range []int{0, 6} {
		v := ticket
		v.Pending.RecoverySlot = slot
		if _, err := SignOwnerTransportTicket(v, key); err == nil {
			t.Fatal("invalid recovery slot signed")
		}
	}
	raw, _ := json.Marshal(signed)
	if _, err := DecodeOwnerTransportTicket(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOwnerTransportTicket([]byte(strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1))); err == nil {
		t.Fatal("duplicate field accepted")
	}
	signed.Signature = ""
	raw, _ = json.Marshal(signed)
	if _, err := DecodeOwnerTransportTicket(raw); err == nil {
		t.Fatal("unsigned ticket accepted")
	}
}
