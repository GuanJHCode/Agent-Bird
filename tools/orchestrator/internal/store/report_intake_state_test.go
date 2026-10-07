package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/artifacts"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func assertPreparedReport(t *testing.T, db *DB) {
	t.Helper()
	var phase, state string
	var events, refs, reservations int
	var retained int64
	err := db.sql.QueryRow(`SELECT i.phase,r.state,r.retained_bytes,(SELECT COUNT(*) FROM report_events),(SELECT COUNT(*) FROM managed_artifact_refs),(SELECT COUNT(*) FROM managed_artifact_reservations WHERE class='report') FROM managed_artifact_intakes i JOIN managed_artifact_reservations r ON r.id=i.reservation_id`).Scan(&phase, &state, &retained, &events, &refs, &reservations)
	if err != nil || phase != "prepared" || state != "reserved" || retained != 0 || events != 0 || refs != 0 || reservations != 1 {
		t.Fatal("failed commit did not preserve exactly one intent", phase, state, retained, events, refs, reservations, err)
	}
}

func TestReportIntakeReauthorizesAfterCopy(t *testing.T) {
	for _, change := range []string{"revoke", "epoch", "root"} {
		t.Run(change, func(t *testing.T) {
			db, spec, source := newReportIntakeFixture(t)
			_, err := db.commitReportEventWithCopy(context.Background(), spec, func(ctx context.Context, root string, desc artifacts.Intake) (artifacts.Proof, error) {
				p, err := artifacts.Materialize(ctx, root, desc)
				if err != nil {
					return p, err
				}
				switch change {
				case "revoke":
					_, err = db.sql.Exec("UPDATE report_capabilities SET status='revoked'")
				case "epoch":
					_, err = db.sql.Exec("UPDATE runtime_hosts SET coordinator_epoch=coordinator_epoch+1; UPDATE report_capabilities SET execution_epoch=execution_epoch+1")
				case "root":
					_, err = db.sql.Exec("UPDATE report_capabilities SET capability_dir=capability_dir||'-changed'")
				}
				return p, err
			})
			if err == nil {
				t.Fatal("authorization changed during copy but event committed")
			}
			assertPreparedReport(t, db)
			if err = os.Remove(source); err != nil {
				t.Fatal(err)
			}
			_, err = db.CommitReportEvent(context.Background(), spec)
			if change == "epoch" {
				if err != nil {
					t.Fatal("same immutable intake could not reauthorize in the new epoch", err)
				}
				var count, epoch int
				if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM managed_artifact_reservations WHERE class='report'),json_extract(body_json,'$.execution_epoch') FROM report_events`).Scan(&count, &epoch); err != nil || count != 1 || epoch != 2 {
					t.Fatal(count, epoch, err)
				}
			} else {
				if err == nil {
					t.Fatal("rejected authority was silently restored")
				}
				assertPreparedReport(t, db)
			}
		})
	}
}

func TestReportIntakeCompetingExactRequestsCommitOnce(t *testing.T) {
	db, spec, source := newReportIntakeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	type result struct {
		ack contract.DurableAck
		err error
	}
	first := make(chan result, 1)
	go func() {
		ack, err := db.commitReportEventWithCopy(ctx, spec, func(ctx context.Context, root string, desc artifacts.Intake) (artifacts.Proof, error) {
			p, err := artifacts.Materialize(ctx, root, desc)
			if err != nil {
				return p, err
			}
			close(ready)
			select {
			case <-release:
				return p, nil
			case <-ctx.Done():
				return artifacts.Proof{}, ctx.Err()
			}
		})
		first <- result{ack, err}
	}()
	select {
	case <-ready:
	case early := <-first:
		t.Fatal("first copy failed", early.err)
	case <-ctx.Done():
		t.Fatal("first copy stalled")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	second, err := db.CommitReportEvent(ctx, spec)
	if err != nil {
		t.Fatal("second request did not reuse private copy outside DB mutex", err)
	}
	unblock()
	select {
	case r := <-first:
		if r.err != nil || r.ack != second {
			t.Fatal("competing completion changed the ACK", r.err)
		}
	case <-ctx.Done():
		t.Fatal("original request did not join")
	}
	var events, refs, intakes, reservations int
	err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM report_events),(SELECT COUNT(*) FROM managed_artifact_refs),(SELECT COUNT(*) FROM managed_artifact_intakes),(SELECT COUNT(*) FROM managed_artifact_reservations WHERE class='report')`).Scan(&events, &refs, &intakes, &reservations)
	if err != nil || events != 1 || refs != 1 || intakes != 1 || reservations != 1 {
		t.Fatal("same request charged or committed twice", events, refs, intakes, reservations, err)
	}
}

func TestReportIntakeCompletionQuotaRollbackAndSourceFreeRecovery(t *testing.T) {
	db, spec, source := newReportIntakeFixture(t)
	var preparedUsage int64
	_, err := db.commitReportEventWithCopy(context.Background(), spec, func(ctx context.Context, root string, desc artifacts.Intake) (artifacts.Proof, error) {
		p, err := artifacts.Materialize(ctx, root, desc)
		if err != nil {
			return p, err
		}
		err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&preparedUsage)
		db.runControlLimit = preparedUsage
		return p, err
	})
	if !errors.Is(err, CodeError("control_spool_full")) {
		t.Fatal("second transaction quota was not enforced", err)
	}
	assertPreparedReport(t, db)
	var afterFailure, used int64
	if err = db.sql.QueryRow(`SELECT (SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'),used_bytes FROM report_capabilities`).Scan(&afterFailure, &used); err != nil || afterFailure != preparedUsage || used != 0 {
		t.Fatal("failed commit leaked control usage", afterFailure, preparedUsage, used, err)
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	// Reopening must preserve the failed second transaction's original intent
	// and private object even though its provider source has disappeared.
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(db.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertPreparedReport(t, db)
	ack, err := db.CommitReportEvent(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	var total, accounted, body, refCost, reserved, retained int64
	err = db.sql.QueryRow(`SELECT (SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'),e.accounted_bytes,length(CAST(e.body_json AS BLOB)),64+length(CAST(f.id AS BLOB))+length(CAST(f.reservation_id AS BLOB))+length(CAST(f.run_id AS BLOB))+length(CAST(f.slot AS BLOB))+length(CAST(f.intake_id AS BLOB))+length(CAST(f.proof_json AS BLOB)),r.reserved_bytes,r.retained_bytes FROM report_events e JOIN managed_artifact_intakes i ON i.event_id=e.event_id JOIN managed_artifact_refs f ON f.intake_id=i.id JOIN managed_artifact_reservations r ON r.id=i.reservation_id`).Scan(&total, &accounted, &body, &refCost, &reserved, &retained)
	if err != nil || accounted != body || total-preparedUsage != accounted+refCost-1 || reserved != retained {
		t.Fatal("control/raw ledgers diverged", total, preparedUsage, accounted, body, refCost, reserved, retained, err)
	}
	again, err := db.CommitReportEvent(context.Background(), spec)
	if err != nil || again != ack {
		t.Fatal(again, err)
	}
	var replayUsage int64
	if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&replayUsage); err != nil || replayUsage != total {
		t.Fatal("duplicate charged metadata again", replayUsage, total, err)
	}
}

func TestReportIntakeReservationLimitsAndPendingIdentity(t *testing.T) {
	for _, kind := range []string{"artifact", "disk", "control", "sequence", "event"} {
		t.Run(kind, func(t *testing.T) {
			db, spec, _ := newReportIntakeFixture(t)
			switch kind {
			case "artifact":
				db.artifactLimit = 1
			case "disk":
				db.storageAvailable = func(string) (uint64, error) { return 0, nil }
			case "control":
				db.runControlLimit = 0
			default:
				_, err := db.commitReportEventWithCopy(context.Background(), spec, func(context.Context, string, artifacts.Intake) (artifacts.Proof, error) {
					return artifacts.Proof{}, context.Canceled
				})
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if kind == "sequence" {
					spec.EventID = "different"
				} else {
					spec.Sequence++
				}
			}
			if _, err := db.CommitReportEvent(context.Background(), spec); err == nil {
				t.Fatal("invalid intake admitted")
			}
			var reservations, refs, events int
			if err := db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM managed_artifact_reservations WHERE class='report'),(SELECT COUNT(*) FROM managed_artifact_refs),(SELECT COUNT(*) FROM report_events)`).Scan(&reservations, &refs, &events); err != nil {
				t.Fatal(err)
			}
			want := 0
			if kind == "sequence" || kind == "event" {
				want = 1
			}
			if reservations != want || refs != 0 || events != 0 {
				t.Fatal("rejection created another obligation", reservations, refs, events)
			}
			if _, err := os.Lstat(filepath.Join(filepath.Dir(db.path), "managed-artifacts")); !os.IsNotExist(err) {
				t.Fatal("admission failure touched destination files", err)
			}
		})
	}
}

func TestReportIntakeSchemaBindsReferencesAndCompletion(t *testing.T) {
	db, spec, _ := newReportIntakeFixture(t)
	i, err := db.beginReportIntake(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`UPDATE managed_artifact_intakes SET phase='committed' WHERE id=?`, i.id); err == nil {
		t.Fatal("phase committed without event/ref/retained proof")
	}
	proof := artifacts.Proof{IntakeID: "foreign", DescriptorSHA256: runtimeHash(i.descriptor), SHA256: i.spec.SHA256, Size: i.spec.Size}
	raw, _ := json.Marshal(proof)
	if _, err = db.sql.Exec(`INSERT INTO managed_artifact_refs(id,reservation_id,run_id,slot,intake_id,proof_json) VALUES(?,?,?,'report',?,?)`, i.id, i.reservation, i.authority.run, i.id, string(raw)); err == nil {
		t.Fatal("foreign proof identity accepted")
	}
	assertPreparedReport(t, db)
}

func TestReportIntakeDiskPromiseBoundary(t *testing.T) {
	db, spec, _ := newReportIntakeFixture(t)
	ctx := context.Background()
	prepared, err := db.beginReportIntake(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	db.minFreeBytes, db.sqliteWriteOverhead, db.criticalDiskReserve = 64, 32, 20<<10
	available := uint64(64 + 32 + prepared.spec.Size + contract.HostArtifactReservedBytes)
	db.storageAvailable = func(string) (uint64, error) { return available, nil }
	available--
	if _, err = db.beginReportIntake(ctx, spec); !errors.Is(err, CodeError("storage_blocked")) {
		t.Fatal("retry borrowed an outstanding artifact promise", err)
	}
	assertPreparedReport(t, db)
	available++
	again, err := db.beginReportIntake(ctx, spec)
	if err != nil || again.id != prepared.id || again.reservation != prepared.reservation {
		t.Fatal("exact retry charged its own promise twice or could not use emergency reserve", err)
	}
	assertPreparedReport(t, db)

	// A new report needs room for both the existing promise and its own raw
	// bytes/metadata. Critical status cannot waive either obligation.
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	const incoming, metadata = int64(7), int64(11)
	available += uint64(incoming + metadata)
	available--
	if err = db.managedReportArtifactAdmission(ctx, tx, incoming, metadata); !errors.Is(err, CodeError("storage_blocked")) {
		t.Fatal("new report borrowed an outstanding artifact promise", err)
	}
	available++
	db.artifactLimit = prepared.spec.Size + incoming + contract.HostArtifactReservedBytes
	if err = db.managedReportArtifactAdmission(ctx, tx, incoming, metadata); err != nil {
		t.Fatal("exact disk/raw allowance boundary rejected", err)
	}
	db.artifactLimit--
	if err = db.managedReportArtifactAdmission(ctx, tx, incoming, metadata); !errors.Is(err, CodeError("artifact_spool_full")) {
		t.Fatal("critical report bypassed raw artifact allowance", err)
	}
}
