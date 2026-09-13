package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

const (
	defaultSegmentActiveMS = int64(60 * 60 * 1000)
	defaultGroupActiveMS   = int64(120 * 60 * 1000)
)

const (
	ErrNoSlot            CodeError = "no_execution_slot"
	ErrNoReady           CodeError = "no_ready_task"
	ErrInvalidDAG        CodeError = "invalid_dag"
	ErrHostRejected      CodeError = "host_rejected"
	ErrEventSequence     CodeError = "event_sequence_conflict"
	ErrAdmissionDeferred CodeError = "admission_deferred"
)

type HostLaunchSpec struct {
	OriginContextID string
	HostGeneration  string
	Executable      string
}

type PlanSpec struct {
	Run          RunSpec
	Host         HostLaunchSpec
	Tasks        []TaskSpec
	LaunchID     string
	LaunchToken  string
	ControlToken string
}

type SubmitReceipt struct {
	LaunchID     string `json:"launch_id"`
	LaunchToken  string `json:"launch_token"`
	ControlToken string `json:"control_token"`
}

type HostBinding struct {
	HostID string
	PID    int
	Birth  string
	Active int
}

func (d *DB) ensureRuntimeSchema() error {
	_, err := d.sql.Exec(`
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
	_, err = d.sql.Exec(`ALTER TABLE task_runtime ADD COLUMN adapter_payload TEXT NOT NULL DEFAULT '{}'`)
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
		if _, alterErr := d.sql.Exec(statement); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column") {
			return alterErr
		}
	}
	if _, err = d.sql.Exec(`UPDATE report_events SET accounted_bytes=length(body_json) WHERE accounted_bytes=0`); err != nil {
		return err
	}
	_, err = d.sql.Exec(`INSERT OR IGNORE INTO delivery_order(event_id,task_id)
	 SELECT event_id,task_id FROM (
	   SELECT event_id,task_id,created_at FROM runtime_events WHERE delivery_status!='internal'
	   UNION ALL
	   SELECT e.event_id,c.task_id,e.created_at FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id
	 ) ORDER BY created_at,event_id`)
	return err
}

func (d *DB) BeginCoordinatorEpoch(ctx context.Context, epoch uint64) error {
	if epoch == 0 {
		return CodeError("invalid_epoch")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO coordinator(id,epoch,updated_at) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET epoch=excluded.epoch,updated_at=excluded.updated_at`, epoch, now); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='offline',coordinator_epoch=?,updated_at=?`, epoch, now); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='offline' WHERE status='ready'`); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func validatePlan(p PlanSpec) error {
	if p.Run.ID == "" || p.Run.ControllerThread == "" || p.Run.PlanRevision < 1 || p.Run.OriginContextID == "" || p.Run.OriginPID <= 0 || p.Run.OriginBirth == "" {
		return CodeError("invalid_run")
	}
	if p.Host.OriginContextID != p.Run.OriginContextID || p.Host.HostGeneration == "" || p.Host.Executable == "" || len(p.Tasks) == 0 || len(p.Tasks) > 64 {
		return ErrInvalidDAG
	}
	byID := make(map[string]TaskSpec, len(p.Tasks))
	groupAttempts := make(map[string]int)
	groupActive := make(map[string]int64)
	for _, task := range p.Tasks {
		if task.ID == "" || task.RunID != p.Run.ID || task.MaxAttempts < 1 || task.MaxAttempts > 3 {
			return ErrInvalidDAG
		}
		if _, exists := byID[task.ID]; exists {
			return ErrInvalidDAG
		}
		if task.CompletionPolicy != "" && task.CompletionPolicy != "owner_review" && task.CompletionPolicy != "artifact" && task.CompletionPolicy != "exit_success_fixture" {
			return ErrInvalidDAG
		}
		if task.CompletionPolicy == "artifact" && !validDigest(task.ExpectedArtifactSHA256) {
			return ErrInvalidDAG
		}
		if task.CompletionPolicy != "artifact" && task.ExpectedArtifactSHA256 != "" {
			return ErrInvalidDAG
		}
		if task.CompletionPolicy == "exit_success_fixture" {
			var discriminator struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(task.AdapterPayload, &discriminator) != nil || discriminator.Kind != "fake" {
				return ErrInvalidDAG
			}
		}
		for _, fallback := range task.FallbackPayloads {
			if len(fallback) == 0 || !json.Valid(fallback) {
				return ErrInvalidDAG
			}
		}
		group := task.BudgetGroupID
		if group == "" {
			group = task.ID
		}
		if existing, ok := groupAttempts[group]; ok && existing != task.MaxAttempts {
			return ErrInvalidDAG
		}
		groupAttempts[group] = task.MaxAttempts
		active := task.MaxActiveMS
		if active <= 0 || active > defaultSegmentActiveMS {
			active = defaultSegmentActiveMS
		}
		if existing, ok := groupActive[group]; ok && existing != active {
			return ErrInvalidDAG
		}
		groupActive[group] = active
		byID[task.ID] = task
	}
	state := make(map[string]uint8, len(byID))
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return ErrInvalidDAG
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, dep := range byID[id].Dependencies {
			if dep == id {
				return ErrInvalidDAG
			}
			if _, ok := byID[dep]; !ok {
				return ErrInvalidDAG
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) SubmitPlan(ctx context.Context, p PlanSpec) (SubmitReceipt, error) {
	if err := validatePlan(p); err != nil {
		return SubmitReceipt{}, err
	}
	launchID, token, controlToken := p.LaunchID, p.LaunchToken, p.ControlToken
	var err error
	if launchID == "" {
		launchID, err = newID("host-launch")
		if err != nil {
			return SubmitReceipt{}, err
		}
	}
	if token == "" {
		token, err = newID("host-token")
		if err != nil {
			return SubmitReceipt{}, err
		}
	}
	if controlToken == "" {
		controlToken, err = newID("control-token")
		if err != nil {
			return SubmitReceipt{}, err
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return SubmitReceipt{}, err
	}
	rollback := func(e error) (SubmitReceipt, error) { _ = tx.Rollback(); return SubmitReceipt{}, e }
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var activeHosts int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM host_launches WHERE status!='released'`).Scan(&activeHosts); err != nil {
		return rollback(err)
	}
	if activeHosts >= 4 {
		return rollback(ErrAdmissionDeferred)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at) VALUES(?,?,?,?,?,?,?,?)`, p.Run.ID, p.Run.ControllerThread, p.Run.PlanRevision, p.Run.OriginContextID, p.Run.OriginPID, p.Run.OriginBirth, "queued", now); err != nil {
		return rollback(mapConflict(err))
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO host_launches(id,token_hash,origin_context_id,host_generation,executable,status,created_at) VALUES(?,?,?,?,?,'reserved',?)`, launchID, runtimeHash(token), p.Host.OriginContextID, p.Host.HostGeneration, p.Host.Executable, now); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO run_control(run_id,token_hash) VALUES(?,?)`, p.Run.ID, runtimeHash(controlToken)); err != nil {
		return rollback(err)
	}
	for _, task := range p.Tasks {
		deps, _ := json.Marshal(task.Dependencies)
		status := "queued"
		var ready any
		if len(task.Dependencies) == 0 {
			status = "ready"
			sequence, seqErr := takeReadySequence(ctx, tx)
			if seqErr != nil {
				return rollback(seqErr)
			}
			ready = sequence
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,run_id,status,dependencies_json,max_attempts,created_at) VALUES(?,?,?,?,?,?)`, task.ID, p.Run.ID, status, string(deps), task.MaxAttempts, now); err != nil {
			return rollback(mapConflict(err))
		}
		revision := task.WorkRevision
		if revision < 1 {
			revision = 1
		}
		group := task.BudgetGroupID
		if group == "" {
			group = task.ID
		}
		active := task.MaxActiveMS
		if active <= 0 || active > defaultSegmentActiveMS {
			active = defaultSegmentActiveMS
		}
		payload := task.AdapterPayload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		if !json.Valid(payload) {
			return rollback(CodeError("invalid_adapter_payload"))
		}
		fallbacks, _ := json.Marshal(task.FallbackPayloads)
		policy := task.CompletionPolicy
		if policy == "" {
			policy = "owner_review"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_runtime(task_id,host_launch_id,work_revision,budget_group_id,max_active_ms,ready_sequence,adapter_payload,fallback_payloads,fallback_index,completion_policy,expected_artifact_sha256) VALUES(?,?,?,?,?,?,?,?,0,?,?)`, task.ID, launchID, revision, group, active, ready, string(payload), string(fallbacks), policy, strings.ToLower(task.ExpectedArtifactSHA256)); err != nil {
			return rollback(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return SubmitReceipt{}, err
	}
	return SubmitReceipt{LaunchID: launchID, LaunchToken: token, ControlToken: controlToken}, nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func mapConflict(err error) error {
	if err != nil && (strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "PRIMARY KEY")) {
		return ErrConflict
	}
	return err
}

func takeReadySequence(ctx context.Context, tx *sql.Tx) (int64, error) {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT next_ready_sequence FROM runtime_meta WHERE id=1`).Scan(&sequence); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_meta SET next_ready_sequence=? WHERE id=1`, sequence+1); err != nil {
		return 0, err
	}
	return sequence, nil
}

func (d *DB) RegisterHost(ctx context.Context, hello contract.HostHello, epoch uint64) (string, error) {
	if hello.LaunchID == "" || hello.LaunchToken == "" || hello.OriginContextID == "" || hello.OriginPID <= 0 || hello.OriginBirth == "" || hello.HostGeneration == "" || hello.PID <= 0 || hello.Birth == "" || hello.Executable == "" || epoch == 0 {
		return "", ErrHostRejected
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	rollback := func(e error) (string, error) { _ = tx.Rollback(); return "", e }
	var tokenHash, origin, generation, executable string
	var existing sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT token_hash,origin_context_id,host_generation,executable,host_id FROM host_launches WHERE id=?`, hello.LaunchID).Scan(&tokenHash, &origin, &generation, &executable, &existing); err != nil {
		return rollback(ErrHostRejected)
	}
	want := runtimeHash(hello.LaunchToken)
	if subtle.ConstantTimeCompare([]byte(tokenHash), []byte(want)) != 1 || origin != hello.OriginContextID || generation != hello.HostGeneration || executable != hello.Executable {
		return rollback(ErrHostRejected)
	}
	var originPID int
	var originBirth string
	if err = tx.QueryRowContext(ctx, `SELECT r.origin_pid,r.origin_birth FROM runs r JOIN tasks t ON t.run_id=r.id JOIN task_runtime tr ON tr.task_id=t.id WHERE tr.host_launch_id=? LIMIT 1`, hello.LaunchID).Scan(&originPID, &originBirth); err != nil || originPID != hello.OriginPID || originBirth != hello.OriginBirth {
		return rollback(ErrHostRejected)
	}
	hostID := existing.String
	if !existing.Valid || hostID == "" {
		hostID = hello.LaunchID
	} else {
		var pid int
		var birth, hostStatus string
		if err = tx.QueryRowContext(ctx, `SELECT pid,birth,status FROM runtime_hosts WHERE id=?`, hostID).Scan(&pid, &birth, &hostStatus); err != nil {
			return rollback(ErrHostRejected)
		}
		if pid != hello.PID || birth != hello.Birth {
			var active int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime WHERE host_id=? AND status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, hostID).Scan(&active); err != nil || (active != 0 && hostStatus != "reconcile_authorized") {
				return rollback(ErrHostRejected)
			}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_hosts(id,launch_id,origin_context_id,host_generation,pid,birth,executable,coordinator_epoch,status,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET pid=excluded.pid,birth=excluded.birth,executable=excluded.executable,coordinator_epoch=excluded.coordinator_epoch,status='ready',updated_at=excluded.updated_at`, hostID, hello.LaunchID, hello.OriginContextID, hello.HostGeneration, hello.PID, hello.Birth, hello.Executable, epoch, "ready", now); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host_launches SET host_id=?,status='ready' WHERE id=?`, hostID, hello.LaunchID); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return hostID, nil
}

func (d *DB) HostBinding(ctx context.Context, launchID string) (HostBinding, error) {
	var binding HostBinding
	err := d.sql.QueryRowContext(ctx, `SELECT h.id,h.pid,h.birth,(SELECT COUNT(*) FROM segment_runtime s WHERE s.host_id=h.id AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')) FROM runtime_hosts h WHERE h.launch_id=?`, launchID).Scan(&binding.HostID, &binding.PID, &binding.Birth, &binding.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return binding, ErrNotFound
	}
	return binding, err
}

func (d *DB) AuthorizeHostRebind(ctx context.Context, launchID string, pid int, birth string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	result, err := d.sql.ExecContext(ctx, `UPDATE runtime_hosts SET status='reconcile_authorized',updated_at=? WHERE launch_id=? AND pid=? AND birth=? AND status='offline'`, time.Now().UTC().Format(time.RFC3339Nano), launchID, pid, birth)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrHostRejected
	}
	return nil
}

func (d *DB) BeginHostReconcile(ctx context.Context, hostID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='reconciling' WHERE id=?`, hostID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='reconciling' WHERE host_id=?`, hostID); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (d *DB) FinishHostReconcile(ctx context.Context, hostID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='ready' WHERE id=? AND status='reconciling'`, hostID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='ready' WHERE host_id=?`, hostID); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (d *DB) HostLaunchStatus(ctx context.Context, launchID, token string) (string, error) {
	if launchID == "" || token == "" {
		return "", ErrHostRejected
	}
	var tokenHash, status string
	if err := d.sql.QueryRowContext(ctx, `SELECT token_hash,status FROM host_launches WHERE id=?`, launchID).Scan(&tokenHash, &status); err != nil {
		return "", ErrHostRejected
	}
	if subtle.ConstantTimeCompare([]byte(tokenHash), []byte(runtimeHash(token))) != 1 {
		return "", ErrHostRejected
	}
	return status, nil
}

func (d *DB) MarkHostOffline(ctx context.Context, hostID string, epoch uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	result, err := tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='offline',updated_at=? WHERE id=? AND coordinator_epoch=? AND status='ready'`, time.Now().UTC().Format(time.RFC3339Nano), hostID, epoch)
	if err != nil {
		return rollback(err)
	}
	changed, _ := result.RowsAffected()
	if changed == 1 {
		if _, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='offline' WHERE host_id=?`, hostID); err != nil {
			return rollback(err)
		}
	}
	return tx.Commit()
}

func (d *DB) ClaimReady(ctx context.Context, hostID string, epoch uint64, slots int) (contract.LaunchCommand, error) {
	if slots < 1 || slots > 2 {
		slots = 2
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.LaunchCommand{}, err
	}
	rollback := func(e error) (contract.LaunchCommand, error) { _ = tx.Rollback(); return contract.LaunchCommand{}, e }
	var launchID string
	var registeredEpoch uint64
	if err = tx.QueryRowContext(ctx, `SELECT launch_id,coordinator_epoch FROM runtime_hosts WHERE id=? AND status='ready'`, hostID).Scan(&launchID, &registeredEpoch); err != nil || registeredEpoch != epoch {
		return rollback(ErrHostRejected)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime WHERE status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`).Scan(&active); err != nil {
		return rollback(err)
	}
	if active >= slots {
		return rollback(ErrNoSlot)
	}
	var taskID, runID, taskStatus, budgetGroup string
	var workRevision, maxAttempts int
	var maxActive int64
	var adapterPayload string
	err = tx.QueryRowContext(ctx, `SELECT t.id,t.run_id,t.status,r.work_revision,r.budget_group_id,r.max_active_ms,t.max_attempts,r.adapter_payload FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE r.host_launch_id=? AND t.status IN ('ready','resume_queued') ORDER BY r.ready_sequence LIMIT 1`, launchID).Scan(&taskID, &runID, &taskStatus, &workRevision, &budgetGroup, &maxActive, &maxAttempts, &adapterPayload)
	if errors.Is(err, sql.ErrNoRows) {
		return rollback(ErrNoReady)
	}
	if err != nil {
		return rollback(err)
	}
	if err = d.storageAdmission(ctx, tx, runID, 0, false); err != nil {
		return rollback(err)
	}
	var attemptID string
	var attemptNo, segmentNo int
	if taskStatus == "resume_queued" {
		if err = tx.QueryRowContext(ctx, `SELECT id,attempt_no FROM attempts WHERE task_id=? ORDER BY attempt_no DESC LIMIT 1`, taskID).Scan(&attemptID, &attemptNo); err != nil {
			return rollback(err)
		}
		var owned int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime WHERE attempt_id=? AND status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, attemptID).Scan(&owned); err != nil || owned != 0 {
			if err == nil {
				err = ErrConflict
			}
			return rollback(err)
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segments WHERE attempt_id=?`, attemptID).Scan(&segmentNo); err != nil {
			return rollback(err)
		}
		segmentNo++
	} else {
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts a JOIN task_runtime r ON r.task_id=a.task_id WHERE r.budget_group_id=?`, budgetGroup).Scan(&attemptNo); err != nil {
			return rollback(err)
		}
		if attemptNo >= maxAttempts || attemptNo >= 3 {
			return rollback(ErrAttemptLimit)
		}
		attemptNo++
		attemptID, err = newID("attempt")
		if err != nil {
			return rollback(err)
		}
		segmentNo = 1
	}
	segmentID, err := newID("segment")
	if err != nil {
		return rollback(err)
	}
	commandID, err := newID("command")
	if err != nil {
		return rollback(err)
	}
	slotToken, err := newID("slot")
	if err != nil {
		return rollback(err)
	}
	reservationID, err := newID("reservation")
	if err != nil {
		return rollback(err)
	}
	reportCapabilityID, err := newID("report-capability")
	if err != nil {
		return rollback(err)
	}
	var charged int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='settled' THEN used_ms ELSE granted_ms END),0) FROM budget_runtime WHERE budget_group_id=?`, budgetGroup).Scan(&charged); err != nil {
		return rollback(err)
	}
	grant := maxActive
	if remaining := defaultGroupActiveMS - charged; grant > remaining {
		grant = remaining
	}
	if grant <= 0 {
		return rollback(CodeError("budget_exhausted"))
	}
	now := time.Now().UTC()
	deadline := now.Add(time.Duration(grant) * time.Millisecond).UnixMilli()
	intentHash := runtimeHash(runID + "\x00" + taskID + "\x00" + attemptID + "\x00" + segmentID + "\x00" + slotToken)
	stamp := now.Format(time.RFC3339Nano)
	if taskStatus != "resume_queued" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO attempts(id,task_id,attempt_no,status,created_at) VALUES(?,?,?,?,?)`, attemptID, taskID, attemptNo, "running", stamp); err != nil {
			return rollback(err)
		}
	} else if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status='running' WHERE id=?`, attemptID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO segments(id,attempt_id,segment_no,status,created_at) VALUES(?,?,?,?,?)`, segmentID, attemptID, segmentNo, "launch_requested", stamp); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO segment_runtime(segment_id,attempt_id,host_id,command_id,execution_epoch,slot_token,launch_intent_hash,reservation_id,status,outcome,deadline_unix_ms,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, segmentID, attemptID, hostID, commandID, epoch, slotToken, intentHash, reservationID, "launch_requested", "", deadline, stamp); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_runtime(id,budget_group_id,segment_id,granted_ms,used_ms,status,created_at) VALUES(?,?,?,?,0,'reserved',?)`, reservationID, budgetGroup, segmentID, grant, stamp); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO report_capabilities(capability_id,segment_id,producer_id,run_id,task_id,attempt_id,work_revision,execution_epoch,status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, reportCapabilityID, segmentID, hostID, runID, taskID, attemptID, workRevision, epoch, "pending", stamp); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='running' WHERE id=?`, taskID); err != nil {
		return rollback(err)
	}
	command := contract.LaunchCommand{CommandID: commandID, HostID: hostID, RunID: runID, TaskID: taskID, AttemptID: attemptID, SegmentID: segmentID, WorkRevision: workRevision, ExecutionEpoch: epoch, SlotToken: slotToken, LaunchIntentHash: intentHash, ReservationID: reservationID, BudgetGroupID: budgetGroup, GrantedActiveMS: grant, DeadlineUnixMS: deadline, AdapterPayload: json.RawMessage(adapterPayload), ReportCapabilityID: reportCapabilityID}
	if taskStatus == "resume_queued" {
		err = tx.QueryRowContext(ctx, `SELECT question_id,question_revision,session_kind,session_id,answer_text FROM runtime_questions WHERE task_id=? AND attempt_id=? AND status='resume_queued' ORDER BY question_revision DESC LIMIT 1`, taskID, attemptID).Scan(&command.QuestionID, &command.QuestionRevision, &command.SessionKind, &command.SessionID, &command.Answer)
		if err == nil {
			if _, err = tx.ExecContext(ctx, `UPDATE runtime_questions SET status='resume_started' WHERE question_id=? AND status='resume_queued'`, command.QuestionID); err != nil {
				return rollback(err)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return rollback(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return contract.LaunchCommand{}, err
	}
	return command, nil
}

// PendingLaunches returns crash-window grants without creating a new segment or
// budget reservation. The caller may resend these idempotently after a Host
// reconnects to a new coordinator epoch.
func (d *DB) PendingLaunches(ctx context.Context, hostID string, epoch uint64) ([]contract.LaunchCommand, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	rollback := func(e error) ([]contract.LaunchCommand, error) { _ = tx.Rollback(); return nil, e }
	var registered uint64
	if err = tx.QueryRowContext(ctx, `SELECT coordinator_epoch FROM runtime_hosts WHERE id=? AND status='ready'`, hostID).Scan(&registered); err != nil || registered != epoch {
		return rollback(ErrHostRejected)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE segment_runtime SET execution_epoch=? WHERE host_id=? AND status='launch_requested'`, epoch, hostID); err != nil {
		return rollback(err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,r.work_revision,s.execution_epoch,s.slot_token,s.launch_intent_hash,s.reservation_id,r.budget_group_id,b.granted_ms,s.deadline_unix_ms,r.adapter_payload,(SELECT capability_id FROM report_capabilities WHERE segment_id=s.segment_id) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime r ON r.task_id=t.id JOIN budget_runtime b ON b.id=s.reservation_id WHERE s.host_id=? AND s.status='launch_requested' ORDER BY s.created_at,s.segment_id`, hostID)
	if err != nil {
		return rollback(err)
	}
	var commands []contract.LaunchCommand
	for rows.Next() {
		var command contract.LaunchCommand
		var adapterPayload string
		if err = rows.Scan(&command.CommandID, &command.HostID, &command.RunID, &command.TaskID, &command.AttemptID, &command.SegmentID, &command.WorkRevision, &command.ExecutionEpoch, &command.SlotToken, &command.LaunchIntentHash, &command.ReservationID, &command.BudgetGroupID, &command.GrantedActiveMS, &command.DeadlineUnixMS, &adapterPayload, &command.ReportCapabilityID); err != nil {
			_ = rows.Close()
			return rollback(err)
		}
		command.AdapterPayload = json.RawMessage(adapterPayload)
		commands = append(commands, command)
	}
	if err = rows.Close(); err != nil {
		return rollback(err)
	}
	for index := range commands {
		command := &commands[index]
		err = tx.QueryRowContext(ctx, `SELECT question_id,question_revision,session_kind,session_id,answer_text FROM runtime_questions WHERE task_id=? AND attempt_id=? AND status='resume_started' ORDER BY question_revision DESC LIMIT 1`, command.TaskID, command.AttemptID).Scan(&command.QuestionID, &command.QuestionRevision, &command.SessionKind, &command.SessionID, &command.Answer)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return rollback(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return commands, nil
}

func (d *DB) storageAdmission(ctx context.Context, tx *sql.Tx, runID string, incoming int64, diagnostic bool) error {
	if incoming < 0 {
		return CodeError("control_spool_full")
	}
	available, err := d.storageAvailable(filepath.Dir(d.path))
	if err != nil {
		return CodeError("storage_blocked")
	}
	required := d.minFreeBytes
	addRequired := func(value uint64) bool {
		if ^uint64(0)-required < value {
			return false
		}
		required += value
		return true
	}
	if diagnostic && !addRequired(d.criticalDiskReserve) {
		return CodeError("storage_blocked")
	}
	if !addRequired(uint64(incoming)) || !addRequired(d.sqliteWriteOverhead) || available < required {
		return CodeError("storage_blocked")
	}
	var runBytes, runProgressBytes, userBytes, userProgressBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT
	 COALESCE((SELECT SUM(length(e.body_json)) FROM runtime_events e JOIN tasks t ON t.id=e.task_id WHERE t.run_id=?),0) +
	 COALESCE((SELECT SUM(e.accounted_bytes) FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.run_id=?),0),
	 COALESCE((SELECT SUM(e.accounted_bytes) FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.run_id=? AND e.kind='progress'),0)`, runID, runID, runID).Scan(&runBytes, &runProgressBytes); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT
	 COALESCE((SELECT SUM(length(body_json)) FROM runtime_events),0) + COALESCE((SELECT SUM(accounted_bytes) FROM report_events),0),
	 COALESCE((SELECT SUM(accounted_bytes) FROM report_events WHERE kind='progress'),0)`).Scan(&userBytes, &userProgressBytes); err != nil {
		return err
	}
	exceeds := func(used, limit int64) bool { return incoming > limit || used > limit-incoming }
	if exceeds(runBytes, d.runControlLimit) || exceeds(userBytes, d.userControlLimit) {
		return CodeError("control_spool_full")
	}
	if diagnostic && (exceeds(runProgressBytes, d.runProgressLimit) || exceeds(userProgressBytes, d.userProgressLimit)) {
		return CodeError("report_budget_exhausted")
	}
	return nil
}

func (d *DB) CommitHostEvent(ctx context.Context, event contract.Event) (contract.DurableAck, error) {
	if event.Version != 1 || event.ProducerID == "" || event.EventID == "" || event.Sequence < 1 || event.TaskID == "" || event.AttemptID == "" || event.SegmentID == "" || event.CommandID == "" || event.PayloadHash == "" {
		return contract.DurableAck{}, CodeError("invalid_event")
	}
	body, err := json.Marshal(event)
	if err != nil {
		return contract.DurableAck{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.DurableAck{}, err
	}
	rollback := func(e error) (contract.DurableAck, error) { _ = tx.Rollback(); return contract.DurableAck{}, e }
	var registeredEpoch uint64
	// A rebound Host must replay its durable spool before it is marked ready
	// for new dispatch. Those replay events still belong to the newly verified
	// Host session and epoch, so accept them while the Host is reconciling.
	if err = tx.QueryRowContext(ctx, `SELECT coordinator_epoch FROM runtime_hosts WHERE id=? AND status IN ('ready','reconciling')`, event.ProducerID).Scan(&registeredEpoch); err != nil || registeredEpoch != event.ExecutionEpoch {
		return rollback(ErrHostRejected)
	}
	var oldID, oldHash, oldBody string
	err = tx.QueryRowContext(ctx, `SELECT event_id,payload_hash,body_json FROM runtime_events WHERE producer_id=? AND sequence=?`, event.ProducerID, event.Sequence).Scan(&oldID, &oldHash, &oldBody)
	if err == nil {
		oldCanonical, oldErr := canonicalHostEvent([]byte(oldBody))
		newCanonical, newErr := canonicalHostEvent(body)
		if oldID != event.EventID || oldHash != event.PayloadHash || oldErr != nil || newErr != nil || subtle.ConstantTimeCompare(oldCanonical, newCanonical) != 1 {
			return rollback(ErrEventSequence)
		}
		_ = tx.Rollback()
		return contract.DurableAck{ProducerID: event.ProducerID, AckedThrough: event.Sequence, EventID: event.EventID, PayloadHash: event.PayloadHash, Status: "durable"}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	var last int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM runtime_events WHERE producer_id=?`, event.ProducerID).Scan(&last); err != nil {
		return rollback(err)
	}
	if event.Sequence != last+1 {
		return rollback(ErrEventSequence)
	}
	if err = d.storageAdmission(ctx, tx, event.RunID, int64(len(body)), false); err != nil {
		return rollback(err)
	}
	var storedHost, taskID, attemptID string
	if err = tx.QueryRowContext(ctx, `SELECT host_id,(SELECT task_id FROM attempts WHERE id=segment_runtime.attempt_id),attempt_id FROM segment_runtime WHERE segment_id=? AND command_id=?`, event.SegmentID, event.CommandID).Scan(&storedHost, &taskID, &attemptID); err != nil || storedHost != event.ProducerID || taskID != event.TaskID || attemptID != event.AttemptID {
		return rollback(ErrConflict)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	deliveryStatus := "pending"
	if event.Kind == contract.EventPrepared || event.Kind == contract.EventSpawned || event.Kind == contract.EventRunning || event.Kind == contract.EventSession || event.Kind == contract.EventExited {
		deliveryStatus = "internal"
	}
	actionSlot := "action-" + runtimeHash(event.ProducerID + "\x00" + event.EventID)[:32]
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,event_revision,action_slot,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, event.EventID, event.ProducerID, event.Sequence, event.TaskID, event.AttemptID, event.SegmentID, event.PayloadHash, string(body), event.Sequence, actionSlot, deliveryStatus, now); err != nil {
		return rollback(mapConflict(err))
	}
	if deliveryStatus == "pending" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_order(event_id,task_id) VALUES(?,?)`, event.EventID, event.TaskID); err != nil {
			return rollback(mapConflict(err))
		}
	}
	if err = applyEvent(ctx, tx, event); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return contract.DurableAck{}, err
	}
	return contract.DurableAck{ProducerID: event.ProducerID, AckedThrough: event.Sequence, EventID: event.EventID, PayloadHash: event.PayloadHash, Status: "durable"}, nil
}

func canonicalHostEvent(body []byte) ([]byte, error) {
	var event contract.Event
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, err
	}
	event.ExecutionEpoch = 0
	event.EventRevision = 0
	event.ActionSlot = ""
	return json.Marshal(event)
}

func applyEvent(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	switch event.Kind {
	case contract.EventPrepared, contract.EventSpawned, contract.EventRunning:
		allowed := ""
		switch event.Kind {
		case contract.EventPrepared:
			allowed = "launch_requested,prepared"
		case contract.EventSpawned:
			allowed = "prepared,spawned"
		case contract.EventRunning:
			allowed = "spawned,running"
		}
		result, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET status=? WHERE segment_id=? AND instr(?,status)>0 AND status!='exited'`, event.Kind, event.SegmentID, allowed)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE segments SET status=? WHERE id=?`, event.Kind, event.SegmentID)
		return err
	case contract.EventSession:
		if event.SessionKind == "" || len(event.SessionKind) > 64 || event.SessionID == "" || len(event.SessionID) > 512 {
			return CodeError("invalid_session")
		}
		var segmentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM segment_runtime WHERE segment_id=?`, event.SegmentID).Scan(&segmentStatus); err != nil {
			return err
		}
		if segmentStatus == "exited" {
			return nil
		}
		var sessionKind, sessionID string
		err := tx.QueryRowContext(ctx, `SELECT session_kind,session_id FROM segment_sessions WHERE segment_id=?`, event.SegmentID).Scan(&sessionKind, &sessionID)
		if err == nil {
			if sessionKind != event.SessionKind || sessionID != event.SessionID {
				return ErrConflict
			}
			return materializePendingReportQuestion(ctx, tx, event.SegmentID, sessionKind, sessionID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO segment_sessions(segment_id,attempt_id,session_kind,session_id,event_id,created_at) VALUES(?,?,?,?,?,?)`, event.SegmentID, event.AttemptID, event.SessionKind, event.SessionID, event.EventID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return mapConflict(err)
		}
		return materializePendingReportQuestion(ctx, tx, event.SegmentID, event.SessionKind, event.SessionID)
	case contract.EventQuestion:
		if event.QuestionID == "" || event.QuestionRevision < 1 || event.SessionKind == "" || event.SessionID == "" {
			return CodeError("invalid_question")
		}
		var segmentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM segment_runtime WHERE segment_id=?`, event.SegmentID).Scan(&segmentStatus); err != nil {
			return err
		}
		if segmentStatus == "exited" {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_questions(question_id,task_id,attempt_id,segment_id,question_revision,work_revision,session_kind,session_id,status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, event.QuestionID, event.TaskID, event.AttemptID, event.SegmentID, event.QuestionRevision, event.WorkRevision, event.SessionKind, event.SessionID, "open", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return mapConflict(err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET outcome=? WHERE segment_id=? AND status!='exited' AND outcome=''`, event.Kind, event.SegmentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return ErrConflict
		}
		return nil
	case contract.EventResult, contract.EventStopped, contract.EventFailed:
		_, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET outcome=? WHERE segment_id=? AND status!='exited' AND (outcome='' OR outcome=?)`, event.Kind, event.SegmentID, event.Kind)
		if err == nil && event.Kind == contract.EventStopped {
			_, err = tx.ExecContext(ctx, `UPDATE stop_runtime SET status='accepted' WHERE segment_id=?`, event.SegmentID)
		}
		return err
	case contract.EventUnknown:
		result, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET status='unknown' WHERE segment_id=? AND status!='exited'`, event.SegmentID)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			return nil
		}
		if _, err = tx.ExecContext(ctx, `UPDATE segments SET status='unknown' WHERE id=?`, event.SegmentID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status='unknown' WHERE id=?`, event.AttemptID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET status='unknown' WHERE id=?`, event.TaskID)
		return err
	case contract.EventExited:
		return applyExit(ctx, tx, event)
	default:
		return CodeError("invalid_event_kind")
	}
}

func materializePendingReportQuestion(ctx context.Context, tx *sql.Tx, segmentID, sessionKind, sessionID string) error {
	var eventID, body, taskID string
	err := tx.QueryRowContext(ctx, `SELECT e.event_id,e.body_json,c.task_id FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.segment_id=? AND e.kind='question' AND e.delivery_status='pending_session' ORDER BY e.sequence LIMIT 1`, segmentID).Scan(&eventID, &body, &taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var event contract.Event
	if json.Unmarshal([]byte(body), &event) != nil || event.Kind != contract.EventQuestion || event.SegmentID != segmentID {
		return ErrConflict
	}
	event.SessionKind, event.SessionID = sessionKind, sessionID
	bodyBytes, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE report_events SET body_json=?,delivery_status='pending' WHERE event_id=? AND delivery_status='pending_session'`, string(bodyBytes), eventID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_order(event_id,task_id) VALUES(?,?)`, eventID, taskID); err != nil {
		return mapConflict(err)
	}
	if err = applyEvent(ctx, tx, event); err != nil {
		return err
	}
	return ensureQuestionStop(ctx, tx, event)
}

func ensureQuestionStop(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	var existingReason string
	err := tx.QueryRowContext(ctx, `SELECT reason FROM stop_runtime WHERE segment_id=?`, event.SegmentID).Scan(&existingReason)
	if err == nil {
		if existingReason != "waiting_question" {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	commandID, err := newID("stop")
	if err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second).UnixMilli()
	result, err := tx.ExecContext(ctx, `INSERT INTO stop_runtime(command_id,host_id,segment_id,reason,deadline_unix_ms,status,created_at) SELECT ?,host_id,?,'waiting_question',?,'pending',? FROM segment_runtime WHERE segment_id=? AND status IN ('prepared','spawned','running')`, commandID, event.SegmentID, deadline, time.Now().UTC().Format(time.RFC3339Nano), event.SegmentID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE segment_runtime SET status='stopping' WHERE segment_id=? AND status IN ('prepared','spawned','running')`, event.SegmentID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET status='stopping' WHERE id=?`, event.TaskID)
	return err
}

func applyExit(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	var outcome, reservation, segmentStatus string
	var grant int64
	if err := tx.QueryRowContext(ctx, `SELECT outcome,reservation_id,status,(SELECT granted_ms FROM budget_runtime WHERE id=segment_runtime.reservation_id) FROM segment_runtime WHERE segment_id=?`, event.SegmentID).Scan(&outcome, &reservation, &segmentStatus, &grant); err != nil {
		return err
	}
	if segmentStatus == "exited" {
		return nil
	}
	used := event.ActiveMS
	if used < 0 {
		used = grant
	}
	if used > grant {
		used = grant
	}
	if _, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET status='exited' WHERE segment_id=?`, event.SegmentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE segments SET status='exited' WHERE id=?`, event.SegmentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE budget_runtime SET status='settled',used_ms=? WHERE id=?`, used, reservation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE stop_runtime SET status='completed' WHERE segment_id=?`, event.SegmentID); err != nil {
		return err
	}
	attemptStatus, taskStatus := "interrupted", "interrupted"
	switch outcome {
	case contract.EventResult:
		attemptStatus, taskStatus = "result_ready", "result_ready"
		var policy, expected string
		if err := tx.QueryRowContext(ctx, `SELECT completion_policy,expected_artifact_sha256 FROM task_runtime WHERE task_id=?`, event.TaskID).Scan(&policy, &expected); err != nil {
			return err
		}
		if policy == "exit_success_fixture" || (policy == "artifact" && event.Artifact != nil && subtle.ConstantTimeCompare([]byte(strings.ToLower(event.Artifact.SHA256)), []byte(expected)) == 1) {
			attemptStatus, taskStatus = "result_ready", "completed"
		}
	case contract.EventQuestion:
		attemptStatus, taskStatus = "interrupted", "waiting_question"
	case contract.EventStopped:
		attemptStatus, taskStatus = "interrupted", "interrupted"
	case contract.EventFailed:
		attemptStatus, taskStatus = "failed", "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attempts SET status=? WHERE id=?`, attemptStatus, event.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_questions SET status='resolved' WHERE task_id=? AND attempt_id=? AND status='resume_started'`, event.TaskID, event.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=? WHERE id=?`, taskStatus, event.TaskID); err != nil {
		return err
	}
	if taskStatus == "completed" {
		return releaseReady(ctx, tx)
	}
	if taskStatus == "failed" {
		var attempts, maxAttempts int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),t.max_attempts FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE a.task_id=?`, event.TaskID).Scan(&attempts, &maxAttempts); err != nil {
			return err
		}
		if attempts >= maxAttempts || attempts >= 3 {
			return blockFailedDependencies(ctx, tx)
		}
	}
	return nil
}

func blockFailedDependencies(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,status,dependencies_json FROM tasks`)
	if err != nil {
		return err
	}
	type state struct {
		status string
		deps   []string
	}
	tasks := map[string]state{}
	for rows.Next() {
		var id, status, raw string
		if err = rows.Scan(&id, &status, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		var deps []string
		if err = json.Unmarshal([]byte(raw), &deps); err != nil {
			_ = rows.Close()
			return err
		}
		tasks[id] = state{status: status, deps: deps}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	changed := true
	for changed {
		changed = false
		for id, task := range tasks {
			if task.status != "queued" && task.status != "ready" {
				continue
			}
			for _, dep := range task.deps {
				depStatus := tasks[dep].status
				if depStatus == "failed" || depStatus == "cancelled" || depStatus == "budget_exhausted" || depStatus == "blocked_dependency" {
					task.status = "blocked_dependency"
					tasks[id] = task
					changed = true
					break
				}
			}
		}
	}
	for id, task := range tasks {
		if task.status == "blocked_dependency" {
			if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='blocked_dependency' WHERE id=? AND status IN ('queued','ready')`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func releaseReady(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,dependencies_json FROM tasks WHERE status='queued' ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	type candidate struct{ id, deps string }
	var candidates []candidate
	for rows.Next() {
		var candidate candidate
		if err = rows.Scan(&candidate.id, &candidate.deps); err != nil {
			_ = rows.Close()
			return err
		}
		candidates = append(candidates, candidate)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, candidate := range candidates {
		var deps []string
		if err = json.Unmarshal([]byte(candidate.deps), &deps); err != nil {
			return err
		}
		ready := true
		for _, dep := range deps {
			var status string
			if err = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id=?`, dep).Scan(&status); err != nil || (status != "completed" && status != "integrated") {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		sequence, seqErr := takeReadySequence(ctx, tx)
		if seqErr != nil {
			return seqErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='ready' WHERE id=? AND status='queued'`, candidate.id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, sequence, candidate.id); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) QueueResume(ctx context.Context, taskID string, workRevision int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	var status string
	var revision int
	if err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, taskID).Scan(&status, &revision); err != nil {
		return rollback(ErrNotFound)
	}
	if revision != workRevision || status != "interrupted" {
		return rollback(ErrConflict)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, taskID).Scan(&active); err != nil || active != 0 {
		if err == nil {
			err = ErrConflict
		}
		return rollback(err)
	}
	sequence, err := takeReadySequence(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='resume_queued' WHERE id=?`, taskID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, sequence, taskID); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

type AnswerSpec struct {
	TaskID           string
	WorkRevision     int
	QuestionID       string
	QuestionRevision int
	Answer           string
}

func (d *DB) AnswerQuestion(ctx context.Context, spec AnswerSpec) (string, error) {
	if spec.TaskID == "" || spec.WorkRevision < 1 || spec.QuestionID == "" || spec.QuestionRevision < 1 || strings.TrimSpace(spec.Answer) == "" || len(spec.Answer) > 32*1024 {
		return "", CodeError("invalid_answer")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	rollback := func(e error) (string, error) { _ = tx.Rollback(); return "", e }
	var taskStatus, questionStatus, answerHash, segmentID string
	var workRevision, questionRevision int
	if err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision,q.question_revision,q.status,q.answer_hash,q.segment_id FROM runtime_questions q JOIN tasks t ON t.id=q.task_id JOIN task_runtime r ON r.task_id=t.id WHERE q.question_id=? AND q.task_id=?`, spec.QuestionID, spec.TaskID).Scan(&taskStatus, &workRevision, &questionRevision, &questionStatus, &answerHash, &segmentID); err != nil {
		return rollback(ErrNotFound)
	}
	wantHash := runtimeHash(spec.Answer)
	if workRevision != spec.WorkRevision || questionRevision != spec.QuestionRevision {
		return rollback(ErrConflict)
	}
	if questionStatus != "open" {
		if answerHash == wantHash {
			_ = tx.Rollback()
			return questionStatus, nil
		}
		return rollback(ErrConflict)
	}
	if taskStatus != "waiting_question" {
		return rollback(ErrConflict)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, spec.TaskID).Scan(&active); err != nil || active != 0 {
		if err == nil {
			err = ErrConflict
		}
		return rollback(err)
	}
	var latestSegment string
	if err = tx.QueryRowContext(ctx, `SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1`, spec.TaskID).Scan(&latestSegment); err != nil || latestSegment != segmentID {
		return rollback(ErrConflict)
	}
	sequence, err := takeReadySequence(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_questions SET status='resume_queued',answer_hash=?,answer_text=? WHERE question_id=? AND status='open'`, wantHash, spec.Answer, spec.QuestionID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='resume_queued' WHERE id=?`, spec.TaskID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, sequence, spec.TaskID); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return "resume_queued", nil
}

type ReviewSpec struct {
	TaskID        string
	WorkRevision  int
	EventID       string
	EventRevision int64
	EventHash     string
	ActionSlot    string
	Decision      string
	CommandID     string
}

func (d *DB) ReviewResult(ctx context.Context, spec ReviewSpec) (string, error) {
	if spec.TaskID == "" || spec.WorkRevision < 1 || spec.EventID == "" || spec.EventRevision < 1 || spec.EventHash == "" || spec.ActionSlot == "" || spec.CommandID == "" || (spec.Decision != "accept" && spec.Decision != "reject") {
		return "", CodeError("invalid_review")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	rollback := func(e error) (string, error) { _ = tx.Rollback(); return "", e }
	var previousDecision, eventHash string
	var eventRevision int64
	err = tx.QueryRowContext(ctx, `SELECT decision,event_hash,event_revision FROM review_decisions WHERE action_slot=?`, spec.ActionSlot).Scan(&previousDecision, &eventHash, &eventRevision)
	if err == nil {
		if previousDecision != spec.Decision || eventHash != strings.ToLower(spec.EventHash) || eventRevision != spec.EventRevision {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return previousDecision + "ed", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	var taskStatus, storedHash, storedSlot, body string
	var workRevision int
	var storedRevision int64
	if err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision,e.payload_hash,e.event_revision,e.action_slot,e.body_json FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN runtime_events e ON e.task_id=t.id WHERE t.id=? AND e.event_id=?`, spec.TaskID, spec.EventID).Scan(&taskStatus, &workRevision, &storedHash, &storedRevision, &storedSlot, &body); err != nil || taskStatus != "result_ready" || workRevision != spec.WorkRevision || storedRevision != spec.EventRevision || storedSlot != spec.ActionSlot || subtle.ConstantTimeCompare([]byte(storedHash), []byte(strings.ToLower(spec.EventHash))) != 1 {
		return rollback(ErrConflict)
	}
	var event contract.Event
	if json.Unmarshal([]byte(body), &event) != nil || event.Kind != contract.EventResult {
		return rollback(ErrConflict)
	}
	var latestSegment string
	if err = tx.QueryRowContext(ctx, `SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1`, spec.TaskID).Scan(&latestSegment); err != nil || latestSegment != event.SegmentID {
		return rollback(ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_decisions(action_slot,task_id,event_id,event_revision,event_hash,decision,command_id,created_at) VALUES(?,?,?,?,?,?,?,?)`, spec.ActionSlot, spec.TaskID, spec.EventID, spec.EventRevision, strings.ToLower(spec.EventHash), spec.Decision, spec.CommandID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return rollback(mapConflict(err))
	}
	status := "accepted"
	if spec.Decision == "accept" {
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='completed' WHERE id=?`, spec.TaskID); err != nil {
			return rollback(err)
		}
		if err = releaseReady(ctx, tx); err != nil {
			return rollback(err)
		}
	} else {
		status = "rejected"
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='failed' WHERE id=?`, spec.TaskID); err != nil {
			return rollback(err)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status='failed' WHERE id=?`, event.AttemptID); err != nil {
			return rollback(err)
		}
		var attempts, maxAttempts int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),t.max_attempts FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE a.task_id=?`, spec.TaskID).Scan(&attempts, &maxAttempts); err != nil {
			return rollback(err)
		}
		if attempts >= maxAttempts || attempts >= 3 {
			if err = blockFailedDependencies(ctx, tx); err != nil {
				return rollback(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return status, nil
}

type RetrySpec struct {
	TaskID          string
	WorkRevision    int
	EventID         string
	EventRevision   int64
	EventHash       string
	ActionSlot      string
	SegmentID       string
	NextAttemptNo   int
	UseNextFallback bool
	CommandID       string
}

type RetryReceipt struct {
	Status          string `json:"status"`
	TaskID          string `json:"task_id"`
	WorkRevision    int    `json:"work_revision"`
	EventID         string `json:"event_id"`
	ActionSlot      string `json:"action_slot"`
	SegmentID       string `json:"segment_id"`
	NextAttemptNo   int    `json:"next_attempt_no"`
	UseNextFallback bool   `json:"use_next_fallback"`
}

func (d *DB) QueueRetry(ctx context.Context, spec RetrySpec) (RetryReceipt, error) {
	empty := RetryReceipt{}
	if spec.TaskID == "" || spec.WorkRevision < 1 || spec.EventID == "" || spec.EventRevision < 1 || !validDigest(spec.EventHash) || spec.ActionSlot == "" || spec.SegmentID == "" || spec.NextAttemptNo < 2 || spec.CommandID == "" || len(spec.CommandID) > 128 {
		return empty, CodeError("invalid_retry")
	}
	spec.EventHash = strings.ToLower(spec.EventHash)
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	rollback := func(e error) (RetryReceipt, error) { _ = tx.Rollback(); return empty, e }
	readReceipt := func(row *sql.Row) (RetryReceipt, string, int64, string, string, error) {
		var receipt RetryReceipt
		var eventHash, commandID string
		var eventRevision int64
		var useNext int
		err := row.Scan(&receipt.ActionSlot, &receipt.TaskID, &receipt.WorkRevision, &receipt.EventID, &eventRevision, &eventHash, &receipt.SegmentID, &receipt.NextAttemptNo, &useNext, &commandID)
		receipt.Status, receipt.UseNextFallback = "retry_queued", useNext != 0
		return receipt, eventHash, eventRevision, commandID, receipt.ActionSlot, err
	}
	existing, eventHash, eventRevision, _, _, existingErr := readReceipt(tx.QueryRowContext(ctx, `SELECT action_slot,task_id,work_revision,source_event_id,source_event_revision,source_event_hash,source_segment_id,next_attempt_no,use_next_fallback,command_id FROM retry_decisions WHERE action_slot=?`, spec.ActionSlot))
	if existingErr == nil {
		if existing.TaskID != spec.TaskID || existing.WorkRevision != spec.WorkRevision || existing.EventID != spec.EventID || eventRevision != spec.EventRevision || eventHash != spec.EventHash || existing.SegmentID != spec.SegmentID || existing.NextAttemptNo != spec.NextAttemptNo || existing.UseNextFallback != spec.UseNextFallback {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return existing, nil
	}
	if !errors.Is(existingErr, sql.ErrNoRows) {
		return rollback(existingErr)
	}
	var occupied string
	if err = tx.QueryRowContext(ctx, `SELECT action_slot FROM retry_decisions WHERE source_event_id=? OR command_id=? LIMIT 1`, spec.EventID, spec.CommandID).Scan(&occupied); err == nil {
		return rollback(ErrConflict)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	var status, fallbacksJSON, budgetGroup string
	var revision, fallbackIndex, maxAttempts int
	if err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision,t.max_attempts,r.fallback_payloads,r.fallback_index,r.budget_group_id FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, spec.TaskID).Scan(&status, &revision, &maxAttempts, &fallbacksJSON, &fallbackIndex, &budgetGroup); err != nil {
		return rollback(ErrNotFound)
	}
	if status != "failed" || revision != spec.WorkRevision {
		return rollback(ErrConflict)
	}
	var active, attempts int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, spec.TaskID).Scan(&active); err != nil || active != 0 {
		if err == nil {
			err = ErrConflict
		}
		return rollback(err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts a JOIN task_runtime r ON r.task_id=a.task_id WHERE r.budget_group_id=?`, budgetGroup).Scan(&attempts); err != nil {
		return rollback(err)
	}
	if attempts >= maxAttempts || attempts >= 3 {
		return rollback(ErrAttemptLimit)
	}
	if spec.NextAttemptNo != attempts+1 {
		return rollback(ErrConflict)
	}
	var sourceTask, sourceSegment, sourceHash, sourceSlot, sourceBody string
	var sourceRevision int64
	err = tx.QueryRowContext(ctx, `SELECT task_id,segment_id,payload_hash,event_revision,action_slot,body_json FROM runtime_events WHERE event_id=?`, spec.EventID).Scan(&sourceTask, &sourceSegment, &sourceHash, &sourceRevision, &sourceSlot, &sourceBody)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT c.task_id,c.segment_id,e.payload_hash,e.event_revision,e.action_slot,e.body_json FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE e.event_id=?`, spec.EventID).Scan(&sourceTask, &sourceSegment, &sourceHash, &sourceRevision, &sourceSlot, &sourceBody)
	}
	if err != nil {
		return rollback(ErrConflict)
	}
	if sourceTask != spec.TaskID || sourceSegment != spec.SegmentID || sourceHash != spec.EventHash || sourceRevision != spec.EventRevision || sourceSlot != spec.ActionSlot {
		return rollback(ErrConflict)
	}
	var sourceEvent contract.Event
	if json.Unmarshal([]byte(sourceBody), &sourceEvent) != nil || (sourceEvent.Kind != contract.EventFailed && sourceEvent.Kind != contract.EventResult) {
		return rollback(ErrConflict)
	}
	if sourceEvent.Kind == contract.EventResult {
		var decision string
		if err = tx.QueryRowContext(ctx, `SELECT decision FROM review_decisions WHERE event_id=? AND action_slot=?`, spec.EventID, spec.ActionSlot).Scan(&decision); err != nil || decision != "reject" {
			return rollback(ErrConflict)
		}
	}
	var attemptStatus string
	var sourceAttemptNo, latestTaskAttemptNo int
	if err = tx.QueryRowContext(ctx, `SELECT a.status,a.attempt_no,(SELECT MAX(latest.attempt_no) FROM attempts latest WHERE latest.task_id=a.task_id) FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE s.id=? AND a.task_id=?`, spec.SegmentID, spec.TaskID).Scan(&attemptStatus, &sourceAttemptNo, &latestTaskAttemptNo); err != nil || attemptStatus != "failed" || sourceAttemptNo != latestTaskAttemptNo {
		return rollback(ErrConflict)
	}
	adapterUpdate := ""
	if spec.UseNextFallback {
		var fallbacks []json.RawMessage
		if json.Unmarshal([]byte(fallbacksJSON), &fallbacks) != nil || fallbackIndex >= len(fallbacks) {
			return rollback(CodeError("fallback_unavailable"))
		}
		adapterUpdate = string(fallbacks[fallbackIndex])
		fallbackIndex++
	}
	sequence, err := takeReadySequence(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO retry_decisions(action_slot,task_id,work_revision,source_event_id,source_event_revision,source_event_hash,source_segment_id,next_attempt_no,use_next_fallback,command_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, spec.ActionSlot, spec.TaskID, spec.WorkRevision, spec.EventID, spec.EventRevision, spec.EventHash, spec.SegmentID, spec.NextAttemptNo, spec.UseNextFallback, spec.CommandID, now); err != nil {
		return rollback(mapConflict(err))
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='ready' WHERE id=?`, spec.TaskID); err != nil {
		return rollback(err)
	}
	if adapterUpdate == "" {
		_, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=? WHERE task_id=?`, sequence, spec.TaskID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE task_runtime SET ready_sequence=?,adapter_payload=?,fallback_index=? WHERE task_id=?`, sequence, adapterUpdate, fallbackIndex, spec.TaskID)
	}
	if err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return RetryReceipt{Status: "retry_queued", TaskID: spec.TaskID, WorkRevision: spec.WorkRevision, EventID: spec.EventID, ActionSlot: spec.ActionSlot, SegmentID: spec.SegmentID, NextAttemptNo: spec.NextAttemptNo, UseNextFallback: spec.UseNextFallback}, nil
}

type StopDispatch struct {
	HostID  string
	Command contract.StopCommand
}

type ActiveTask struct {
	TaskID       string
	WorkRevision int
}

type ReleasableTask struct {
	TaskID     string
	Executable string
}

func (d *DB) ReleasableTasks(ctx context.Context) ([]ReleasableTask, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT t.id,h.executable FROM tasks t
	 JOIN task_runtime r ON r.task_id=t.id JOIN host_launches h ON h.id=r.host_launch_id
	 WHERE (t.status IN ('completed','cancelled','budget_exhausted','blocked_dependency')
	        OR (t.status='failed' AND (SELECT COUNT(*) FROM attempts a JOIN task_runtime ar ON ar.task_id=a.task_id WHERE ar.budget_group_id=r.budget_group_id) >= MIN(t.max_attempts,3)))
	 AND NOT EXISTS (SELECT 1 FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown'))
	 AND NOT EXISTS (SELECT 1 FROM runtime_events e WHERE e.task_id=t.id AND e.delivery_status='pending')
	 AND NOT EXISTS (SELECT 1 FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=t.id AND e.delivery_status IN ('pending','pending_session'))
	 AND NOT EXISTS (SELECT 1 FROM runtime_questions q WHERE q.task_id=t.id AND q.status IN ('open','resume_queued','resume_started'))
	 ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReleasableTask
	for rows.Next() {
		var item ReleasableTask
		if err = rows.Scan(&item.TaskID, &item.Executable); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (d *DB) ActiveTasks(ctx context.Context) ([]ActiveTask, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT DISTINCT t.id,r.work_revision FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN attempts a ON a.task_id=t.id JOIN segment_runtime s ON s.attempt_id=a.id WHERE s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown') ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []ActiveTask
	for rows.Next() {
		var task ActiveTask
		if err = rows.Scan(&task.TaskID, &task.WorkRevision); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (d *DB) CompletelyIdle(ctx context.Context) (bool, error) {
	var count int
	err := d.sql.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM segment_runtime WHERE status IN ('launch_requested','prepared','spawned','running','stopping','unknown')) +
 (SELECT COUNT(*) FROM tasks WHERE status IN ('queued','ready','resume_queued','running','stopping','unknown','waiting_question','interrupted','result_ready')) +
 (SELECT COUNT(*) FROM runtime_events WHERE delivery_status='pending') +
	 (SELECT COUNT(*) FROM report_events WHERE delivery_status IN ('pending','pending_session')) +
 (SELECT COUNT(*) FROM stop_runtime WHERE status IN ('pending','accepted')) +
 (SELECT COUNT(*) FROM runtime_hosts WHERE status='ready')`).Scan(&count)
	return count == 0, err
}

func (d *DB) RetirableHosts(ctx context.Context) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT h.id FROM runtime_hosts h WHERE h.status='ready'
	 AND NOT EXISTS (SELECT 1 FROM task_runtime r JOIN tasks t ON t.id=r.task_id WHERE r.host_launch_id=h.launch_id AND t.status NOT IN ('completed','result_ready','cancelled','failed','budget_exhausted','blocked_dependency'))
	 AND NOT EXISTS (SELECT 1 FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.producer_id=h.id AND e.delivery_status='pending_session')
 ORDER BY h.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (d *DB) RetireHost(ctx context.Context, hostID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_hosts SET status='released',updated_at=? WHERE id=? AND status='ready'`, time.Now().UTC().Format(time.RFC3339Nano), hostID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host_launches SET status='released' WHERE host_id=?`, hostID); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (d *DB) MarkStoppingUnknown(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	if _, err = tx.ExecContext(ctx, `UPDATE segment_runtime SET status='unknown' WHERE status='stopping'`); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE attempts SET status='unknown' WHERE id IN (SELECT attempt_id FROM segment_runtime WHERE status='unknown')`); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='unknown' WHERE id IN (SELECT a.task_id FROM attempts a JOIN segment_runtime s ON s.attempt_id=a.id WHERE s.status='unknown')`); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE stop_runtime SET status='unknown' WHERE status IN ('pending','accepted')`); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (d *DB) RequestStop(ctx context.Context, taskID string, workRevision int, reason string) (StopDispatch, error) {
	if taskID == "" || workRevision < 1 || reason == "" {
		return StopDispatch{}, CodeError("invalid_stop")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return StopDispatch{}, err
	}
	rollback := func(e error) (StopDispatch, error) { _ = tx.Rollback(); return StopDispatch{}, e }
	var status string
	var revision int
	if err = tx.QueryRowContext(ctx, `SELECT t.status,r.work_revision FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, taskID).Scan(&status, &revision); err != nil {
		return rollback(ErrNotFound)
	}
	if revision != workRevision {
		return rollback(ErrConflict)
	}
	if status == "queued" || status == "ready" || status == "resume_queued" {
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='cancelled' WHERE id=?`, taskID); err != nil {
			return rollback(err)
		}
		if err = blockFailedDependencies(ctx, tx); err != nil {
			return rollback(err)
		}
		return StopDispatch{}, tx.Commit()
	}
	var segmentID, hostID, segmentStatus string
	if err = tx.QueryRowContext(ctx, `SELECT s.segment_id,s.host_id,s.status FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown') ORDER BY s.created_at DESC LIMIT 1`, taskID).Scan(&segmentID, &hostID, &segmentStatus); err != nil {
		return rollback(ErrConflict)
	}
	var command contract.StopCommand
	err = tx.QueryRowContext(ctx, `SELECT command_id,segment_id,reason,deadline_unix_ms FROM stop_runtime WHERE segment_id=?`, segmentID).Scan(&command.CommandID, &command.SegmentID, &command.Reason, &command.DeadlineUnixMS)
	if err == nil {
		_ = tx.Rollback()
		return StopDispatch{HostID: hostID, Command: command}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	commandID, err := newID("stop")
	if err != nil {
		return rollback(err)
	}
	command = contract.StopCommand{CommandID: commandID, SegmentID: segmentID, Reason: reason, DeadlineUnixMS: time.Now().Add(10 * time.Second).UnixMilli()}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO stop_runtime(command_id,host_id,segment_id,reason,deadline_unix_ms,status,created_at) VALUES(?,?,?,?,?,'pending',?)`, command.CommandID, hostID, segmentID, reason, command.DeadlineUnixMS, now); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE segment_runtime SET status='stopping',outcome='stopped' WHERE segment_id=?`, segmentID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='stopping' WHERE id=?`, taskID); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return StopDispatch{}, err
	}
	return StopDispatch{HostID: hostID, Command: command}, nil
}

func (d *DB) PendingStops(ctx context.Context, hostID string) ([]StopDispatch, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT command_id,segment_id,reason,deadline_unix_ms FROM stop_runtime WHERE host_id=? AND status='pending' ORDER BY created_at`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StopDispatch
	for rows.Next() {
		var command contract.StopCommand
		if err = rows.Scan(&command.CommandID, &command.SegmentID, &command.Reason, &command.DeadlineUnixMS); err != nil {
			return nil, err
		}
		out = append(out, StopDispatch{HostID: hostID, Command: command})
	}
	return out, rows.Err()
}

func (d *DB) RegisterReportCapability(ctx context.Context, registration contract.ReportCapabilityRegistration) (contract.DurableAck, error) {
	if registration.CapabilityID == "" || registration.ProducerID == "" || registration.SegmentID == "" || registration.TokenHash == "" || !validDigest(registration.TokenHash) || registration.WorkRevision < 1 || registration.ExecutionEpoch == 0 || !filepath.IsAbs(registration.CapabilityDir) {
		return contract.DurableAck{}, CodeError("invalid_report_capability")
	}
	info, err := os.Lstat(registration.CapabilityDir)
	stat, owned := infoSyscall(info)
	if err != nil || !owned || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || stat.Uid != uint32(os.Geteuid()) {
		return contract.DurableAck{}, CodeError("unsafe_report_capability")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.DurableAck{}, err
	}
	rollback := func(e error) (contract.DurableAck, error) { _ = tx.Rollback(); return contract.DurableAck{}, e }
	var segmentID, producerID, runID, taskID, attemptID, tokenHash, capabilityDir, status string
	var revision int
	var hostEpoch uint64
	if err = tx.QueryRowContext(ctx, `SELECT c.segment_id,c.producer_id,c.run_id,c.task_id,c.attempt_id,c.work_revision,c.token_hash,c.capability_dir,c.status,h.coordinator_epoch FROM report_capabilities c JOIN runtime_hosts h ON h.id=c.producer_id WHERE c.capability_id=? AND h.status IN ('ready','reconciling')`, registration.CapabilityID).Scan(&segmentID, &producerID, &runID, &taskID, &attemptID, &revision, &tokenHash, &capabilityDir, &status, &hostEpoch); err != nil || segmentID != registration.SegmentID || producerID != registration.ProducerID || runID != registration.RunID || taskID != registration.TaskID || attemptID != registration.AttemptID || revision != registration.WorkRevision || hostEpoch != registration.ExecutionEpoch {
		return rollback(ErrConflict)
	}
	if status == "registered" {
		if tokenHash != strings.ToLower(registration.TokenHash) || capabilityDir != registration.CapabilityDir {
			return rollback(ErrConflict)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE report_capabilities SET execution_epoch=? WHERE capability_id=?`, registration.ExecutionEpoch, registration.CapabilityID); err != nil {
			return rollback(err)
		}
		if err = tx.Commit(); err != nil {
			return contract.DurableAck{}, err
		}
		return contract.DurableAck{ProducerID: producerID, EventID: registration.CapabilityID, PayloadHash: tokenHash, Status: "durable"}, nil
	}
	if status != "pending" {
		return rollback(ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE report_capabilities SET token_hash=?,capability_dir=?,execution_epoch=?,status='registered' WHERE capability_id=?`, strings.ToLower(registration.TokenHash), registration.CapabilityDir, registration.ExecutionEpoch, registration.CapabilityID); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return contract.DurableAck{}, err
	}
	return contract.DurableAck{ProducerID: producerID, EventID: registration.CapabilityID, PayloadHash: strings.ToLower(registration.TokenHash), Status: "durable"}, nil
}

func infoSyscall(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

type ReportEventSpec struct {
	CapabilityID string
	Token        string
	EventID      string
	Sequence     int64
	Kind         string
	Payload      json.RawMessage
}

var reportIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (d *DB) CommitReportEvent(ctx context.Context, spec ReportEventSpec) (contract.DurableAck, error) {
	if spec.CapabilityID == "" || spec.Token == "" || !reportIDPattern.MatchString(spec.EventID) || spec.Sequence < 1 || len(spec.Payload) == 0 || len(spec.Payload) > 60*1024 || !json.Valid(spec.Payload) {
		return contract.DurableAck{}, CodeError("invalid_report_event")
	}
	if spec.Kind != "progress" && spec.Kind != "question" && spec.Kind != "result" && spec.Kind != "failure" {
		return contract.DurableAck{}, CodeError("invalid_report_kind")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return contract.DurableAck{}, err
	}
	rollback := func(e error) (contract.DurableAck, error) { _ = tx.Rollback(); return contract.DurableAck{}, e }
	var tokenHash, capabilityDir, runID, taskID, attemptID, segmentID, producerID, status string
	var revision int
	var epoch uint64
	var used, progressUsed int64
	var hostEpoch uint64
	var hostStatus string
	if err = tx.QueryRowContext(ctx, `SELECT c.token_hash,c.capability_dir,c.run_id,c.task_id,c.attempt_id,c.segment_id,c.producer_id,c.work_revision,c.execution_epoch,c.status,c.used_bytes,(SELECT COALESCE(SUM(e.accounted_bytes),0) FROM report_events e WHERE e.capability_id=c.capability_id AND e.kind='progress'),h.coordinator_epoch,h.status FROM report_capabilities c JOIN runtime_hosts h ON h.id=c.producer_id WHERE c.capability_id=?`, spec.CapabilityID).Scan(&tokenHash, &capabilityDir, &runID, &taskID, &attemptID, &segmentID, &producerID, &revision, &epoch, &status, &used, &progressUsed, &hostEpoch, &hostStatus); err != nil || status != "registered" || hostStatus != "ready" || hostEpoch != epoch || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(runtimeHash(spec.Token))) != 1 {
		return rollback(CodeError("report_capability_rejected"))
	}
	canonicalPayload, err := canonicalReportPayload(spec.Payload)
	if err != nil {
		return rollback(CodeError("invalid_report_event"))
	}
	payloadHash := runtimeHash(string(canonicalPayload))
	var oldKind, oldHash, oldBody string
	duplicate := false
	if err = tx.QueryRowContext(ctx, `SELECT kind,payload_hash,body_json FROM report_events WHERE capability_id=? AND sequence=? AND event_id=?`, spec.CapabilityID, spec.Sequence, spec.EventID).Scan(&oldKind, &oldHash, &oldBody); err == nil {
		if oldKind != spec.Kind || oldHash != payloadHash {
			return rollback(ErrConflict)
		}
		duplicate = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	if !duplicate {
		var last int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM report_events WHERE capability_id=?`, spec.CapabilityID).Scan(&last); err != nil || spec.Sequence != last+1 {
			return rollback(ErrEventSequence)
		}
	}
	var event contract.Event
	if duplicate {
		event, err = decodeReportPayload(spec.Kind, canonicalPayload)
	} else {
		event, err = validateReportPayload(spec.Kind, canonicalPayload, capabilityDir)
	}
	if err != nil {
		return rollback(err)
	}
	event.Version, event.ProducerID, event.EventID = 1, spec.CapabilityID, spec.EventID
	event.RunID, event.TaskID, event.AttemptID, event.SegmentID = runID, taskID, attemptID, segmentID
	eventKind := spec.Kind
	if eventKind == "failure" {
		eventKind = contract.EventFailed
	}
	event.WorkRevision, event.ExecutionEpoch, event.Sequence, event.Kind, event.PayloadHash = revision, epoch, spec.Sequence, eventKind, payloadHash
	if spec.Kind == "question" {
		sessionErr := tx.QueryRowContext(ctx, `SELECT session_kind,session_id FROM segment_sessions WHERE segment_id=?`, segmentID).Scan(&event.SessionKind, &event.SessionID)
		if sessionErr != nil && !errors.Is(sessionErr, sql.ErrNoRows) {
			return rollback(sessionErr)
		}
	}
	body, _ := json.Marshal(event)
	if duplicate {
		oldCanonical, oldErr := canonicalReportEvent([]byte(oldBody))
		newCanonical, newErr := canonicalReportEvent(body)
		if oldErr != nil || newErr != nil || subtle.ConstantTimeCompare(oldCanonical, newCanonical) != 1 {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return contract.DurableAck{ProducerID: spec.CapabilityID, AckedThrough: spec.Sequence, EventID: spec.EventID, PayloadHash: payloadHash, Status: "durable"}, nil
	}
	diagnostic := spec.Kind == "progress"
	accountedBytes := int64(len(body))
	if spec.Kind == "question" && event.SessionID == "" {
		accountedBytes += 4096
	}
	if err = d.storageAdmission(ctx, tx, runID, accountedBytes, diagnostic); err != nil {
		return rollback(err)
	}
	delivery := "pending"
	if diagnostic {
		delivery = "internal"
	} else if spec.Kind == "question" && event.SessionID == "" {
		delivery = "pending_session"
	}
	actionSlot := "action-" + runtimeHash(spec.CapabilityID + "\x00" + spec.EventID)[:32]
	if accountedBytes > d.reportControlLimit || used > d.reportControlLimit-accountedBytes || (diagnostic && (accountedBytes > d.reportProgressLimit || progressUsed > d.reportProgressLimit-accountedBytes)) {
		return rollback(CodeError("report_budget_exhausted"))
	}
	if spec.Kind == "question" {
		var openQuestions int
		if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runtime_questions WHERE task_id=? AND status IN ('open','resume_queued','resume_started')) + (SELECT COUNT(*) FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=? AND e.kind='question' AND e.delivery_status='pending_session')`, taskID, taskID).Scan(&openQuestions); err != nil || openQuestions != 0 {
			if err == nil {
				err = CodeError("question_already_open")
			}
			return rollback(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO report_events(event_id,capability_id,sequence,kind,payload_hash,body_json,event_revision,action_slot,delivery_status,accounted_bytes,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, spec.EventID, spec.CapabilityID, spec.Sequence, spec.Kind, payloadHash, string(body), spec.Sequence, actionSlot, delivery, accountedBytes, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return rollback(mapConflict(err))
	}
	if delivery != "pending_session" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_order(event_id,task_id) VALUES(?,?)`, spec.EventID, taskID); err != nil {
			return rollback(mapConflict(err))
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE report_capabilities SET used_bytes=used_bytes+? WHERE capability_id=?`, accountedBytes, spec.CapabilityID); err != nil {
		return rollback(err)
	}
	if spec.Kind == "question" && event.SessionID != "" {
		if err = applyEvent(ctx, tx, event); err != nil {
			return rollback(err)
		}
		if err = ensureQuestionStop(ctx, tx, event); err != nil {
			return rollback(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return contract.DurableAck{}, err
	}
	return contract.DurableAck{ProducerID: spec.CapabilityID, AckedThrough: spec.Sequence, EventID: spec.EventID, PayloadHash: payloadHash, Status: "durable"}, nil
}

func canonicalReportEvent(body []byte) ([]byte, error) {
	var event contract.Event
	if err := json.Unmarshal(body, &event); err != nil {
		return nil, err
	}
	event.ExecutionEpoch = 0
	return json.Marshal(event)
}

func canonicalReportPayload(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func validateReportPayload(kind string, raw json.RawMessage, capabilityDir string) (contract.Event, error) {
	event, err := decodeReportPayload(kind, raw)
	if err != nil || kind == "progress" {
		return event, err
	}
	if err = validateReportArtifactPath(event, capabilityDir); err != nil {
		return contract.Event{}, err
	}
	value := event.Artifact
	info, err := os.Lstat(value.Path)
	stat, owned := infoSyscall(info)
	if err != nil || !owned || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || info.Size() != value.Size {
		return contract.Event{}, CodeError("unsafe_report_artifact")
	}
	data, err := os.ReadFile(value.Path)
	if err != nil || runtimeHash(string(data)) != strings.ToLower(value.SHA256) {
		return contract.Event{}, CodeError("report_artifact_hash_mismatch")
	}
	return event, nil
}

func decodeReportPayload(kind string, raw json.RawMessage) (contract.Event, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if kind == "progress" {
		var value struct {
			Status    string `json:"status"`
			Sequence  int64  `json:"sequence"`
			ElapsedMS int64  `json:"elapsed_ms"`
		}
		if decoder.Decode(&value) != nil || (value.Status != "resource_sample" && value.Status != "checkpoint") || value.Sequence < 0 || value.ElapsedMS < 0 {
			return contract.Event{}, CodeError("invalid_report_payload")
		}
		return contract.Event{}, nil
	}
	var value struct {
		Status           string               `json:"status"`
		QuestionID       string               `json:"question_id,omitempty"`
		QuestionRevision int                  `json:"question_revision,omitempty"`
		QuestionKind     string               `json:"question_kind,omitempty"`
		Artifact         contract.ArtifactRef `json:"artifact"`
	}
	if decoder.Decode(&value) != nil {
		return contract.Event{}, CodeError("invalid_report_payload")
	}
	allowed := (kind == "result" && (value.Status == "completed" || value.Status == "partial")) || (kind == "failure" && (value.Status == "failed" || value.Status == "blocked")) || (kind == "question" && value.Status == "waiting_user" && value.QuestionID != "" && value.QuestionRevision > 0 && value.QuestionKind == "technical")
	if !allowed || value.Artifact.Path == "" || !filepath.IsAbs(value.Artifact.Path) || !validDigest(value.Artifact.SHA256) || value.Artifact.Size < 0 {
		return contract.Event{}, CodeError("invalid_report_payload")
	}
	return contract.Event{Artifact: &value.Artifact, QuestionID: value.QuestionID, QuestionRevision: value.QuestionRevision, QuestionKind: value.QuestionKind}, nil
}

func validateReportArtifactPath(event contract.Event, capabilityDir string) error {
	if event.Artifact == nil {
		return CodeError("invalid_report_payload")
	}
	rel, err := filepath.Rel(capabilityDir, event.Artifact.Path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return CodeError("report_artifact_outside_capability")
	}
	return nil
}

type PendingPage struct {
	Events     []contract.Event
	NextCursor string
}

const (
	collectPageEvents = 32
	collectPageBytes  = 48 * 1024
)

func deliveryCursor(taskID string, ordinal int64) string {
	value := strconv.FormatInt(ordinal, 10)
	return "delivery-v1-" + value + "-" + runtimeHash(taskID + "\x00" + value)[:24]
}

func parseDeliveryCursor(taskID, cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	const prefix = "delivery-v1-"
	if !strings.HasPrefix(cursor, prefix) {
		return 0, CodeError("invalid_cursor")
	}
	remainder := strings.TrimPrefix(cursor, prefix)
	separator := strings.LastIndexByte(remainder, '-')
	if separator < 1 {
		return 0, CodeError("invalid_cursor")
	}
	value, proof := remainder[:separator], remainder[separator+1:]
	ordinal, err := strconv.ParseInt(value, 10, 64)
	want := runtimeHash(taskID + "\x00" + value)[:24]
	if err != nil || ordinal < 1 || subtle.ConstantTimeCompare([]byte(proof), []byte(want)) != 1 {
		return 0, CodeError("invalid_cursor")
	}
	return ordinal, nil
}

func (d *DB) CollectPending(ctx context.Context, taskID, cursor string, limit int, includeDiagnostics bool) (PendingPage, error) {
	if limit < 1 || limit > collectPageEvents {
		limit = collectPageEvents
	}
	ordinal, err := parseDeliveryCursor(taskID, cursor)
	if err != nil {
		return PendingPage{}, err
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT o.ordinal,e.body_json,e.event_revision,e.action_slot FROM delivery_order o JOIN (
	 SELECT event_id,body_json,event_revision,action_slot FROM runtime_events WHERE task_id=? AND delivery_status='pending'
	 UNION ALL
	 SELECT e.event_id,e.body_json,e.event_revision,e.action_slot FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=? AND (e.delivery_status='pending' OR (? AND e.kind='progress'))
	) e ON e.event_id=o.event_id WHERE o.task_id=? AND o.ordinal>? ORDER BY o.ordinal LIMIT ?`, taskID, taskID, includeDiagnostics, taskID, ordinal, limit+1)
	if err != nil {
		return PendingPage{}, err
	}
	defer rows.Close()
	page := PendingPage{Events: make([]contract.Event, 0, collectPageEvents)}
	pageBytes := 0
	var lastOrdinal int64
	for rows.Next() {
		var body, actionSlot string
		var eventOrdinal, revision int64
		if err = rows.Scan(&eventOrdinal, &body, &revision, &actionSlot); err != nil {
			return PendingPage{}, err
		}
		var event contract.Event
		if err = json.Unmarshal([]byte(body), &event); err != nil {
			return PendingPage{}, err
		}
		event.EventRevision, event.ActionSlot = revision, actionSlot
		encoded, err := json.Marshal(event)
		if err != nil {
			return PendingPage{}, err
		}
		if len(page.Events) >= limit || (len(page.Events) > 0 && pageBytes+len(encoded)+1 > collectPageBytes) {
			page.NextCursor = deliveryCursor(taskID, lastOrdinal)
			break
		}
		page.Events = append(page.Events, event)
		pageBytes += len(encoded) + 1
		lastOrdinal = eventOrdinal
	}
	if err = rows.Err(); err != nil {
		return PendingPage{}, err
	}
	return page, nil
}

type AckDecision struct {
	EventID       string `json:"event_id"`
	EventRevision int64  `json:"event_revision"`
	EventHash     string `json:"event_hash"`
	ActionSlot    string `json:"action_slot"`
	Decision      string `json:"decision"`
	CommandID     string `json:"command_id"`
}

func validAckDecision(decision AckDecision) bool {
	return decision.EventID != "" && decision.EventRevision > 0 && decision.EventHash != "" && decision.ActionSlot != "" && decision.CommandID != "" && (decision.Decision == "handled" || decision.Decision == "waiting_user" || decision.Decision == "stale" || decision.Decision == "rejected")
}

func (d *DB) AckDecisions(ctx context.Context, taskID, deliveryID, historyProofSHA256 string, decisions []AckDecision) error {
	if taskID == "" || deliveryID == "" || !validDigest(historyProofSHA256) || len(decisions) == 0 || len(decisions) > 64 {
		return CodeError("invalid_ack")
	}
	seen := make(map[string]bool, len(decisions))
	for _, decision := range decisions {
		if !validAckDecision(decision) || seen[decision.ActionSlot] {
			return CodeError("invalid_ack")
		}
		seen[decision.ActionSlot] = true
	}
	encoded, _ := json.Marshal(decisions)
	decisionsHash := runtimeHash(string(encoded))
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	var storedTask, storedProof, storedDecisions string
	err = tx.QueryRowContext(ctx, `SELECT task_id,history_proof_sha256,decisions_hash FROM delivery_receipts WHERE delivery_id=?`, deliveryID).Scan(&storedTask, &storedProof, &storedDecisions)
	if err == nil {
		if storedTask != taskID || storedProof != strings.ToLower(historyProofSHA256) || storedDecisions != decisionsHash {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return rollback(err)
	}
	for _, decision := range decisions {
		if err = applyAckDecision(ctx, tx, taskID, decision); err != nil {
			return rollback(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_receipts(delivery_id,task_id,history_proof_sha256,decisions_hash,created_at) VALUES(?,?,?,?,?)`, deliveryID, taskID, strings.ToLower(historyProofSHA256), decisionsHash, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return rollback(mapConflict(err))
	}
	return tx.Commit()
}

func applyAckDecision(ctx context.Context, tx *sql.Tx, taskID string, decision AckDecision) error {
	var err error
	var storedTask, storedHash, storedSlot, body, delivery string
	var storedRevision int64
	eventTable := "runtime_events"
	if err = tx.QueryRowContext(ctx, `SELECT task_id,payload_hash,event_revision,action_slot,body_json,delivery_status FROM runtime_events WHERE event_id=?`, decision.EventID).Scan(&storedTask, &storedHash, &storedRevision, &storedSlot, &body, &delivery); err != nil || storedTask != taskID || storedRevision != decision.EventRevision || storedSlot != decision.ActionSlot || subtle.ConstantTimeCompare([]byte(storedHash), []byte(strings.ToLower(decision.EventHash))) != 1 {
		if !errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		eventTable = "report_events"
		err = tx.QueryRowContext(ctx, `SELECT c.task_id,e.payload_hash,e.event_revision,e.action_slot,e.body_json,e.delivery_status FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE e.event_id=?`, decision.EventID).Scan(&storedTask, &storedHash, &storedRevision, &storedSlot, &body, &delivery)
		if err != nil || storedTask != taskID || storedRevision != decision.EventRevision || storedSlot != decision.ActionSlot || subtle.ConstantTimeCompare([]byte(storedHash), []byte(strings.ToLower(decision.EventHash))) != 1 {
			return ErrConflict
		}
	}
	var existingDecision, existingHash string
	var existingRevision int64
	err = tx.QueryRowContext(ctx, `SELECT decision,event_hash,event_revision FROM runtime_decisions WHERE action_slot=?`, decision.ActionSlot).Scan(&existingDecision, &existingHash, &existingRevision)
	if err == nil {
		if existingDecision != decision.Decision || existingHash != storedHash || existingRevision != storedRevision {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if delivery != "pending" {
		return ErrConflict
	}
	if decision.Decision == "handled" || decision.Decision == "waiting_user" {
		var event contract.Event
		if json.Unmarshal([]byte(body), &event) != nil {
			return ErrConflict
		}
		var workRevision int
		var currentSegment sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT r.work_revision,(SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1) FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, taskID).Scan(&workRevision, &currentSegment); err != nil || event.WorkRevision != workRevision || !currentSegment.Valid {
			return ErrConflict
		}
		if currentSegment.String != event.SegmentID && (decision.Decision != "handled" || !hasBoundBusinessEffect(ctx, tx, taskID, event, decision)) {
			return ErrConflict
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_decisions(action_slot,event_id,event_revision,event_hash,decision,command_id,created_at) VALUES(?,?,?,?,?,?,?)`, decision.ActionSlot, decision.EventID, decision.EventRevision, strings.ToLower(decision.EventHash), decision.Decision, decision.CommandID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return mapConflict(err)
	}
	statement := `UPDATE runtime_events SET delivery_status='acked' WHERE event_id=? AND delivery_status='pending'`
	if eventTable == "report_events" {
		statement = `UPDATE report_events SET delivery_status='acked' WHERE event_id=? AND delivery_status='pending'`
	}
	if _, err = tx.ExecContext(ctx, statement, decision.EventID); err != nil {
		return err
	}
	return nil
}

func hasBoundBusinessEffect(ctx context.Context, tx *sql.Tx, taskID string, event contract.Event, decision AckDecision) bool {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM retry_decisions WHERE action_slot=? AND task_id=? AND source_event_id=? AND source_event_revision=? AND source_event_hash=? AND source_segment_id=?`, decision.ActionSlot, taskID, decision.EventID, decision.EventRevision, strings.ToLower(decision.EventHash), event.SegmentID).Scan(&found)
	if err == nil {
		return true
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM review_decisions WHERE action_slot=? AND task_id=? AND event_id=? AND event_revision=? AND event_hash=?`, decision.ActionSlot, taskID, decision.EventID, decision.EventRevision, strings.ToLower(decision.EventHash)).Scan(&found)
	if err == nil {
		return true
	}
	if event.Kind != contract.EventQuestion || event.QuestionID == "" || event.QuestionRevision < 1 {
		return false
	}
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM runtime_questions WHERE question_id=? AND task_id=? AND segment_id=? AND question_revision=? AND work_revision=? AND answer_hash!='' AND status IN ('resume_queued','resume_started')`, event.QuestionID, taskID, event.SegmentID, event.QuestionRevision, event.WorkRevision).Scan(&found)
	return err == nil
}

func (d *DB) AckDecision(ctx context.Context, taskID string, decision AckDecision) error {
	return d.AckDecisions(ctx, taskID, "delivery-"+decision.CommandID, runtimeHash("proof:"+decision.CommandID), []AckDecision{decision})
}

func (d *DB) ValidateTaskOwner(ctx context.Context, taskID, controller, controlToken string) error {
	var stored, tokenHash string
	err := d.sql.QueryRowContext(ctx, `SELECT r.controller_thread,c.token_hash FROM runs r JOIN tasks t ON t.run_id=r.id JOIN run_control c ON c.run_id=r.id WHERE t.id=?`, taskID).Scan(&stored, &tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	want := runtimeHash(controlToken)
	if controller == "" || controlToken == "" || stored != controller || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(want)) != 1 {
		return CodeError("owner_mismatch")
	}
	return nil
}

type RunOwnerBinding struct {
	OriginContextID string
	OriginPID       int
	OriginBirth     string
}

func (d *DB) ValidateRunOwner(ctx context.Context, runID, controller, controlToken string) (RunOwnerBinding, error) {
	var binding RunOwnerBinding
	var storedController, tokenHash string
	err := d.sql.QueryRowContext(ctx, `SELECT r.controller_thread,r.origin_context_id,r.origin_pid,r.origin_birth,c.token_hash FROM runs r JOIN run_control c ON c.run_id=r.id WHERE r.id=?`, runID).Scan(&storedController, &binding.OriginContextID, &binding.OriginPID, &binding.OriginBirth, &tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return binding, ErrNotFound
	}
	if err != nil {
		return binding, err
	}
	if controller == "" || controlToken == "" || storedController != controller || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(runtimeHash(controlToken))) != 1 {
		return binding, CodeError("owner_mismatch")
	}
	return binding, nil
}

type RebindOwnerSpec struct {
	RunID                 string
	OriginContextID       string
	OriginPID             int
	OriginBirth           string
	HostGeneration        string
	AttachmentProofSHA256 string
}

func (d *DB) RebindOwner(ctx context.Context, spec RebindOwnerSpec) error {
	if spec.RunID == "" || spec.OriginContextID == "" || spec.OriginPID <= 0 || spec.OriginBirth == "" || spec.HostGeneration == "" || !validDigest(spec.AttachmentProofSHA256) {
		return CodeError("invalid_owner_rebind")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	var origin, oldGeneration string
	if err = tx.QueryRowContext(ctx, `SELECT r.origin_context_id,h.host_generation FROM runs r JOIN tasks t ON t.run_id=r.id JOIN task_runtime tr ON tr.task_id=t.id JOIN host_launches h ON h.id=tr.host_launch_id WHERE r.id=? LIMIT 1`, spec.RunID).Scan(&origin, &oldGeneration); err != nil || origin != spec.OriginContextID {
		return rollback(ErrConflict)
	}
	var proof string
	err = tx.QueryRowContext(ctx, `SELECT attachment_proof_sha256 FROM owner_rebinds WHERE run_id=? AND host_generation=?`, spec.RunID, spec.HostGeneration).Scan(&proof)
	if err == nil {
		if proof != strings.ToLower(spec.AttachmentProofSHA256) {
			return rollback(ErrConflict)
		}
		_ = tx.Rollback()
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) || oldGeneration == spec.HostGeneration {
		return rollback(ErrConflict)
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, spec.RunID).Scan(&active); err != nil || active != 0 {
		return rollback(ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO owner_rebinds(run_id,host_generation,origin_pid,origin_birth,attachment_proof_sha256,created_at) VALUES(?,?,?,?,?,?)`, spec.RunID, spec.HostGeneration, spec.OriginPID, spec.OriginBirth, strings.ToLower(spec.AttachmentProofSHA256), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return rollback(mapConflict(err))
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET origin_pid=?,origin_birth=? WHERE id=?`, spec.OriginPID, spec.OriginBirth, spec.RunID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE host_launches SET host_generation=?,status='offline' WHERE id IN (SELECT host_launch_id FROM task_runtime r JOIN tasks t ON t.id=r.task_id WHERE t.run_id=?)`, spec.HostGeneration, spec.RunID); err != nil {
		return rollback(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_hosts SET host_generation=?,status='offline' WHERE launch_id IN (SELECT host_launch_id FROM task_runtime r JOIN tasks t ON t.id=r.task_id WHERE t.run_id=?)`, spec.HostGeneration, spec.RunID); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

type TaskSnapshot struct {
	TaskID           string                    `json:"task_id"`
	RunID            string                    `json:"run_id"`
	Status           string                    `json:"status"`
	WorkRevision     int                       `json:"work_revision"`
	AttemptID        string                    `json:"attempt_id,omitempty"`
	SegmentID        string                    `json:"segment_id,omitempty"`
	SegmentStatus    string                    `json:"segment_status,omitempty"`
	QuestionID       string                    `json:"question_id,omitempty"`
	QuestionRevision int                       `json:"question_revision,omitempty"`
	HostPID          int                       `json:"host_pid,omitempty"`
	HostBirth        string                    `json:"host_birth,omitempty"`
	HostStatus       string                    `json:"host_status,omitempty"`
	Process          *contract.ProcessIdentity `json:"process,omitempty"`
}

func (d *DB) TaskSnapshot(ctx context.Context, taskID string) (TaskSnapshot, error) {
	var snapshot TaskSnapshot
	var attempt, segment, segmentStatus sql.NullString
	err := d.sql.QueryRowContext(ctx, `SELECT t.id,t.run_id,t.status,r.work_revision,(SELECT id FROM attempts WHERE task_id=t.id ORDER BY attempt_no DESC LIMIT 1),(SELECT s.id FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1),(SELECT s.status FROM segments s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=t.id ORDER BY a.attempt_no DESC,s.segment_no DESC LIMIT 1) FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.id=?`, taskID).Scan(&snapshot.TaskID, &snapshot.RunID, &snapshot.Status, &snapshot.WorkRevision, &attempt, &segment, &segmentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, ErrNotFound
	}
	if err != nil {
		return snapshot, err
	}
	snapshot.AttemptID, snapshot.SegmentID, snapshot.SegmentStatus = attempt.String, segment.String, segmentStatus.String
	_ = d.sql.QueryRowContext(ctx, `SELECT h.pid,h.birth,h.status FROM task_runtime r JOIN host_launches l ON l.id=r.host_launch_id LEFT JOIN runtime_hosts h ON h.launch_id=l.id WHERE r.task_id=?`, taskID).Scan(&snapshot.HostPID, &snapshot.HostBirth, &snapshot.HostStatus)
	if snapshot.SegmentID != "" {
		rows, queryErr := d.sql.QueryContext(ctx, `SELECT body_json FROM runtime_events WHERE segment_id=? ORDER BY sequence DESC`, snapshot.SegmentID)
		if queryErr == nil {
			for rows.Next() {
				var body string
				var event contract.Event
				if rows.Scan(&body) == nil && json.Unmarshal([]byte(body), &event) == nil && event.Process != nil {
					snapshot.Process = event.Process
					break
				}
			}
			_ = rows.Close()
		}
	}
	if snapshot.Status == "waiting_question" || snapshot.Status == "resume_queued" || snapshot.Status == "running" {
		_ = d.sql.QueryRowContext(ctx, `SELECT question_id,question_revision FROM runtime_questions WHERE task_id=? AND status IN ('open','resume_queued','resume_started') ORDER BY question_revision DESC LIMIT 1`, taskID).Scan(&snapshot.QuestionID, &snapshot.QuestionRevision)
	}
	return snapshot, nil
}

func runtimeHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
