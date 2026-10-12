package store

import "database/sql"

func migrateOwnerPublicationSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE owner_rebind_publications(
 run_id TEXT NOT NULL REFERENCES runs(id), host_generation TEXT NOT NULL,
 launch_id TEXT NOT NULL REFERENCES host_launches(id), prior_sha256 TEXT NOT NULL, expected_sha256 TEXT NOT NULL,
 bootstrap_json TEXT NOT NULL, facts_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','published')),
 created_at TEXT NOT NULL, published_at TEXT,
 scratch_bytes INTEGER NOT NULL CHECK(scratch_bytes>=0),
 PRIMARY KEY(run_id,host_generation),
 FOREIGN KEY(run_id,host_generation) REFERENCES owner_rebinds(run_id,host_generation),
 CHECK((state='pending' AND published_at IS NULL AND scratch_bytes=length(CAST(bootstrap_json AS BLOB))) OR (state='published' AND length(published_at)>0 AND length(published_at)<=30 AND scratch_bytes=0))
);
CREATE UNIQUE INDEX owner_rebind_publication_one_pending ON owner_rebind_publications(run_id) WHERE state='pending';
CREATE TRIGGER owner_rebind_publication_immutable BEFORE UPDATE ON owner_rebind_publications
 WHEN NEW.run_id IS NOT OLD.run_id OR NEW.host_generation IS NOT OLD.host_generation OR NEW.launch_id IS NOT OLD.launch_id OR NEW.prior_sha256 IS NOT OLD.prior_sha256 OR NEW.expected_sha256 IS NOT OLD.expected_sha256 OR NEW.bootstrap_json IS NOT OLD.bootstrap_json OR NEW.facts_json IS NOT OLD.facts_json OR NEW.created_at IS NOT OLD.created_at OR OLD.state!='pending' OR NEW.state!='published' OR NEW.scratch_bytes!=0
 BEGIN SELECT RAISE(ABORT,'owner_rebind_publication_immutable'); END;
CREATE TRIGGER owner_rebind_publication_no_delete BEFORE DELETE ON owner_rebind_publications BEGIN SELECT RAISE(ABORT,'owner_rebind_publication_immutable'); END;
CREATE TRIGGER quota_owner_rebind_publication_insert AFTER INSERT ON owner_rebind_publications BEGIN
 INSERT OR IGNORE INTO storage_usage VALUES('global','',0,0); INSERT OR IGNORE INTO storage_usage VALUES('run',NEW.run_id,0,0);
 UPDATE storage_usage SET total_bytes=total_bytes+128+length(CAST(NEW.run_id AS BLOB))+length(CAST(NEW.host_generation AS BLOB))+length(CAST(NEW.launch_id AS BLOB))+length(CAST(NEW.prior_sha256 AS BLOB))+length(CAST(NEW.expected_sha256 AS BLOB))+length(CAST(NEW.bootstrap_json AS BLOB))+length(CAST(NEW.facts_json AS BLOB))+length(CAST(NEW.created_at AS BLOB))+NEW.scratch_bytes WHERE (scope='global' AND scope_id='') OR (scope='run' AND scope_id=NEW.run_id);
END;
CREATE TRIGGER quota_owner_rebind_publication_update AFTER UPDATE ON owner_rebind_publications BEGIN
 UPDATE storage_usage SET total_bytes=total_bytes-OLD.scratch_bytes WHERE (scope='global' AND scope_id='') OR (scope='run' AND scope_id=NEW.run_id);
END;`)
	return err
}
