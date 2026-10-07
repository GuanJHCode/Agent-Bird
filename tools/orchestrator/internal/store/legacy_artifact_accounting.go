package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// legacy_artifact_claims records accounting declarations only. It is not a
// filesystem receipt and cannot authorize a managed-artifact namespace.
const legacyArtifactClaimDDL = `
CREATE TABLE legacy_artifact_claims(
 source_class TEXT NOT NULL CHECK(source_class IN ('runtime','report','progress')),
 source_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 capability_id TEXT NOT NULL DEFAULT '',
 declaration_json TEXT NOT NULL,
 declaration_sha256 TEXT NOT NULL,
 raw_bytes INTEGER NOT NULL CHECK(typeof(raw_bytes)='integer' AND raw_bytes>=0),
 state TEXT NOT NULL CHECK(state IN ('legacy_pending','malformed')),
 PRIMARY KEY(source_class,source_id)
);
CREATE TRIGGER legacy_artifact_claims_no_update BEFORE UPDATE ON legacy_artifact_claims BEGIN SELECT RAISE(ABORT,'legacy_artifact_claim_immutable'); END;
CREATE TRIGGER legacy_artifact_claims_no_delete BEFORE DELETE ON legacy_artifact_claims BEGIN SELECT RAISE(ABORT,'legacy_artifact_claim_immutable'); END;
CREATE TRIGGER quota_legacy_artifact_claims_INSERT AFTER INSERT ON legacy_artifact_claims BEGIN
 INSERT INTO storage_usage VALUES('global','',64+length(CAST(NEW.source_class AS BLOB))+length(CAST(NEW.source_id AS BLOB))+length(CAST(NEW.run_id AS BLOB))+length(CAST(NEW.capability_id AS BLOB))+length(CAST(NEW.declaration_json AS BLOB))+length(CAST(NEW.declaration_sha256 AS BLOB))+length(CAST(NEW.state AS BLOB)),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;
 INSERT INTO storage_usage VALUES('run',NEW.run_id,64+length(CAST(NEW.source_class AS BLOB))+length(CAST(NEW.source_id AS BLOB))+length(CAST(NEW.run_id AS BLOB))+length(CAST(NEW.capability_id AS BLOB))+length(CAST(NEW.declaration_json AS BLOB))+length(CAST(NEW.declaration_sha256 AS BLOB))+length(CAST(NEW.state AS BLOB)),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;
END;`

type legacyArtifactClaim struct {
	class, id, run, capability, declaration, digest, state string
	raw                                                    int64
	absent                                                 bool
}

func legacyClaimFromBody(class, id, run, capability string, body []byte, excludeExited bool) legacyArtifactClaim {
	// A malformed declaration stores its full original source body as an
	// explicitly reversible sentinel. Its old control/raw charge is untouched.
	claim := legacyArtifactClaim{class: class, id: id, run: run, capability: capability, state: "malformed", declaration: string(body), digest: runtimeHash(string(body))}
	raw, size, absent, err := legacyDeclaration(body, excludeExited)
	if err != nil {
		return claim
	}
	claim.absent = absent
	if absent {
		return claim
	}
	claim.declaration, claim.digest, claim.raw, claim.state = string(raw), runtimeHash(string(raw)), size, "legacy_pending"
	return claim
}
func insertLegacyClaim(ctx context.Context, tx *sql.Tx, claim legacyArtifactClaim, admit bool, d *DB) error {
	if claim.state == "legacy_pending" && admit {
		if err := d.legacyArtifactAdmission(ctx, tx, claim.raw); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO legacy_artifact_claims(source_class,source_id,run_id,capability_id,declaration_json,declaration_sha256,raw_bytes,state) VALUES(?,?,?,?,?,?,?,?)`, claim.class, claim.id, claim.run, claim.capability, claim.declaration, claim.digest, claim.raw, claim.state)
	if err != nil {
		return mapConflict(err)
	}
	return nil
}

// legacyArtifactAdmission includes declarations and every unreleased managed
// promise. A malformed historical source deliberately blocks further promises.
func (d *DB) legacyArtifactAdmission(ctx context.Context, tx *sql.Tx, incoming int64) error {
	if incoming < 0 {
		return CodeError("artifact_spool_full")
	}
	var malformed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM legacy_artifact_claims WHERE state='malformed'`).Scan(&malformed); err != nil {
		return err
	}
	if malformed != 0 {
		return CodeError("legacy_artifact_unclassified")
	}
	var used int64
	rows, err := tx.QueryContext(ctx, `SELECT raw_bytes FROM legacy_artifact_claims WHERE state='legacy_pending' UNION ALL SELECT reserved_bytes FROM managed_artifact_reservations WHERE state!='released'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return err
		}
		if n < 0 || used > math.MaxInt64-n {
			return CodeError("artifact_spool_full")
		}
		used += n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if incoming > d.artifactLimit || used > d.artifactLimit-incoming {
		return CodeError("artifact_spool_full")
	}
	return nil
}

func runtimeLegacyRawBytes(column string) string {
	row := column[:len(column)-len(".body_json")]
	return "CASE WHEN " + row + ".legacy_artifact_claimed=1 THEN 0 ELSE CASE WHEN json_valid(" + column + ") THEN CASE WHEN json_extract(" + column + ",'$.kind')='exited' OR " + row + ".host_artifact_verified=1 THEN 0 ELSE COALESCE(json_extract(" + column + ",'$.artifact.size'),0) END ELSE 0 END END"
}

func migrateLegacyArtifactAccounting(tx *sql.Tx, d *DB) error {
	var invalid int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM storage_usage WHERE typeof(total_bytes)!='integer' OR typeof(progress_bytes)!='integer' OR total_bytes<0 OR progress_bytes<0 OR progress_bytes>total_bytes`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return CodeError("legacy_artifact_accounting_invalid")
	}
	if err := validateLegacySourceAccounting(tx); err != nil {
		return err
	}

	if _, err := tx.Exec(`ALTER TABLE runtime_events ADD COLUMN legacy_artifact_claimed INTEGER NOT NULL DEFAULT 0 CHECK(typeof(legacy_artifact_claimed)='integer' AND legacy_artifact_claimed IN (0,1));`); err != nil {
		return err
	}
	if _, err := tx.Exec(legacyArtifactClaimDDL); err != nil {
		return err
	}
	// Backfill runtime rows. Verified host slots already have a durable proof and
	// must never be reclassified as legacy declarations.
	rows, err := tx.Query(`SELECT e.event_id,t.run_id,e.body_json,e.host_artifact_verified FROM runtime_events e JOIN tasks t ON t.id=e.task_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, run, body string
		var verified int
		if err = rows.Scan(&id, &run, &body, &verified); err != nil {
			rows.Close()
			return err
		}
		if verified != 0 {
			continue
		}
		claim := legacyClaimFromBody("runtime", id, run, "", []byte(body), true)
		if claim.absent {
			continue
		}
		if err = insertLegacyClaim(context.Background(), tx, claim, false, d); err != nil {
			rows.Close()
			return err
		}
		if claim.state == "legacy_pending" {
			if _, err = tx.Exec(`UPDATE runtime_events SET legacy_artifact_claimed=1 WHERE event_id=?`, id); err != nil {
				rows.Close()
				return err
			}
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	// report/progress rows retain their old accounted total but move only a
	// proved-unmanaged declaration out of its control component.
	if err = migrateLegacyReportRows(tx, d); err != nil {
		return err
	}
	if err = migrateLegacyProgressRows(tx, d); err != nil {
		return err
	}
	if err = rebuildLegacyControlAccounting(tx); err != nil {
		return err
	}
	if err = rebuildLegacyRuntimeTriggers(tx); err != nil {
		return err
	}
	if err = installLegacySourceGuards(tx); err != nil {
		return err
	}
	return nil
}

func rebuildLegacyRuntimeTriggers(tx *sql.Tx) error {
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		if _, err := tx.Exec("DROP TRIGGER quota_runtime_events_" + op); err != nil {
			return err
		}
		refs := []string{"NEW"}
		if op == "DELETE" {
			refs = []string{"OLD"}
		} else if op == "UPDATE" {
			refs = []string{"OLD", "NEW"}
		}
		body := ""
		for _, ref := range refs {
			cost := "(length(CAST(" + ref + ".body_json AS BLOB))+" + runtimeLegacyRawBytes(ref+".body_json") + ")"
			for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", "(SELECT run_id FROM tasks WHERE id=" + ref + ".task_id)"}} {
				if ref == "OLD" {
					body += "UPDATE storage_usage SET total_bytes=total_bytes-" + cost + " WHERE scope='" + scope.name + "' AND scope_id=" + scope.id + ";"
				} else {
					body += "INSERT INTO storage_usage VALUES('" + scope.name + "'," + scope.id + "," + cost + ",0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;"
				}
			}
		}
		when := op
		if op == "UPDATE" {
			when += " OF body_json,task_id"
		}
		if _, err := tx.Exec("CREATE TRIGGER quota_runtime_events_" + op + " AFTER " + when + " ON runtime_events BEGIN " + body + " END"); err != nil {
			return err
		}
	}
	return nil
}

func migrateLegacyReportRows(tx *sql.Tx, d *DB) error {
	rows, err := tx.Query(`SELECT e.event_id,c.run_id,e.capability_id,e.body_json,e.accounted_bytes,CASE WHEN e.kind='question' AND e.delivery_status='pending_session' THEN 4096 ELSE 0 END,
	 EXISTS(SELECT 1 FROM managed_artifact_intakes i WHERE i.event_id=e.event_id)
 FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, run, cap, body string
		var accounted, reserve int64
		var managed int
		if err = rows.Scan(&id, &run, &cap, &body, &accounted, &reserve, &managed); err != nil {
			return err
		}
		if managed != 0 {
			valid, err := d.legacyReportHasOriginalProof(tx, id, cap, []byte(body))
			if err != nil {
				return err
			}
			if valid {
				continue
			}
			// Preserve the old control charge when its claimed managed
			// provenance cannot be reconstructed exactly.
			claim := legacyArtifactClaim{class: "report", id: id, run: run, capability: cap, state: "malformed", declaration: body, digest: runtimeHash(body)}
			if err := insertLegacyClaim(context.Background(), tx, claim, false, d); err != nil {
				return err
			}
			continue
		}
		claim := legacyClaimFromBody("report", id, run, cap, []byte(body), false)
		if claim.absent {
			continue
		}
		if claim.state == "legacy_pending" && (accounted < int64(len(body))+reserve || claim.raw > accounted-int64(len(body))-reserve) {
			return CodeError("legacy_artifact_accounting_invalid")
		}
		if claim.state == "legacy_pending" {
			var sufficient bool
			if err := tx.QueryRow(`SELECT used_bytes>=(SELECT COALESCE(SUM(accounted_bytes),0) FROM report_events WHERE capability_id=?) FROM report_capabilities WHERE capability_id=?`, cap, cap).Scan(&sufficient); err != nil {
				return err
			}
			if !sufficient {
				return CodeError("legacy_artifact_accounting_invalid")
			}
		}
		if err = insertLegacyClaim(context.Background(), tx, claim, false, d); err != nil {
			return err
		}
		if claim.state == "legacy_pending" {
			result, updateErr := tx.Exec(`UPDATE report_events SET accounted_bytes=accounted_bytes-? WHERE event_id=? AND accounted_bytes>=?`, claim.raw, id, claim.raw)
			if updateErr != nil {
				return updateErr
			}
			changed, updateErr := result.RowsAffected()
			if updateErr != nil || changed != 1 {
				return CodeError("legacy_artifact_accounting_invalid")
			}
			result, updateErr = tx.Exec(`UPDATE report_capabilities SET used_bytes=used_bytes-? WHERE capability_id=? AND used_bytes>=?`, claim.raw, cap, claim.raw)
			if updateErr != nil {
				return updateErr
			}
			changed, updateErr = result.RowsAffected()
			if updateErr != nil || changed != 1 {
				return CodeError("legacy_artifact_accounting_invalid")
			}
		}
	}
	return rows.Err()
}

// Check the source portion before adding ledger metadata. Metadata growth must
// never hide a deficient historical counter. Capability and progress counters
// contain only these events; global/run totals also include unrelated metadata,
// which is retained by applying exact deltas rather than reconstructed here.
func validateLegacySourceAccounting(tx *sql.Tx) error {
	// Schema21 also charged progress-owned ref and bridge metadata here.
	refCost := "64"
	for _, column := range []string{"id", "reservation_id", "run_id", "slot", "intake_id", "proof_json"} {
		refCost += "+COALESCE(length(CAST(f." + column + " AS BLOB)),0)"
	}
	extra := " UNION ALL SELECT f.run_id,(" + refCost + "),(" + refCost + ") FROM managed_artifact_refs f JOIN managed_artifact_reservations r ON r.id=f.reservation_id WHERE f.manifest_bytes>0 AND r.class='progress'"
	for _, table := range bridgeQuotaTables {
		if table.progress {
			extra += " UNION ALL SELECT " + table.runID("b") + "," + bridgeRowBytes("b", table) + "," + bridgeRowBytes("b", table) + " FROM " + table.name + " b"
		}
	}
	queries := []string{
		`SELECT COUNT(*) FROM report_capabilities c LEFT JOIN storage_usage u ON u.scope='capability' AND u.scope_id=c.capability_id
 WHERE COALESCE(u.total_bytes,0)!=(SELECT COALESCE(SUM(accounted_bytes),0) FROM report_events WHERE capability_id=c.capability_id)
 OR COALESCE(u.progress_bytes,0)!=(SELECT COALESCE(SUM(accounted_bytes),0) FROM report_events WHERE capability_id=c.capability_id AND kind='progress')`,
		`WITH source(run_id,total,progress) AS (
 SELECT t.run_id,` + managedHostRuntimeBytes("e.body_json") + `,0 FROM runtime_events e JOIN tasks t ON t.id=e.task_id
 UNION ALL SELECT c.run_id,e.accounted_bytes,CASE WHEN e.kind='progress' THEN e.accounted_bytes ELSE 0 END FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id
 UNION ALL SELECT t.run_id,p.accounted_bytes,p.accounted_bytes FROM host_progress p JOIN tasks t ON t.id=p.task_id` + extra + `
 ), expected(scope,id,total,progress) AS (
 SELECT 'global','',COALESCE(SUM(total),0),COALESCE(SUM(progress),0) FROM source
 UNION ALL SELECT 'run',run_id,SUM(total),SUM(progress) FROM source GROUP BY run_id
 ) SELECT COUNT(*) FROM expected e LEFT JOIN storage_usage u ON u.scope=e.scope AND u.scope_id=e.id
 WHERE COALESCE(u.total_bytes,0)<e.total OR COALESCE(u.progress_bytes,0)!=e.progress`,
	}
	for _, query := range queries {
		var invalid int
		if err := tx.QueryRow(query).Scan(&invalid); err != nil {
			return err
		}
		if invalid != 0 {
			return CodeError("legacy_artifact_accounting_invalid")
		}
	}
	return nil
}

func migrateLegacyProgressRows(tx *sql.Tx, d *DB) error {
	rows, err := tx.Query(`SELECT p.event_id,t.run_id,p.body_json,p.accounted_bytes,
	 CASE WHEN json_valid(p.body_json) THEN EXISTS(SELECT 1 FROM managed_artifact_refs f JOIN managed_artifact_reservations r ON r.id=f.reservation_id JOIN managed_host_artifact_metadata m ON m.reservation_id=r.id AND m.run_id=r.run_id
	 WHERE r.class='progress' AND r.state='active' AND r.retained_bytes>=json_extract(p.body_json,'$.artifact.size') AND r.retained_bytes<=r.reserved_bytes AND f.intake_id IS NULL AND f.manifest_bytes>0
	 AND r.run_id=t.run_id AND f.run_id=r.run_id AND r.task_id=p.task_id AND r.attempt_id=json_extract(p.body_json,'$.attempt_id') AND r.scope_key=p.segment_id AND r.producer_id=json_extract(p.body_json,'$.producer_id') AND f.slot=CAST(p.sequence-1 AS TEXT)
	 AND f.proof_json=json_extract(p.body_json,'$.artifact.host_artifact') AND json_extract(p.body_json,'$.artifact.path')=json_extract(f.proof_json,'$.proof.path')
	 AND json_extract(p.body_json,'$.artifact.size')=json_extract(f.proof_json,'$.proof.size') AND json_extract(p.body_json,'$.artifact.sha256')=json_extract(f.proof_json,'$.proof.sha256')) ELSE 0 END
 FROM host_progress p JOIN tasks t ON t.id=p.task_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, run, body string
		var accounted int64
		var managed int
		if err = rows.Scan(&id, &run, &body, &accounted, &managed); err != nil {
			return err
		}
		claim := legacyClaimFromBody("progress", id, run, "", []byte(body), false)
		var event contract.Event
		parsed := json.Unmarshal([]byte(body), &event) == nil
		hasReceipt := parsed && event.Artifact != nil && event.Artifact.HostArtifact != nil
		if managed != 0 && claim.state == "legacy_pending" && hasReceipt {
			item, proofErr := authorizeHostArtifact(context.Background(), tx, d.path, event, true)
			if proofErr == nil && item != nil {
				continue
			}
		}
		if hasReceipt {
			claim = legacyArtifactClaim{class: "progress", id: id, run: run, state: "malformed", declaration: body, digest: runtimeHash(body)}
		}
		if claim.absent {
			continue
		}
		if claim.state == "legacy_pending" && (accounted < int64(len(body)) || claim.raw > accounted-int64(len(body))) {
			return CodeError("legacy_artifact_accounting_invalid")
		}
		if err = insertLegacyClaim(context.Background(), tx, claim, false, d); err != nil {
			return err
		}
		if claim.state == "legacy_pending" {
			if _, err = tx.Exec(`UPDATE host_progress SET accounted_bytes=accounted_bytes-? WHERE event_id=?`, claim.raw, id); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

func rebuildLegacyControlAccounting(tx *sql.Tx) error {
	// Report adjustments fired their existing UPDATE triggers. Host progress
	// has no UPDATE quota trigger; explicitly split its two counters below.
	// Runtime source bytes remain unchanged and their raw refund is frozen.
	_, err := tx.Exec(`
UPDATE storage_usage SET total_bytes=total_bytes-COALESCE((SELECT SUM(raw_bytes) FROM legacy_artifact_claims l WHERE l.state='legacy_pending' AND l.source_class='runtime' AND (storage_usage.scope='global' OR (storage_usage.scope='run' AND storage_usage.scope_id=l.run_id))),0);
UPDATE storage_usage SET total_bytes=total_bytes-COALESCE((SELECT SUM(raw_bytes) FROM legacy_artifact_claims l WHERE l.state='legacy_pending' AND l.source_class='progress' AND (storage_usage.scope='global' OR (storage_usage.scope='run' AND storage_usage.scope_id=l.run_id))),0),progress_bytes=progress_bytes-COALESCE((SELECT SUM(raw_bytes) FROM legacy_artifact_claims l WHERE l.state='legacy_pending' AND l.source_class='progress' AND (storage_usage.scope='global' OR (storage_usage.scope='run' AND storage_usage.scope_id=l.run_id))),0)`)
	return err
}

func installLegacySourceGuards(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TRIGGER runtime_legacy_artifact_claimed_immutable BEFORE UPDATE OF legacy_artifact_claimed ON runtime_events BEGIN SELECT RAISE(ABORT,'runtime_legacy_artifact_claim_immutable'); END;
CREATE TRIGGER runtime_legacy_artifact_binding BEFORE INSERT ON runtime_events
WHEN NEW.legacy_artifact_claimed=1 AND (NEW.host_artifact_verified!=0 OR NOT EXISTS(
 SELECT 1 FROM legacy_artifact_claims l JOIN tasks t ON t.id=NEW.task_id
 WHERE l.source_class='runtime' AND l.source_id=NEW.event_id AND l.state='legacy_pending'
 AND l.run_id=t.run_id AND l.raw_bytes=json_extract(NEW.body_json,'$.artifact.size')
 AND json(l.declaration_json)=json(json_extract(NEW.body_json,'$.artifact'))
 AND COALESCE(json_extract(NEW.body_json,'$.kind'),'')!='exited'))
BEGIN SELECT RAISE(ABORT,'runtime_legacy_artifact_binding'); END;`); err != nil {
		return err
	}
	for _, source := range []struct {
		class, table string
		identity     []string
		accounted    bool
	}{
		{"runtime", "runtime_events", []string{"event_id", "producer_id", "task_id", "attempt_id", "segment_id"}, false},
		{"report", "report_events", []string{"event_id", "capability_id", "sequence", "kind"}, true},
		{"progress", "host_progress", []string{"event_id", "task_id", "segment_id", "sequence"}, true},
	} {
		changed := "0"
		for _, column := range source.identity {
			changed += " OR OLD." + column + " IS NOT NEW." + column
		}
		if source.accounted {
			changed += " OR OLD.accounted_bytes IS NOT NEW.accounted_bytes"
		}
		changed += ` OR CASE WHEN l.state='malformed' THEN OLD.body_json IS NOT NEW.body_json
 ELSE CASE WHEN json_valid(OLD.body_json) AND json_valid(NEW.body_json) THEN
 (SELECT COUNT(*) FROM json_each(NEW.body_json) WHERE key='artifact')!=1 OR json(json_extract(OLD.body_json,'$.artifact')) IS NOT json(json_extract(NEW.body_json,'$.artifact')) ELSE 1 END END`
		ddl := fmt.Sprintf(`CREATE TRIGGER legacy_%[1]s_source_update BEFORE UPDATE ON %[2]s
 WHEN EXISTS(SELECT 1 FROM legacy_artifact_claims l WHERE l.source_class='%[1]s' AND l.source_id=OLD.event_id AND (%[3]s))
 BEGIN SELECT RAISE(ABORT,'legacy_artifact_source_immutable'); END;
 CREATE TRIGGER legacy_%[1]s_source_delete BEFORE DELETE ON %[2]s
 WHEN EXISTS(SELECT 1 FROM legacy_artifact_claims l WHERE l.source_class='%[1]s' AND l.source_id=OLD.event_id AND l.state='malformed')
 BEGIN SELECT RAISE(ABORT,'legacy_artifact_source_retained'); END;
 CREATE TRIGGER legacy_%[1]s_source_late BEFORE INSERT ON legacy_artifact_claims
 WHEN NEW.source_class='%[1]s' AND EXISTS(SELECT 1 FROM %[2]s WHERE event_id=NEW.source_id)
 BEGIN SELECT RAISE(ABORT,'legacy_artifact_source_already_registered'); END;`, source.class, source.table, changed)
		if _, err := tx.Exec(ddl); err != nil {
			return err
		}
	}
	return nil
}

// This is a data-only check: restoreManagedReportArtifact validates its exact
// immutable descriptor/proof and scope but never opens or modifies a file.
func (d *DB) legacyReportHasOriginalProof(tx *sql.Tx, id, capability string, body []byte) (bool, error) {
	var event contract.Event
	if json.Unmarshal(body, &event) != nil || event.Artifact == nil {
		return false, nil
	}
	if _, _, absent, err := legacyDeclaration(body, false); err != nil || absent {
		return false, nil
	}
	var sequence int64
	var kind string
	err := tx.QueryRow(`SELECT i.sequence,i.kind FROM managed_artifact_intakes i
 JOIN managed_artifact_refs f ON f.intake_id=i.id AND f.id=i.id AND f.reservation_id=i.reservation_id AND f.run_id=i.run_id AND f.slot='report'
 JOIN managed_artifact_reservations r ON r.id=i.reservation_id AND r.class='report' AND r.policy_sha256=''
 JOIN report_capabilities c ON c.capability_id=i.capability_id AND r.run_id=c.run_id AND r.task_id=c.task_id AND r.attempt_id=c.attempt_id AND r.producer_id=c.producer_id
 JOIN report_events e ON e.event_id=i.event_id AND e.capability_id=i.capability_id AND e.sequence=i.sequence AND e.kind=i.kind AND e.payload_hash=i.payload_hash
 WHERE i.event_id=? AND i.capability_id=? AND i.run_id=c.run_id AND i.task_id=c.task_id AND f.run_id=c.run_id AND i.phase='committed'`, id, capability).Scan(&sequence, &kind)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	original := *event.Artifact
	if err = d.restoreManagedReportArtifact(context.Background(), tx, ReportEventSpec{CapabilityID: capability, Sequence: sequence, EventID: id, Kind: kind}, &event); err != nil {
		return false, nil
	}
	return event.Artifact.Path == original.Path && event.Artifact.Size == original.Size && event.Artifact.SHA256 == original.SHA256, nil
}
