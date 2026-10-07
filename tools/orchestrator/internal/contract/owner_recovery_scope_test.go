package contract

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestOriginalOwnerRecoveryScopePreservesV2Domain(t *testing.T) {
	b, _ := ownerContractBinding()
	s, _ := OriginalOwnerScopeSHA256(b.Scope)
	i, _ := OriginalOwnerIdentitySHA256(b.Owner)
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("original-owner-recovery-v2\x00"+s+"\x00"+i)))
	got, err := OriginalOwnerRecoveryScopeSHA256(b.Scope, b.Owner)
	if err != nil || got != want {
		t.Fatalf("old v2 hash changed: %q %v", got, err)
	}
	b.Owner.Birth += "-other"
	changed, err := OriginalOwnerRecoveryScopeSHA256(b.Scope, b.Owner)
	if err != nil || changed == got {
		t.Fatal("physical owner was not bound", err)
	}
	b.Owner.PID = 0
	if _, err = OriginalOwnerRecoveryScopeSHA256(b.Scope, b.Owner); err == nil {
		t.Fatal("invalid identity accepted")
	}
	b, _ = ownerContractBinding()
	b.Scope.Incarnation = 0
	if _, err = OriginalOwnerRecoveryScopeSHA256(b.Scope, b.Owner); err == nil {
		t.Fatal("invalid scope accepted")
	}
}
