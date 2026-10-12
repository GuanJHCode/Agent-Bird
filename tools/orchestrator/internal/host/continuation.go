package host

import (
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"path/filepath"
)

func (h *Host) validateTrustedContinuation(g contract.LaunchCommand) error {
	c := g.Continuation
	if err := adapter.ValidateContinuationReference(c); err != nil {
		return err
	}
	p, err := adapter.DecodeInvocationPayload(g.AdapterPayload)
	if err != nil || p.Profile == nil || p.CandidateAction != nil || p.ProviderLock == nil || (p.Provider != "claude-code" && p.Provider != "antigravity-cli") || p.SessionID != "" || p.SessionKind != "" {
		return errors.New("continuation_payload_invalid")
	}
	kind := "session-id"
	if p.Provider == "antigravity-cli" {
		kind = "conversation-id"
	}
	if g.SessionKind != kind || g.SessionID == "" || hashBytes(g.AdapterPayload) != c.AdapterPayloadSHA256 || c.OwnerContextID == "" || c.OwnerContextID != h.continuationOwner {
		return errors.New("continuation_origin_changed")
	}
	if c.Mode == contract.ContinuationQuestion {
		if g.QuestionID == "" || g.QuestionRevision < 1 || g.Answer == "" {
			return errors.New("invalid_question_resume")
		}
	} else if g.QuestionID != "" || g.QuestionRevision != 0 || g.Answer != "" {
		return errors.New("invalid_interrupted_resume")
	}
	entries, err := h.spoolEntries()
	if err != nil {
		return err
	}
	foundInit := false
	for _, entry := range entries {
		records, err := entry.spool.Read()
		if err != nil {
			return err
		}
		for _, e := range records {
			if e.EventID != c.SessionEventID {
				continue
			}
			if e.Kind != contract.EventSession || e.ProducerID != h.producerID || e.RunID != g.RunID || e.TaskID != g.TaskID || e.AttemptID != g.AttemptID || e.WorkRevision != g.WorkRevision || e.SessionKind != g.SessionKind || e.SessionID != g.SessionID {
				return errors.New("resume_session_mismatch")
			}
			path := filepath.Join(h.spoolRoot, e.AttemptID, e.SegmentID)
			if err = continuationOwnedDirectory(path, 0700); err != nil {
				return err
			}
			origin, err := readContinuationOrigin(filepath.Join(path, "continuation-origin.json"))
			if err != nil {
				return err
			}
			if origin.RunID != g.RunID || origin.TaskID != g.TaskID || origin.AttemptID != g.AttemptID || origin.SegmentID != e.SegmentID || origin.WorkRevision != g.WorkRevision || origin.PlanRevision != g.PlanRevision || origin.Provider != p.Provider || origin.AdapterPayloadSHA256 != c.AdapterPayloadSHA256 || origin.OwnerContextID != c.OwnerContextID {
				return errors.New("continuation_origin_changed")
			}
			foundInit = true
		}
	}
	if !foundInit {
		return errors.New("resume_session_unbound")
	}
	dir := filepath.Join(h.spoolRoot, g.AttemptID, c.SourceSegmentID)
	if err = continuationOwnedDirectory(dir, 0700); err != nil {
		return err
	}
	spool, err := events.Open(dir)
	if err != nil {
		return err
	}
	records, err := spool.Read()
	if err != nil {
		return err
	}
	init, exited, source, stopped := false, false, false, false
	for _, e := range records {
		if e.ProducerID != h.producerID || e.RunID != g.RunID || e.TaskID != g.TaskID || e.AttemptID != g.AttemptID || e.SegmentID != c.SourceSegmentID || e.WorkRevision != g.WorkRevision {
			return errors.New("continuation_source_changed")
		}
		switch e.Kind {
		case contract.EventUnknown:
			return errors.New("continuation_source_unknown")
		case contract.EventSession:
			if e.SessionKind != g.SessionKind || e.SessionID != g.SessionID {
				return errors.New("resume_session_mismatch")
			}
			init = true
		case contract.EventExited:
			if e.Process != nil && e.Process.PID > 0 && e.Process.Birth != "" {
				exited = true
			}
		case contract.EventStopped:
			if e.Process != nil {
				stopped = true
			}
		}
		if e.EventID == c.SourceEventID {
			if c.SourceKind != "host" || e.Sequence != c.SourceEventRevision || e.PayloadHash != c.SourceEventSHA256 {
				return errors.New("continuation_source_changed")
			}
			if c.Mode == contract.ContinuationQuestion {
				source = e.Kind == contract.EventQuestion && e.QuestionID == g.QuestionID && e.QuestionRevision == g.QuestionRevision && e.SessionKind == g.SessionKind && e.SessionID == g.SessionID && e.QuestionKind == "technical"
			} else {
				source = e.Kind == contract.EventStopped
			}
		}
	}
	if !init || !exited {
		return errors.New("continuation_source_not_exited")
	}
	if c.SourceKind == "host" && !source {
		return errors.New("continuation_source_missing")
	}
	if c.SourceKind == "report" {
		if c.Mode != contract.ContinuationQuestion || !stopped {
			return errors.New("continuation_report_source_invalid")
		}
		for _, e := range records {
			if e.Kind == contract.EventQuestion {
				return errors.New("continuation_report_source_invalid")
			}
		}
	}
	return nil
}
