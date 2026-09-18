package store

import (
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

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

func validateInvocationPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var kind struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &kind) != nil {
		return CodeError("invalid_adapter_payload")
	}
	// Git operations have a separate request protocol, decoded by gitopsworker.
	if kind.Kind == "gitops" {
		return nil
	}
	payload, err := adapter.DecodeInvocationPayload(raw)
	if err != nil {
		return CodeError("invalid_adapter_payload")
	}
	if payload.Profile != nil && payload.Profile.Permission == adapter.WorkspaceWrite && payload.CandidateWorkspace == nil {
		return CodeError("managed_workspace_required")
	}
	if a := payload.CandidateAction; a != nil && a.Operation == "validate" && len(a.Command) == 0 {
		return CodeError("candidate_validation_command_required")
	}
	return nil
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
	groupTasks := make(map[string]int)
	groupActive := make(map[string]int64)
	for _, task := range p.Tasks {
		if err := validateInvocationPayload(task.AdapterPayload); err != nil {
			return err
		}
		if err := rejectSubmittedRework(task.AdapterPayload); err != nil {
			return err
		}
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
			if err := validateInvocationPayload(fallback); err != nil {
				return err
			}
			if err := rejectSubmittedRework(fallback); err != nil {
				return err
			}
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
		groupTasks[group]++
		if groupTasks[group] > task.MaxAttempts {
			return CodeError("budget_group_attempts_insufficient")
		}
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
	if err := validateCandidatePlan(byID); err != nil {
		return err
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
	if p.Run.DeliveryMode == "" {
		p.Run.DeliveryMode = "native"
	}
	if p.Run.DeliveryMode != "native" && p.Run.DeliveryMode != "collect" {
		return SubmitReceipt{}, CodeError("delivery_mode_invalid")
	}

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
	for _, task := range p.Tasks {
		for _, raw := range append([]json.RawMessage{task.AdapterPayload}, task.FallbackPayloads...) {
			if err = checkProviderEnabled(ctx, tx, p.Run.ControllerThread, raw); err != nil {
				return rollback(err)
			}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var activeHosts int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM host_launches WHERE status!='released'`).Scan(&activeHosts); err != nil {
		return rollback(err)
	}
	if activeHosts >= 4 {
		return rollback(ErrAdmissionDeferred)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,controller_thread,plan_revision,origin_context_id,origin_pid,origin_birth,status,created_at,delivery_mode) VALUES(?,?,?,?,?,?,?,?,?)`, p.Run.ID, p.Run.ControllerThread, p.Run.PlanRevision, p.Run.OriginContextID, p.Run.OriginPID, p.Run.OriginBirth, "queued", now, p.Run.DeliveryMode); err != nil {
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
