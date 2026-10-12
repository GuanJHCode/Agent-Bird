package gitopsworker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"io"
	"os"
	"syscall"
)

func (j *durableJournal) readBoundedRecord(ctx context.Context, kind string, value any) (bool, error) {
	f, err := os.OpenFile(j.file.Name(), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	owner, owned := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || !owned || owner.Uid != uint32(os.Geteuid()) || st.Size() > 16*1024*1024 {
		return false, errors.New("gitops_journal_unsafe")
	}
	dec := json.NewDecoder(io.LimitReader(f, 16*1024*1024+1))
	dec.DisallowUnknownFields()
	var body []byte
	for {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		var r struct {
			Version int             `json:"version"`
			Kind    string          `json:"kind"`
			Value   json.RawMessage `json:"value"`
		}
		var raw json.RawMessage
		err = dec.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil || decodeJournalStrict(raw, &r) != nil || r.Version != 1 {
			return false, errors.New("gitops_journal_invalid")
		}
		if r.Kind != kind {
			continue
		}
		if len(r.Value) > 1024*1024 {
			return false, errors.New("gitops_journal_limit")
		}
		if body != nil && string(body) != string(r.Value) {
			return false, errors.New("gitops_journal_binding_conflict")
		}
		body = append([]byte(nil), r.Value...)
	}
	if body == nil {
		return false, nil
	}
	if err = decodeJournalStrict(body, value); err != nil {
		return false, err
	}
	return true, nil
}

func (j *durableJournal) LoadWorkspacePrepared(ctx context.Context) (gitops.PreparedWorkspace, error) {
	var p gitops.PreparedWorkspace
	found, err := j.readBoundedRecord(ctx, "workspace_prepared", &p)
	if err != nil {
		return p, err
	}
	if !found {
		return p, errors.New("workspace_prepared_missing")
	}
	return p, nil
}

func (j *durableJournal) LoadCandidateFreeze(ctx context.Context) (gitops.CandidateReceipt, bool, error) {
	var c gitops.CandidateReceipt
	found, err := j.readBoundedRecord(ctx, "candidate_receipt", &c)
	return c, found, err
}

// LoadCleanupRecovery scans one inode-stable journal snapshot. Recovery
// records are ordered authority: a plan is immutable and each next step is
// authorized exactly once (an exact duplicate is an idempotent replay).
func (j *durableJournal) LoadCleanupRecovery(ctx context.Context, receipt string) (state gitops.CleanupRecoveryState, err error) {
	state.Step = -1
	err = j.withLock(syscall.LOCK_SH, func() error {
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
		if err != nil || !os.SameFile(st, opened) {
			return errors.New("gitops_journal_replaced")
		}
		initial := st.Size()
		dec := json.NewDecoder(io.NewSectionReader(f, 0, initial))
		dec.DisallowUnknownFields()
		planDigest := ""
		final := false
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var raw json.RawMessage
			err := dec.Decode(&raw)
			if err == io.EOF {
				break
			}
			if err != nil {
				return errors.New("gitops_journal_invalid")
			}
			var r struct {
				Version int             `json:"version"`
				Kind    string          `json:"kind"`
				Value   json.RawMessage `json:"value"`
			}
			if decodeJournalStrict(raw, &r) != nil || r.Version != 1 || len(r.Value) > 1024*1024 {
				return errors.New("gitops_journal_invalid")
			}
			switch r.Kind {
			case "cleanup_intent":
				var v gitops.CleanupIntent
				if decodeJournalStrict(r.Value, &v) != nil || v.ReceiptSHA256 == "" {
					return errors.New("gitops_journal_invalid")
				}
				if v.ReceiptSHA256 != receipt {
					return errors.New("gitops_journal_foreign_cleanup")
				}
				if state.Found && state.Intent != v {
					return errors.New("gitops_journal_binding_conflict")
				}
				state.Intent, state.Found = v, true
			case "cleanup_plan":
				var v gitops.CleanupPlan
				if decodeJournalStrict(r.Value, &v) != nil || v.Intent.ReceiptSHA256 != receipt || gitops.CleanupPlanDigest(v) == "" {
					return errors.New("gitops_journal_invalid")
				}
				d := gitops.CleanupPlanDigest(v)
				if state.Plan != nil && planDigest != d {
					return errors.New("gitops_journal_binding_conflict")
				}
				if state.Plan == nil {
					state.Plan = &v
					planDigest = d
					state.Intent = v.Intent
					state.Found = true
				}
			case "cleanup_step":
				var v gitops.CleanupStep
				if decodeJournalStrict(r.Value, &v) != nil || state.Plan == nil || v.ReceiptSHA256 != receipt || v.PlanSHA256 != planDigest || v.Index < 0 || v.Index >= gitops.CleanupPlanStepCount(*state.Plan) {
					return errors.New("gitops_journal_invalid")
				}
				if final {
					return errors.New("gitops_journal_binding_conflict")
				}
				if v.Index == state.Step {
					continue
				}
				if v.Index != state.Step+1 {
					return errors.New("gitops_journal_binding_conflict")
				}
				state.Step = v.Index
			case "cleanup_outcome":
				var v gitops.CleanupOutcome
				if decodeJournalStrict(r.Value, &v) != nil || v.ReceiptSHA256 != receipt {
					return errors.New("gitops_journal_invalid")
				}
				if state.Outcome.ReceiptSHA256 != "" && state.Outcome != v {
					return errors.New("gitops_journal_binding_conflict")
				}
				if v.State == gitops.CleanupRemoved {
					if state.Plan != nil && state.Step != gitops.CleanupPlanStepCount(*state.Plan)-1 {
						return errors.New("gitops_journal_invalid")
					}
					final = true
				}
				state.Outcome, state.Found = v, true
			}
		}
		latest, err := j.validJournalFile()
		if err != nil || latest.Size() != initial {
			if err != nil {
				return err
			}
			return errors.New("gitops_journal_changed")
		}
		return nil
	})
	return state, err
}

// LoadCleanup reads one bounded, strict journal snapshot.  The pathname must
// still name the inode held by this journal; otherwise a replacement could
// fabricate a completion proof during replay.
func (j *durableJournal) LoadCleanup(ctx context.Context, receiptSHA256 string) (intent gitops.CleanupIntent, outcome gitops.CleanupOutcome, found bool, err error) {
	err = j.withLock(syscall.LOCK_SH, func() error {
		intent, outcome, found, err = j.loadCleanupLocked(ctx, receiptSHA256)
		return err
	})
	return intent, outcome, found, err
}

func (j *durableJournal) loadCleanupLocked(ctx context.Context, receiptSHA256 string) (gitops.CleanupIntent, gitops.CleanupOutcome, bool, error) {
	var intent gitops.CleanupIntent
	var outcome gitops.CleanupOutcome
	f, err := os.OpenFile(j.file.Name(), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return intent, outcome, false, err
	}
	defer f.Close()
	if err = f.Sync(); err != nil {
		return intent, outcome, false, err
	}
	st, err := f.Stat()
	if err != nil {
		return intent, outcome, false, err
	}
	held, err := j.file.Stat()
	if err != nil || !os.SameFile(st, held) {
		return intent, outcome, false, errors.New("gitops_journal_replaced")
	}
	pathInfo, err := os.Stat(j.file.Name())
	if err != nil || !os.SameFile(st, pathInfo) {
		return intent, outcome, false, errors.New("gitops_journal_replaced")
	}
	owner, owned := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || !owned || owner.Uid != uint32(os.Geteuid()) || st.Size() > 16*1024*1024 {
		return intent, outcome, false, errors.New("gitops_journal_unsafe")
	}
	initialSize := st.Size()
	dec := json.NewDecoder(io.NewSectionReader(f, 0, initialSize))
	dec.DisallowUnknownFields()
	foundIntent, foreign := false, false
	for {
		if err = ctx.Err(); err != nil {
			return intent, outcome, false, err
		}
		var raw json.RawMessage
		err = dec.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			return intent, outcome, false, errors.New("gitops_journal_invalid")
		}
		var record struct {
			Version int             `json:"version"`
			Kind    string          `json:"kind"`
			Value   json.RawMessage `json:"value"`
		}
		if decodeJournalStrict(raw, &record) != nil || record.Version != 1 || len(record.Value) > 1024*1024 {
			return intent, outcome, false, errors.New("gitops_journal_invalid")
		}
		switch record.Kind {
		case "cleanup_intent":
			var value gitops.CleanupIntent
			if decodeJournalStrict(record.Value, &value) != nil || value.ReceiptSHA256 == "" {
				return intent, outcome, false, errors.New("gitops_journal_invalid")
			}
			if value.ReceiptSHA256 != receiptSHA256 {
				foreign = true
				continue
			}
			if foundIntent {
				return intent, outcome, false, errors.New("gitops_journal_binding_conflict")
			}
			intent, foundIntent = value, true
		case "cleanup_outcome":
			var value gitops.CleanupOutcome
			if decodeJournalStrict(record.Value, &value) != nil || value.ReceiptSHA256 == "" {
				return intent, outcome, false, errors.New("gitops_journal_invalid")
			}
			if value.ReceiptSHA256 != receiptSHA256 {
				foreign = true
				continue
			}
			if outcome.ReceiptSHA256 != "" {
				return intent, outcome, false, errors.New("gitops_journal_binding_conflict")
			}
			outcome = value
		}
	}
	if foreign {
		return intent, outcome, false, errors.New("gitops_journal_foreign_cleanup")
	}
	if outcome.ReceiptSHA256 != "" && !foundIntent {
		return intent, outcome, false, errors.New("gitops_journal_orphan_outcome")
	}
	latest, statErr := f.Stat()
	if statErr != nil || latest.Size() != initialSize || !os.SameFile(latest, st) {
		return intent, outcome, false, errors.New("gitops_journal_changed")
	}
	held, statErr = j.file.Stat()
	if statErr != nil || !os.SameFile(latest, held) || held.Size() != initialSize {
		return intent, outcome, false, errors.New("gitops_journal_changed")
	}
	pathInfo, statErr = os.Stat(j.file.Name())
	if statErr != nil || !os.SameFile(latest, pathInfo) || pathInfo.Size() != initialSize {
		return intent, outcome, false, errors.New("gitops_journal_changed")
	}
	return intent, outcome, foundIntent, nil
}
