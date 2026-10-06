package store

import (
	"database/sql"
	"fmt"
	"strings"
)

func migrateContinuationSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE attempt_provider_bindings(
 attempt_id TEXT PRIMARY KEY REFERENCES attempts(id), version INTEGER NOT NULL CHECK(version=1),
 run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT NOT NULL REFERENCES tasks(id), work_revision INTEGER NOT NULL,
 provider TEXT NOT NULL, provider_lock_json TEXT NOT NULL, profile_json TEXT NOT NULL, original_cwd TEXT NOT NULL,
 adapter_payload_sha256 TEXT NOT NULL, controller_thread TEXT NOT NULL, owner_context_id TEXT NOT NULL,
 session_kind TEXT NOT NULL, session_id TEXT NOT NULL, session_event_id TEXT NOT NULL UNIQUE REFERENCES runtime_events(event_id),
 workspace_source_segment_id TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE continuation_decisions(
 decision_id TEXT PRIMARY KEY, version INTEGER NOT NULL CHECK(version=1),
 run_id TEXT NOT NULL REFERENCES runs(id), task_id TEXT NOT NULL REFERENCES tasks(id), attempt_id TEXT NOT NULL REFERENCES attempts(id),
 work_revision INTEGER NOT NULL, mode TEXT NOT NULL CHECK(mode IN ('question','interrupted')), source_kind TEXT NOT NULL CHECK(source_kind IN ('host','report')),
 source_segment_id TEXT NOT NULL REFERENCES segments(id), source_event_id TEXT NOT NULL UNIQUE,
 source_event_revision INTEGER NOT NULL, source_event_sha256 TEXT NOT NULL, action_slot TEXT NOT NULL UNIQUE,
 command_id TEXT NOT NULL UNIQUE, question_id TEXT NOT NULL, answer_sha256 TEXT NOT NULL,
 ref_json TEXT NOT NULL, target_segment_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL CHECK(status IN ('queued','claimed')),
 created_at TEXT NOT NULL
);
CREATE INDEX continuation_attempt_status ON continuation_decisions(attempt_id,status,target_segment_id);
`)
	if err != nil {
		return err
	}
	for _, table := range []struct{ name, bytes string }{
		{"attempt_provider_bindings", `length(CAST(NEW.provider_lock_json AS BLOB))+length(CAST(NEW.profile_json AS BLOB))+length(CAST(NEW.original_cwd AS BLOB))+length(CAST(NEW.controller_thread AS BLOB))+length(CAST(NEW.owner_context_id AS BLOB))+length(CAST(NEW.session_id AS BLOB))+length(CAST(NEW.session_kind AS BLOB))+length(CAST(NEW.session_event_id AS BLOB))+length(CAST(NEW.workspace_source_segment_id AS BLOB))+length(CAST(NEW.adapter_payload_sha256 AS BLOB))+length(CAST(NEW.attempt_id AS BLOB))+length(CAST(NEW.task_id AS BLOB))+length(CAST(NEW.run_id AS BLOB))+length(CAST(NEW.provider AS BLOB))+length(CAST(NEW.created_at AS BLOB))+16`},
		{"continuation_decisions", `length(CAST(NEW.source_kind AS BLOB))+length(CAST(NEW.ref_json AS BLOB))+length(CAST(NEW.decision_id AS BLOB))+length(CAST(NEW.run_id AS BLOB))+length(CAST(NEW.task_id AS BLOB))+length(CAST(NEW.attempt_id AS BLOB))+length(CAST(NEW.mode AS BLOB))+length(CAST(NEW.source_segment_id AS BLOB))+length(CAST(NEW.source_event_id AS BLOB))+length(CAST(NEW.source_event_sha256 AS BLOB))+length(CAST(NEW.action_slot AS BLOB))+length(CAST(NEW.command_id AS BLOB))+length(CAST(NEW.question_id AS BLOB))+length(CAST(NEW.answer_sha256 AS BLOB))+length(CAST(NEW.target_segment_id AS BLOB))+length(CAST(NEW.status AS BLOB))+length(CAST(NEW.created_at AS BLOB))+24`},
	} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			refs := []string{"NEW"}
			if op == "DELETE" {
				refs = []string{"OLD"}
			}
			if op == "UPDATE" {
				refs = []string{"OLD", "NEW"}
			}
			body := ""
			for _, ref := range refs {
				bytes := strings.ReplaceAll(table.bytes, "NEW.", ref+".")
				for _, scope := range []struct{ name, id string }{{"global", "''"}, {"run", ref + ".run_id"}} {
					if ref == "OLD" {
						body += fmt.Sprintf("UPDATE storage_usage SET total_bytes=total_bytes-(%s) WHERE scope='%s' AND scope_id=%s;", bytes, scope.name, scope.id)
					} else {
						body += fmt.Sprintf("INSERT INTO storage_usage VALUES('%s',%s,(%s),0) ON CONFLICT(scope,scope_id) DO UPDATE SET total_bytes=total_bytes+excluded.total_bytes;", scope.name, scope.id, bytes)
					}
				}
			}
			if _, err = tx.Exec("CREATE TRIGGER quota_" + table.name + "_" + op + " AFTER " + op + " ON " + table.name + " BEGIN " + body + " END"); err != nil {
				return err
			}
		}
	}
	return nil
}
