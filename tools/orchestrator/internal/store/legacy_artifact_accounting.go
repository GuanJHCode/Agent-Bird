package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strconv"

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
}

func legacyClaimFromBody(class, id, run, capability string, body []byte, excludeExited bool) legacyArtifactClaim {
	claim := legacyArtifactClaim{class: class, id: id, run: run, capability: capability, state: "malformed", declaration: "{}", digest: runtimeHash("malformed:" + class + "\x00" + id)}
	var event contract.Event
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &event) != nil || json.Unmarshal(body, &top) != nil {
		return claim
	}
	artifact, hasArtifact := top["artifact"]
	if !hasArtifact || string(artifact) == "null" || (excludeExited && event.Kind == contract.EventExited) {
		return claim
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(artifact, &fields) != nil {
		return claim
	}
	size, ok := fields["size"]
	if !ok {
		return claim
	}
	// Decode the original scalar: typed Event decoding silently turns an absent
	// size into zero and cannot distinguish fractional or overflowing history.
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(size))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil || decoder.More() {
		return claim
	}
	rawBytes, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || rawBytes < 0 {
		return claim
	}
	raw, err := json.Marshal(event.Artifact)
	if err != nil {
		return claim
	}
	claim.declaration, claim.digest, claim.raw, claim.state = string(raw), runtimeHash(string(raw)), rawBytes, "legacy_pending"
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
		if claim.state == "malformed" {
			var e contract.Event
			if json.Unmarshal([]byte(body), &e) == nil && (e.Artifact == nil || e.Kind == contract.EventExited) {
				continue
			}
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
	if _, err = tx.Exec(`CREATE TRIGGER runtime_legacy_artifact_claimed_immutable BEFORE UPDATE OF legacy_artifact_claimed ON runtime_events BEGIN SELECT RAISE(ABORT,'runtime_legacy_artifact_claim_immutable'); END;
CREATE TRIGGER runtime_legacy_artifact_body_immutable BEFORE UPDATE OF body_json ON runtime_events WHEN OLD.legacy_artifact_claimed=1 AND COALESCE(json_extract(OLD.body_json,'$.artifact'),'') IS NOT COALESCE(json_extract(NEW.body_json,'$.artifact'),'') BEGIN SELECT RAISE(ABORT,'runtime_legacy_artifact_body_immutable'); END;`); err != nil {
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
	rows, err := tx.Query(`SELECT e.event_id,c.run_id,e.capability_id,e.body_json,e.accounted_bytes,
	 EXISTS(SELECT 1 FROM managed_artifact_intakes i JOIN managed_artifact_refs f ON f.intake_id=i.id AND f.id=i.id AND f.slot='report' JOIN managed_artifact_reservations r ON r.id=f.reservation_id
	 WHERE i.event_id=e.event_id AND i.capability_id=e.capability_id AND i.sequence=e.sequence AND i.kind=e.kind AND i.payload_hash=e.payload_hash
	 AND i.phase='committed' AND f.reservation_id=i.reservation_id AND f.run_id=i.run_id AND r.state='active' AND r.retained_bytes=r.reserved_bytes AND r.class='report'
	 AND json_valid(e.body_json) AND json_extract(e.body_json,'$.artifact.path')=json_extract(f.proof_json,'$.path') AND json_extract(e.body_json,'$.artifact.size')=json_extract(f.proof_json,'$.size') AND json_extract(e.body_json,'$.artifact.sha256')=json_extract(f.proof_json,'$.sha256'))
 FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, run, cap, body string
		var accounted int64
		var managed int
		if err = rows.Scan(&id, &run, &cap, &body, &accounted, &managed); err != nil {
			return err
		}
		if managed != 0 {
			continue
		}
		claim := legacyClaimFromBody("report", id, run, cap, []byte(body), false)
		if claim.state == "malformed" {
			var e contract.Event
			if json.Unmarshal([]byte(body), &e) == nil && e.Artifact == nil {
				continue
			}
		}
		if claim.state == "legacy_pending" && accounted < claim.raw {
			return CodeError("legacy_artifact_accounting_invalid")
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

func migrateLegacyProgressRows(tx *sql.Tx, d *DB) error {
	rows, err := tx.Query(`SELECT p.event_id,t.run_id,p.body_json,p.accounted_bytes,
	 EXISTS(SELECT 1 FROM managed_artifact_refs f JOIN managed_artifact_reservations r ON r.id=f.reservation_id JOIN managed_host_artifact_metadata m ON m.reservation_id=r.id AND m.run_id=r.run_id
	 WHERE r.class='progress' AND r.state='active' AND r.retained_bytes=r.reserved_bytes AND f.intake_id IS NULL AND f.manifest_bytes>0
	 AND r.run_id=t.run_id AND r.task_id=p.task_id AND r.scope_key=p.segment_id AND r.producer_id=json_extract(p.body_json,'$.producer_id')
	 AND f.proof_json=json_extract(p.body_json,'$.artifact.host_artifact') AND json_extract(p.body_json,'$.artifact.path')=json_extract(f.proof_json,'$.proof.path')
	 AND json_extract(p.body_json,'$.artifact.size')=json_extract(f.proof_json,'$.proof.size') AND json_extract(p.body_json,'$.artifact.sha256')=json_extract(f.proof_json,'$.proof.sha256'))
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
		if managed != 0 {
			continue
		}
		claim := legacyClaimFromBody("progress", id, run, "", []byte(body), false)
		if claim.state == "malformed" {
			var e contract.Event
			if json.Unmarshal([]byte(body), &e) == nil && e.Artifact == nil {
				continue
			}
		}
		if claim.state == "legacy_pending" && accounted < claim.raw {
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
	// report/progress adjustments fired their existing UPDATE triggers. Runtime
	// rows remain byte-for-byte, so remove only their frozen legacy raw portion.
	_, err := tx.Exec(`
UPDATE storage_usage SET total_bytes=total_bytes-COALESCE((SELECT SUM(raw_bytes) FROM legacy_artifact_claims l WHERE l.state='legacy_pending' AND l.source_class='runtime' AND (storage_usage.scope='global' OR (storage_usage.scope='run' AND storage_usage.scope_id=l.run_id))),0);
UPDATE storage_usage SET total_bytes=total_bytes-COALESCE((SELECT SUM(raw_bytes) FROM legacy_artifact_claims l WHERE l.state='legacy_pending' AND l.source_class='progress' AND (storage_usage.scope='global' OR (storage_usage.scope='run' AND storage_usage.scope_id=l.run_id))),0),progress_bytes=progress_bytes-COALESCE((SELECT SUM(raw_bytes) FROM legacy_artifact_claims l WHERE l.state='legacy_pending' AND l.source_class='progress' AND (storage_usage.scope='global' OR (storage_usage.scope='run' AND storage_usage.scope_id=l.run_id))),0)`)
	return err
}
