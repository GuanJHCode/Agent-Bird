package store

import (
	"crypto/sha256"
	"encoding/hex"
)

func runtimeHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
