package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestLegacyMigrationExcludesPartiallyRetainedManagedProgress(t *testing.T) {
	for _, size := range []int{1, 64 * 1024} {
		t.Run(stringSize(size), func(t *testing.T) {
			f := newTypedContinuationFixture(t, adapter.ProviderClaude)
			f.start(t)
			ref := managedHostArtifactForTest(t, f.db, f.grant, "progress", 0, bytes.Repeat([]byte("p"), size))
			event := f.event(contract.EventProgress)
			event.EventID, event.Sequence, event.Artifact, event.PayloadHash = "managed-migration-progress", 1, ref, ref.SHA256
			if err := f.db.CommitHostProgress(t.Context(), event, f.receipt.LaunchID, f.receipt.LaunchToken, 10, "host"); err != nil {
				t.Fatal(err)
			}
			before := continuationQuotaState(t, f.db)
			downgradeLegacyArtifactSchema(t, f.db)

			// Schema migration is data-only even when a previously verified
			// physical object has since become unavailable.
			if err := os.Remove(ref.Path); err != nil {
				t.Fatal(err)
			}
			path := f.db.path
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				db, err := Open(path)
				if err != nil {
					t.Fatal("normal managed progress prevented schema22 upgrade", err)
				}
				var count int
				if err = db.sql.QueryRow(`SELECT COUNT(*) FROM legacy_artifact_claims`).Scan(&count); err != nil || count != 0 {
					db.Close()
					t.Fatal("managed progress became unverified legacy", count, err)
				}
				after := continuationQuotaState(t, db)
				if after != before {
					db.Close()
					t.Fatal("managed progress accounting changed", before, after)
				}
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func seedLegacySource(t *testing.T, class, body string, raw, reserve int) *DB {
	t.Helper()
	db, g := quotaFixture(t)
	downgradeLegacyArtifactSchema(t, db)
	var err error
	switch class {
	case "runtime":
		_, err = db.sql.Exec(quotaInsert, "legacy-source", g.HostID, 1, g.AttemptID, g.SegmentID, body)
	case "report":
		_, err = db.sql.Exec(`INSERT INTO report_events(event_id,capability_id,sequence,kind,payload_hash,body_json,event_revision,action_slot,delivery_status,accounted_bytes,created_at) VALUES('legacy-source',?,1,'question','hash',?,0,'','internal',?,'now')`, g.ReportCapabilityID, body, len(body)+raw+reserve)
		if err == nil {
			_, err = db.sql.Exec(`UPDATE report_capabilities SET used_bytes=? WHERE capability_id=?`, len(body)+raw+reserve, g.ReportCapabilityID)
		}
	case "progress":
		_, err = db.sql.Exec(`INSERT INTO host_progress(event_id,task_id,segment_id,sequence,body_json,accounted_bytes) VALUES('legacy-source',?,?,1,?,?)`, g.TaskID, g.SegmentID, body, len(body)+raw)
	}
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func reopenLegacySource(t *testing.T, db *DB) *DB {
	t.Helper()
	path := db.path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestLegacySourceOriginalDeclarationAndGuards(t *testing.T) {
	const declaration = `{ "unknown": ["中文", 2], "size" : 7, "path" : "/legacy" }`
	for _, class := range []string{"runtime", "report", "progress"} {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/malformed=%v", class, malformed), func(t *testing.T) {
				body := `{"kind":"result","artifact":` + declaration + `}`
				expected, state := declaration, "legacy_pending"
				if malformed {
					body = `{"kind":"result","artifact":{"size":7,"size":1}}`
					expected, state = body, "malformed"
				}
				db := reopenLegacySource(t, seedLegacySource(t, class, body, 7, 4096))
				var saved, hash, gotState string
				if err := db.sql.QueryRow(`SELECT declaration_json,declaration_sha256,state FROM legacy_artifact_claims WHERE source_class=? AND source_id='legacy-source'`, class).Scan(&saved, &hash, &gotState); err != nil || saved != expected || hash != runtimeHash(expected) || gotState != state {
					t.Fatal(saved, hash, gotState, err)
				}
				table := map[string]string{"runtime": "runtime_events", "report": "report_events", "progress": "host_progress"}[class]
				identity := map[string]string{"runtime": "task_id", "report": "capability_id", "progress": "task_id"}[class]
				for _, assignment := range []string{`event_id='moved'`, identity + `='other'`, `body_json='{"artifact":{"size":8}}'`} {
					if _, err := db.sql.Exec("UPDATE " + table + " SET " + assignment + " WHERE event_id='legacy-source'"); err == nil {
						t.Fatal("mutable source", assignment)
					}
				}
				if _, err := db.sql.Exec("DELETE FROM " + table + " WHERE event_id='legacy-source'"); (err != nil) != malformed {
					t.Fatal("source deletion must retain unknown charge only", err)
				}
				var count int
				if err := db.sql.QueryRow(`SELECT COUNT(*) FROM legacy_artifact_claims`).Scan(&count); err != nil || count != 1 {
					t.Fatal("claim refunded", count, err)
				}
			})
		}
	}
}

func TestLegacyProgressAndQuestionRoundTripPreservesCounters(t *testing.T) {
	for _, class := range []string{"report", "progress"} {
		t.Run(class, func(t *testing.T) {
			body := `{"kind":"question","artifact":{"size":7}}`
			db := reopenLegacySource(t, seedLegacySource(t, class, body, 7, 4096))
			var accounted int
			table := "host_progress"
			want := len(body)
			if class == "report" {
				table = "report_events"
				want += 4096
			}
			if err := db.sql.QueryRow("SELECT accounted_bytes FROM " + table + " WHERE event_id='legacy-source'").Scan(&accounted); err != nil || accounted != want {
				t.Fatal("control/reserve lost", accounted, want, err)
			}
			before := continuationQuotaState(t, db)
			downgradeLegacyArtifactSchema(t, db)
			db = reopenLegacySource(t, db)
			if continuationQuotaState(t, db) != before {
				t.Fatal("v22-v21-v22 changed persisted state")
			}
		})
	}
}

func TestLegacyMigrationRejectsInsufficientControlAtomically(t *testing.T) {
	for _, mode := range []string{"report-accounted", "progress-accounted", "capability-used", "capability-storage", "run-storage", "question-reserve"} {
		t.Run(mode, func(t *testing.T) {
			class := "report"
			if mode == "progress-accounted" {
				class = "progress"
			}
			db := seedLegacySource(t, class, `{"artifact":{"size":7}}`, 7, 0)
			stmt := map[string]string{
				"report-accounted":   `UPDATE report_events SET accounted_bytes=7`,
				"progress-accounted": `UPDATE host_progress SET accounted_bytes=7`,
				"capability-used":    `UPDATE report_capabilities SET used_bytes=7`,
				"capability-storage": `UPDATE storage_usage SET total_bytes=7 WHERE scope='capability'`,
				"run-storage":        `UPDATE storage_usage SET total_bytes=7 WHERE scope='run'`,
				"question-reserve":   `UPDATE report_events SET delivery_status='pending_session'`,
			}[mode]
			if _, err := db.sql.Exec(stmt); err != nil {
				t.Fatal(err)
			}
			before := continuationQuotaState(t, db)
			path := db.path
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if opened, err := Open(path); err == nil {
				opened.Close()
				t.Fatal("invalid historical accounting upgraded")
			}
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			old := &DB{sql: raw, path: path}
			if continuationQuotaState(t, old) != before {
				t.Fatal("migration escaped rollback")
			}
			var n int
			if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'legacy_%' OR name='runtime_legacy_artifact_binding'`).Scan(&n); err != nil || n != 0 {
				t.Fatal("migration DDL escaped rollback", n, err)
			}
		})
	}
}

func TestLegacyQuestionSessionPreservesOriginalArtifactDeclaration(t *testing.T) {
	db, g := quotaFixture(t)
	downgradeLegacyArtifactSchema(t, db)
	e := contract.Event{Version: 1, ProducerID: g.ReportCapabilityID, EventID: "legacy-question", Sequence: 1, Kind: contract.EventQuestion, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, PayloadHash: runtimeHash("old"), QuestionID: "question", QuestionRevision: 1, QuestionKind: "technical"}
	const declaration = `{ "unknown" : "<future>&中文", "size" : 7 }`
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body[:len(body)-1], []byte(`,"artifact":`+declaration+`}`)...)
	accounted := len(body) + 7 + 4096
	if _, err = db.sql.Exec(`INSERT INTO report_events(event_id,capability_id,sequence,kind,payload_hash,body_json,event_revision,action_slot,delivery_status,accounted_bytes,created_at) VALUES(?,?,1,'question',?,?,1,'legacy-question-action','pending_session',?,'now')`, e.EventID, g.ReportCapabilityID, e.PayloadHash, string(body), accounted); err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`UPDATE report_capabilities SET used_bytes=? WHERE capability_id=?`, accounted, g.ReportCapabilityID); err != nil {
		t.Fatal(err)
	}
	db = reopenLegacySource(t, db)
	for n, kind := range []string{contract.EventPrepared, contract.EventSpawned, contract.EventRunning, contract.EventSession} {
		h := contract.Event{Version: 1, ProducerID: g.HostID, EventID: fmt.Sprintf("host-session-%d", n), Sequence: int64(n + 1), Kind: kind, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, PayloadHash: runtimeHash(kind)}
		if kind == contract.EventSession {
			h.SessionKind, h.SessionID = "conversation-id", "trusted-session"
		}
		if _, err = db.CommitHostEvent(t.Context(), h); err != nil {
			t.Fatal("authenticated session must materialize legacy question", kind, err)
		}
	}
	var saved, hash, session, unknown, status string
	var after int
	if err = db.sql.QueryRow(`SELECT l.declaration_json,l.declaration_sha256,json_extract(e.body_json,'$.session_id'),json_extract(e.body_json,'$.artifact.unknown'),e.delivery_status,e.accounted_bytes FROM report_events e JOIN legacy_artifact_claims l ON l.source_id=e.event_id AND l.source_class='report' WHERE e.event_id=?`, e.EventID).Scan(&saved, &hash, &session, &unknown, &status, &after); err != nil {
		t.Fatal(err)
	}
	if saved != declaration || hash != runtimeHash(declaration) || session != "trusted-session" || unknown != "<future>&中文" || status != "pending" || after != accounted-7 {
		t.Fatal("session rewrote original declaration or charge", saved, hash, session, unknown, status, after)
	}
	page, err := db.CollectPending(t.Context(), g.TaskID, "", 0, false)
	if err != nil || len(page.Events) != 1 || page.Events[0].SessionID != "trusted-session" {
		t.Fatal("question not delivered", page, err)
	}
}

func TestLegacyManagedReportRequiresCompleteOriginalScope(t *testing.T) {
	for _, mutation := range []string{"none", "scope_key", "run_id", "namespace_sha256"} {
		t.Run(mutation, func(t *testing.T) {
			db, spec, source := newReportIntakeFixture(t)
			if _, err := db.CommitReportEvent(t.Context(), spec); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			downgradeLegacyArtifactSchema(t, db)
			if mutation != "none" {
				if mutation == "run_id" {
					if _, err := db.sql.Exec(`INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) SELECT 'foreign',controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode FROM runs LIMIT 1`); err != nil {
						t.Fatal(err)
					}
				}
				// Model corrupt historical scope while retaining the production
				// trigger definition around this fixture-only mutation.
				var ddl string
				if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='artifact_reservation_identity_immutable'`).Scan(&ddl); err != nil {
					t.Fatal(err)
				}
				if _, err := db.sql.Exec(`DROP TRIGGER artifact_reservation_identity_immutable`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.sql.Exec("UPDATE managed_artifact_reservations SET " + mutation + "='foreign' WHERE class='report'"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.sql.Exec(ddl); err != nil {
					t.Fatal(err)
				}
			}
			var before int
			if err := db.sql.QueryRow(`SELECT accounted_bytes FROM report_events WHERE event_id=?`, spec.EventID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			db = reopenLegacySource(t, db)
			var count, accounted int
			if err := db.sql.QueryRow(`SELECT (SELECT COUNT(*) FROM legacy_artifact_claims WHERE state='malformed'),accounted_bytes FROM report_events WHERE event_id=?`, spec.EventID).Scan(&count, &accounted); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mutation == "none" {
				want = 0
			}
			if count != want || accounted != before {
				t.Fatal("unproved managed receipt refunded control", count, accounted, before)
			}
		})
	}
}

func TestLegacyEscapedExitedDoesNotInventRawClaim(t *testing.T) {
	db := reopenLegacySource(t, seedLegacySource(t, "runtime", `{"kind":"\u0065xited","artifact":{"size":7}}`, 7, 0))
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM legacy_artifact_claims`).Scan(&count); err != nil || count != 0 {
		t.Fatal("exited reference became new raw charge", count, err)
	}
}

func stringSize(n int) string {
	if n == 1 {
		return "tiny"
	}
	return "large"
}

func TestLegacyMigrationRejectsAmbiguousJSONClassification(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate_artifact": `{"kind":"result","artifact":{"size":7},"artifact":null}`,
		"duplicate_size":     `{"kind":"result","artifact":{"size":7,"size":1}}`,
		"duplicate_kind":     `{"kind":"result","kind":"exited","artifact":{"size":7}}`,
	} {
		t.Run(name, func(t *testing.T) {
			db, g := quotaFixture(t)
			downgradeLegacyArtifactSchema(t, db)
			if _, err := db.sql.Exec(quotaInsert, "ambiguous", g.HostID, 1, g.AttemptID, g.SegmentID, body); err != nil {
				t.Fatal(err)
			}
			path := db.path
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			var count int
			if err := opened.sql.QueryRow(`SELECT COUNT(*) FROM legacy_artifact_claims WHERE state='malformed'`).Scan(&count); err != nil || count != 1 {
				t.Fatal("ambiguous history silently classified", count, err)
			}
			var saved string
			if err := opened.sql.QueryRow(`SELECT body_json FROM runtime_events WHERE event_id='ambiguous'`).Scan(&saved); err != nil || saved != body {
				t.Fatal("history rewritten", saved, err)
			}
			tx, err := opened.sql.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := opened.managedArtifactAdmission(t.Context(), tx, 1, 0); err == nil {
				t.Fatal("ambiguous original raw charge omitted from admission")
			}
		})
	}
}

func TestLegacyCurrentSegmentTerminalRecordsExistingClaimOverAllowance(t *testing.T) {
	db, g := quotaFixture(t)
	historicalHostGrantForTest(t, db, &g)
	db.artifactLimit = 0
	e := contract.Event{Version: 1, ProducerID: g.HostID, EventID: "late-legacy", Sequence: 1, Kind: contract.EventResult, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, WorkRevision: g.WorkRevision, ExecutionEpoch: g.ExecutionEpoch, PayloadHash: runtimeHash("old"), Artifact: &contract.ArtifactRef{ID: "legacy", Path: "/old/source", Size: 3, SHA256: runtimeHash("old")}}
	ack, err := db.CommitHostEvent(t.Context(), e)
	if err != nil {
		t.Fatal("preexisting authenticated legacy output rejected as a new reservation", err)
	}
	again, err := db.CommitHostEvent(t.Context(), e)
	if err != nil || ack != again {
		t.Fatal("legacy exact replay", err)
	}
	var count, raw int
	if err := db.sql.QueryRow(`SELECT COUNT(*),SUM(raw_bytes) FROM legacy_artifact_claims`).Scan(&count, &raw); err != nil || count != 1 || raw != 3 {
		t.Fatal(count, raw, err)
	}
	body, _ := json.Marshal(e)
	var claimed int
	if err := db.sql.QueryRow(`SELECT legacy_artifact_claimed FROM runtime_events WHERE event_id=? AND body_json=?`, e.EventID, string(body)).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatal(claimed, err)
	}
}
