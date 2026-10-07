package store

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/diagnostics"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/ipc"
)

func diagnosticsLaunchFixture(t *testing.T, mode string, tasks int) (*DB, contract.HostHello) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var policy *contract.DiagnosticsPolicy
	if mode != "omitted" {
		policy = &contract.DiagnosticsPolicy{Version: 1, Mode: mode}
	}
	p := diagnosticsPlan(policy)
	for i := 1; i < tasks; i++ {
		next := p.Tasks[0]
		next.ID = fmt.Sprint("task-", i+1)
		p.Tasks = append(p.Tasks, next)
	}
	r, err := db.SubmitPlan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	h := contract.HostHello{LaunchID: r.LaunchID, LaunchToken: r.LaunchToken, OriginContextID: "origin", OriginPID: 7, OriginBirth: "birth", HostGeneration: "generation", PID: 101, Birth: "host-birth", Executable: "/private/tmp/orchestrator"}
	if _, err = db.RegisterHost(context.Background(), h, 1); err != nil {
		t.Fatal(err)
	}
	return db, h
}

func assertDiagnosticsReservation(t *testing.T, db *DB, g contract.LaunchCommand, ordinal int) {
	t.Helper()
	if g.Diagnostics == nil || g.Diagnostics.Validate() != nil || g.Diagnostics.SegmentOrdinal != ordinal {
		t.Fatalf("missing/malformed detailed grant: %+v", g.Diagnostics)
	}
	d := g.Diagnostics
	namespace, err := (diagnostics.Scope{Version: 1, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, ProducerID: g.HostID, PolicySHA256: d.PolicySHA256}).Hash()
	if err != nil {
		t.Fatal(err)
	}
	var scope, hash, state, phase string
	var maximum, retained, next, history int64
	err = db.sql.QueryRow(`SELECT r.scope_key,r.namespace_sha256,r.state,r.reserved_bytes,r.retained_bytes,d.phase,d.next_revision,length(d.reading_request_json)+length(d.pending_envelope)+length(d.last_ack_json)+length(d.acked_cursor_json) FROM managed_artifact_reservations r JOIN diagnostic_delivery_state d ON d.reservation_id=r.id AND d.run_id=r.run_id AND d.task_id=r.task_id AND d.attempt_id=r.attempt_id AND d.producer_id=r.producer_id WHERE r.id=? AND r.run_id=? AND r.task_id=? AND r.attempt_id=? AND r.producer_id=? AND r.policy_sha256=? AND r.class='diagnostics'`, d.ReservationID, g.RunID, g.TaskID, g.AttemptID, g.HostID, d.PolicySHA256).Scan(&scope, &hash, &state, &maximum, &retained, &phase, &next, &history)
	if err != nil || scope != "attempt" || hash != namespace || state != "reserved" || maximum != 21102592 || retained != 0 || phase != "idle" || next != 1 || history != 0 {
		t.Fatal("reservation/delivery not bound to exact launch", scope, hash, state, maximum, retained, phase, next, history, err)
	}
}

func TestDiagnosticsLaunchReservesOnlyExplicitDetailedAndReplays(t *testing.T) {
	for _, mode := range []string{"omitted", "summary", "detailed"} {
		t.Run(mode, func(t *testing.T) {
			db, h := diagnosticsLaunchFixture(t, mode, 1)
			g, err := db.ClaimReady(context.Background(), h.LaunchID, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			var reservations, deliveries int
			if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM managed_artifact_reservations WHERE class='diagnostics'),(SELECT COUNT(*) FROM diagnostic_delivery_state)`).Scan(&reservations, &deliveries); err != nil {
				t.Fatal(err)
			}
			if mode != "detailed" {
				if g.Diagnostics != nil || reservations != 0 || deliveries != 0 {
					t.Fatal("implicit raw capture authority", g.Diagnostics, reservations, deliveries)
				}
				return
			}
			assertDiagnosticsReservation(t, db, g, 1)
			if reservations != 1 || deliveries != 1 {
				t.Fatal("not exactly one attempt reservation", reservations, deliveries)
			}
			var used int64
			if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&used); err != nil || used <= hostArtifactManifestReservationBytes || used >= hostArtifactManifestReservationBytes+4096 {
				t.Fatal("artifact allowance entered control quota or metadata omitted", used, err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(db.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			pending, err := reopened.PendingLaunches(context.Background(), h.LaunchID, 1)
			if err != nil || len(pending) != 1 {
				t.Fatal("missing replay", pending, err)
			}
			a, _ := json.Marshal(g)
			b, _ := json.Marshal(pending[0])
			if string(a) != string(b) {
				t.Fatal("reopen changed exact launch wire")
			}
			var after int64
			if err = reopened.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&after); err != nil || after != used {
				t.Fatal("replay charged again", used, after, err)
			}
			// Same producer survives a real Store epoch/reconnect protocol.
			if err = reopened.BeginCoordinatorEpoch(context.Background(), 2); err != nil {
				t.Fatal(err)
			}
			if _, err = reopened.RegisterHost(context.Background(), h, 2); err != nil {
				t.Fatal(err)
			}
			pending, err = reopened.PendingLaunches(context.Background(), h.LaunchID, 2)
			if err != nil || len(pending) != 1 || pending[0].Diagnostics == nil || pending[0].ExecutionEpoch != 2 || *pending[0].Diagnostics != *g.Diagnostics {
				t.Fatal("reconnect changed diagnostics authority", pending, err)
			}
		})
	}
}

func TestDiagnosticsTypedResumeRetainsAttemptReservationAndAdvancesOrdinal(t *testing.T) {
	for _, provider := range []adapter.Provider{adapter.ProviderClaude, adapter.ProviderAGY, adapter.ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			f := newTypedContinuationFixtureWithDiagnostics(t, provider, "task", nil, &contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"})
			first := f.grant
			assertDiagnosticsReservation(t, f.db, first, 1)
			f.start(t)
			mode, remaining := contract.ContinuationInterrupted, int64(750)
			if provider == adapter.ProviderCodex {
				// Codex currently authorizes only a Host question continuation;
				// interrupted resume still requires its separate seal contract.
				q := f.question(t, 1)
				if _, err := f.db.AnswerQuestion(context.Background(), AnswerSpec{TaskID: first.TaskID, WorkRevision: 1, QuestionID: q.QuestionID, QuestionRevision: 1, Answer: "Authorized answer"}); err != nil {
					t.Fatal(err)
				}
				mode, remaining = contract.ContinuationQuestion, 800
			} else {
				spec := f.interrupted(t, 250)
				if _, err := f.db.QueueContinuation(context.Background(), spec); err != nil {
					t.Fatal(err)
				}
			}
			// Diagnostics reuses its attempt reservation; the new segment needs
			// another Host terminal/progress pair at the exact raw boundary.
			f.db.artifactLimit = detailedArtifactReservationBytes + 2*contract.HostArtifactReservedBytes
			next, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			assertDiagnosticsReservation(t, f.db, next, 2)
			if next.AttemptID != first.AttemptID || next.SegmentID == first.SegmentID || next.Diagnostics.ReservationID != first.Diagnostics.ReservationID || next.Continuation == nil || next.Continuation.Mode != mode || next.SessionID != f.sessionID || next.GrantedActiveMS != remaining {
				t.Fatal("resume changed existing scope/continuation", next)
			}
			var count int
			if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM managed_artifact_reservations`).Scan(&count); err != nil || count != 5 || next.HostArtifacts.TerminalReservationID == first.HostArtifacts.TerminalReservationID || next.HostArtifacts.ProgressReservationID == first.HostArtifacts.ProgressReservationID {
				t.Fatal("resume lost diagnostics reuse or new Host slots", count, err)
			}
			pending, err := f.db.PendingLaunches(context.Background(), f.host, 1)
			if err != nil || len(pending) != 1 {
				t.Fatal(pending, err)
			}
			a, _ := json.Marshal(next)
			b, _ := json.Marshal(pending[0])
			if string(a) != string(b) {
				t.Fatal("resumed replay changed ordinal or namespace")
			}
		})
	}
}

func TestDiagnosticsNewClaimQuotaAndControlRollback(t *testing.T) {
	for _, kind := range []string{"allowance", "control", "disk_overflow"} {
		t.Run(kind, func(t *testing.T) {
			db, h := diagnosticsLaunchFixture(t, "detailed", 1)
			var before int64
			if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			want := CodeError("artifact_spool_full")
			switch kind {
			case "allowance":
				db.artifactLimit = detailedArtifactReservationBytes - 1
			case "control":
				db.userControlLimit = before + 1
				want = "control_spool_full"
			case "disk_overflow":
				db.criticalDiskReserve = math.MaxUint64
				want = "storage_blocked"
			}
			if _, err := db.ClaimReady(context.Background(), h.LaunchID, 1, 1); err != want {
				t.Fatal("new Claim did not enforce admission", err, want)
			}
			var attempts, segments, budgets, reservations, deliveries int
			var after int64
			if err := db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM budget_runtime),(SELECT COUNT(*) FROM managed_artifact_reservations),(SELECT COUNT(*) FROM diagnostic_delivery_state),(SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id='')`).Scan(&attempts, &segments, &budgets, &reservations, &deliveries, &after); err != nil || attempts != 0 || segments != 0 || budgets != 0 || reservations != 0 || deliveries != 0 || before != after {
				t.Fatal("failed Claim changed ledger", attempts, segments, budgets, reservations, deliveries, before, after, err)
			}
		})
	}
}

func TestDiagnosticsAllowanceCountsOtherClassesAndRetainedPromises(t *testing.T) {
	for _, state := range []string{"reserved", "active", "deleting", "legacy_pending", "released", "overflow"} {
		t.Run(state, func(t *testing.T) {
			db, h := diagnosticsLaunchFixture(t, "detailed", 2)
			first, err := db.ClaimReady(context.Background(), h.LaunchID, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			assertDiagnosticsReservation(t, db, first, 1)
			amount := db.artifactLimit - detailedArtifactReservationBytes
			storedState := state
			if state == "overflow" {
				db.artifactLimit = math.MaxInt64
				amount = math.MaxInt64
				storedState = "legacy_pending"
			}
			// Controlled ledger fixture, not evidence of physical file deletion/intake.
			if _, err = db.sql.Exec(`INSERT INTO managed_artifact_reservations(id,run_id,task_id,attempt_id,producer_id,class,scope_key,policy_sha256,namespace_sha256,reserved_bytes,retained_bytes,state,created_at) VALUES('legacy',?,?,?,?,'legacy','legacy','','',?,?,?,'now')`, first.RunID, first.TaskID, first.AttemptID, first.HostID, amount, amount, storedState); err != nil {
				t.Fatal(err)
			}
			db.storageAvailable = func(string) (uint64, error) { return math.MaxUint64, nil }
			next, err := db.ClaimReady(context.Background(), h.LaunchID, 1, 2)
			if state == "released" {
				if err != nil {
					t.Fatal(err)
				}
				assertDiagnosticsReservation(t, db, next, 1)
				return
			}
			if err != CodeError("artifact_spool_full") {
				t.Fatal("unreleased history/overflow did not consume allowance", state, err)
			}
			var count int
			if err = db.sql.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&count); err != nil || count != 1 {
				t.Fatal("quota rejection leaked attempt", count, err)
			}
		})
	}
}

func TestDiagnosticsReplayRejectsChangedOrMissingStoredAuthority(t *testing.T) {
	for _, kind := range []string{"namespace", "policy_hash", "noncanonical_policy", "delivery_task", "producer", "missing_delivery", "missing_policy", "deleting", "released"} {
		t.Run(kind, func(t *testing.T) {
			db, h := diagnosticsLaunchFixture(t, "detailed", 2)
			g, err := db.ClaimReady(context.Background(), h.LaunchID, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			assertDiagnosticsReservation(t, db, g, 1)
			// Simulate persisted corruption by removing only the relevant immutable
			// guard; normal callers never obtain this mutation authority.
			switch kind {
			case "namespace":
				_, err = db.sql.Exec(`DROP TRIGGER artifact_reservation_identity_immutable; UPDATE managed_artifact_reservations SET namespace_sha256=? WHERE id=?`, strings.Repeat("0", 64), g.Diagnostics.ReservationID)
			case "policy_hash":
				_, err = db.sql.Exec(`DROP TRIGGER diagnostics_policy_immutable; UPDATE task_diagnostics_policies SET policy_sha256=? WHERE task_id=?`, strings.Repeat("0", 64), g.TaskID)
			case "noncanonical_policy":
				body := `{ "version":1,"mode":"detailed"}`
				_, err = db.sql.Exec(`DROP TRIGGER diagnostics_policy_immutable; UPDATE task_diagnostics_policies SET body_json=?,policy_sha256=? WHERE task_id=?`, body, runtimeHash(body), g.TaskID)
			case "delivery_task":
				_, err = db.sql.Exec(`DROP TRIGGER diagnostic_delivery_identity_immutable; UPDATE diagnostic_delivery_state SET task_id='task-2' WHERE attempt_id=?`, g.AttemptID)
			case "producer":
				p := diagnosticsPlan(nil)
				p.Run.ID, p.Tasks[0].RunID, p.Tasks[0].ID = "foreign-run", "foreign-run", "foreign-task"
				p.Host.HostGeneration = "foreign-generation"
				r, e := db.SubmitPlan(context.Background(), p)
				if e != nil {
					t.Fatal(e)
				}
				foreign := h
				foreign.LaunchID, foreign.LaunchToken, foreign.HostGeneration, foreign.PID, foreign.Birth = r.LaunchID, r.LaunchToken, "foreign-generation", 202, "foreign-birth"
				if _, e = db.RegisterHost(context.Background(), foreign, 1); e != nil {
					t.Fatal(e)
				}
				_, err = db.sql.Exec(`DROP TRIGGER artifact_reservation_identity_immutable; DROP TRIGGER diagnostic_delivery_identity_immutable; UPDATE managed_artifact_reservations SET producer_id=? WHERE id=?; UPDATE diagnostic_delivery_state SET producer_id=? WHERE attempt_id=?`, r.LaunchID, g.Diagnostics.ReservationID, r.LaunchID, g.AttemptID)
			case "missing_delivery":
				_, err = db.sql.Exec(`DROP TRIGGER diagnostic_delivery_retained; DELETE FROM diagnostic_delivery_state WHERE attempt_id=?`, g.AttemptID)
			case "missing_policy":
				_, err = db.sql.Exec(`DROP TRIGGER diagnostics_policy_retained; DELETE FROM task_diagnostics_policies WHERE task_id=?`, g.TaskID)
			default:
				_, err = db.sql.Exec(`UPDATE managed_artifact_reservations SET state=? WHERE id=?`, kind, g.Diagnostics.ReservationID)
			}
			if err != nil {
				t.Fatal("fixture did not inject intended drift", err)
			}
			var before int64
			if err = db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err = db.PendingLaunches(context.Background(), h.LaunchID, 1); err != CodeError("diagnostics_grant_invalid") {
				t.Fatal("drifted capture authority replayed", kind, err)
			}
			var after int64
			var count int
			if err = db.sql.QueryRow(`SELECT (SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''),(SELECT COUNT(*) FROM managed_artifact_reservations)`).Scan(&after, &count); err != nil || before != after || count != 3 {
				t.Fatal("replay manufactured replacement authority", before, after, count, err)
			}
		})
	}
}

func TestDiagnosticsLaunchDiskPromisesPreventOvercommitAndRollBack(t *testing.T) {
	db, h := diagnosticsLaunchFixture(t, "detailed", 2)
	// Exactly30MiB above existing control/disk reserves: enough for one21MiB
	// promise, never two, even though neither has written an artifact yet.
	db.storageAvailable = func(string) (uint64, error) {
		return db.minFreeBytes + db.criticalDiskReserve + db.sqliteWriteOverhead + 30*1024*1024, nil
	}
	first, err := db.ClaimReady(context.Background(), h.LaunchID, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimReady(context.Background(), h.LaunchID, 1, 2); err != CodeError("storage_blocked") {
		t.Fatal("double promised the same disk", err)
	}
	assertDiagnosticsReservation(t, db, first, 1)
	var attempts, segments, budgets, reservations, deliveries int
	if err = db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM attempts),(SELECT COUNT(*) FROM segments),(SELECT COUNT(*) FROM budget_runtime),(SELECT COUNT(*) FROM managed_artifact_reservations),(SELECT COUNT(*) FROM diagnostic_delivery_state)`).Scan(&attempts, &segments, &budgets, &reservations, &deliveries); err != nil || attempts != 1 || segments != 1 || budgets != 1 || reservations != 3 || deliveries != 1 {
		t.Fatal("rejected Claim leaked rows", attempts, segments, budgets, reservations, deliveries, err)
	}
	var status string
	if err = db.sql.QueryRow(`SELECT status FROM tasks WHERE id='task-2'`).Scan(&status); err != nil || status != "ready" {
		t.Fatal("rejected Claim consumed ready task", status, err)
	}
}

func TestDiagnosticsDetailedAdmissionRejectsUnrepresentableScope(t *testing.T) {
	for _, bad := range []string{strings.Repeat("x", 257), "nul\x00id", string([]byte{'x', 0xff})} {
		for _, field := range []string{"run", "task"} {
			t.Run(fmt.Sprintf("%s-%x", field, []byte(bad)[:2]), func(t *testing.T) {
				db, err := Open(filepath.Join(t.TempDir(), "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				p := diagnosticsPlan(&contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"})
				if field == "run" {
					p.Run.ID = bad
					p.Tasks[0].RunID = bad
				} else {
					p.Tasks[0].ID = bad
				}
				if _, err = db.SubmitPlan(context.Background(), p); err == nil {
					t.Fatal("detailed task cannot be represented by M1 scope")
				}
				var count int
				if err = db.sql.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 0 {
					t.Fatal("invalid scope admitted rows", count, err)
				}
			})
		}
	}
}

func TestDiagnosticsCandidateReviewRetryReconstructsHistoricalGrant(t *testing.T) {
	for _, vector := range []bool{false, true} {
		t.Run(fmt.Sprint(vector), func(t *testing.T) {
			f := newReviewRetryFixturePlan(t, adapter.ProviderClaude, contract.EventStopped, nil, vector, false, func(tasks []TaskSpec) []TaskSpec {
				for i := range tasks {
					if tasks[i].ID == "review" {
						tasks[i].Diagnostics = &contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"}
					}
				}
				return tasks
			})
			assertDiagnosticsReservation(t, f.db, f.grant, 1)
			if _, err := f.db.QueueRetry(context.Background(), f.spec); err != nil {
				t.Fatal("historical detailed grant lost from retry proof", err)
			}
			next, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			assertDiagnosticsReservation(t, f.db, next, 1)
			if next.AttemptID == f.grant.AttemptID || next.Diagnostics.ReservationID == f.grant.Diagnostics.ReservationID {
				t.Fatal("retry reused old attempt namespace")
			}
			pending, err := f.db.PendingLaunches(context.Background(), f.host, 1)
			if err != nil || len(pending) != 1 {
				t.Fatal(pending, err)
			}
			a, _ := json.Marshal(next)
			b, _ := json.Marshal(pending[0])
			if string(a) != string(b) {
				t.Fatal("candidate replay changed grant")
			}
		})
	}
}

func TestDiagnosticsDoesNotAuthorizeInterruptedCodexWithoutSeal(t *testing.T) {
	f := newTypedContinuationFixtureWithDiagnostics(t, adapter.ProviderCodex, "task", nil, &contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"})
	f.start(t)
	spec := f.interrupted(t, 250)
	before := continuationQuotaState(t, f.db)
	if _, err := f.db.QueueContinuation(context.Background(), spec); err != CodeError("codex_continuation_source_unsupported") {
		t.Fatal("diagnostics bypassed Codex seal boundary", err)
	}
	if continuationQuotaState(t, f.db) != before {
		t.Fatal("rejected continuation changed execution state")
	}
	assertDiagnosticsReservation(t, f.db, f.grant, 1)
}

func TestDiagnosticsAllowanceExactBoundary(t *testing.T) {
	db, h := diagnosticsLaunchFixture(t, "detailed", 2)
	db.artifactLimit = detailedArtifactReservationBytes + contract.HostArtifactReservedBytes
	first, err := db.ClaimReady(context.Background(), h.LaunchID, 1, 2)
	if err != nil {
		t.Fatal("exact allowance rejected", err)
	}
	assertDiagnosticsReservation(t, db, first, 1)
	if _, err = db.ClaimReady(context.Background(), h.LaunchID, 1, 2); err != CodeError("artifact_spool_full") {
		t.Fatal("exact allowance reused", err)
	}
}

func TestDiagnosticsSignedLaunchSizeFailureRollsBackReservation(t *testing.T) {
	ctx := context.Background()
	active := func() (originalOwnerFixture, OriginalOwnerBindResult) {
		t.Helper()
		f := newOriginalOwnerFixtureWithDiagnostics(t, &contract.DiagnosticsPolicy{Version: 1, Mode: "detailed"})
		intent, err := f.db.PrepareOriginalOwner(ctx, f.prepare)
		if err != nil {
			t.Fatal(err)
		}
		bound, err := f.db.BindOriginalOwner(ctx, f.bindSpec(intent), f.key)
		if err != nil {
			t.Fatal(err)
		}
		a := appliedFixture(t, f, bound)
		if err = f.db.CommitOriginalOwnerApplied(ctx, a, f.key.Public().(ed25519.PublicKey)); err != nil {
			t.Fatal(err)
		}
		if err = f.db.ActivateOriginalOwner(ctx, a, f.key.Public().(ed25519.PublicKey)); err != nil {
			t.Fatal(err)
		}
		return f, bound
	}
	prototype, bound := active()
	g, err := prototype.db.ClaimReady(ctx, prototype.prepare.Host.LaunchID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertDiagnosticsReservation(t, prototype.db, g, 1)
	body, _ := json.Marshal(g)
	frame, _ := json.Marshal(ipc.Envelope{Version: ipc.Version, Kind: ipc.KindLaunch, RequestID: g.CommandID, Epoch: 1, Payload: body})
	payload, _ := json.Marshal(map[string]string{"pad": strings.Repeat("p", ipc.MaxMessageSize-len(frame)-32)})
	g.AdapterPayload = payload
	if err = validateLaunchSize(g); err != nil {
		t.Fatal("fixture must fit legacy frame", err)
	}
	body, _ = json.Marshal(g)
	if _, err = contract.SignOwnerCommand(contract.OwnerCommand{Version: 1, Permit: bound.Permit, Kind: "launch", RequestID: g.CommandID, Body: body}, prototype.key); err == nil {
		t.Fatal("fixture must exceed signed frame")
	}
	f, _ := active()
	if _, err = f.db.sql.Exec(`UPDATE task_runtime SET adapter_payload=? WHERE host_launch_id=?`, string(payload), f.prepare.Host.LaunchID); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err = f.db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ClaimReady(ctx, f.prepare.Host.LaunchID, 1, 1); err != CodeError("launch_message_too_large") {
		t.Fatal("untransmittable detailed grant committed", err)
	}
	var rows int
	if err = f.db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM attempts)+(SELECT COUNT(*) FROM segments)+(SELECT COUNT(*) FROM budget_runtime)+(SELECT COUNT(*) FROM managed_artifact_reservations)+(SELECT COUNT(*) FROM diagnostic_delivery_state),(SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id='')`).Scan(&rows, &after); err != nil || rows != 0 || after != before {
		t.Fatal("signed size failure leaked reservation/accounting", rows, before, after, err)
	}
}
