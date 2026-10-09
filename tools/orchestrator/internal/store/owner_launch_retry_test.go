package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestPreBindRetryAdmissionCommitsSecondAttemptAndMovesHost(t *testing.T) {
	f := newOriginalOwnerFixture(t)
	ctx := context.Background()
	first, err := f.db.PrepareSupervisedOriginalOwner(ctx, f.prepare, "", contract.OriginalOwnerLaunchSupervisor{Epoch: 1, PID: 700, Birth: "coordinator", Nonce: strings.Repeat("a", 64), ExecutableSHA256: f.prepare.ExecutableSHA256}, f.key)
	if err != nil {
		t.Fatal(err)
	}
	newHost := f.prepare.Host
	newHost.PID, newHost.Birth = 303, "replacement-host"
	a, err := f.db.ResolvePreBindRetryAdmission(ctx, newHost, 1, f.prepare.ExecutableSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if a.OwnerID != first.Intent.Scope.OwnerID || a.ExpectedAttemptSHA256 != first.LaunchAttempt.AttemptSHA256 || a.OldFirstHost != first.Intent.FirstHost || a.NewHost.PID != newHost.PID {
		t.Fatalf("bad admission: %#v", a)
	}
	a.Supervisor = contract.OriginalOwnerLaunchSupervisor{Epoch: 1, PID: 701, Birth: "coordinator-retry", Nonce: strings.Repeat("b", 64), ExecutableSHA256: f.prepare.ExecutableSHA256}
	a.Prepare = f.prepare
	a.Prepare.Host = newHost
	a.Prepare.BootstrapID, a.Prepare.RequestID = "retry-bootstrap", "retry-request"
	a.Prepare.BootstrapTokenSHA256, _ = contract.OriginalOwnerBootstrapSHA256(strings.Repeat("cd", 32))
	a.Prepare.RequestSHA256 = strings.Repeat("d", 64)
	a.Prepare.StartupDeadlineUnixMS = time.Now().Add(time.Minute).UnixMilli()
	prepared, err := f.db.CommitPreBindRetryAdmission(ctx, a, first.Intent.FirstHost, f.key)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.LaunchAttempt == nil || prepared.LaunchAttempt.Attempt != 2 || prepared.Intent.FirstHost.PID != newHost.PID {
		t.Fatalf("retry not prepared: %#v", prepared)
	}
	state, err := f.db.InspectOriginalOwnerLaunch(ctx, first.Intent.Scope.OwnerID)
	if err != nil || state.BaseIntent == nil || *state.BaseIntent != first.Intent || state.Intent != prepared.Intent {
		t.Fatalf("immutable base/effective projection: %#v %v", state, err)
	}
	if _, err = f.db.ResolvePreBindRetryAdmission(ctx, newHost, 1, f.prepare.ExecutableSHA256); err == nil {
		t.Fatal("second attempt admitted a third launch")
	}
}
