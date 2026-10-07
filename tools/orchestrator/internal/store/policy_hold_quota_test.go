package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func policyQuotaUsage(t *testing.T, db *DB) int64 {
	t.Helper()
	var run, global int64
	if err := db.sql.QueryRow(`SELECT (SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'),(SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id='')`).Scan(&run, &global); err != nil || run != global {
		t.Fatalf("quota scopes diverged: run=%d global=%d err=%v", run, global, err)
	}
	return run
}

func TestPolicyHoldQuotaAdmissionAndHistory(t *testing.T) {
	ctx := context.Background()
	for _, end := range []string{"claim", "cancel"} {
		t.Run(end, func(t *testing.T) {
			// A real successful event transaction establishes the exact stored cost;
			// an independent identical-width fixture must reject one byte less.
			reference := newPolicyFixture(t, []TaskSpec{policyTask("a", 3)})
			before := policyQuotaUsage(t, reference.db)
			referenceEvent := policyRejection(&reference, "probe_start_eagain")
			if _, err := reference.db.CommitHostEvent(ctx, referenceEvent); err != nil {
				t.Fatal(err)
			}
			growth := policyQuotaUsage(t, reference.db) - before
			var slot, command, policySHA string
			if err := reference.db.sql.QueryRow(`SELECT action_slot,command_id,policy_sha256 FROM retry_decisions`).Scan(&slot, &command, &policySHA); err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(referenceEvent)
			if err != nil {
				t.Fatal(err)
			}
			// Contract accounting: event bytes/artifact, policy decision metadata,
			// legacy binding bytes, then exactly sixteen new hold-history bytes.
			withoutHold := int64(len(body)) + referenceEvent.Artifact.Size + 24 + int64(len("policy")+len(command)+len(policySHA)) + 16 + int64(len(slot)+len("a"))
			if growth-withoutHold != 16 {
				t.Fatalf("hold history cost=%d want16", growth-withoutHold)
			}
			f := newPolicyFixture(t, []TaskSpec{policyTask("a", 3)})
			initial := policyQuotaUsage(t, f.db)
			f.db.runControlLimit = initial + growth - 1
			e := policyRejection(&f, "probe_start_eagain")
			if _, err := f.db.CommitHostEvent(ctx, e); !errors.Is(err, CodeError("control_spool_full")) {
				t.Fatalf("one-byte-short admission accepted: %v", err)
			}
			if got := policyQuotaUsage(t, f.db); got != initial {
				t.Fatalf("failed admission charged quota: %d/%d", got, initial)
			}
			var rows int
			if err := f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM runtime_events)+(SELECT COUNT(*) FROM retry_decisions)+(SELECT COUNT(*) FROM decision_attempt_bindings)`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("partial rejection transaction: %d %v", rows, err)
			}
			f.db.runControlLimit++
			if _, err := f.db.CommitHostEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			stored := policyQuotaUsage(t, f.db)
			if stored != initial+growth {
				t.Fatalf("exact quota mismatch: %d/%d", stored, initial+growth)
			}
			if end == "cancel" {
				if _, err := f.db.RequestStop(ctx, "a", 1, "owner_stop_requested"); err != nil {
					t.Fatal(err)
				}
				if got := policyQuotaUsage(t, f.db); got != stored {
					t.Fatalf("retiring a promise erased its stored history: %d/%d", got, stored)
				}
			} else {
				f.db.runControlLimit += 1024
				if _, err := f.db.sql.Exec(`UPDATE retry_decisions SET eligible_at_unix_ms=1`); err != nil {
					t.Fatal(err)
				}
				g, err := f.db.ClaimReady(ctx, f.host, 1, 2)
				if err != nil {
					t.Fatal(err)
				}
				if got := policyQuotaUsage(t, f.db); got != stored+int64(len(g.AttemptID))+hostArtifactControlBytes(t, f.db, g.SegmentID) {
					t.Fatalf("consuming promise lost history or double-charged hold: %d/%d", got, stored+int64(len(g.AttemptID))+hostArtifactControlBytes(t, f.db, g.SegmentID))
				}
			}
			var hold int64
			if err := f.db.sql.QueryRow(`SELECT reserved_active_ms FROM decision_attempt_bindings`).Scan(&hold); err != nil || hold != 200 {
				t.Fatalf("historical grant changed: %d %v", hold, err)
			}
		})
	}
}
