package contract

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// HostDisposition terminates one authenticated Host identity, independently of
// work grants and provider sessions. The proof binds its reason and epoch to
// the launch capability received and verified through HostHello.
type HostDisposition struct {
	HostID           string `json:"host_id"`
	CoordinatorEpoch uint64 `json:"coordinator_epoch"`
	Reason           string `json:"reason"`
	Proof            string `json:"proof"`
}

func (d HostDisposition) launchProof(hello HostHello) string {
	d.Proof = ""
	body, _ := json.Marshal(d)
	mac := hmac.New(sha256.New, []byte(hello.LaunchToken))
	_, _ = mac.Write([]byte("host-disposition-v1\x00" + HostIdentitySHA256(hello) + "\x00"))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func NewHostDisposition(hello HostHello, epoch uint64, reason string) HostDisposition {
	d := HostDisposition{HostID: hello.LaunchID, CoordinatorEpoch: epoch, Reason: reason}
	d.Proof = d.launchProof(hello)
	return d
}

func (d HostDisposition) AuthenticatedBy(hello HostHello) bool {
	return hello.LaunchToken != "" && d.HostID == hello.LaunchID && d.CoordinatorEpoch > 0 &&
		(d.Reason == "host_retired" || d.Reason == "coordinator_exit") &&
		hmac.Equal([]byte(d.Proof), []byte(d.launchProof(hello)))
}

// HostIdentitySHA256 identifies one registered OS process. It is used only by
// terminal lifecycle fences; recovery attempt budgets use a separate stable
// launch scope that intentionally excludes mutable PID/birth identities.
func HostIdentitySHA256(hello HostHello) string {
	hello.LaunchToken = ""
	hello.ResumeCommandID = ""
	body, _ := json.Marshal(hello)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
