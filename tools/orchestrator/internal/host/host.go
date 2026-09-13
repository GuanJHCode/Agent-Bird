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
)

type Host struct {
	db        *store.DB
	spoolRoot string
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
	return &Host{db: db, spoolRoot: spoolRoot}, nil
}
func (h *Host) spool(a store.Attempt) (*events.Spool, error) {
	return events.Open(filepath.Join(h.spoolRoot, a.ID, a.SegmentID))
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
	p, err := process.Start(ctx, cmd)
	if err != nil {
		_ = h.db.FinishAttempt(context.Background(), a.ID, "failed")
		return contract.Result{Status: "failed", ExitCode: -1, EventCount: 1}, err
	}
	waitErr := p.Wait(ctx)
	if waitErr != nil {
		stopErr := p.Stop(ctx)
		if stopErr != nil {
			_ = h.db.FinishAttempt(context.Background(), a.ID, "unknown")
			return contract.Result{Status: "unknown", ExitCode: p.ExitCode(), EventCount: 1, OutputHash: hashText(p.Output())}, stopErr
		}
		_ = h.db.FinishAttempt(context.Background(), a.ID, "interrupted")
		return contract.Result{Status: "interrupted", ExitCode: p.ExitCode(), EventCount: 1, OutputHash: hashText(p.Output())}, waitErr
	}
	status := "result_ready"
	if p.ExitCode() != 0 {
		status = "failed"
	}
	outHash := hashText(p.Output())
	if err = spool.Append(contract.Event{Version: 1, RunID: runID, TaskID: taskID, AttemptID: a.ID, SegmentID: a.SegmentID, Sequence: 2, Kind: "result", PayloadHash: outHash}); err != nil {
		return contract.Result{}, err
	}
	if err = h.db.FinishAttempt(context.Background(), a.ID, status); err != nil {
		return contract.Result{}, err
	}
	return contract.Result{Status: status, ExitCode: p.ExitCode(), EventCount: 2, OutputHash: outHash}, nil
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
	if err = s.AckThrough(ev[len(ev)-1].Sequence); err != nil {
		return err
	}
	return h.db.AcknowledgeAttempt(ctx, a.ID)
}
func hashText(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
