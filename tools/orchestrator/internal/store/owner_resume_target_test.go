package store

import (
	"context"
	"testing"
)

func TestOwnerResumeTargetAuthenticatesAndDoesNotAdmit(t *testing.T) {
	f, request, dead := ownerSuccessionResumeFixture(t)
	before := liveOwnerResumeSnapshot(t, f.db)
	r, err := f.db.InspectOriginalOwnerResumeTarget(context.Background(), request.TaskID, request.ControllerThread, request.ControlToken)
	if err != nil || r.Mode != "supervised-v1" || r.LaunchID != f.prepare.Host.LaunchID || r.RunID != "owner-run" || r.Origin != dead.Origin || r.Owner == nil || r.Owner.Intent.Scope.OwnerID != dead.OwnerID {
		t.Fatal("wrong authenticated target", r, err)
	}
	if _, err = f.db.InspectOriginalOwnerResumeTarget(context.Background(), request.TaskID, request.ControllerThread, "wrong-token"); err != CodeError("owner_mismatch") {
		t.Fatal("untrusted target inspection", err)
	}
	if before != liveOwnerResumeSnapshot(t, f.db) {
		t.Fatal("target inspection wrote state")
	}
}
