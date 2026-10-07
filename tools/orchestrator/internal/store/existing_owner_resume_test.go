package store

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
)

// Private synthetic Store facts are not a native liveness observation.
func liveOwnerObservation(o OriginalOwnerSuccessionObservation) OriginalOwnerLiveObservation {
	return OriginalOwnerLiveObservation{OwnerID: o.OwnerID, Owner: o.Owner, Origin: o.Origin, Epoch: o.Epoch, ExecutableSHA256: o.ExecutableSHA256, OwnerAliveAt: "2026-10-08T01:00:00Z"}
}

func liveOwnerResumeSnapshot(t *testing.T, db *DB) string {
	return ownerSuccessionResumeSnapshot(t, db) + ownerSuccessionResumeTablesSnapshot(t, db, "existing_owner_resume_commands")
}

func TestExistingOwnerResumeCommitsOnceAndReplaysAfterRestart(t *testing.T) {
	f, request, dead := ownerSuccessionResumeFixture(t)
	ctx := context.Background()
	before := liveOwnerResumeSnapshot(t, f.db)
	if _, found, err := f.db.LookupOriginalOwnerResume(ctx, request); err != nil || found {
		t.Fatal("fresh command lookup", found, err)
	}
	if liveOwnerResumeSnapshot(t, f.db) != before {
		t.Fatal("lookup changed fresh state")
	}
	ownerBefore := ownerSuccessionResumeTablesSnapshot(t, f.db, "execution_owners", "owner_control_changes", "runtime_hosts", "host_launches", "attempts", "segments", "budget_runtime")
	r, err := f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(dead), f.key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != "existing_owner" || r.CommandID != request.CommandID || r.OwnerID != dead.OwnerID || r.AuthorizationID != "" || r.ReadySequence <= 0 || r.ControlRevision != 1 {
		t.Fatal("wrong immutable live receipt", r)
	}
	if ownerSuccessionResumeTablesSnapshot(t, f.db, "execution_owners", "owner_control_changes", "runtime_hosts", "host_launches", "attempts", "segments", "budget_runtime") != ownerBefore {
		t.Fatal("live queue changed signer, transport or execution allowance")
	}
	before = liveOwnerResumeSnapshot(t, f.db)
	if again, err := f.db.ResumeExistingOriginalOwner(ctx, request, OriginalOwnerLiveObservation{}, nil); err != nil || again != r {
		t.Fatal("exact replay re-observed liveness or changed receipt", err)
	}
	if loaded, found, err := f.db.LookupOriginalOwnerResume(ctx, request); err != nil || !found || loaded != r {
		t.Fatal("unified lookup changed live receipt", found, err)
	}
	if liveOwnerResumeSnapshot(t, f.db) != before {
		t.Fatal("exact replay changed queue, quota or history")
	}
	if err = f.db.BeginCoordinatorEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	path := f.db.path
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	before = liveOwnerResumeSnapshot(t, reopened)
	if again, err := reopened.ResumeExistingOriginalOwner(ctx, request, OriginalOwnerLiveObservation{}, nil); err != nil || again != r {
		t.Fatal("restart replay depended on current epoch", err)
	}
	changed := request
	changed.WorkRevision++
	if _, _, err = reopened.LookupOriginalOwnerResume(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed command body did not conflict", err)
	}
	if liveOwnerResumeSnapshot(t, reopened) != before {
		t.Fatal("restarted replay/conflict changed state")
	}
}

func TestExistingOwnerResumeRejectsUnprovedLiveFacts(t *testing.T) {
	for _, kind := range []string{"owner_id", "owner_pid", "owner_birth", "origin_pid", "origin_birth", "origin_context", "epoch", "pin", "alive_missing", "alive_invalid", "key", "request_revision", "request_token", "empty_command"} {
		t.Run(kind, func(t *testing.T) {
			f, request, dead := ownerSuccessionResumeFixture(t)
			o := liveOwnerObservation(dead)
			key := f.key.Public().(ed25519.PublicKey)
			switch kind {
			case "owner_id":
				o.OwnerID = "foreign"
			case "owner_pid":
				o.Owner.PID++
			case "owner_birth":
				o.Owner.Birth = "foreign"
			case "origin_pid":
				o.Origin.OriginPID++
			case "origin_birth":
				o.Origin.OriginBirth = "foreign"
			case "origin_context":
				o.Origin.OriginContextID = "foreign"
			case "epoch":
				o.Epoch++
			case "pin":
				o.ExecutableSHA256 = "foreign"
			case "alive_missing":
				o.OwnerAliveAt = ""
			case "alive_invalid":
				o.OwnerAliveAt = "not-time"
			case "key":
				key = nil
			case "request_revision":
				request.WorkRevision++
			case "request_token":
				request.ControlToken = "foreign"
			case "empty_command":
				request.CommandID = ""
			}
			before := liveOwnerResumeSnapshot(t, f.db)
			if _, err := f.db.ResumeExistingOriginalOwner(context.Background(), request, o, key); err == nil {
				t.Fatal("unproved current owner granted a live receipt")
			}
			if liveOwnerResumeSnapshot(t, f.db) != before {
				t.Fatal("refused live command changed persistent state")
			}
		})
	}
}

func TestExistingOwnerResumeAndSuccessionShareCommandIdentity(t *testing.T) {
	for _, first := range []string{"live", "successor"} {
		t.Run(first, func(t *testing.T) {
			f, request, dead := ownerSuccessionResumeFixture(t)
			ctx, key := context.Background(), f.key.Public().(ed25519.PublicKey)
			mode := "existing_owner"
			if first == "live" {
				if _, err := f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(dead), key); err != nil {
					t.Fatal(err)
				}
			} else {
				mode = "successor"
				if _, err := f.db.ResumeOriginalOwner(ctx, request, dead, key); err != nil {
					t.Fatal(err)
				}
			}
			before := liveOwnerResumeSnapshot(t, f.db)
			var err error
			if first == "live" {
				_, err = f.db.ResumeOriginalOwner(ctx, request, dead, key)
			} else {
				_, err = f.db.ResumeExistingOriginalOwner(ctx, request, liveOwnerObservation(dead), key)
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatal("same command was retargeted to a new owner mode", err)
			}
			r, found, err := f.db.LookupOriginalOwnerResume(ctx, request)
			if err != nil || !found || r.Mode != mode || r.CommandID != request.CommandID {
				t.Fatal("unified replay lost original mode", r, err)
			}
			if mode == "successor" && r.AuthorizationID == "" {
				t.Fatal("successor receipt lost original authorization")
			}
			if before != liveOwnerResumeSnapshot(t, f.db) {
				t.Fatal("cross-mode replay changed queue or owner")
			}
		})
	}
}
