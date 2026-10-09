package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func validationToolPin(raw json.RawMessage) (string, error) {
	var p struct {
		Action *struct {
			Pin string `json:"command_sha256"`
		} `json:"candidate_action"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Action == nil || p.Action.Pin == "" {
		return "", nil
	}
	a, err := adapter.DecodeInvocationPayload(raw)
	if err != nil || a.Kind != "candidate" || a.CandidateAction == nil || a.CandidateAction.Operation != "validate" || !adapter.ValidCommandSHA256(a.CandidateAction.CommandSHA256) {
		return "", contract.ErrValidationToolGrant
	}
	return a.CandidateAction.CommandSHA256, nil
}

func (d *DB) reserveValidationTool(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	if c.ValidationTool != nil {
		return contract.ErrValidationToolGrant
	}
	pin, err := validationToolPin(c.AdapterPayload)
	if err != nil {
		return err
	}
	if pin == "" {
		return nil
	}
	scope, err := contract.ValidationToolScopeSHA256(*c, pin)
	if err != nil {
		return err
	}
	id, err := newID("validation-tool")
	if err != nil {
		return err
	}
	physical, err := validationToolHostIdentity(ctx, tx, c)
	if err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	control := int64(validationToolFixedControlBytes)
	for _, s := range []string{id, c.RunID, c.TaskID, c.AttemptID, c.SegmentID, c.HostID, c.CommandID, scope, pin, stamp, "reserved", physical} {
		control += int64(len(s))
	}
	if err = d.storageAdmission(ctx, tx, c.RunID, control, false); err != nil {
		return err
	}
	if err = d.managedArtifactAdmission(ctx, tx, contract.ValidationToolMaximumBytes, control); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO validation_tool_reservations(id,run_id,task_id,attempt_id,segment_id,producer_id,command_id,work_revision,plan_revision,scope_sha256,tool_sha256,maximum_bytes,metadata_bytes,created_at,host_identity_sha256,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'reserved')`, id, c.RunID, c.TaskID, c.AttemptID, c.SegmentID, c.HostID, c.CommandID, c.WorkRevision, c.PlanRevision, scope, pin, contract.ValidationToolMaximumBytes, contract.ValidationToolMetadataBytes, stamp, physical)
	if err != nil {
		return mapConflict(err)
	}
	c.ValidationTool = &contract.ValidationToolGrant{Version: 1, ReservationID: id, ScopeSHA256: scope, ToolSHA256: pin, MaximumBytes: contract.ValidationToolMaximumBytes}
	return contract.RequireValidationTool(*c, pin)
}

func bindValidationTool(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, live bool) error {
	pin, err := validationToolPin(c.AdapterPayload)
	if err != nil {
		return err
	}
	// The only absent schema permitted is genuinely pre34 historical data;
	// no such data could declare this new pin or storage authority.
	var present, version int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='validation_tool_reservations'`).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		if !live && pin == "" && c.ValidationTool == nil && tx.QueryRowContext(ctx, `SELECT version FROM schema_meta`).Scan(&version) == nil && version < 34 {
			return nil
		}
		return contract.ErrValidationToolGrant
	}
	var g contract.ValidationToolGrant
	var run, task, attempt, segment, host, command, state, cleanup, physical string
	var revision, plan int
	var metadata int64
	var release sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,run_id,task_id,attempt_id,segment_id,producer_id,command_id,work_revision,plan_revision,scope_sha256,tool_sha256,maximum_bytes,metadata_bytes,state,release_event_id,cleanup_json,host_identity_sha256 FROM validation_tool_reservations WHERE segment_id=?`, c.SegmentID).Scan(&g.ReservationID, &run, &task, &attempt, &segment, &host, &command, &revision, &plan, &g.ScopeSHA256, &g.ToolSHA256, &g.MaximumBytes, &metadata, &state, &release, &cleanup, &physical)
	if errors.Is(err, sql.ErrNoRows) {
		if pin == "" && c.ValidationTool == nil {
			return nil
		}
		return contract.ErrValidationToolGrant
	}
	if err != nil {
		return err
	}
	g.Version = 1
	if pin == "" || run != c.RunID || task != c.TaskID || attempt != c.AttemptID || segment != c.SegmentID || host != c.HostID || command != c.CommandID || revision != c.WorkRevision || plan != c.PlanRevision || metadata != contract.ValidationToolMetadataBytes || g.ToolSHA256 != pin || c.ValidationTool != nil && *c.ValidationTool != g {
		return contract.ErrValidationToolGrant
	}
	if state == "reserved" {
		if release.Valid || cleanup != "" {
			return contract.ErrValidationToolGrant
		}
		current, e := validationToolHostIdentity(ctx, tx, c)
		if e != nil || current != physical {
			return contract.ErrValidationToolGrant
		}
	} else if state == "released" && !live {
		if !release.Valid || verifyValidationToolRelease(ctx, tx, g, run, task, attempt, segment, host, command, revision, release.String, cleanup) != nil {
			return contract.ErrValidationToolGrant
		}
	} else {
		return contract.ErrValidationToolGrant
	}
	c.ValidationTool = &g
	return contract.RequireValidationTool(*c, pin)
}

func verifyValidationToolRelease(ctx context.Context, tx *sql.Tx, g contract.ValidationToolGrant, run, task, attempt, segment, host, command string, revision int, eventID, raw string) error {
	var cleanup contract.ValidationToolCleanup
	if len(raw) > validationToolCleanupLimit || json.Unmarshal([]byte(raw), &cleanup) != nil || cleanup.Validate() != nil || cleanup.ReservationID != g.ReservationID || cleanup.ScopeSHA256 != g.ScopeSHA256 || cleanup.ToolSHA256 != g.ToolSHA256 {
		return contract.ErrValidationToolGrant
	}
	canonical, _ := json.Marshal(cleanup)
	if string(canonical) != raw {
		return contract.ErrValidationToolGrant
	}
	var body, status, budget string
	err := tx.QueryRowContext(ctx, `SELECT e.body_json,s.status,b.status FROM runtime_events e JOIN segment_runtime s ON s.segment_id=e.segment_id JOIN budget_runtime b ON b.id=s.reservation_id WHERE e.event_id=?`, eventID).Scan(&body, &status, &budget)
	var e contract.Event
	if err != nil || json.Unmarshal([]byte(body), &e) != nil || e.ValidationToolCleanup == nil || *e.ValidationToolCleanup != cleanup || e.ProducerID != host || e.RunID != run || e.TaskID != task || e.AttemptID != attempt || e.SegmentID != segment || e.CommandID != command || e.WorkRevision != revision || budget != "settled" || !(e.Kind == contract.EventLaunchRejected && status == "not_started" || e.Kind == contract.EventExited && status == "exited") {
		return contract.ErrValidationToolGrant
	}
	return nil
}

// Called only inside authenticated lifecycle commit, after applyEvent has
// established not_started/exited and settled the original budget. No FS IO.
func applyValidationToolRelease(ctx context.Context, tx *sql.Tx, e contract.Event) error {
	if e.ValidationToolCleanup == nil {
		if e.Kind == contract.EventLaunchRejected || e.Kind == contract.EventExited {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM validation_tool_reservations WHERE segment_id=?`, e.SegmentID).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return contract.ErrValidationToolGrant
			}
		}
		return nil
	}
	r := e.ValidationToolCleanup
	if r.Validate() != nil || (e.Kind != contract.EventLaunchRejected && e.Kind != contract.EventExited) || len(e.EventID) == 0 || len(e.EventID) > validationToolReleaseEventLimit {
		return contract.ErrValidationToolGrant
	}
	var g contract.ValidationToolGrant
	var run, task, attempt, segment, host, command, state, raw, physical string
	var revision int
	var release sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,scope_sha256,tool_sha256,maximum_bytes,run_id,task_id,attempt_id,segment_id,producer_id,command_id,work_revision,state,release_event_id,cleanup_json,host_identity_sha256 FROM validation_tool_reservations WHERE id=?`, r.ReservationID).Scan(&g.ReservationID, &g.ScopeSHA256, &g.ToolSHA256, &g.MaximumBytes, &run, &task, &attempt, &segment, &host, &command, &revision, &state, &release, &raw, &physical)
	g.Version = 1
	if err != nil || g.Validate() != nil || run != e.RunID || task != e.TaskID || attempt != e.AttemptID || segment != e.SegmentID || host != e.ProducerID || command != e.CommandID || revision != e.WorkRevision {
		return contract.ErrValidationToolGrant
	}
	body, _ := json.Marshal(r)
	if verifyValidationToolRelease(ctx, tx, g, run, task, attempt, segment, host, command, revision, e.EventID, string(body)) != nil {
		return contract.ErrValidationToolGrant
	}
	if state == "released" {
		if !release.Valid || release.String != e.EventID || raw != string(body) {
			return contract.ErrValidationToolGrant
		}
		return nil
	}
	if state != "reserved" || release.Valid || raw != "" {
		return contract.ErrValidationToolGrant
	}
	current, identityErr := validationToolHostIdentity(ctx, tx, &contract.LaunchCommand{HostID: host, RunID: run, TaskID: task})
	if identityErr != nil || current != physical {
		return contract.ErrValidationToolGrant
	}
	result, err := tx.ExecContext(ctx, `UPDATE validation_tool_reservations SET state='released',release_event_id=?,cleanup_json=? WHERE id=? AND state='reserved'`, e.EventID, string(body), r.ReservationID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return contract.ErrValidationToolGrant
	}
	return nil
}

// HostID can survive a physical rebind. Reconstruct the same immutable tuple
// used by policy retry, but permit authenticated spool replay while reconciling.
func validationToolHostIdentity(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) (string, error) {
	var identity struct {
		HostID, LaunchID, ContextID, Generation, Executable string
		PID, OriginPID                                      int
		Birth, OriginBirth                                  string
	}
	var status, generation, executable, contextID string
	err := tx.QueryRowContext(ctx, `SELECT h.id,h.launch_id,h.origin_context_id,h.host_generation,h.executable,h.pid,h.birth,r.origin_pid,r.origin_birth,h.status,l.host_generation,l.executable,l.origin_context_id FROM runtime_hosts h JOIN host_launches l ON l.id=h.launch_id JOIN task_runtime tr ON tr.host_launch_id=l.id JOIN tasks t ON t.id=tr.task_id JOIN runs r ON r.id=t.run_id WHERE h.id=? AND t.id=? AND r.id=?`, c.HostID, c.TaskID, c.RunID).Scan(&identity.HostID, &identity.LaunchID, &identity.ContextID, &identity.Generation, &identity.Executable, &identity.PID, &identity.Birth, &identity.OriginPID, &identity.OriginBirth, &status, &generation, &executable, &contextID)
	if err != nil || (status != "ready" && status != "reconciling") || identity.Generation != generation || identity.Executable != executable || identity.ContextID != contextID || identity.PID <= 0 || identity.Birth == "" || identity.OriginPID <= 0 || identity.OriginBirth == "" {
		return "", contract.ErrValidationToolGrant
	}
	body, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return runtimeHash("validation-tool-host-v1\x00" + string(body)), nil
}
