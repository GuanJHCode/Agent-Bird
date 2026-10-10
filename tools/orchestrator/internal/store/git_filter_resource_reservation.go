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

func gitFilterDeclaredManifest(raw json.RawMessage) (*contract.GitFilterManifestV1, error) {
	m, err := contract.GitFilterManifestFromPayload(raw)
	if err != nil || m == nil {
		return m, err
	}
	p, err := adapter.DecodeInvocationPayload(raw)
	if err != nil || p.GitFilter == nil {
		return nil, contract.ErrGitFilterContract
	}
	clone := p.GitFilter.Clone()
	return &clone, nil
}

func (d *DB) reserveGitFilter(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) error {
	if c.GitFilter != nil {
		return contract.ErrGitFilterContract
	}
	m, err := gitFilterDeclaredManifest(c.AdapterPayload)
	if err != nil || m == nil {
		return err
	}
	budget, err := readExecutionBudget(ctx, tx, c.TaskID)
	if err != nil {
		return err
	}
	if budget.policy != nil && budget.policy.Retry != nil {
		return CodeError("git_filter_retry_unsupported")
	}
	manifest, err := m.Digest()
	if err != nil {
		return err
	}
	scope, err := contract.GitFilterResourceScopeSHA256(*c, manifest)
	if err != nil {
		return err
	}
	id, err := newID("git-filter")
	if err != nil {
		return err
	}
	physical, err := gitFilterHostIdentity(ctx, tx, c)
	if err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	control := int64(gitFilterFixedControlBytes)
	for _, s := range []string{id, c.RunID, c.TaskID, c.AttemptID, c.SegmentID, c.HostID, c.CommandID, scope, manifest, m.Clean.SHA256, m.Smudge.SHA256, stamp, "reserved", physical} {
		control += int64(len(s))
	}
	if err = d.storageAdmission(ctx, tx, c.RunID, control, false); err != nil {
		return err
	}
	if err = d.managedArtifactAdmission(ctx, tx, contract.GitFilterResourceMaximumBytes, control); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO git_filter_resource_reservations(id,run_id,task_id,attempt_id,segment_id,producer_id,command_id,work_revision,plan_revision,scope_sha256,manifest_sha256,clean_tool_sha256,smudge_tool_sha256,tool_maximum_bytes,stream_maximum_bytes,stream_slots,maximum_bytes,metadata_bytes,created_at,host_identity_sha256,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'reserved')`, id, c.RunID, c.TaskID, c.AttemptID, c.SegmentID, c.HostID, c.CommandID, c.WorkRevision, c.PlanRevision, scope, manifest, m.Clean.SHA256, m.Smudge.SHA256, contract.GitFilterToolMaximumBytes, contract.GitFilterStreamMaximumBytes, contract.GitFilterStreamSlots, contract.GitFilterResourceMaximumBytes, contract.GitFilterMetadataBytes, stamp, physical)
	if err != nil {
		return mapConflict(err)
	}
	c.GitFilter = &contract.GitFilterResourceGrant{Version: 1, ReservationID: id, ScopeSHA256: scope, ManifestSHA256: manifest, CleanToolSHA256: m.Clean.SHA256, SmudgeToolSHA256: m.Smudge.SHA256, ToolMaximumBytes: contract.GitFilterToolMaximumBytes, StreamMaximumBytes: contract.GitFilterStreamMaximumBytes, StreamSlots: contract.GitFilterStreamSlots}
	return contract.RequireGitFilter(*c)
}

func bindGitFilter(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand, live bool) error {
	m, err := gitFilterDeclaredManifest(c.AdapterPayload)
	if err != nil {
		return err
	}
	var present, version int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='git_filter_resource_reservations'`).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		if !live && m == nil && c.GitFilter == nil && tx.QueryRowContext(ctx, `SELECT version FROM schema_meta`).Scan(&version) == nil && version < 36 {
			return nil
		}
		return contract.ErrGitFilterContract
	}
	var g contract.GitFilterResourceGrant
	var run, task, attempt, segment, host, command, state, cleanup, physical string
	var revision, plan int
	var maximum, metadata int64
	var release sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,run_id,task_id,attempt_id,segment_id,producer_id,command_id,work_revision,plan_revision,scope_sha256,manifest_sha256,clean_tool_sha256,smudge_tool_sha256,tool_maximum_bytes,stream_maximum_bytes,stream_slots,maximum_bytes,metadata_bytes,state,release_event_id,cleanup_json,host_identity_sha256 FROM git_filter_resource_reservations WHERE segment_id=?`, c.SegmentID).Scan(&g.ReservationID, &run, &task, &attempt, &segment, &host, &command, &revision, &plan, &g.ScopeSHA256, &g.ManifestSHA256, &g.CleanToolSHA256, &g.SmudgeToolSHA256, &g.ToolMaximumBytes, &g.StreamMaximumBytes, &g.StreamSlots, &maximum, &metadata, &state, &release, &cleanup, &physical)
	if errors.Is(err, sql.ErrNoRows) {
		if m == nil && c.GitFilter == nil {
			return nil
		}
		return contract.ErrGitFilterContract
	}
	if err != nil {
		return err
	}
	g.Version = 1
	if m == nil || run != c.RunID || task != c.TaskID || attempt != c.AttemptID || segment != c.SegmentID || host != c.HostID || command != c.CommandID || revision != c.WorkRevision || plan != c.PlanRevision || maximum != contract.GitFilterResourceMaximumBytes || metadata != contract.GitFilterMetadataBytes || c.GitFilter != nil && *c.GitFilter != g {
		return contract.ErrGitFilterContract
	}
	if state == "reserved" {
		if release.Valid || cleanup != "" {
			return contract.ErrGitFilterContract
		}
		current, e := gitFilterHostIdentity(ctx, tx, c)
		if e != nil || current != physical {
			return contract.ErrGitFilterContract
		}
	} else if state == "released" && !live {
		if !release.Valid || verifyGitFilterRelease(ctx, tx, g, run, task, attempt, segment, host, command, revision, release.String, cleanup) != nil {
			return contract.ErrGitFilterContract
		}
	} else {
		return contract.ErrGitFilterContract
	}
	c.GitFilter = &g
	return contract.RequireGitFilter(*c)
}

func verifyGitFilterRelease(ctx context.Context, tx *sql.Tx, g contract.GitFilterResourceGrant, run, task, attempt, segment, host, command string, revision int, eventID, raw string) error {
	var cleanup contract.GitFilterCleanup
	if len(raw) > gitFilterCleanupLimit || json.Unmarshal([]byte(raw), &cleanup) != nil || cleanup.Validate() != nil || cleanup.ReservationID != g.ReservationID || cleanup.ScopeSHA256 != g.ScopeSHA256 || cleanup.ManifestSHA256 != g.ManifestSHA256 || cleanup.CleanToolSHA256 != g.CleanToolSHA256 || cleanup.SmudgeToolSHA256 != g.SmudgeToolSHA256 {
		return contract.ErrGitFilterContract
	}
	canonical, _ := json.Marshal(cleanup)
	if string(canonical) != raw {
		return contract.ErrGitFilterContract
	}
	var body, status, budget string
	err := tx.QueryRowContext(ctx, `SELECT e.body_json,s.status,b.status FROM runtime_events e JOIN segment_runtime s ON s.segment_id=e.segment_id JOIN budget_runtime b ON b.id=s.reservation_id WHERE e.event_id=?`, eventID).Scan(&body, &status, &budget)
	var e contract.Event
	if err != nil || json.Unmarshal([]byte(body), &e) != nil || e.GitFilterCleanup == nil || *e.GitFilterCleanup != cleanup || e.ProducerID != host || e.RunID != run || e.TaskID != task || e.AttemptID != attempt || e.SegmentID != segment || e.CommandID != command || e.WorkRevision != revision || budget != "settled" || !(e.Kind == contract.EventLaunchRejected && status == "not_started" || e.Kind == contract.EventExited && status == "exited") {
		return contract.ErrGitFilterContract
	}
	return nil
}

// Authenticated lifecycle transaction only: applyEvent has established terminal
// not_started/exited and settled budget. UNKNOWN never releases resources.
func applyGitFilterRelease(ctx context.Context, tx *sql.Tx, e contract.Event) error {
	if e.GitFilterCleanup == nil {
		if e.Kind == contract.EventLaunchRejected || e.Kind == contract.EventExited {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM git_filter_resource_reservations WHERE segment_id=?`, e.SegmentID).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return contract.ErrGitFilterContract
			}
		}
		return nil
	}
	r := e.GitFilterCleanup
	if r.Validate() != nil || (e.Kind != contract.EventLaunchRejected && e.Kind != contract.EventExited) || len(e.EventID) < 1 || len(e.EventID) > gitFilterReleaseEventLimit {
		return contract.ErrGitFilterContract
	}
	var g contract.GitFilterResourceGrant
	var run, task, attempt, segment, host, command, state, raw, physical string
	var revision int
	var release sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,scope_sha256,manifest_sha256,clean_tool_sha256,smudge_tool_sha256,tool_maximum_bytes,stream_maximum_bytes,stream_slots,run_id,task_id,attempt_id,segment_id,producer_id,command_id,work_revision,state,release_event_id,cleanup_json,host_identity_sha256 FROM git_filter_resource_reservations WHERE id=?`, r.ReservationID).Scan(&g.ReservationID, &g.ScopeSHA256, &g.ManifestSHA256, &g.CleanToolSHA256, &g.SmudgeToolSHA256, &g.ToolMaximumBytes, &g.StreamMaximumBytes, &g.StreamSlots, &run, &task, &attempt, &segment, &host, &command, &revision, &state, &release, &raw, &physical)
	g.Version = 1
	if err != nil || g.Validate() != nil || run != e.RunID || task != e.TaskID || attempt != e.AttemptID || segment != e.SegmentID || host != e.ProducerID || command != e.CommandID || revision != e.WorkRevision {
		return contract.ErrGitFilterContract
	}
	body, _ := json.Marshal(r)
	if verifyGitFilterRelease(ctx, tx, g, run, task, attempt, segment, host, command, revision, e.EventID, string(body)) != nil {
		return contract.ErrGitFilterContract
	}
	if state == "released" {
		if !release.Valid || release.String != e.EventID || raw != string(body) {
			return contract.ErrGitFilterContract
		}
		return nil
	}
	if state != "reserved" || release.Valid || raw != "" {
		return contract.ErrGitFilterContract
	}
	current, identityErr := gitFilterHostIdentity(ctx, tx, &contract.LaunchCommand{HostID: host, RunID: run, TaskID: task})
	if identityErr != nil || current != physical {
		return contract.ErrGitFilterContract
	}
	result, err := tx.ExecContext(ctx, `UPDATE git_filter_resource_reservations SET state='released',release_event_id=?,cleanup_json=? WHERE id=? AND state='reserved'`, e.EventID, string(body), r.ReservationID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return contract.ErrGitFilterContract
	}
	return nil
}

// Reconstruct the original immutable physical tuple in a filter-specific domain.
func gitFilterHostIdentity(ctx context.Context, tx *sql.Tx, c *contract.LaunchCommand) (string, error) {
	var identity struct {
		HostID, LaunchID, ContextID, Generation, Executable string
		PID, OriginPID                                      int
		Birth, OriginBirth                                  string
	}
	var status, generation, executable, contextID string
	err := tx.QueryRowContext(ctx, `SELECT h.id,h.launch_id,h.origin_context_id,h.host_generation,h.executable,h.pid,h.birth,r.origin_pid,r.origin_birth,h.status,l.host_generation,l.executable,l.origin_context_id FROM runtime_hosts h JOIN host_launches l ON l.id=h.launch_id JOIN task_runtime tr ON tr.host_launch_id=l.id JOIN tasks t ON t.id=tr.task_id JOIN runs r ON r.id=t.run_id WHERE h.id=? AND t.id=? AND r.id=?`, c.HostID, c.TaskID, c.RunID).Scan(&identity.HostID, &identity.LaunchID, &identity.ContextID, &identity.Generation, &identity.Executable, &identity.PID, &identity.Birth, &identity.OriginPID, &identity.OriginBirth, &status, &generation, &executable, &contextID)
	if err != nil || (status != "ready" && status != "reconciling") || identity.Generation != generation || identity.Executable != executable || identity.ContextID != contextID || identity.PID <= 0 || identity.Birth == "" || identity.OriginPID <= 0 || identity.OriginBirth == "" {
		return "", contract.ErrGitFilterContract
	}
	body, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return runtimeHash("git-filter-host-v1\x00" + string(body)), nil
}
