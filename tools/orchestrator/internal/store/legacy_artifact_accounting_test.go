package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func TestLegacyClaimRequiresOriginalIntegerSize(t *testing.T) {
	for name, body := range map[string]string{
		"missing":    `{"kind":"result","artifact":{"id":"x"}}`,
		"fractional": `{"kind":"result","artifact":{"size":1.5}}`,
		"negative":   `{"kind":"result","artifact":{"size":-1}}`,
		"overflow":   `{"kind":"result","artifact":{"size":9223372036854775808}}`,
		"string":     `{"kind":"result","artifact":{"size":"1"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if claim := legacyClaimFromBody("runtime", name, "run", "", []byte(body), true); claim.state != "malformed" {
				t.Fatalf("claim=%+v", claim)
			}
		})
	}
	if claim := legacyClaimFromBody("runtime", "zero", "run", "", []byte(`{"kind":"result","artifact":{"size":0}}`), true); claim.state != "legacy_pending" || claim.raw != 0 {
		t.Fatalf("zero-size declaration=%+v", claim)
	}
}

func downgradeLegacyArtifactSchema(t *testing.T, db *DB) {
	t.Helper()
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// A schema22 fixture stores unmanaged legacy raw bytes in the immutable
	// claim ledger, rather than in control usage.  Reconstruct the schema21
	// counters before dropping that ledger so a later Open exercises one (not
	// two) raw/control split.
	if _, err = tx.Exec(`
UPDATE storage_usage SET total_bytes=total_bytes-COALESCE((SELECT SUM(64+length(CAST(source_class AS BLOB))+length(CAST(source_id AS BLOB))+length(CAST(run_id AS BLOB))+length(CAST(capability_id AS BLOB))+length(CAST(declaration_json AS BLOB))+length(CAST(declaration_sha256 AS BLOB))+length(CAST(state AS BLOB))) FROM legacy_artifact_claims l WHERE storage_usage.scope='global' OR (storage_usage.scope='run' AND storage_usage.scope_id=l.run_id)),0);
UPDATE storage_usage SET total_bytes=total_bytes+COALESCE((SELECT SUM(raw_bytes) FROM legacy_artifact_claims l WHERE l.state='legacy_pending' AND l.source_class='runtime' AND (storage_usage.scope='global' OR (storage_usage.scope='run' AND storage_usage.scope_id=l.run_id))),0);
UPDATE report_events SET accounted_bytes=accounted_bytes+COALESCE((SELECT raw_bytes FROM legacy_artifact_claims l WHERE l.source_class='report' AND l.source_id=report_events.event_id AND l.state='legacy_pending'),0);
UPDATE report_capabilities SET used_bytes=used_bytes+COALESCE((SELECT SUM(raw_bytes) FROM legacy_artifact_claims l WHERE l.source_class='report' AND l.capability_id=report_capabilities.capability_id AND l.state='legacy_pending'),0);
UPDATE host_progress SET accounted_bytes=accounted_bytes+COALESCE((SELECT raw_bytes FROM legacy_artifact_claims l WHERE l.source_class='progress' AND l.source_id=host_progress.event_id AND l.state='legacy_pending'),0);`); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		if _, err = tx.Exec("DROP TRIGGER IF EXISTS quota_runtime_events_" + op); err != nil {
			t.Fatal(err)
		}
		refs := []string{"NEW"}
		if op == "DELETE" {
			refs = []string{"OLD"}
		} else if op == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		}
		body := ""
		for _, ref := range refs {
			cost := "(" + managedHostRuntimeBytes(ref+".body_json") + ")"
			for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", "(SELECT run_id FROM tasks WHERE id=" + ref + ".task_id)"}} {
				if ref == "OLD" {
					body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-%s WHERE scope='%s' AND scope_id=%s;", cost, scope.name, scope.id)
				} else {
					body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,%s,0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, cost)
				}
			}
		}
		when := op
		if op == "UPDATE" {
			when += " OF body_json,task_id"
		}
		if _, err = tx.Exec("CREATE TRIGGER quota_runtime_events_" + op + " AFTER " + when + " ON runtime_events BEGIN " + body + " END"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(`DROP TRIGGER IF EXISTS runtime_legacy_artifact_claimed_immutable; DROP TRIGGER IF EXISTS runtime_legacy_artifact_body_immutable; DROP TRIGGER IF EXISTS legacy_artifact_claims_no_update; DROP TRIGGER IF EXISTS legacy_artifact_claims_no_delete; DROP TABLE IF EXISTS legacy_artifact_claims; ALTER TABLE runtime_events DROP COLUMN legacy_artifact_claimed; UPDATE schema_meta SET version=21`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyArtifactMigrationSplitsRuntimeTerminalAndExited(t *testing.T) {
	db, g := quotaFixture(t)
	downgradeHostMetadataSchema(t, db)
	result := contract.Event{Version: 1, EventID: "legacy-result", ProducerID: g.HostID, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, Kind: contract.EventResult, Artifact: &contract.ArtifactRef{ID: "result-output.bin", Path: "/old/result", Size: 123, SHA256: runtimeHash("result")}}
	exited := result
	exited.EventID = "legacy-exited"
	exited.Kind = contract.EventExited
	for n, e := range []contract.Event{result, exited} {
		b, _ := json.Marshal(e)
		if _, err := db.sql.Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,1,'legacy','internal','now')`, e.EventID, e.ProducerID, n+1, e.TaskID, e.AttemptID, e.SegmentID, e.PayloadHash, string(b)); err != nil {
			t.Fatal(err)
		}
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
	var claims, raw int
	if err = opened.sql.QueryRow(`SELECT COUNT(*),COALESCE(SUM(raw_bytes),0) FROM legacy_artifact_claims WHERE state='legacy_pending'`).Scan(&claims, &raw); err != nil || claims != 1 || raw != 123 {
		t.Fatalf("claims=%d raw=%d err=%v", claims, raw, err)
	}
	var refs int
	if err = opened.sql.QueryRow(`SELECT COUNT(*) FROM managed_artifact_refs`).Scan(&refs); err != nil || refs != 0 {
		t.Fatalf("migration granted managed ref: %d %v", refs, err)
	}
}

func TestLegacyArtifactMalformedBlocksReservationAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeHostMetadataSchema(t, db)
	if _, err = db.sql.Exec(`INSERT INTO legacy_artifact_claims(source_class,source_id,run_id,capability_id,declaration_json,declaration_sha256,raw_bytes,state) VALUES('runtime','bad','run','','{}','bad',0,'malformed')`); err == nil {
		t.Fatal("schema22 table remained after downgrade")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.sql.Exec(`INSERT INTO legacy_artifact_claims(source_class,source_id,run_id,capability_id,declaration_json,declaration_sha256,raw_bytes,state) VALUES('runtime','bad','run','','{}','bad',0,'malformed')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = db.legacyArtifactAdmission(t.Context(), tx, 1); err == nil || err.Error() != "legacy_artifact_unclassified" {
		t.Fatalf("admission=%v", err)
	}
}

func TestLegacyArtifactClaimsBlockNewReservationAndOverflow(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.artifactLimit = 5
	if _, err = db.sql.Exec(`INSERT INTO legacy_artifact_claims(source_class,source_id,run_id,capability_id,declaration_json,declaration_sha256,raw_bytes,state) VALUES('runtime','full','run','','{}','full',5,'legacy_pending')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = db.managedArtifactAdmission(t.Context(), tx, 1, 0); err == nil || err.Error() != "artifact_spool_full" {
		t.Fatalf("legacy limit admission=%v", err)
	}
	_ = tx.Rollback()
	if _, err = db.sql.Exec(`INSERT INTO legacy_artifact_claims(source_class,source_id,run_id,capability_id,declaration_json,declaration_sha256,raw_bytes,state) VALUES('runtime','overflow','run','','{}','overflow',9223372036854775807,'legacy_pending')`); err != nil {
		t.Fatal(err)
	}
	tx, err = db.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = db.legacyArtifactAdmission(t.Context(), tx, 1); err == nil || err.Error() != "artifact_spool_full" {
		t.Fatalf("overflow admission=%v", err)
	}
	_ = tx.Rollback()
}

func TestLegacyRuntimeClaimFreezesRawClassificationAndArtifact(t *testing.T) {
	db, g := quotaFixture(t)
	downgradeLegacyArtifactSchema(t, db)
	e := contract.Event{Version: 1, EventID: "frozen", ProducerID: g.HostID, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, Kind: contract.EventResult, Artifact: &contract.ArtifactRef{ID: "legacy", Size: 9, SHA256: runtimeHash("old")}}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at,host_artifact_verified) VALUES(?,?,?,?,?,?,?,?,1,'legacy','internal','now',0)`, e.EventID, e.ProducerID, 1, e.TaskID, e.AttemptID, e.SegmentID, e.PayloadHash, string(body)); err != nil {
		t.Fatal(err)
	}
	path := db.path
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if _, err = opened.sql.Exec(`UPDATE runtime_events SET body_json=replace(body_json,'"size":9','"size":10') WHERE event_id='frozen'`); err == nil {
		t.Fatal("legacy artifact body was mutable")
	}
	var charged int64
	if err = opened.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&charged); err != nil {
		t.Fatal(err)
	}
	if _, err = opened.sql.Exec(`DELETE FROM runtime_events WHERE event_id='frozen'`); err != nil {
		t.Fatal(err)
	}
	var refunded int64
	if err = opened.sql.QueryRow(`SELECT total_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&refunded); err != nil {
		t.Fatal(err)
	}
	if charged-refunded != int64(len(body)) {
		t.Fatalf("delete changed frozen raw charge: %d", charged-refunded)
	}
}

func TestLegacyArtifactMigrationSplitsProgressRawAndPreservesClaim(t *testing.T) {
	db, g := quotaFixture(t)
	downgradeLegacyArtifactSchema(t, db)
	e := contract.Event{Version: 1, EventID: "legacy-progress", ProducerID: g.HostID, RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, CommandID: g.CommandID, Kind: contract.EventProgress, Artifact: &contract.ArtifactRef{ID: "provider-progress", Size: 37, SHA256: runtimeHash("progress")}}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.sql.Exec(`INSERT INTO host_progress(event_id,task_id,segment_id,sequence,body_json,accounted_bytes) VALUES(?,?,?,?,?,?)`, e.EventID, e.TaskID, e.SegmentID, 1, string(body), len(body)+37); err != nil {
		t.Fatal(err)
	}
	var beforeTotal, beforeProgress int64
	if err = db.sql.QueryRow(`SELECT total_bytes,progress_bytes FROM storage_usage WHERE scope='global' AND scope_id=''`).Scan(&beforeTotal, &beforeProgress); err != nil {
		t.Fatal(err)
	}
	path := db.path
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	var accounted, total, progress, raw, metadata int64
	if err = opened.sql.QueryRow(`SELECT p.accounted_bytes,u.total_bytes,u.progress_bytes,l.raw_bytes,64+length(CAST(l.source_class AS BLOB))+length(CAST(l.source_id AS BLOB))+length(CAST(l.run_id AS BLOB))+length(CAST(l.capability_id AS BLOB))+length(CAST(l.declaration_json AS BLOB))+length(CAST(l.declaration_sha256 AS BLOB))+length(CAST(l.state AS BLOB)) FROM host_progress p JOIN legacy_artifact_claims l ON l.source_class='progress' AND l.source_id=p.event_id JOIN storage_usage u ON u.scope='global' AND u.scope_id='' WHERE p.event_id=?`, e.EventID).Scan(&accounted, &total, &progress, &raw, &metadata); err != nil {
		t.Fatal(err)
	}
	if accounted != int64(len(body)) || raw != 37 || total != beforeTotal-raw+metadata || progress != beforeProgress-raw {
		t.Fatalf("progress split accounted=%d total=%d/%d progress=%d/%d raw=%d metadata=%d", accounted, total, beforeTotal, progress, beforeProgress, raw, metadata)
	}
}

var _ = sql.ErrNoRows
