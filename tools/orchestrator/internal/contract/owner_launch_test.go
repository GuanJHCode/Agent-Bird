package contract

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestOriginalOwnerLaunchAttemptAndReceiptBindEffectiveIntent(t *testing.T) {
	binding, key := ownerContractBinding()
	base := OriginalOwnerIntent{Scope: binding.Scope, BootstrapID: "bootstrap", BootstrapTokenSHA256: strings.Repeat("a", 64), CoordinatorKeyID: binding.CoordinatorKeyID, RequestID: "request", RequestSHA256: strings.Repeat("b", 64), StartupDeadlineUnixMS: 1000, FirstHost: binding.FirstHost, CoordinatorEpoch: 7}
	base.Scope.ProducerID = base.Scope.LaunchID
	effective := base
	effective.FirstHost = OriginalOwnerTransport{PID: 303, Birth: "replacement-host"}
	effective.BootstrapID = "retry-bootstrap"
	effective.BootstrapTokenSHA256 = strings.Repeat("c", 64)
	effective.RequestID = "retry-request"
	effective.RequestSHA256 = strings.Repeat("d", 64)
	effective.StartupDeadlineUnixMS++
	effective.CoordinatorEpoch++
	supervisor := OriginalOwnerLaunchSupervisor{Epoch: 8, PID: 404, Birth: "coordinator-birth", Nonce: strings.Repeat("e", 64), ExecutableSHA256: effective.Scope.ExecutableSHA256}
	attempt := OriginalOwnerLaunchAttempt{Version: 2, Attempt: 2, BaseIntent: base, EffectiveIntent: effective, PriorReceiptSHA256: strings.Repeat("1", 64), Supervisor: supervisor}
	attempt, err := SignOriginalOwnerLaunchAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOriginalOwnerLaunchAttempt(attempt); err != nil {
		t.Fatal(err)
	}
	receipt, err := SignOriginalOwnerLaunchReceipt(OriginalOwnerLaunchReceipt{Version: 2, AttemptSHA256: attempt.AttemptSHA256, Phase: OriginalOwnerLaunchPreparedNotStarted, Supervisor: supervisor}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyOriginalOwnerLaunchReceipt(receipt, key.Public().(ed25519.PublicKey), attempt); err != nil {
		t.Fatal(err)
	}
	receipt.AttemptSHA256 = strings.Repeat("0", 64)
	if VerifyOriginalOwnerLaunchReceipt(receipt, key.Public().(ed25519.PublicKey), attempt) == nil {
		t.Fatal("receipt accepted a different attempt")
	}
}

func TestOwnerRuntimeBootstrapV1BytesAndV2ReceiptBoundary(t *testing.T) {
	v1, _ := ownerBootstrapFixture()
	if v1.Environment != nil || v1.LaunchReceipt != nil || v1.Prepared.LaunchAttempt != nil {
		t.Fatal("v1 fixture acquired v2 fields")
	}
	if err := v1.Validate(); err != nil {
		t.Fatal(err)
	}
}
