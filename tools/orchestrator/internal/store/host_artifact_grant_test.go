package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestHostArtifactClaimReservesSegmentBeforeDispatch(t *testing.T) {
	db, host := diagnosticsLaunchFixture(t, "omitted", 1)
	g, err := db.ClaimReady(context.Background(), host.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(g)
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire["host_artifacts"]) == 0 || string(wire["host_artifacts"]) == "null" {
		t.Fatal("new dispatch has no durable Host artifact grant")
	}
	for class, size := range map[string]int64{"terminal": 1 << 20, "progress": 21 * 64 << 10} {
		var count int
		err = db.sql.QueryRow(`SELECT COUNT(*) FROM managed_artifact_reservations WHERE run_id=? AND task_id=? AND attempt_id=? AND producer_id=? AND scope_key=? AND class=? AND reserved_bytes=? AND retained_bytes=0 AND state='reserved'`, g.RunID, g.TaskID, g.AttemptID, g.HostID, g.SegmentID, class, size).Scan(&count)
		if err != nil || count != 1 {
			t.Fatal("dispatch escaped exact segment slot reservation", class, count, err)
		}
	}
}

func TestHostArtifactReplayReopensWithoutNewCharge(t *testing.T) {
	db, host := diagnosticsLaunchFixture(t, "omitted", 1)
	ctx := context.Background()
	g, err := db.ClaimReady(ctx, host.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := continuationQuotaUsage(t, db)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, epoch := range []uint64{1, 2} {
		if epoch == 2 {
			if err = db.BeginCoordinatorEpoch(ctx, epoch); err != nil {
				t.Fatal(err)
			}
			if _, err = db.RegisterHost(ctx, host, epoch); err != nil {
				t.Fatal(err)
			}
		}
		pending, err := db.PendingLaunches(ctx, host.LaunchID, epoch)
		if err != nil || len(pending) != 1 {
			t.Fatal(pending, err)
		}
		want := g
		want.ExecutionEpoch = epoch
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(pending[0])
		if string(a) != string(b) {
			t.Fatal("replay changed immutable grant", string(a), string(b))
		}
		after, _ := continuationQuotaUsage(t, db)
		if before != after {
			t.Fatal("replay charged control metadata again", before, after)
		}
		var count int
		if err = db.sql.QueryRow(`SELECT COUNT(*) FROM managed_artifact_reservations`).Scan(&count); err != nil || count != 2 {
			t.Fatal("replay created another raw promise", count, err)
		}
	}
}

func TestHostArtifactPendingRejectsMissingAndReleasedAuthority(t *testing.T) {
	for _, mode := range []string{"missing", "terminal-only", "progress-only", "released"} {
		t.Run(mode, func(t *testing.T) {
			db, host := diagnosticsLaunchFixture(t, "omitted", 1)
			g, err := db.ClaimReady(context.Background(), host.LaunchID, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "released" {
				_, err = db.sql.Exec(`DROP TRIGGER host_reservation_transition; UPDATE managed_artifact_reservations SET state='released' WHERE id=?`, g.HostArtifacts.TerminalReservationID)
			} else {
				// Simulate legacy absence/corrupt partial persistence. The public
				// Store cannot delete these immutable reservation rows.
				_, err = db.sql.Exec(`DROP TRIGGER host_cleanup_authority_binding; DROP TRIGGER retained_host_artifact_cleanup_authorities; DROP TRIGGER artifact_reservation_retained; DROP TRIGGER host_artifact_metadata_retained; DELETE FROM host_artifact_cleanup_authorities; DELETE FROM managed_host_artifact_metadata`)
				if err == nil {
					keep := ""
					if mode == "terminal-only" {
						keep = "terminal"
					}
					if mode == "progress-only" {
						keep = "progress"
					}
					_, err = db.sql.Exec(`DELETE FROM managed_artifact_reservations WHERE class!=?`, keep)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := continuationQuotaState(t, db)
			if _, err = db.PendingLaunches(context.Background(), host.LaunchID, 1); !errors.Is(err, contract.ErrHostArtifactGrant) {
				t.Fatal("invalid authority was replayed or repaired", err)
			}
			if continuationQuotaState(t, db) != before {
				t.Fatal("replay silently repaired original authority")
			}
		})
	}
}

func TestHostArtifactResumeReservesNewSegmentAndRetainsOldPromise(t *testing.T) {
	f := newTypedContinuationFixture(t, adapter.ProviderClaude)
	f.start(t)
	spec := f.interrupted(t, 250)
	if _, err := f.db.QueueContinuation(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	f.db.artifactLimit = contract.HostArtifactReservedBytes
	before := continuationQuotaState(t, f.db)
	if _, err := f.db.ClaimReady(context.Background(), f.host, 1, 2); !errors.Is(err, CodeError("artifact_spool_full")) {
		t.Fatal("resume borrowed previous segment capacity", err)
	}
	if continuationQuotaState(t, f.db) != before {
		t.Fatal("failed resume consumed continuation or budget")
	}
	f.db.artifactLimit = 2 * contract.HostArtifactReservedBytes
	next, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	old := f.grant
	if next.AttemptID != old.AttemptID || next.SegmentID == old.SegmentID || next.HostArtifacts == nil || old.HostArtifacts == nil || next.HostArtifacts.TerminalReservationID == old.HostArtifacts.TerminalReservationID || next.HostArtifacts.ProgressReservationID == old.HostArtifacts.ProgressReservationID || next.HostArtifacts.ScopeSHA256 == old.HostArtifacts.ScopeSHA256 {
		t.Fatal("resume reused previous segment's slots")
	}
	var total int64
	var count int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*),SUM(reserved_bytes) FROM managed_artifact_reservations`).Scan(&count, &total); err != nil || count != 4 || total != 2*contract.HostArtifactReservedBytes {
		t.Fatal("old/new segment promise not retained", count, total, err)
	}
}

func TestHostArtifactHistoricalNilCannotAuthorizeLiveReplay(t *testing.T) {
	db, host := diagnosticsLaunchFixture(t, "omitted", 1)
	ctx := context.Background()
	g, err := db.ClaimReady(ctx, host.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`DROP TRIGGER host_artifact_metadata_retained; DELETE FROM managed_host_artifact_metadata; DROP TRIGGER artifact_reservation_retained; DELETE FROM managed_artifact_reservations`); err != nil {
		t.Fatal(err)
	}
	g.HostArtifacts = nil
	g.LaunchIntentHash, err = contract.CandidateIntentHash(g)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = bindCandidateInputForDecision(ctx, tx, &g, true, nil, candidateArtifactHistoricalProof); err != nil {
		t.Fatal("byte-bound historical proof could not be read", err)
	}
	if g.HostArtifacts != nil {
		t.Fatal("legacy proof minted filesystem authority")
	}
	if err = bindCandidateInputForDecision(ctx, tx, &g, true, nil, candidateArtifactLive); !errors.Is(err, contract.ErrHostArtifactGrant) {
		t.Fatal("history mode escaped into live replay", err)
	}
	g.LaunchIntentHash = "different"
	if err = bindCandidateInputForDecision(ctx, tx, &g, true, nil, candidateArtifactHistoricalProof); err == nil {
		t.Fatal("historical proof did not verify original intent bytes")
	}
}

func TestHostArtifactClaimFailureRollsBackAllObligations(t *testing.T) {
	for _, constraint := range []string{"raw", "disk"} {
		t.Run(constraint, func(t *testing.T) {
			db, host := diagnosticsLaunchFixture(t, "omitted", 1)
			want := CodeError("artifact_spool_full")
			if constraint == "raw" {
				db.artifactLimit = (1 << 20) + (21 * 64 << 10) - 1
			} else {
				db.storageAvailable = func(string) (uint64, error) { return 0, nil }
				want = CodeError("storage_blocked")
			}
			before := continuationQuotaState(t, db)
			_, err := db.ClaimReady(context.Background(), host.LaunchID, 1, 1)
			if !errors.Is(err, want) {
				t.Fatal("unreserved Host write capacity was dispatched", err)
			}
			if after := continuationQuotaState(t, db); after != before {
				t.Fatal("failed artifact admission retained execution/budget/report/slot state")
			}
		})
	}
}
