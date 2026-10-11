package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"strings"
	"testing"
)

// This fixture isolates the companion transaction from accepted-review DAG
// setup. Public filtered integration must separately qualify the complete DAG.
func carrierStoreFixture(t *testing.T, reserve bool) (*DB, contract.LaunchCommand) {
	t.Helper()
	db, c, _ := gitFilterClaimFixture(t)
	var old adapter.InvocationPayload
	if err := json.Unmarshal(c.AdapterPayload, &old); err != nil {
		t.Fatal(err)
	}
	p := adapter.InvocationPayload{Kind: "candidate", Directory: "/private/repo", GitFilter: old.GitFilter, CandidateAction: &adapter.CandidateAction{Version: 1, Operation: "filter_integrate", DependencyInputVersion: 1, AutoDirectory: true, SourceTask: "review", Target: adapter.CandidateTarget{Worktree: "/private/repo", Ref: "refs/heads/main", BaseOID: strings.Repeat("b", 40)}}}
	c.AdapterPayload, _ = json.Marshal(p)
	if reserve {
		owned, err := db.beginOwnerTransaction(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer owned.close()
		if err = db.reserveGitFilterCarrier(context.Background(), owned.tx, &c); err != nil {
			t.Fatal(err)
		}
		if err = owned.commit(); err != nil {
			t.Fatal(err)
		}
	}
	return db, c
}

func TestGitFilterCarrierQuotaReplayAndMigration(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"reserve-replay", "control-full", "physical-full", "schema37"} {
		t.Run(mode, func(t *testing.T) {
			db, c := carrierStoreFixture(t, false)
			if mode == "schema37" {
				if _, err := db.sql.Exec(`DROP TABLE git_filter_carrier_reservations;UPDATE schema_meta SET version=37`); err != nil {
					t.Fatal(err)
				}
				if err := db.migrateRuntime(); err != nil {
					t.Fatal(err)
				}
			}
			var before int64
			if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if mode == "control-full" {
				db.userControlLimit = before
			}
			if mode == "physical-full" {
				db.storageAvailable = func(string) (uint64, error) { return 0, nil }
			}
			owned, err := db.beginOwnerTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.close()
			err = db.reserveGitFilterCarrier(ctx, owned.tx, &c)
			if mode == "control-full" || mode == "physical-full" {
				if err == nil {
					t.Fatal("unfunded carrier admitted")
				}
				owned.close()
				var count int
				var after int64
				if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM git_filter_carrier_reservations),(SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id='')`).Scan(&count, &after); err != nil || count != 0 || before != after {
					t.Fatal("failed reservation left state", count, after, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = owned.commit(); err != nil {
				t.Fatal(err)
			}
			owned.close()
			owned, err = db.beginOwnerTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.close()
			expected := *c.GitFilterCarrier
			c.GitFilterCarrier = nil
			if err = bindGitFilterCarrier(ctx, owned.tx, &c, true); err != nil || *c.GitFilterCarrier != expected {
				t.Fatal("replay changed grant", err)
			}
			c.GitFilterCarrier.ReservationID = "foreign"
			if bindGitFilterCarrier(ctx, owned.tx, &c, true) == nil {
				t.Fatal("foreign replay accepted")
			}
			if _, err = owned.tx.Exec(`DELETE FROM git_filter_carrier_reservations`); err == nil {
				t.Fatal("retained metadata erased")
			}
			var charged int64
			if err = owned.tx.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&charged); err != nil || charged-before <= contract.GitFilterCarrierMetadataBytes {
				t.Fatal("metadata row not fully charged", charged, before, err)
			}
		})
	}
}

func TestGitFilterCarrierAtomicCleanup(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"known", "missing", "wrong", "unknown", "unknown-with-receipt"} {
		t.Run(mode, func(t *testing.T) {
			db, c := carrierStoreFixture(t, true)
			g := c.GitFilter
			k := c.GitFilterCarrier
			e := contract.Event{Version: 1, ProducerID: c.HostID, EventID: "carrier-terminal", RunID: c.RunID, TaskID: c.TaskID, AttemptID: c.AttemptID, SegmentID: c.SegmentID, CommandID: c.CommandID, WorkRevision: c.WorkRevision, ExecutionEpoch: 1, Sequence: 1, Kind: contract.EventExited, PayloadHash: runtimeHash("terminal"), ActiveMS: 1,
				GitFilterCleanup:        &contract.GitFilterCleanup{Version: 1, ReservationID: g.ReservationID, ScopeSHA256: g.ScopeSHA256, ManifestSHA256: g.ManifestSHA256, CleanToolSHA256: g.CleanToolSHA256, SmudgeToolSHA256: g.SmudgeToolSHA256, JournalSHA256: runtimeHash("removed"), RemovedBytes: 0},
				GitFilterCarrierCleanup: &contract.GitFilterCarrierCleanup{Version: 1, ReservationID: k.ReservationID, ScopeSHA256: k.ScopeSHA256, FilterReservationID: g.ReservationID, ManifestSHA256: g.ManifestSHA256, RuntimeNamespace: contract.GitFilterCarrierNamespace, Outcome: "not_created", JournalSHA256: runtimeHash("")}}
			switch mode {
			case "missing":
				e.GitFilterCarrierCleanup = nil
			case "wrong":
				e.GitFilterCarrierCleanup.ScopeSHA256 = strings.Repeat("e", 64)
			case "unknown":
				e.Kind = contract.EventUnknown
				e.GitFilterCleanup = nil
				e.GitFilterCarrierCleanup = nil
			case "unknown-with-receipt":
				e.Kind = contract.EventUnknown
			}
			var before int64
			_ = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&before)
			ack, err := db.CommitHostEvent(ctx, e)
			var first, second string
			if scan := db.sql.QueryRow(`SELECT f.state,c.state FROM git_filter_resource_reservations f JOIN git_filter_carrier_reservations c ON c.filter_reservation_id=f.id`).Scan(&first, &second); scan != nil {
				t.Fatal(scan)
			}
			if mode != "known" {
				if first != "reserved" || second != "reserved" {
					t.Fatal("partial release", first, second)
				}
				if mode == "unknown" && err != nil || mode != "unknown" && err == nil {
					t.Fatal("terminal acceptance", err)
				}
				return
			}
			if err != nil || first != "released" || second != "released" {
				t.Fatal("known release", first, second, err)
			}
			again, err := db.CommitHostEvent(ctx, e)
			if err != nil || again != ack {
				t.Fatal("terminal replay", err)
			}
			var after int64
			_ = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&after)
			if after < before {
				t.Fatal("metadata refunded")
			}
			owned, err := db.beginOwnerTransaction(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.close()
			copy := c
			copy.GitFilter = nil
			copy.GitFilterCarrier = nil
			if err = bindGitFilter(ctx, owned.tx, &copy, false); err != nil || *copy.GitFilterCarrier != *k {
				t.Fatal("historical companion lost", err)
			}
			if err = bindGitFilter(ctx, owned.tx, &copy, true); !errors.Is(err, contract.ErrGitFilterContract) {
				t.Fatal("released carrier became live", err)
			}
		})
	}
}
