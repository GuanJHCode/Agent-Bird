package contract

import "testing"

func TestHostDispositionAuthenticatesIdentityReasonAndEpoch(t *testing.T) {
	hello := HostHello{LaunchID: "launch", LaunchToken: "secret"}
	value := NewHostDisposition(hello, 7, "host_retired")
	if !value.AuthenticatedBy(hello) {
		t.Fatal("valid disposition rejected")
	}
	for _, kind := range []string{"token", "host", "epoch", "reason", "empty-token", "process"} {
		t.Run(kind, func(t *testing.T) {
			changed, owner := value, hello
			switch kind {
			case "token":
				owner.LaunchToken = "other"
			case "host":
				changed.HostID = "other"
			case "epoch":
				changed.CoordinatorEpoch = 8
			case "reason":
				changed.Reason = "other"
			case "empty-token":
				owner.LaunchToken = ""
			case "process":
				owner.PID, owner.Birth = 20, "new-birth"
			}
			if changed.AuthenticatedBy(owner) {
				t.Fatalf("tampered %s disposition accepted", kind)
			}
		})
	}
}
