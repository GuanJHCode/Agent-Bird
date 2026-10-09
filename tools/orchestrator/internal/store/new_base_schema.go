package store

import (
	"database/sql"
	"fmt"
)

var newBaseLineageTexts = []string{"run_id", "source_run_id", "source_task_id", "source_event_id", "source_event_hash", "command_id", "request_sha256", "context_json", "launch_id", "launch_token_hash", "control_token_hash", "created_at"}

func migrateNewBaseSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE new_base_lineage(
 run_id TEXT PRIMARY KEY REFERENCES runs(id), source_run_id TEXT NOT NULL REFERENCES runs(id), source_task_id TEXT NOT NULL REFERENCES tasks(id),
 source_event_id TEXT NOT NULL REFERENCES runtime_events(event_id),source_event_revision INTEGER NOT NULL CHECK(source_event_revision>0),source_event_hash TEXT NOT NULL CHECK(length(source_event_hash)=64),
 command_id TEXT NOT NULL UNIQUE CHECK(length(CAST(command_id AS BLOB)) BETWEEN 1 AND 128),request_sha256 TEXT NOT NULL UNIQUE CHECK(length(request_sha256)=64),context_json TEXT NOT NULL CHECK(json_valid(context_json)),
 launch_id TEXT NOT NULL UNIQUE REFERENCES host_launches(id),launch_token_hash TEXT NOT NULL CHECK(length(launch_token_hash)=64),control_token_hash TEXT NOT NULL CHECK(length(control_token_hash)=64),created_at TEXT NOT NULL,admission_bytes INTEGER NOT NULL CHECK(admission_bytes>0),
 UNIQUE(source_task_id,source_event_id,source_event_revision,source_event_hash));
 CREATE TRIGGER immutable_new_base_lineage_update BEFORE UPDATE ON new_base_lineage BEGIN SELECT RAISE(ABORT,'immutable new base lineage'); END;
 CREATE TRIGGER immutable_new_base_lineage_delete BEFORE DELETE ON new_base_lineage BEGIN SELECT RAISE(ABORT,'immutable new base lineage'); END;`)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE TABLE new_base_target_reservations(
 run_id TEXT PRIMARY KEY REFERENCES runs(id),task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id),target_key TEXT NOT NULL CHECK(length(target_key)=64),target_json TEXT NOT NULL CHECK(json_valid(target_json)),
 status TEXT NOT NULL CHECK(status IN ('reserved','released')),created_at TEXT NOT NULL CHECK(length(CAST(created_at AS BLOB))=30),updated_at TEXT NOT NULL CHECK(length(CAST(updated_at AS BLOB))=30));
 CREATE UNIQUE INDEX new_base_active_target ON new_base_target_reservations(target_key) WHERE status='reserved';
 CREATE TRIGGER immutable_new_base_target_identity BEFORE UPDATE ON new_base_target_reservations WHEN NEW.run_id!=OLD.run_id OR NEW.task_id!=OLD.task_id OR NEW.target_key!=OLD.target_key OR NEW.target_json!=OLD.target_json OR NEW.created_at!=OLD.created_at OR OLD.status!='reserved' OR NEW.status!='released' BEGIN SELECT RAISE(ABORT,'immutable new base target'); END;
 CREATE TRIGGER immutable_new_base_target_delete BEFORE DELETE ON new_base_target_reservations BEGIN SELECT RAISE(ABORT,'immutable new base target'); END;`); err != nil {
		return err
	}
	reservationCost := originalOwnerBytesSQL("NEW.", newBaseTargetTexts, 0)
	if _, err = tx.Exec(fmt.Sprintf(`CREATE TRIGGER quota_new_base_target_INSERT AFTER INSERT ON new_base_target_reservations BEGIN
 INSERT INTO storage_usage VALUES('global','',(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;
 INSERT INTO storage_usage VALUES('run',NEW.run_id,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;END;`, reservationCost, reservationCost)); err != nil {
		return err
	}
	if _, err = tx.Exec(`CREATE TRIGGER new_base_target_release_proof BEFORE UPDATE ON new_base_target_reservations WHEN NOT EXISTS(SELECT 1 FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=OLD.task_id AND ` + newBaseTargetReleaseSQL + `) BEGIN SELECT RAISE(ABORT,'new base target release proof');END;`); err != nil {
		return err
	}
	cost := originalOwnerBytesSQL("NEW.", newBaseLineageTexts, 16) + "+NEW.admission_bytes"
	_, err = tx.Exec(fmt.Sprintf(`CREATE TRIGGER quota_new_base_lineage_INSERT AFTER INSERT ON new_base_lineage BEGIN
 INSERT INTO storage_usage VALUES('global','',(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;
 INSERT INTO storage_usage VALUES('run',NEW.run_id,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes; END;`, cost, cost))
	return err
}
