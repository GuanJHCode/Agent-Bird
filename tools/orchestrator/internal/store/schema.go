package store

import (
	"database/sql"
	"strings"
)

func ensureRuntimeSchema(tx *sql.Tx) error {
	_, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS runtime_meta(
  id INTEGER PRIMARY KEY CHECK(id=1), next_ready_sequence INTEGER NOT NULL
);
INSERT OR IGNORE INTO runtime_meta(id,next_ready_sequence) VALUES(1,1);
CREATE TABLE IF NOT EXISTS host_launches(
  id TEXT PRIMARY KEY, token_hash TEXT NOT NULL, origin_context_id TEXT NOT NULL,
  host_generation TEXT NOT NULL, executable TEXT NOT NULL, status TEXT NOT NULL,
  host_id TEXT, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_hosts(
  id TEXT PRIMARY KEY, launch_id TEXT NOT NULL UNIQUE REFERENCES host_launches(id),
  origin_context_id TEXT NOT NULL, host_generation TEXT NOT NULL,
  pid INTEGER NOT NULL, birth TEXT NOT NULL, executable TEXT NOT NULL,
  coordinator_epoch INTEGER NOT NULL, status TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS run_control(
  run_id TEXT PRIMARY KEY REFERENCES runs(id), token_hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS owner_rebinds(
  run_id TEXT NOT NULL REFERENCES runs(id), host_generation TEXT NOT NULL,
  origin_pid INTEGER NOT NULL, origin_birth TEXT NOT NULL, attachment_proof_sha256 TEXT NOT NULL,
  created_at TEXT NOT NULL, PRIMARY KEY(run_id,host_generation)
);
CREATE TABLE IF NOT EXISTS task_runtime(
  task_id TEXT PRIMARY KEY REFERENCES tasks(id), host_launch_id TEXT NOT NULL REFERENCES host_launches(id),
  work_revision INTEGER NOT NULL, budget_group_id TEXT NOT NULL,
  max_active_ms INTEGER NOT NULL, ready_sequence INTEGER, adapter_payload TEXT NOT NULL DEFAULT '{}',
  fallback_payloads TEXT NOT NULL DEFAULT '[]', fallback_index INTEGER NOT NULL DEFAULT 0,
  completion_policy TEXT NOT NULL DEFAULT 'owner_review', expected_artifact_sha256 TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS segment_runtime(
  segment_id TEXT PRIMARY KEY REFERENCES segments(id), attempt_id TEXT NOT NULL REFERENCES attempts(id),
  host_id TEXT NOT NULL REFERENCES runtime_hosts(id), command_id TEXT NOT NULL UNIQUE,
  execution_epoch INTEGER NOT NULL, slot_token TEXT NOT NULL UNIQUE,
  launch_intent_hash TEXT NOT NULL, reservation_id TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL, outcome TEXT NOT NULL, deadline_unix_ms INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS budget_runtime(
  id TEXT PRIMARY KEY, budget_group_id TEXT NOT NULL, segment_id TEXT NOT NULL UNIQUE REFERENCES segments(id),
  granted_ms INTEGER NOT NULL, used_ms INTEGER NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_events(
  event_id TEXT PRIMARY KEY, producer_id TEXT NOT NULL REFERENCES runtime_hosts(id),
  sequence INTEGER NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id),
  attempt_id TEXT NOT NULL REFERENCES attempts(id), segment_id TEXT NOT NULL REFERENCES segments(id),
  payload_hash TEXT NOT NULL, body_json TEXT NOT NULL, event_revision INTEGER NOT NULL DEFAULT 0,
  action_slot TEXT NOT NULL DEFAULT '', delivery_status TEXT NOT NULL,
  created_at TEXT NOT NULL, UNIQUE(producer_id,sequence)
);
CREATE TABLE IF NOT EXISTS runtime_decisions(
  action_slot TEXT PRIMARY KEY, event_id TEXT NOT NULL UNIQUE,
  event_revision INTEGER NOT NULL, event_hash TEXT NOT NULL, decision TEXT NOT NULL,
  command_id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS delivery_receipts(
  delivery_id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id),
  history_proof_sha256 TEXT NOT NULL, decisions_hash TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS review_decisions(
  action_slot TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id),
  event_id TEXT NOT NULL UNIQUE, event_revision INTEGER NOT NULL, event_hash TEXT NOT NULL,
  decision TEXT NOT NULL, command_id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS retry_decisions(
  action_slot TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id),
  work_revision INTEGER NOT NULL, source_event_id TEXT NOT NULL UNIQUE,
  source_event_revision INTEGER NOT NULL, source_event_hash TEXT NOT NULL,
  source_segment_id TEXT NOT NULL REFERENCES segments(id), next_attempt_no INTEGER NOT NULL,
  use_next_fallback INTEGER NOT NULL, command_id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS report_capabilities(
  capability_id TEXT PRIMARY KEY, segment_id TEXT NOT NULL UNIQUE REFERENCES segments(id),
  producer_id TEXT NOT NULL REFERENCES runtime_hosts(id), token_hash TEXT NOT NULL DEFAULT '',
  capability_dir TEXT NOT NULL DEFAULT '', run_id TEXT NOT NULL, task_id TEXT NOT NULL,
  attempt_id TEXT NOT NULL, work_revision INTEGER NOT NULL, execution_epoch INTEGER NOT NULL,
  status TEXT NOT NULL, used_bytes INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS report_events(
  event_id TEXT PRIMARY KEY, capability_id TEXT NOT NULL REFERENCES report_capabilities(capability_id),
  sequence INTEGER NOT NULL, kind TEXT NOT NULL, payload_hash TEXT NOT NULL, body_json TEXT NOT NULL,
  event_revision INTEGER NOT NULL, action_slot TEXT NOT NULL, delivery_status TEXT NOT NULL,
  accounted_bytes INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, UNIQUE(capability_id,sequence)
);
CREATE TABLE IF NOT EXISTS delivery_order(
  ordinal INTEGER PRIMARY KEY AUTOINCREMENT, event_id TEXT NOT NULL UNIQUE,
  task_id TEXT NOT NULL REFERENCES tasks(id)
);
CREATE TABLE IF NOT EXISTS stop_runtime(
  command_id TEXT PRIMARY KEY, host_id TEXT NOT NULL REFERENCES runtime_hosts(id),
  segment_id TEXT NOT NULL UNIQUE REFERENCES segments(id), reason TEXT NOT NULL,
  deadline_unix_ms INTEGER NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_questions(
  question_id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id),
  attempt_id TEXT NOT NULL REFERENCES attempts(id), segment_id TEXT NOT NULL REFERENCES segments(id),
  question_revision INTEGER NOT NULL, work_revision INTEGER NOT NULL,
  session_kind TEXT NOT NULL, session_id TEXT NOT NULL, status TEXT NOT NULL,
  answer_hash TEXT NOT NULL DEFAULT '', answer_text TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
  UNIQUE(task_id,question_revision)
);
CREATE TABLE IF NOT EXISTS segment_sessions(
  segment_id TEXT PRIMARY KEY REFERENCES segments(id), attempt_id TEXT NOT NULL REFERENCES attempts(id),
  session_kind TEXT NOT NULL, session_id TEXT NOT NULL, event_id TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL
);`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`ALTER TABLE task_runtime ADD COLUMN adapter_payload TEXT NOT NULL DEFAULT '{}'`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE task_runtime ADD COLUMN fallback_payloads TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE task_runtime ADD COLUMN fallback_index INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE task_runtime ADD COLUMN completion_policy TEXT NOT NULL DEFAULT 'owner_review'`,
		`ALTER TABLE task_runtime ADD COLUMN expected_artifact_sha256 TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runtime_events ADD COLUMN event_revision INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runtime_events ADD COLUMN action_slot TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE report_events ADD COLUMN accounted_bytes INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, alterErr := tx.Exec(statement); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column") {
			return alterErr
		}
	}
	if _, err = tx.Exec(`UPDATE report_events SET accounted_bytes=length(body_json) WHERE accounted_bytes=0`); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO delivery_order(event_id,task_id)
	 SELECT event_id,task_id FROM (
	   SELECT event_id,task_id,created_at FROM runtime_events WHERE delivery_status!='internal'
	   UNION ALL
	   SELECT e.event_id,c.task_id,e.created_at FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id
	 ) ORDER BY created_at,event_id`)
	return err
}
