package store

import (
	"database/sql"
	"fmt"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const gitFilterCarrierCleanupLimit = 2048
const gitFilterCarrierFixedControlBytes = 8 + contract.GitFilterCarrierMetadataBytes + gitFilterCarrierCleanupLimit + gitFilterReleaseEventLimit

var gitFilterCarrierTexts = []string{"id", "filter_reservation_id", "run_id", "segment_id", "scope_sha256", "grant_json", "created_at", "state"}

// The immutable original reservation holds the complete Host/segment identity;
// this unique FK is not a second mutable copy of that authority.
func migrateGitFilterCarrierReservations(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE git_filter_carrier_reservations(
 id TEXT PRIMARY KEY,filter_reservation_id TEXT NOT NULL UNIQUE REFERENCES git_filter_resource_reservations(id),
 run_id TEXT NOT NULL REFERENCES runs(id),segment_id TEXT NOT NULL UNIQUE REFERENCES segment_runtime(segment_id),
 scope_sha256 TEXT NOT NULL CHECK(length(scope_sha256)=64),grant_json TEXT NOT NULL CHECK(json_valid(grant_json) AND length(CAST(grant_json AS BLOB))<=2048),
 metadata_bytes INTEGER NOT NULL CHECK(typeof(metadata_bytes)='integer' AND metadata_bytes=131074),created_at TEXT NOT NULL CHECK(length(CAST(created_at AS BLOB))=30),
 state TEXT NOT NULL CHECK(state IN ('reserved','released')),release_event_id TEXT REFERENCES runtime_events(event_id),cleanup_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(cleanup_json AS BLOB))<=2048),
 CHECK((state='reserved' AND release_event_id IS NULL AND cleanup_json='') OR (state='released' AND release_event_id IS NOT NULL AND length(CAST(release_event_id AS BLOB)) BETWEEN 1 AND 128 AND json_valid(cleanup_json))));
CREATE TRIGGER git_filter_carrier_identity_immutable BEFORE UPDATE ON git_filter_carrier_reservations
 WHEN NEW.id!=OLD.id OR NEW.filter_reservation_id!=OLD.filter_reservation_id OR NEW.run_id!=OLD.run_id OR NEW.segment_id!=OLD.segment_id OR NEW.scope_sha256!=OLD.scope_sha256 OR NEW.grant_json!=OLD.grant_json OR NEW.metadata_bytes!=OLD.metadata_bytes OR NEW.created_at!=OLD.created_at OR OLD.state!='reserved' OR NEW.state!='released'
 BEGIN SELECT RAISE(ABORT,'git_filter_carrier_immutable'); END;
CREATE TRIGGER git_filter_carrier_retained BEFORE DELETE ON git_filter_carrier_reservations BEGIN SELECT RAISE(ABORT,'git_filter_carrier_retained'); END;
CREATE TRIGGER git_filter_carrier_initial BEFORE INSERT ON git_filter_carrier_reservations WHEN NEW.state!='reserved' OR NEW.release_event_id IS NOT NULL OR NEW.cleanup_json!='' OR NOT EXISTS(
 SELECT 1 FROM git_filter_resource_reservations f WHERE f.id=NEW.filter_reservation_id AND f.run_id=NEW.run_id AND f.segment_id=NEW.segment_id AND f.state='reserved'
 AND json_extract(NEW.grant_json,'$.version')=1 AND json_extract(NEW.grant_json,'$.reservation_id')=NEW.id AND json_extract(NEW.grant_json,'$.scope_sha256')=NEW.scope_sha256
 AND json_extract(NEW.grant_json,'$.filter_reservation_id')=f.id AND json_extract(NEW.grant_json,'$.filter_scope_sha256')=f.scope_sha256 AND json_extract(NEW.grant_json,'$.manifest_sha256')=f.manifest_sha256
 AND json_extract(NEW.grant_json,'$.metadata_bytes')=131074 AND json_extract(NEW.grant_json,'$.runtime_namespace')='darwin-private-tmp-v1' AND json_extract(NEW.grant_json,'$.directory_slots')=1 AND json_extract(NEW.grant_json,'$.socket_slots')=1)
 BEGIN SELECT RAISE(ABORT,'git_filter_carrier_initial'); END;
CREATE TRIGGER git_filter_carrier_release_proof BEFORE UPDATE ON git_filter_carrier_reservations WHEN NOT EXISTS(
 SELECT 1 FROM git_filter_resource_reservations f JOIN runtime_events e ON e.event_id=f.release_event_id
 WHERE f.id=OLD.filter_reservation_id AND f.state='released' AND f.release_event_id=NEW.release_event_id
 AND json_extract(e.body_json,'$.git_filter_carrier_cleanup')=NEW.cleanup_json
 AND json_extract(NEW.cleanup_json,'$.version')=1 AND json_extract(NEW.cleanup_json,'$.reservation_id')=OLD.id AND json_extract(NEW.cleanup_json,'$.scope_sha256')=OLD.scope_sha256
 AND json_extract(NEW.cleanup_json,'$.filter_reservation_id')=f.id AND json_extract(NEW.cleanup_json,'$.manifest_sha256')=f.manifest_sha256
 AND json_extract(NEW.cleanup_json,'$.runtime_namespace')='darwin-private-tmp-v1' AND json_extract(NEW.cleanup_json,'$.outcome') IN ('removed','not_created')
 AND json_extract(NEW.cleanup_json,'$.retained_bytes') BETWEEN 0 AND 65537 AND length(json_extract(NEW.cleanup_json,'$.journal_sha256'))=64)
 BEGIN SELECT RAISE(ABORT,'git_filter_carrier_release_proof'); END;`)
	if err != nil {
		return err
	}
	cost := originalOwnerBytesSQL("NEW.", gitFilterCarrierTexts, int(gitFilterCarrierFixedControlBytes))
	_, err = tx.Exec(fmt.Sprintf(`CREATE TRIGGER quota_git_filter_carrier_INSERT AFTER INSERT ON git_filter_carrier_reservations BEGIN
 INSERT INTO storage_usage VALUES('global','',(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;
 INSERT INTO storage_usage VALUES('run',NEW.run_id,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes; END;`, cost, cost))
	return err
}
