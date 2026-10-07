package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func continuationQuotaUsage(t *testing.T, db *DB) (run, global int64) {
	t.Helper()
	if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='run' AND scope_id='run'`).Scan(&run); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&global); err != nil {
		t.Fatal(err)
	}
	return
}

func setContinuationQuotaRemaining(t *testing.T, db *DB, scope string, remaining int64) {
	t.Helper()
	run, global := continuationQuotaUsage(t, db)
	if scope == "run" {
		db.runControlLimit = run + remaining
	} else {
		db.userControlLimit = global + remaining
	}
}

// Complete logical state catches answers/ready sequence, grants, reservations,
// report capabilities and usage escaping a transaction that must roll back.
func continuationQuotaState(t *testing.T, db *DB) string {
	t.Helper()
	rows, err := db.sql.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err = rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	state := map[string][][]any{}
	for _, table := range tables {
		rows, err = db.sql.Query(`SELECT * FROM "` + table + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			refs := make([]any, len(columns))
			for i := range values {
				refs[i] = &values[i]
			}
			if err = rows.Scan(refs...); err != nil {
				t.Fatal(err)
			}
			state[table] = append(state[table], values)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertContinuationQuotaRefusedAtomically(t *testing.T, db *DB, before string, err error) {
	t.Helper()
	if err == nil || err.Error() != "control_spool_full" {
		t.Errorf("transaction must refuse exact logical growth: %v", err)
	}
	run, global := continuationQuotaUsage(t, db)
	if run > db.runControlLimit || global > db.userControlLimit {
		t.Errorf("committed usage exceeds limits: run=%d limit=%d global=%d limit=%d", run, db.runControlLimit, global, db.userControlLimit)
	}
	if after := continuationQuotaState(t, db); after != before {
		t.Error("quota refusal left business rows or storage_usage changed")
	}
	assertContinuationQuotaCounters(t, db)
}

// Independently recompute these single-run fixtures from actual stored rows.
// All Host events in this fixture have no artifacts. Managed report raw bytes
// have a separate reservation; control usage includes its durable metadata and
// the physical intake descriptor. Continuation integer columns each charge8.
func assertContinuationQuotaCounters(t *testing.T, db *DB) {
	t.Helper()
	var expected int64
	if err := db.sql.QueryRow(`SELECT COALESCE((SELECT SUM(length(CAST(body_json AS BLOB))) FROM runtime_events),0)+COALESCE((SELECT SUM(accounted_bytes) FROM report_events),0)`).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"attempt_provider_bindings", "continuation_decisions"} {
		rows, err := db.sql.Query(`SELECT name,type FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		var parts []string
		for rows.Next() {
			var name, kind string
			if err = rows.Scan(&name, &kind); err != nil {
				t.Fatal(err)
			}
			if kind == "INTEGER" {
				parts = append(parts, "8")
			} else {
				parts = append(parts, `length(CAST("`+name+`" AS BLOB))`)
			}
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		var charged int64
		if err = db.sql.QueryRow(`SELECT COALESCE(SUM(` + strings.Join(parts, "+") + `),0) FROM "` + table + `"`).Scan(&charged); err != nil {
			t.Fatal(err)
		}
		expected += charged
	}
	// Keep this independent of production cost helpers and quota triggers.
	for _, query := range []string{
		`SELECT COALESCE(SUM(64+length(CAST(id AS BLOB))+length(CAST(run_id AS BLOB))+length(CAST(task_id AS BLOB))+length(CAST(attempt_id AS BLOB))+length(CAST(producer_id AS BLOB))+length(CAST(class AS BLOB))+length(CAST(scope_key AS BLOB))+length(CAST(policy_sha256 AS BLOB))+length(CAST(namespace_sha256 AS BLOB))+length(CAST(state AS BLOB))+length(CAST(created_at AS BLOB))),0) FROM managed_artifact_reservations`,
		`SELECT COALESCE(SUM(64+length(CAST(id AS BLOB))+length(CAST(reservation_id AS BLOB))+length(CAST(run_id AS BLOB))+length(CAST(task_id AS BLOB))+length(CAST(capability_id AS BLOB))+length(CAST(event_id AS BLOB))+length(CAST(kind AS BLOB))+length(CAST(payload_hash AS BLOB))+2*length(CAST(descriptor_json AS BLOB))+length(CAST(phase AS BLOB))+length(CAST(created_at AS BLOB))),0) FROM managed_artifact_intakes`,
		`SELECT COALESCE(SUM(64+length(CAST(id AS BLOB))+length(CAST(reservation_id AS BLOB))+length(CAST(run_id AS BLOB))+length(CAST(slot AS BLOB))+COALESCE(length(CAST(intake_id AS BLOB)),0)+length(CAST(proof_json AS BLOB))),0) FROM managed_artifact_refs`,
	} {
		var charged int64
		if err := db.sql.QueryRow(query).Scan(&charged); err != nil {
			t.Fatal(err)
		}
		expected += charged
	}
	run, global := continuationQuotaUsage(t, db)
	if run != expected || global != expected || run > db.runControlLimit || global > db.userControlLimit {
		t.Errorf("real rows/counters/limits disagree: rows=%d run=%d global=%d runLimit=%d globalLimit=%d", expected, run, global, db.runControlLimit, db.userControlLimit)
	}
}

func admittedContinuationReportQuestion(t *testing.T, question string) (*typedContinuationFixture, AnswerSpec) {
	t.Helper()
	f := newTypedContinuationFixture(t, adapter.ProviderClaude)
	f.start(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	g := f.grant
	_, err = f.db.RegisterReportCapability(context.Background(), contract.ReportCapabilityRegistration{CapabilityID: g.ReportCapabilityID, ProducerID: f.host, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: 1, ExecutionEpoch: 1, TokenHash: runtimeHash("token"), CapabilityDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	path, body := filepath.Join(dir, "question.txt"), []byte("Choose a technical value")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"status": "waiting_user", "question_id": question, "question_revision": 1, "question_kind": "technical", "artifact": contract.ArtifactRef{ID: "technical-question", Path: path, Size: int64(len(body)), SHA256: runtimeHash(string(body))}})
	_, err = f.db.CommitReportEvent(context.Background(), ReportEventSpec{CapabilityID: g.ReportCapabilityID, Token: "token", EventID: "report-question", Sequence: 1, Kind: "question", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	stop := f.event(contract.EventStopped)
	stop.StopReason = "owner_stop_requested"
	f.send(t, stop)
	x := f.event(contract.EventExited)
	code := 130
	x.ExitCode, x.ActiveMS = &code, 200
	f.send(t, x)
	return f, AnswerSpec{TaskID: g.TaskID, WorkRevision: 1, QuestionID: question, QuestionRevision: 1, Answer: "Answer"}
}

func TestContinuationDecisionInsertChecksActualLongQuestionQuota(t *testing.T) {
	for _, scope := range []string{"run", "global"} {
		t.Run(scope, func(t *testing.T) {
			f, answer := admittedContinuationReportQuestion(t, strings.Repeat("q", 24*1024))
			setContinuationQuotaRemaining(t, f.db, scope, 16*1024)
			before := continuationQuotaState(t, f.db)
			_, err := f.db.AnswerQuestion(context.Background(), answer)
			assertContinuationQuotaRefusedAtomically(t, f.db, before, err)
			setContinuationQuotaRemaining(t, f.db, scope, 32*1024)
			if _, err = f.db.AnswerQuestion(context.Background(), answer); err != nil {
				t.Fatalf("valid long question refused with fitting row budget: %v", err)
			}
			assertContinuationQuotaCounters(t, f.db)
		})
	}
}

func TestContinuationBindingChecksActualLongTaskQuota(t *testing.T) {
	for _, scope := range []string{"run", "global"} {
		t.Run(scope, func(t *testing.T) {
			f := newTypedContinuationFixtureTask(t, adapter.ProviderAGY, strings.Repeat("t", 20*1024))
			for _, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning} {
				f.send(t, f.event(kind))
			}
			init := f.event(contract.EventSession)
			init.SessionKind, init.SessionID = f.sessionKind, f.sessionID
			body, _ := json.Marshal(init)
			// Runtime-event admission succeeds alone. Binding must check the
			// already charged event plus its full row in this same transaction.
			setContinuationQuotaRemaining(t, f.db, scope, int64(len(body))+16*1024)
			before := continuationQuotaState(t, f.db)
			_, err := f.db.CommitHostEvent(context.Background(), init)
			assertContinuationQuotaRefusedAtomically(t, f.db, before, err)
			setContinuationQuotaRemaining(t, f.db, scope, int64(len(body))+25*1024)
			if _, err = f.db.CommitHostEvent(context.Background(), init); err != nil {
				t.Fatalf("valid long task first Session refused with fitting combined budget: %v", err)
			}
			assertContinuationQuotaCounters(t, f.db)
		})
	}
}

func TestContinuationClaimChecksDecisionUpdateGrowth(t *testing.T) {
	for _, scope := range []string{"run", "global"} {
		t.Run(scope, func(t *testing.T) {
			f, answer := admittedContinuationReportQuestion(t, "short-question")
			if _, err := f.db.AnswerQuestion(context.Background(), answer); err != nil {
				t.Fatal(err)
			}
			setContinuationQuotaRemaining(t, f.db, scope, 1)
			before := continuationQuotaState(t, f.db)
			grant, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
			if err == nil && grant.SegmentID == "" {
				t.Error("fixture did not actually admit a continuation grant")
			}
			assertContinuationQuotaRefusedAtomically(t, f.db, before, err)
			// The admitted segment-ID representation is32 bytes; the target plus
			// queued->claimed grows by exactly33. Both scopes must accept that
			// tight positive boundary after the earlier transaction rolled back.
			setContinuationQuotaRemaining(t, f.db, scope, 33)
			if grant, err = f.db.ClaimReady(context.Background(), f.host, 1, 2); err != nil || grant.Continuation == nil {
				t.Fatalf("fitting claim growth refused: %+v %v", grant, err)
			}
			assertContinuationQuotaCounters(t, f.db)
		})
	}
}

func TestContinuationShortIDsFitAndReplayDoesNotGrowQuota(t *testing.T) {
	f, answer := admittedContinuationReportQuestion(t, "正常问题")
	runBefore, globalBefore := continuationQuotaUsage(t, f.db)
	if _, err := f.db.AnswerQuestion(context.Background(), answer); err != nil {
		t.Fatal(err)
	}
	runAfter, globalAfter := continuationQuotaUsage(t, f.db)
	if runAfter <= runBefore || runAfter-runBefore != globalAfter-globalBefore {
		t.Fatal("successful decision did not charge both scopes equally")
	}
	if _, err := f.db.AnswerQuestion(context.Background(), answer); err != nil {
		t.Fatal(err)
	}
	if run, global := continuationQuotaUsage(t, f.db); run != runAfter || global != globalAfter {
		t.Fatal("idempotent answer charged again")
	}
	grant, err := f.db.ClaimReady(context.Background(), f.host, 1, 2)
	if err != nil || grant.Continuation == nil {
		t.Fatalf("short decision claim: %+v %v", grant, err)
	}
	runClaimed, globalClaimed := continuationQuotaUsage(t, f.db)
	// Only target_segment_id and queued(6)->claimed(7) grow here.
	want := int64(len(grant.SegmentID) + 1)
	if runClaimed-runAfter != want || globalClaimed-globalAfter != want {
		t.Fatalf("actual claim delta run=%d global=%d want=%d", runClaimed-runAfter, globalClaimed-globalAfter, want)
	}
	for replay := 0; replay < 2; replay++ {
		pending, err := f.db.PendingLaunches(context.Background(), f.host, 1)
		if err != nil || len(pending) != 1 || pending[0].SegmentID != grant.SegmentID {
			t.Fatalf("pending replay: %+v %v", pending, err)
		}
	}
	if run, global := continuationQuotaUsage(t, f.db); run != runClaimed || global != globalClaimed || run > f.db.runControlLimit || global > f.db.userControlLimit {
		t.Fatal("pending replay charged again or exceeded quota")
	}
	assertContinuationQuotaCounters(t, f.db)
}
