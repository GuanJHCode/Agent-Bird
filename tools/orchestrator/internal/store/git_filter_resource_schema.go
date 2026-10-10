package store

import (
	"database/sql"
	"fmt"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

const gitFilterCleanupLimit = 1024
const gitFilterReleaseEventLimit = 128
const gitFilterFixedControlBytes = 7*8 + contract.GitFilterMetadataBytes + gitFilterCleanupLimit + gitFilterReleaseEventLimit

var gitFilterTexts = []string{"id", "run_id", "task_id", "attempt_id", "segment_id", "producer_id", "command_id", "scope_sha256", "manifest_sha256", "clean_tool_sha256", "smudge_tool_sha256", "created_at", "state", "host_identity_sha256"}

func migrateGitFilterResourceReservations(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE git_filter_resource_reservations(
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id),task_id TEXT NOT NULL REFERENCES tasks(id),attempt_id TEXT NOT NULL REFERENCES attempts(id),
 segment_id TEXT NOT NULL UNIQUE REFERENCES segment_runtime(segment_id),producer_id TEXT NOT NULL REFERENCES runtime_hosts(id),command_id TEXT NOT NULL UNIQUE,
 work_revision INTEGER NOT NULL CHECK(typeof(work_revision)='integer' AND work_revision>0),plan_revision INTEGER NOT NULL CHECK(typeof(plan_revision)='integer' AND plan_revision>0),
 host_identity_sha256 TEXT NOT NULL CHECK(length(host_identity_sha256)=64),scope_sha256 TEXT NOT NULL CHECK(length(scope_sha256)=64),manifest_sha256 TEXT NOT NULL CHECK(length(manifest_sha256)=64),clean_tool_sha256 TEXT NOT NULL CHECK(length(clean_tool_sha256)=64),smudge_tool_sha256 TEXT NOT NULL CHECK(length(smudge_tool_sha256)=64),
 tool_maximum_bytes INTEGER NOT NULL CHECK(typeof(tool_maximum_bytes)='integer' AND tool_maximum_bytes=33554432),stream_maximum_bytes INTEGER NOT NULL CHECK(typeof(stream_maximum_bytes)='integer' AND stream_maximum_bytes=16777216),stream_slots INTEGER NOT NULL CHECK(typeof(stream_slots)='integer' AND stream_slots=3),
 maximum_bytes INTEGER NOT NULL CHECK(typeof(maximum_bytes)='integer' AND maximum_bytes=117440512),
 metadata_bytes INTEGER NOT NULL CHECK(typeof(metadata_bytes)='integer' AND metadata_bytes=131074),
 created_at TEXT NOT NULL CHECK(length(CAST(created_at AS BLOB))=30),state TEXT NOT NULL CHECK(state IN ('reserved','released')),
 release_event_id TEXT REFERENCES runtime_events(event_id),cleanup_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(cleanup_json AS BLOB))<=1024),
 CHECK((state='reserved' AND release_event_id IS NULL AND cleanup_json='') OR (state='released' AND release_event_id IS NOT NULL AND length(CAST(release_event_id AS BLOB)) BETWEEN 1 AND 128 AND json_valid(cleanup_json))));
CREATE TRIGGER git_filter_resource_identity_immutable BEFORE UPDATE ON git_filter_resource_reservations
 WHEN NEW.id!=OLD.id OR NEW.run_id!=OLD.run_id OR NEW.task_id!=OLD.task_id OR NEW.attempt_id!=OLD.attempt_id OR NEW.segment_id!=OLD.segment_id OR NEW.producer_id!=OLD.producer_id OR NEW.command_id!=OLD.command_id
 OR NEW.host_identity_sha256!=OLD.host_identity_sha256 OR NEW.work_revision!=OLD.work_revision OR NEW.plan_revision!=OLD.plan_revision OR NEW.scope_sha256!=OLD.scope_sha256 OR NEW.manifest_sha256!=OLD.manifest_sha256 OR NEW.clean_tool_sha256!=OLD.clean_tool_sha256 OR NEW.smudge_tool_sha256!=OLD.smudge_tool_sha256 OR NEW.tool_maximum_bytes!=OLD.tool_maximum_bytes OR NEW.stream_maximum_bytes!=OLD.stream_maximum_bytes OR NEW.stream_slots!=OLD.stream_slots OR NEW.maximum_bytes!=OLD.maximum_bytes OR NEW.metadata_bytes!=OLD.metadata_bytes OR NEW.created_at!=OLD.created_at OR OLD.state!='reserved' OR NEW.state!='released'
 BEGIN SELECT RAISE(ABORT,'git_filter_resource_immutable'); END;
CREATE TRIGGER git_filter_resource_retained BEFORE DELETE ON git_filter_resource_reservations BEGIN SELECT RAISE(ABORT,'git_filter_resource_retained'); END;
CREATE TRIGGER git_filter_resource_initial BEFORE INSERT ON git_filter_resource_reservations WHEN NEW.state!='reserved' OR NEW.release_event_id IS NOT NULL OR NEW.cleanup_json!='' OR NOT EXISTS(
 SELECT 1 FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN report_capabilities c ON c.segment_id=s.segment_id JOIN runs r ON r.id=t.run_id
 WHERE s.segment_id=NEW.segment_id AND s.attempt_id=NEW.attempt_id AND s.command_id=NEW.command_id AND s.host_id=NEW.producer_id AND a.task_id=NEW.task_id AND t.run_id=NEW.run_id AND c.work_revision=NEW.work_revision AND r.plan_revision=NEW.plan_revision)
 BEGIN SELECT RAISE(ABORT,'git_filter_resource_initial'); END;
CREATE TRIGGER git_filter_resource_release_proof BEFORE UPDATE ON git_filter_resource_reservations WHEN NOT EXISTS(
 SELECT 1 FROM runtime_events e JOIN segment_runtime s ON s.segment_id=e.segment_id JOIN budget_runtime b ON b.id=s.reservation_id
 WHERE e.event_id=NEW.release_event_id AND e.producer_id=OLD.producer_id AND e.task_id=OLD.task_id AND e.attempt_id=OLD.attempt_id AND e.segment_id=OLD.segment_id
 AND json_extract(e.body_json,'$.command_id')=OLD.command_id AND json_extract(e.body_json,'$.run_id')=OLD.run_id AND json_extract(e.body_json,'$.work_revision')=OLD.work_revision
 AND json_extract(e.body_json,'$.git_filter_cleanup')=NEW.cleanup_json AND b.status='settled'
 AND ((json_extract(e.body_json,'$.kind')='launch_rejected' AND s.status='not_started') OR (json_extract(e.body_json,'$.kind')='exited' AND s.status='exited'))
 AND json_extract(NEW.cleanup_json,'$.version')=1 AND json_extract(NEW.cleanup_json,'$.reservation_id')=OLD.id AND json_extract(NEW.cleanup_json,'$.scope_sha256')=OLD.scope_sha256
 AND json_extract(NEW.cleanup_json,'$.manifest_sha256')=OLD.manifest_sha256 AND json_extract(NEW.cleanup_json,'$.clean_tool_sha256')=OLD.clean_tool_sha256 AND json_extract(NEW.cleanup_json,'$.smudge_tool_sha256')=OLD.smudge_tool_sha256 AND length(json_extract(NEW.cleanup_json,'$.journal_sha256'))=64 AND json_extract(NEW.cleanup_json,'$.removed_bytes') BETWEEN 0 AND OLD.maximum_bytes)
 BEGIN SELECT RAISE(ABORT,'git_filter_resource_release_proof'); END;`)
	if err != nil {
		return err
	}
	cost := originalOwnerBytesSQL("NEW.", gitFilterTexts, int(gitFilterFixedControlBytes))
	_, err = tx.Exec(fmt.Sprintf(`CREATE TRIGGER quota_git_filter_resource_INSERT AFTER INSERT ON git_filter_resource_reservations BEGIN
 INSERT INTO storage_usage VALUES('global','',(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;
 INSERT INTO storage_usage VALUES('run',NEW.run_id,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes; END;`, cost, cost))
	return err
}
