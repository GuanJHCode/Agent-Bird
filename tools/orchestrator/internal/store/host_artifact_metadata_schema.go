package store

import (
	"database/sql"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const hostArtifactManifestReservationBytes = int64((1 + contract.HostProgressArtifactSlots) * contract.HostArtifactManifestLimit)

// Schema21 meters offline Host manifests before dispatch. Historical file
// claims remain unverified: migration never inspects files or credits their
// claimed metadata as physically retained.
func migrateHostArtifactMetadata(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE managed_host_artifact_metadata(
 reservation_id TEXT PRIMARY KEY REFERENCES managed_artifact_reservations(id),
 run_id TEXT NOT NULL REFERENCES runs(id),
 reserved_bytes INTEGER NOT NULL CHECK(typeof(reserved_bytes)='integer' AND reserved_bytes IN (1024,21504)));
CREATE TRIGGER host_artifact_metadata_immutable BEFORE UPDATE ON managed_host_artifact_metadata BEGIN SELECT RAISE(ABORT,'host_artifact_metadata_immutable'); END;
CREATE TRIGGER host_artifact_metadata_retained BEFORE DELETE ON managed_host_artifact_metadata BEGIN SELECT RAISE(ABORT,'host_artifact_metadata_immutable'); END;
CREATE TRIGGER host_artifact_metadata_binding BEFORE INSERT ON managed_host_artifact_metadata
WHEN NOT EXISTS(SELECT 1 FROM managed_artifact_reservations r WHERE r.id=NEW.reservation_id AND r.run_id=NEW.run_id
 AND ((r.class='terminal' AND NEW.reserved_bytes=1024) OR (r.class='progress' AND NEW.reserved_bytes=21504)))
BEGIN SELECT RAISE(ABORT,'host_artifact_metadata_binding'); END;
CREATE TRIGGER quota_host_artifact_metadata_INSERT AFTER INSERT ON managed_host_artifact_metadata BEGIN
 INSERT OR IGNORE INTO storage_usage VALUES('global','',0,0);
 INSERT OR IGNORE INTO storage_usage VALUES('run',NEW.run_id,0,0);
 UPDATE storage_usage SET total_bytes=total_bytes+64+length(CAST(NEW.reservation_id AS BLOB))+length(CAST(NEW.run_id AS BLOB))+NEW.reserved_bytes
 WHERE (scope='global' AND scope_id='') OR (scope='run' AND scope_id=NEW.run_id);
END;
ALTER TABLE managed_artifact_refs ADD COLUMN manifest_bytes INTEGER NOT NULL DEFAULT 0 CHECK(typeof(manifest_bytes)='integer' AND manifest_bytes>=0 AND manifest_bytes<=1024);
CREATE TRIGGER artifact_host_ref_binding BEFORE INSERT ON managed_artifact_refs
WHEN (NEW.intake_id IS NULL OR NEW.manifest_bytes!=0) AND NOT EXISTS(
 SELECT 1 FROM managed_artifact_reservations r JOIN managed_host_artifact_metadata m ON m.reservation_id=r.id AND m.run_id=r.run_id
 WHERE r.id=NEW.reservation_id AND r.run_id=NEW.run_id AND r.state IN ('reserved','active') AND NEW.intake_id IS NULL
 AND NEW.manifest_bytes BETWEEN 1 AND 1024
 AND json_valid(NEW.proof_json) AND json_type(NEW.proof_json,'$.version')='integer' AND json_extract(NEW.proof_json,'$.version')=1
 AND json_type(NEW.proof_json,'$.descriptor')='object' AND json_type(NEW.proof_json,'$.proof')='object'
 AND json_type(NEW.proof_json,'$.descriptor.version')='integer' AND json_extract(NEW.proof_json,'$.descriptor.version')=1
 AND json_extract(NEW.proof_json,'$.descriptor.reservation_id')=r.id
 AND json_extract(NEW.proof_json,'$.descriptor.scope_sha256')=r.namespace_sha256
 AND json_extract(NEW.proof_json,'$.descriptor.class')=r.class
 AND json_type(NEW.proof_json,'$.descriptor.index')='integer'
 AND NEW.slot=CAST(json_extract(NEW.proof_json,'$.descriptor.index') AS TEXT)
 AND ((r.class='terminal' AND NEW.slot='0' AND json_extract(NEW.proof_json,'$.descriptor.kind') IN ('terminal','prelaunch') AND r.reserved_bytes=1048576)
   OR (r.class='progress' AND json_extract(NEW.proof_json,'$.descriptor.index') BETWEEN 0 AND 20 AND json_extract(NEW.proof_json,'$.descriptor.kind')='progress' AND r.reserved_bytes=1376256))
 AND json_type(NEW.proof_json,'$.descriptor.size')='integer'
 AND json_extract(NEW.proof_json,'$.descriptor.size') BETWEEN 0 AND CASE r.class WHEN 'terminal' THEN 1048576 ELSE 65536 END
 AND NEW.id=json_extract(NEW.proof_json,'$.proof.intake_id')
 AND json_extract(NEW.proof_json,'$.proof.size')=json_extract(NEW.proof_json,'$.descriptor.size')
 AND json_extract(NEW.proof_json,'$.proof.sha256')=json_extract(NEW.proof_json,'$.descriptor.sha256')
 AND NEW.manifest_bytes=length(CAST(json_extract(NEW.proof_json,'$.descriptor') AS BLOB))
 AND NEW.manifest_bytes+COALESCE((SELECT SUM(f.manifest_bytes) FROM managed_artifact_refs f WHERE f.reservation_id=r.id),0)<=m.reserved_bytes)
BEGIN SELECT RAISE(ABORT,'artifact_host_ref_binding'); END;
INSERT INTO managed_host_artifact_metadata(reservation_id,run_id,reserved_bytes)
 SELECT id,run_id,CASE class WHEN 'terminal' THEN 1024 ELSE 21504 END
 FROM managed_artifact_reservations WHERE class IN ('terminal','progress');
`)
	if err != nil {
		return err
	}
	return migrateHostRuntimeAccounting(tx)
}
