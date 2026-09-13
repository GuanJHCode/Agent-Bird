package host

import (
	"codex-cli-orchestration-design/tools/orchestrator/internal/contract"
	"codex-cli-orchestration-design/tools/orchestrator/internal/events"
	"codex-cli-orchestration-design/tools/orchestrator/internal/process"
	"codex-cli-orchestration-design/tools/orchestrator/internal/store"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Host struct {
	db           *store.DB
	spoolRoot    string
	mu           sync.Mutex
	active       map[string]*process.Handle
	grantMu      sync.Mutex
	reservations map[string]time.Time
}

func New(db *store.DB, spoolRoot string) (*Host, error) {
	if db == nil {
		return nil, errors.New("store_required")
	}
	if !filepath.IsAbs(spoolRoot) {
		return nil, errors.New("spool_path_not_absolute")
	}
	if err := os.MkdirAll(spoolRoot, 0700); err != nil {
		return nil, err
	}
	return &Host{db: db, spoolRoot: spoolRoot, active: map[string]*process.Handle{}, reservations: map[string]time.Time{}}, nil
}
func (h *Host) spool(a store.Attempt) (*events.Spool, error) {
	return events.Open(filepath.Join(h.spoolRoot, a.ID, a.SegmentID))
}

// ExecuteLaunch is the only Host entrypoint for a coordinator launch grant.
// Invocation remains local to the source Host; the grant carries ownership and
// budget metadata but never authentication or environment data. A reservation
// is consumed once and is retained until process restart so reconnects cannot
// reset its active budget.
func (h *Host) ExecuteLaunch(ctx context.Context, grant contract.LaunchCommand, inv contract.InvocationView) (contract.Result, error) {
	if grant.ReservationID == "" || grant.AttemptID == "" || grant.SegmentID == "" || grant.RunID == "" || grant.TaskID == "" || grant.GrantedActiveMS <= 0 || inv == nil || len(inv.Args()) == 0 || inv.Args()[0] == "" {
		return contract.Result{}, errors.New("invalid_launch_grant")
	}
	now := time.Now()
	budgetDeadline := now.Add(time.Duration(grant.GrantedActiveMS) * time.Millisecond)
	if grant.DeadlineUnixMS > 0 {
		deadline := time.UnixMilli(grant.DeadlineUnixMS)
		if deadline.Before(budgetDeadline) {
			budgetDeadline = deadline
		}
	}
	if !budgetDeadline.After(now) {
		return contract.Result{}, errors.New("launch_deadline_expired")
	}
	h.grantMu.Lock()
	if _, exists := h.reservations[grant.ReservationID]; exists {
		h.grantMu.Unlock()
		return contract.Result{}, errors.New("reservation_reused")
	}
	h.reservations[grant.ReservationID] = budgetDeadline
	h.grantMu.Unlock()
	runCtx, cancel := context.WithDeadline(ctx, budgetDeadline)
	defer cancel()
	return h.Execute(runCtx, store.Attempt{ID: grant.AttemptID, TaskID: grant.TaskID, SegmentID: grant.SegmentID, Status: "running"}, grant.RunID, grant.TaskID, invocationCommand(inv))
}

func invocationCommand(inv contract.InvocationView) process.Command {
	args := inv.Args()
	envMap := inv.Environment()
	env := make([]string, 0, len(envMap))
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}
	return process.Command{Path: args[0], Args: append([]string(nil), args[1:]...), Dir: inv.WorkingDirectory(), Env: env, Stdin: append([]byte(nil), inv.Stdin()...)}
}

func (h *Host) Execute(ctx context.Context, a store.Attempt, runID, taskID string, cmd process.Command) (contract.Result, error) {
	spool, err := h.spool(a)
	if err != nil {
		return contract.Result{}, err
	}
	startHash := hashText("segment_started:" + a.SegmentID)
	if err = spool.Append(contract.Event{Version: 1, RunID: runID, TaskID: taskID, AttemptID: a.ID, SegmentID: a.SegmentID, Sequence: 1, Kind: "segment_started", PayloadHash: startHash}); err != nil {
		return contract.Result{}, err
	}
	if err = publishLaunchPhase(spool, "prepared", a, runID, taskID, cmd, nil, nil); err != nil {
		_ = h.db.FinishAttempt(context.Background(), a.ID, "failed")
		return contract.Result{Status: "failed", ExitCode: -1, EventCount: 1}, err
	}
	p, err := process.Start(ctx, cmd)
	if err != nil {
		_ = publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, nil, err)
		_ = h.db.FinishAttempt(context.Background(), a.ID, "failed")
		return contract.Result{Status: "failed", ExitCode: -1, EventCount: 1}, err
	}
	identity := p.Identity()
	if err = publishLaunchPhase(spool, "spawned", a, runID, taskID, cmd, &identity, nil); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.Stop(stopCtx)
		stopCancel()
		_ = h.db.FinishAttempt(context.Background(), a.ID, "unknown")
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: 1}, err
	}
	if err = publishLaunchPhase(spool, "running", a, runID, taskID, cmd, &identity, nil); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = p.Stop(stopCtx)
		stopCancel()
		_ = h.db.FinishAttempt(context.Background(), a.ID, "unknown")
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: 1}, err
	}
	h.mu.Lock()
	h.active[a.ID] = p
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.active, a.ID); h.mu.Unlock() }()
	waitErr := p.Wait(ctx)
	if waitErr != nil {
		stopErr := p.Stop(ctx)
		if stopErr != nil {
			if phaseErr := publishLaunchPhase(spool, "unknown", a, runID, taskID, cmd, &identity, stopErr); phaseErr != nil {
				stopErr = phaseErr
			}
			_ = h.db.FinishAttempt(context.Background(), a.ID, "unknown")
			return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: 1, OutputHash: hashText(p.Output())}, stopErr
		}
		if phaseErr := publishLaunchPhase(spool, "stopped", a, runID, taskID, cmd, &identity, waitErr); phaseErr != nil {
			_ = h.db.FinishAttempt(context.Background(), a.ID, "unknown")
			return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: 1, OutputHash: hashText(p.Output())}, phaseErr
		}
		_ = h.db.FinishAttempt(context.Background(), a.ID, "interrupted")
		return contract.Result{Status: "interrupted", ExitCode: p.ExitCode(), EventCount: 1, OutputHash: hashText(p.Output())}, waitErr
	}
	status := "result_ready"
	if p.ExitCode() < 0 {
		status = "interrupted"
	} else if p.ExitCode() != 0 {
		status = "failed"
	}
	outHash := hashText(p.Output())
	artifactPath, err := spool.WriteArtifact([]byte(p.Output()))
	if err != nil {
		return contract.Result{}, err
	}
	if err = spool.Append(contract.Event{Version: 1, RunID: runID, TaskID: taskID, AttemptID: a.ID, SegmentID: a.SegmentID, Sequence: 2, Kind: "result", PayloadHash: outHash}); err != nil {
		return contract.Result{}, err
	}
	phase := "exited"
	if status == "interrupted" {
		phase = "stopped"
	}
	if err = publishLaunchPhase(spool, phase, a, runID, taskID, cmd, &identity, nil); err != nil {
		_ = h.db.FinishAttempt(context.Background(), a.ID, "unknown")
		return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: 2, OutputHash: outHash, ArtifactPath: artifactPath}, err
	}
	if err = h.db.FinishAttempt(context.Background(), a.ID, status); err != nil {
		return contract.Result{}, err
	}
	return contract.Result{Status: status, ExitCode: p.ExitCode(), EventCount: 2, OutputHash: outHash, ArtifactPath: artifactPath}, nil
}

func publishLaunchPhase(spool *events.Spool, phase string, a store.Attempt, runID, taskID string, cmd process.Command, identity *process.Identity, cause error) error {
	argsHash := hashBytes([]byte(strings.Join(cmd.Args, "\x00")))
	record := map[string]any{
		"version": 1, "phase": phase, "run_id": runID, "task_id": taskID,
		"attempt_id": a.ID, "segment_id": a.SegmentID, "executable": cmd.Path,
		"args_sha256": argsHash, "started_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if identity != nil {
		record["process"] = map[string]any{"pid": identity.PID, "pgid": identity.PGID, "birth": identity.Birth, "birth_known": identity.BirthKnown, "executable": identity.Executable, "executable_sha256": identity.ExecutableSHA256, "started_at": identity.StartedAt}
	}
	if cause != nil {
		record["cause_code"] = stableCause(cause)
	}
	return spool.WriteMetadata("launch.json", record)
}

func hashBytes(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func stableCause(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, process.ErrProcessTreeUnknown) {
		return "process_tree_unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "process_failed"
}
func (h *Host) Collect(ctx context.Context, a store.Attempt) ([]contract.Event, error) {
	_ = ctx
	s, err := h.spool(a)
	if err != nil {
		return nil, err
	}
	return s.Read()
}
func (h *Host) Ack(ctx context.Context, a store.Attempt) error {
	s, err := h.spool(a)
	if err != nil {
		return err
	}
	ev, err := s.Read()
	if err != nil {
		return err
	}
	if len(ev) == 0 {
		return errors.New("no_events")
	}
	if err = h.db.AcknowledgeAttempt(ctx, a.ID); err != nil {
		return err
	}
	return s.AckThrough(ev[len(ev)-1].Sequence)
}
func hashText(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func (h *Host) ExecuteInvocation(ctx context.Context, a store.Attempt, runID, taskID string, inv contract.InvocationView) (contract.Result, error) {
	args := inv.Args()
	if len(args) == 0 || args[0] == "" {
		return contract.Result{}, errors.New("invocation_args_invalid")
	}
	return h.Execute(ctx, a, runID, taskID, invocationCommand(inv))
}

func (h *Host) Cancel(ctx context.Context, a store.Attempt) error {
	h.mu.Lock()
	p := h.active[a.ID]
	h.mu.Unlock()
	if p == nil {
		return h.db.FinishAttempt(ctx, a.ID, "cancelled")
	}
	if err := p.Stop(ctx); err != nil {
		_ = h.db.FinishAttempt(context.Background(), a.ID, "unknown")
		return err
	}
	return nil
}
