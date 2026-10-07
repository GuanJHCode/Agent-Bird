package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestHostResumeSelectorDoesNotChangePhysicalIdentityOrScope(t *testing.T) {
	hello := HostHello{LaunchID: "launch", LaunchToken: "token", OriginContextID: "origin", OriginPID: 11, OriginBirth: "origin-birth", HostGeneration: "generation", PID: 22, Birth: "host-birth", Executable: "/orchestrator"}
	// Freeze the v1 representation, including the deliberately empty token.
	wire := `{"launch_id":"launch","launch_token":"","origin_context_id":"origin","origin_pid":11,"origin_birth":"origin-birth","host_generation":"generation","pid":22,"birth":"host-birth","executable":"/orchestrator"}`
	digest := sha256.Sum256([]byte(wire))
	want := hex.EncodeToString(digest[:])
	if got := HostIdentitySHA256(hello); got != want {
		t.Fatal("legacy physical identity changed", got, want)
	}
	disposition := NewHostDisposition(hello, 7, "host_retired")
	scope := HostRecoveryScopeSHA256(hello, "digest")
	hello.ResumeCommandID = "resume-generation-selector"
	if got := HostIdentitySHA256(hello); got != want || !disposition.AuthenticatedBy(hello) || HostRecoveryScopeSHA256(hello, "digest") != scope {
		t.Fatal("resume selector changed physical identity or recovery budget scope")
	}
	hello.Birth = "another-process"
	if HostIdentitySHA256(hello) == want || disposition.AuthenticatedBy(hello) {
		t.Fatal("physical birth is no longer bound")
	}
}
