package contract

import (
	"crypto/sha256"
	"encoding/hex"
)

// OriginalOwnerRecoveryScopeSHA256 preserves the v2 local recovery-ledger
// domain. Physical Host replacement and Coordinator restart do not change it.
func OriginalOwnerRecoveryScopeSHA256(scope OriginalOwnerScope, owner OriginalOwnerIdentity) (string, error) {
	s, err := OriginalOwnerScopeSHA256(scope)
	if err != nil {
		return "", err
	}
	i, err := OriginalOwnerIdentitySHA256(owner)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte("original-owner-recovery-v2\x00" + s + "\x00" + i))
	return hex.EncodeToString(sum[:]), nil
}
