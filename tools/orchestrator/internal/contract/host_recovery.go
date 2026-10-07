package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// HostRecoveryScopeSHA256 preserves the existing v1 on-disk scope encoding.
// PID/birth are audited separately; an authenticated owner generation change
// moves only this scope, never the immutable launch's restart counters.
func HostRecoveryScopeSHA256(hello HostHello, executableSHA256 string) string {
	body, _ := json.Marshal(struct {
		LaunchID, OriginContextID, HostGeneration, Executable, ExecutableSHA256 string
	}{hello.LaunchID, hello.OriginContextID, hello.HostGeneration, hello.Executable, executableSHA256})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type HostRecoveryScopeRequest struct {
	Hello            HostHello `json:"hello"`
	PriorScopeSHA256 string    `json:"prior_scope_sha256"`
}

type HostRecoveryScopeAck struct {
	Status           string `json:"status"`
	LaunchID         string `json:"launch_id"`
	PriorScopeSHA256 string `json:"prior_scope_sha256"`
	ScopeSHA256      string `json:"scope_sha256"`
}

type HostPresenceResponse struct {
	Status             string `json:"status"`
	Presence           string `json:"presence"`
	ExecutionOwnerMode string `json:"execution_owner_mode"`
}
