package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
)

const (
	defaultSegmentActiveMS = int64(60 * 60 * 1000)
	defaultGroupActiveMS   = int64(120 * 60 * 1000)
)

const (
	ErrNoSlot        CodeError = "no_execution_slot"
	ErrNoReady       CodeError = "no_ready_task"
	ErrInvalidDAG    CodeError = "invalid_dag"
	ErrHostRejected  CodeError = "host_rejected"
	ErrEventSequence CodeError = "event_sequence_conflict"
)

type HostLaunchSpec struct {
	OriginContextID string
	HostGeneration  string
	Executable      string
}

type PlanSpec struct {
	Run   RunSpec
	Host  HostLaunchSpec
	Tasks []TaskSpec
}

type SubmitReceipt struct {
	LaunchID    string `json:"launch_id"`
	LaunchToken string `json:"launch_token"`
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
CREATE TABLE IF NOT EXISTS task_runtime(
  task_id TEXT PRIMARY KEY REFERENCES tasks(id), host_launch_id TEXT NOT NULL REFERENCES host_launches(id),
  work_revision INTEGER NOT NULL, budget_group_id TEXT NOT NULL,
  max_active_ms INTEGER NOT NULL, ready_sequence INTEGER
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
  payload_hash TEXT NOT NULL, body_json TEXT NOT NULL, delivery_status TEXT NOT NULL,
  created_at TEXT NOT NULL, UNIQUE(producer_id,sequence)
);`)
	return err
}

func validatePlan(p PlanSpec) error {
	if p.Run.ID == "" || p.Run.ControllerThread == "" || p.Run.PlanRevision < 1 || p.Run.OriginContextID == "" || p.Run.OriginPID <= 0 || p.Run.OriginBirth == "" {
		return CodeError("invalid_run")
	}
	if p.Host.OriginContextID != p.Run.OriginContextID || p.Host.HostGeneration == "" || p.Host.Executable == "" || len(p.Tasks) == 0 || len(p.Tasks) > 64 {
		return ErrInvalidDAG
	}
	byID := make(map[string]TaskSpec, len(p.Tasks))
	for _, task := range p.Tasks {
		if task.ID == "" || task.RunID != p.Run.ID || task.MaxAttempts < 1 || task.MaxAttempts > 3 {
			return ErrInvalidDAG
		}
		if _, exists := byID[task.ID]; exists {
			return ErrInvalidDAG
		}
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
	launchID, err := newID("host-launch")
	if err != nil {
		return SubmitReceipt{}, err
	}
	token, err := newID("host-token")
	if err != nil {
		return SubmitReceipt{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return SubmitReceipt{}, err
	}
	rollback := func(e error) (SubmitReceipt, error) { _ = tx.Rollback(); return SubmitReceipt{}, e }
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at) VALUES(?,?,?,?,?,?,?,?)`, p.Run.ID, p.Run.ControllerThread, p.Run.PlanRevision, p.Run.OriginContextID, p.Run.OriginPID, p.Run.OriginBirth, "queued", now); err != nil {
		return rollback(mapConflict(err))
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO host_launches(id,token_hash,origin_context_id,host_generation,executable,status,created_at) VALUES(?,?,?,?,?,'reserved',?)`, launchID, runtimeHash(token), p.Host.OriginContextID, p.Host.HostGeneration, p.Host.Executable, now); err != nil {
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_runtime(task_id,host_launch_id,work_revision,budget_group_id,max_active_ms,ready_sequence) VALUES(?,?,?,?,?,?)`, task.ID, launchID, revision, group, active, ready); err != nil {
			return rollback(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return SubmitReceipt{}, err
	}
	return SubmitReceipt{LaunchID: launchID, LaunchToken: token}, nil
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
	if hello.LaunchID == "" || hello.LaunchToken == "" || hello.OriginContextID == "" || hello.HostGeneration == "" || hello.PID <= 0 || hello.Birth == "" || hello.Executable == "" || epoch == 0 {
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
	hostID := existing.String
	if !existing.Valid || hostID == "" {
		hostID, err = newID("host")
		if err != nil {
			return rollback(err)
		}
	} else {
		var pid int
		var birth string
		if err = tx.QueryRowContext(ctx, `SELECT pid,birth FROM runtime_hosts WHERE id=?`, hostID).Scan(&pid, &birth); err == nil && (pid != hello.PID || birth != hello.Birth) {
			return rollback(ErrHostRejected)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_hosts(id,launch_id,origin_context_id,host_generation,pid,birth,executable,coordinator_epoch,status,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET coordinator_epoch=excluded.coordinator_epoch,status='ready',updated_at=excluded.updated_at`, hostID, hello.LaunchID, hello.OriginContextID, hello.HostGeneration, hello.PID, hello.Birth, hello.Executable, epoch, "ready", now); err != nil {
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
	err = tx.QueryRowContext(ctx, `SELECT t.id,t.run_id,t.status,r.work_revision,r.budget_group_id,r.max_active_ms,t.max_attempts FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE r.host_launch_id=? AND t.status IN ('ready','resume_queued') ORDER BY r.ready_sequence LIMIT 1`, launchID).Scan(&taskID, &runID, &taskStatus, &workRevision, &budgetGroup, &maxActive, &maxAttempts)
	if errors.Is(err, sql.ErrNoRows) {
		return rollback(ErrNoReady)
	}
	if err != nil {
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
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='running' WHERE id=?`, taskID); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return contract.LaunchCommand{}, err
	}
	return contract.LaunchCommand{CommandID: commandID, HostID: hostID, RunID: runID, TaskID: taskID, AttemptID: attemptID, SegmentID: segmentID, WorkRevision: workRevision, ExecutionEpoch: epoch, SlotToken: slotToken, LaunchIntentHash: intentHash, ReservationID: reservationID, BudgetGroupID: budgetGroup, GrantedActiveMS: grant, DeadlineUnixMS: deadline}, nil
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
	rows, err := tx.QueryContext(ctx, `SELECT s.command_id,s.host_id,t.run_id,t.id,s.attempt_id,s.segment_id,r.work_revision,s.execution_epoch,s.slot_token,s.launch_intent_hash,s.reservation_id,r.budget_group_id,b.granted_ms,s.deadline_unix_ms FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id JOIN task_runtime r ON r.task_id=t.id JOIN budget_runtime b ON b.id=s.reservation_id WHERE s.host_id=? AND s.status='launch_requested' ORDER BY s.created_at,s.segment_id`, hostID)
	if err != nil {
		return rollback(err)
	}
	var commands []contract.LaunchCommand
	for rows.Next() {
		var command contract.LaunchCommand
		if err = rows.Scan(&command.CommandID, &command.HostID, &command.RunID, &command.TaskID, &command.AttemptID, &command.SegmentID, &command.WorkRevision, &command.ExecutionEpoch, &command.SlotToken, &command.LaunchIntentHash, &command.ReservationID, &command.BudgetGroupID, &command.GrantedActiveMS, &command.DeadlineUnixMS); err != nil {
			_ = rows.Close()
			return rollback(err)
		}
		commands = append(commands, command)
	}
	if err = rows.Close(); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return commands, nil
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
	if err = tx.QueryRowContext(ctx, `SELECT coordinator_epoch FROM runtime_hosts WHERE id=? AND status='ready'`, event.ProducerID).Scan(&registeredEpoch); err != nil || registeredEpoch != event.ExecutionEpoch {
		return rollback(ErrHostRejected)
	}
	var oldID, oldHash string
	err = tx.QueryRowContext(ctx, `SELECT event_id,payload_hash FROM runtime_events WHERE producer_id=? AND sequence=?`, event.ProducerID, event.Sequence).Scan(&oldID, &oldHash)
	if err == nil {
		if oldID != event.EventID || oldHash != event.PayloadHash {
			return rollback(ErrEventSequence)
		}
		_ = tx.Rollback()
		return contract.DurableAck{ProducerID: event.ProducerID, AckedThrough: event.Sequence, EventID: event.EventID, PayloadHash: event.PayloadHash, Status: "committed"}, nil
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
	var storedHost, taskID, attemptID string
	if err = tx.QueryRowContext(ctx, `SELECT host_id,(SELECT task_id FROM attempts WHERE id=segment_runtime.attempt_id),attempt_id FROM segment_runtime WHERE segment_id=? AND command_id=?`, event.SegmentID, event.CommandID).Scan(&storedHost, &taskID, &attemptID); err != nil || storedHost != event.ProducerID || taskID != event.TaskID || attemptID != event.AttemptID {
		return rollback(ErrConflict)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_events(event_id,producer_id,sequence,task_id,attempt_id,segment_id,payload_hash,body_json,delivery_status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, event.EventID, event.ProducerID, event.Sequence, event.TaskID, event.AttemptID, event.SegmentID, event.PayloadHash, string(body), "pending", now); err != nil {
		return rollback(mapConflict(err))
	}
	if err = applyEvent(ctx, tx, event); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return contract.DurableAck{}, err
	}
	return contract.DurableAck{ProducerID: event.ProducerID, AckedThrough: event.Sequence, EventID: event.EventID, PayloadHash: event.PayloadHash, Status: "committed"}, nil
}

func applyEvent(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	switch event.Kind {
	case contract.EventPrepared, contract.EventSpawned, contract.EventRunning:
		if _, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET status=? WHERE segment_id=?`, event.Kind, event.SegmentID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE segments SET status=? WHERE id=?`, event.Kind, event.SegmentID)
		return err
	case contract.EventQuestion, contract.EventResult, contract.EventStopped:
		_, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET outcome=? WHERE segment_id=?`, event.Kind, event.SegmentID)
		return err
	case contract.EventUnknown:
		if _, err := tx.ExecContext(ctx, `UPDATE segment_runtime SET status='unknown' WHERE segment_id=?`, event.SegmentID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE attempts SET status='unknown' WHERE id=?`, event.AttemptID)
		return err
	case contract.EventExited:
		return applyExit(ctx, tx, event)
	default:
		return CodeError("invalid_event_kind")
	}
}

func applyExit(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	var outcome, reservation string
	var grant int64
	if err := tx.QueryRowContext(ctx, `SELECT outcome,reservation_id,(SELECT granted_ms FROM budget_runtime WHERE id=segment_runtime.reservation_id) FROM segment_runtime WHERE segment_id=?`, event.SegmentID).Scan(&outcome, &reservation, &grant); err != nil {
		return err
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
	attemptStatus, taskStatus := "interrupted", "interrupted"
	switch outcome {
	case contract.EventResult:
		attemptStatus, taskStatus = "result_ready", "completed"
	case contract.EventQuestion:
		attemptStatus, taskStatus = "interrupted", "waiting_question"
	case contract.EventStopped:
		attemptStatus, taskStatus = "interrupted", "interrupted"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE attempts SET status=? WHERE id=?`, attemptStatus, event.AttemptID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status=? WHERE id=?`, taskStatus, event.TaskID); err != nil {
		return err
	}
	if taskStatus == "completed" {
		return releaseReady(ctx, tx)
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
	if revision != workRevision || (status != "interrupted" && status != "waiting_question") {
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

func (d *DB) CollectPending(ctx context.Context, taskID string) ([]contract.Event, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT body_json FROM runtime_events WHERE task_id=? AND delivery_status='pending' ORDER BY created_at,producer_id,sequence`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []contract.Event
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		var event contract.Event
		if err = json.Unmarshal([]byte(body), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (d *DB) AckEvents(ctx context.Context, taskID string, eventIDs []string) error {
	if taskID == "" || len(eventIDs) == 0 {
		return CodeError("invalid_ack")
	}
	ids := append([]string(nil), eventIDs...)
	sort.Strings(ids)
	for i, id := range ids {
		if id == "" || (i > 0 && ids[i-1] == id) {
			return CodeError("invalid_ack")
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	for _, id := range ids {
		var storedTask string
		if err = tx.QueryRowContext(ctx, `SELECT task_id FROM runtime_events WHERE event_id=?`, id).Scan(&storedTask); err != nil || storedTask != taskID {
			return rollback(ErrConflict)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runtime_events SET delivery_status='acked' WHERE event_id=?`, id); err != nil {
			return rollback(err)
		}
	}
	return tx.Commit()
}

func (d *DB) ValidateTaskOwner(ctx context.Context, taskID, controller string) error {
	var stored string
	err := d.sql.QueryRowContext(ctx, `SELECT r.controller_thread FROM runs r JOIN tasks t ON t.run_id=r.id WHERE t.id=?`, taskID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if controller == "" || stored != controller {
		return CodeError("owner_mismatch")
	}
	return nil
}

type TaskSnapshot struct {
	TaskID        string `json:"task_id"`
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	WorkRevision  int    `json:"work_revision"`
	AttemptID     string `json:"attempt_id,omitempty"`
	SegmentID     string `json:"segment_id,omitempty"`
	SegmentStatus string `json:"segment_status,omitempty"`
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
	return snapshot, nil
}

func runtimeHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
