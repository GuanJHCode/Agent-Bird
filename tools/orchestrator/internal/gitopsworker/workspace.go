package gitopsworker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

const journalLimit = 16 * 1024 * 1024
const journalRecordLimit = 1024 * 1024
const cleanupFinalReserve = 4096

type journalRecord struct {
	Version int             `json:"version"`
	Kind    string          `json:"kind"`
	Value   json.RawMessage `json:"value"`
}

// The caller holds events.lock for this entire fixed-length snapshot. No
// pathname reopen can switch authority away from the originally held inode.
func (j *durableJournal) scanLocked(ctx context.Context, visit func(journalRecord) error) error {
	if err := j.file.Sync(); err != nil {
		return err
	}
	st, err := j.validJournalFile()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(j.file.Name(), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(st, opened) || opened.Size() != st.Size() {
		return errors.New("gitops_journal_replaced")
	}
	if st.Size() > 0 {
		var end [1]byte
		if _, err = f.ReadAt(end[:], st.Size()-1); err != nil || end[0] != '\n' {
			return errors.New("gitops_journal_incomplete")
		}
	}
	dec := json.NewDecoder(io.NewSectionReader(f, 0, st.Size()))
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		var raw json.RawMessage
		err = dec.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("gitops_journal_invalid")
		}
		var row journalRecord
		if len(raw) > journalRecordLimit || decodeJournalStrict(raw, &row) != nil || row.Version != 1 || row.Kind == "" {
			return errors.New("gitops_journal_invalid")
		}
		if err = visit(row); err != nil {
			return err
		}
	}
	latest, err := j.validJournalFile()
	if err != nil {
		return err
	}
	after, err := f.Stat()
	if err != nil || latest.Size() != st.Size() || !latest.ModTime().Equal(st.ModTime()) || after.Size() != st.Size() || !os.SameFile(after, st) {
		return errors.New("gitops_journal_changed")
	}
	return nil
}

func (j *durableJournal) readBoundedRecord(ctx context.Context, kind string, value any) (bool, error) {
	var body []byte
	err := j.withLock(syscall.LOCK_SH, func() error {
		return j.scanLocked(ctx, func(row journalRecord) error {
			if row.Kind != kind {
				return nil
			}
			if body != nil && string(body) != string(row.Value) {
				return errors.New("gitops_journal_binding_conflict")
			}
			body = append([]byte(nil), row.Value...)
			return nil
		})
	})
	if err != nil || body == nil {
		return false, err
	}
	if err = decodeJournalStrict(body, value); err != nil {
		return false, err
	}
	return true, nil
}
func (j *durableJournal) LoadWorkspacePrepared(ctx context.Context) (gitops.PreparedWorkspace, error) {
	var p gitops.PreparedWorkspace
	found, err := j.readBoundedRecord(ctx, "workspace_prepared", &p)
	if err == nil && !found {
		err = errors.New("workspace_prepared_missing")
	}
	return p, err
}
func (j *durableJournal) LoadCandidateFreeze(ctx context.Context) (gitops.CandidateReceipt, bool, error) {
	var p gitops.CandidateReceipt
	found, err := j.readBoundedRecord(ctx, "candidate_receipt", &p)
	return p, found, err
}

// applyCleanupRecord is also used before append. Invalid authority never
// poisons the durable journal; exact duplicates do not consume capacity.
func applyCleanupRecord(state *gitops.CleanupRecoveryState, row journalRecord) (bool, error) {
	conflict := func() (bool, error) { return false, errors.New("gitops_journal_binding_conflict") }
	match := func(sha string) bool { return sha != "" && (!state.Found || state.Intent.ReceiptSHA256 == sha) }
	final := state.Outcome.State == gitops.CleanupRemoved
	switch row.Kind {
	case "cleanup_intent":
		var v gitops.CleanupIntent
		if decodeJournalStrict(row.Value, &v) != nil || !match(v.ReceiptSHA256) || state.Plan != nil {
			return conflict()
		}
		if state.Found {
			if state.Intent == v {
				return false, nil
			}
			return conflict()
		}
		if final {
			return conflict()
		}
		state.Intent, state.Found = v, true
	case "cleanup_plan":
		var v gitops.CleanupPlan
		if decodeJournalStrict(row.Value, &v) != nil || v.Version != 1 || !match(v.Intent.ReceiptSHA256) || final {
			return conflict()
		}
		if state.Found {
			if state.Plan != nil && gitops.CleanupPlanDigest(*state.Plan) == gitops.CleanupPlanDigest(v) {
				return false, nil
			}
			return conflict()
		}
		state.Plan = &v
		state.Intent = v.Intent
		state.Found = true
		state.Step = -1
	case "cleanup_step":
		var v gitops.CleanupStep
		if decodeJournalStrict(row.Value, &v) != nil || state.Plan == nil || !match(v.ReceiptSHA256) || v.PlanSHA256 != gitops.CleanupPlanDigest(*state.Plan) || v.Index < 0 || v.Index >= gitops.CleanupPlanStepCount(*state.Plan) || final {
			return conflict()
		}
		if v.Index == state.Step {
			return false, nil
		}
		if v.Index != state.Step+1 {
			return conflict()
		}
		state.Step = v.Index
	case "cleanup_outcome":
		var v gitops.CleanupOutcome
		if decodeJournalStrict(row.Value, &v) != nil || !state.Found || !match(v.ReceiptSHA256) || v.Root != state.Intent.Root || v.PrivateRef != state.Intent.PrivateRef || (v.State != gitops.CleanupPending && v.State != gitops.CleanupRemoved) {
			return conflict()
		}
		if state.Outcome.ReceiptSHA256 != "" {
			if state.Outcome == v {
				return false, nil
			}
			if state.Outcome.State != gitops.CleanupPending || v.State != gitops.CleanupRemoved {
				return conflict()
			}
		}
		if v.State == gitops.CleanupRemoved && state.Plan != nil && state.Step != gitops.CleanupPlanStepCount(*state.Plan)-1 {
			return conflict()
		}
		state.Outcome = v
	}
	return true, nil
}
func (j *durableJournal) cleanupStateLocked(ctx context.Context) (gitops.CleanupRecoveryState, error) {
	state := gitops.CleanupRecoveryState{Step: -1}
	err := j.scanLocked(ctx, func(row journalRecord) error { _, err := applyCleanupRecord(&state, row); return err })
	return state, err
}
func (j *durableJournal) LoadCleanupRecovery(ctx context.Context, receipt string) (state gitops.CleanupRecoveryState, err error) {
	err = j.withLock(syscall.LOCK_SH, func() error {
		state, err = j.cleanupStateLocked(ctx)
		if err != nil {
			return err
		}
		if state.Found && state.Intent.ReceiptSHA256 != receipt {
			return errors.New("gitops_journal_foreign_cleanup")
		}
		return nil
	})
	return state, err
}
func (j *durableJournal) LoadCleanup(ctx context.Context, receipt string) (gitops.CleanupIntent, gitops.CleanupOutcome, bool, error) {
	s, err := j.LoadCleanupRecovery(ctx, receipt)
	return s.Intent, s.Outcome, s.Found, err
}
func cleanupReservedBytes(state gitops.CleanupRecoveryState) int64 {
	if state.Plan == nil || state.Outcome.State == gitops.CleanupRemoved {
		return 0
	}
	finalBody, _ := json.Marshal(map[string]any{"version": 1, "kind": "cleanup_outcome", "value": gitops.CleanupOutcome{State: gitops.CleanupRemoved, Root: state.Intent.Root, PrivateRef: state.Intent.PrivateRef, PrivateRefState: "deleted", RemovedFiles: len(state.Plan.Root.Files), ReceiptSHA256: state.Intent.ReceiptSHA256}})
	reserved := int64(max(cleanupFinalReserve, len(finalBody)+256))
	digest := gitops.CleanupPlanDigest(*state.Plan)
	for i := state.Step + 1; i < gitops.CleanupPlanStepCount(*state.Plan); i++ {
		body, _ := json.Marshal(map[string]any{"version": 1, "kind": "cleanup_step", "value": gitops.CleanupStep{ReceiptSHA256: state.Intent.ReceiptSHA256, PlanSHA256: digest, Index: i}})
		reserved += int64(len(body) + 1)
	}
	return reserved
}
