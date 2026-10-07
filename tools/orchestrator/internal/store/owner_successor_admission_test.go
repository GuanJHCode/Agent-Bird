package store

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestSuccessorAdmissionDerivesAuthorizationAndReplaysIntended(t *testing.T) {
	f, spec, _ := ownerSuccessionPrepareFixture(t, false)
	ctx := context.Background()
	a, err := loadOwnerSuccessionCommandForTest(f.db, spec.AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	spec.Host.ResumeCommandID = a.CommandID
	before := ownerSuccessionResumeSnapshot(t, f.db)
	p, err := f.db.ResolveSuccessorOwnerAdmission(ctx, spec.Host, spec.Epoch, spec.ExecutableSHA256)
	if err != nil || p.AuthorizationID != spec.AuthorizationID || p.CommandID != a.CommandID || p.OwnerPhase != "succession_pending" || p.Scope.Incarnation != 2 {
		t.Fatal("private admission missing", p, err)
	}
	if before != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("admission published state")
	}
	for _, command := range []string{"", "different-command", " leading", "bad\x00id"} {
		h := spec.Host
		h.ResumeCommandID = command
		if _, err := f.db.ResolveSuccessorOwnerAdmission(ctx, h, spec.Epoch, spec.ExecutableSHA256); err == nil {
			t.Fatal("foreign selector admitted", command)
		}
	}
	presence, err := f.db.InspectHostPresence(ctx, spec.Host.LaunchID, spec.Host.LaunchToken)
	if err != nil || presence.OwnerPhase != "succession_pending" || presence.SuccessorCommandID != a.CommandID {
		t.Fatal("pending projection", presence, err)
	}
	intent, err := f.db.PrepareSuccessorOwner(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	p, err = f.db.ResolveSuccessorOwnerAdmission(ctx, spec.Host, spec.Epoch, spec.ExecutableSHA256)
	if err != nil || p.OwnerPhase != "intended" || p.Scope != intent.Scope {
		t.Fatal("lost Prepare ACK cannot reconnect", p, err)
	}
	foreign := spec.Host
	foreign.PID++
	if _, err := f.db.ResolveSuccessorOwnerAdmission(ctx, foreign, spec.Epoch, spec.ExecutableSHA256); err == nil {
		t.Fatal("another FirstHost took intended owner")
	}
	bind := successorOwnerBindSpec(f, spec, intent)
	if _, err = f.db.BindOriginalOwner(ctx, bind, f.key); err != nil {
		t.Fatal(err)
	}
	p, err = f.db.ResolveSuccessorOwnerAdmission(ctx, spec.Host, spec.Epoch, spec.ExecutableSHA256)
	if err != nil || p.OwnerPhase != "bound" {
		t.Fatal("bound physical reconnect classification", p, err)
	}
	presence, err = f.db.InspectHostPresence(ctx, spec.Host.LaunchID, spec.Host.LaunchToken)
	if err != nil || presence.OwnerPhase != "bound" || presence.SuccessorCommandID != a.CommandID {
		t.Fatal("bound projection", presence, err)
	}
}

// Keep this fixture read in a transaction; it does not invent an authorization.
func loadOwnerSuccessionCommandForTest(db *DB, id string) (OriginalOwnerSuccessionAuthorization, error) {
	tx, err := db.sql.Begin()
	if err != nil {
		return OriginalOwnerSuccessionAuthorization{}, err
	}
	defer tx.Rollback()
	return loadOwnerSuccessionAuthorization(context.Background(), tx, id)
}

func TestSuccessorAdmissionCannotTurnInitialLiveOwnerIntoSuccessor(t *testing.T) {
	f, _, _ := ownerSuccessionResumeFixture(t)
	h := f.prepare.Host
	h.ResumeCommandID = "not-authorized"
	if _, err := f.db.ResolveSuccessorOwnerAdmission(context.Background(), h, f.prepare.Epoch, f.prepare.ExecutableSHA256); err == nil {
		t.Fatal("initial owner accepted selector")
	}
	p, err := f.db.InspectHostPresence(context.Background(), h.LaunchID, h.LaunchToken)
	if err != nil || p.OwnerPhase != "bound" || p.SuccessorCommandID != "" {
		t.Fatal("initial projection", p, err)
	}
	if _, err := f.db.InspectHostPresence(context.Background(), h.LaunchID, "foreign"); err == nil {
		t.Fatal("projection leaked without capability")
	}
}

func TestSuccessorAdmissionRejectsScopeAndSelectorDriftWithoutWrites(t *testing.T) {
	f, spec, _ := ownerSuccessionPrepareFixture(t, false)
	a, err := loadOwnerSuccessionCommandForTest(f.db, spec.AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	spec.Host.ResumeCommandID = a.CommandID
	before := ownerSuccessionResumeSnapshot(t, f.db)
	for _, name := range []string{"token", "launch", "origin", "generation", "executable", "prior_host", "digest", "epoch", "long_selector", "invalid_utf8"} {
		t.Run(name, func(t *testing.T) {
			h, epoch, digest := spec.Host, spec.Epoch, spec.ExecutableSHA256
			switch name {
			case "token":
				h.LaunchToken = "wrong"
			case "launch":
				h.LaunchID = "other"
			case "origin":
				h.OriginBirth = "other"
			case "generation":
				h.HostGeneration = "other"
			case "executable":
				h.Executable = "/other"
			case "prior_host":
				h.PID, h.Birth = a.Host.PID, a.Host.Birth
			case "digest":
				digest = strings.Repeat("0", 64)
			case "epoch":
				epoch++
			case "long_selector":
				h.ResumeCommandID = strings.Repeat("x", 129)
			case "invalid_utf8":
				h.ResumeCommandID = string([]byte{0xff})
			}
			if _, err := f.db.ResolveSuccessorOwnerAdmission(context.Background(), h, epoch, digest); err == nil {
				t.Fatal("drift admitted")
			}
			if before != ownerSuccessionResumeSnapshot(t, f.db) {
				t.Fatal("rejected admission changed state")
			}
		})
	}
}

func TestSuccessorAdmissionOldCommandCannotSelectLaterAuthorization(t *testing.T) {
	f, spec, _ := ownerSuccessionPrepareFixture(t, false)
	ctx, pub := context.Background(), f.key.Public().(ed25519.PublicKey)
	a, err := loadOwnerSuccessionCommandForTest(f.db, spec.AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	spec.Host.ResumeCommandID = a.CommandID
	i, err := f.db.PrepareSuccessorOwner(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	bs := successorOwnerBindSpec(f, spec, i)
	b, err := f.db.BindOriginalOwner(ctx, bs, f.key)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.CommitOriginalOwnerApplied(ctx, successorOwnerApplied(t, bs, b, 0), pub); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE tasks SET status='interrupted' WHERE id='owner-run-task'`); err != nil {
		t.Fatal(err)
	}
	r := OriginalOwnerResumeRequest{TaskID: "owner-run-task", ControllerThread: "controller", ControlToken: "resume-test-control-token", WorkRevision: 1, CommandID: "later-resume-command"}
	_, err = f.db.ResumeOriginalOwner(ctx, r, OriginalOwnerSuccessionObservation{OwnerID: i.Scope.OwnerID, Owner: contract.OriginalOwnerTransport{PID: b.Binding.Owner.PID, Birth: b.Binding.Owner.Birth}, Host: i.FirstHost, Origin: RunOwnerBinding{OriginContextID: i.Scope.OriginalOriginContextID, OriginPID: i.Scope.OriginalOriginPID, OriginBirth: i.Scope.OriginalOriginBirth}, Epoch: spec.Epoch, ExecutableSHA256: spec.ExecutableSHA256, OwnerExitedAt: "2026-10-08T00:00:00Z", HostExitedAt: "2026-10-08T00:00:00Z"}, pub)
	if err != nil {
		t.Fatal(err)
	}
	h := spec.Host
	h.PID, h.Birth = 505, "third-host"
	before := ownerSuccessionResumeSnapshot(t, f.db)
	if _, err = f.db.ResolveSuccessorOwnerAdmission(ctx, h, spec.Epoch, spec.ExecutableSHA256); err != ErrConflict {
		t.Fatal("stale query selected newer authorization", err)
	}
	if before != ownerSuccessionResumeSnapshot(t, f.db) {
		t.Fatal("stale selector changed state")
	}
	h.ResumeCommandID = r.CommandID
	p, err := f.db.ResolveSuccessorOwnerAdmission(ctx, h, spec.Epoch, spec.ExecutableSHA256)
	if err != nil || p.Scope.Incarnation != 3 || p.CommandID != r.CommandID {
		t.Fatal("fresh third-generation selection", p, err)
	}
}
