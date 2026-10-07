package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

func reworkEventTuple(e contract.Event) ReworkEvent {
	return ReworkEvent{e.EventID, e.EventRevision, e.PayloadHash}
}

// Capture only current exact accepted inputs at admission. Validation thereafter
// uses these old revisions and segments, never the new revision's latest result.
func captureDAGEvidence(ctx context.Context, tx *sql.Tx, s *dagReworkDecision, all map[string]*reworkTask, raw map[string]json.RawMessage, task string, expected ReworkEvent, accepted bool) error {
	if old, ok := s.Evidence[task]; ok {
		if old.Event != expected || old.Accepted != accepted {
			return ErrConflict
		}
		return nil
	}
	t := all[task]
	if t == nil || len(s.Evidence) >= 128 {
		return ErrConflict
	}
	e, a, err := readReworkEvent(ctx, tx, t, expected, accepted)
	if err != nil {
		return err
	}
	proof := dagReworkEvidence{TaskID: task, Revision: t.revision, Dependencies: append([]string{}, t.dependencies...), Adapter: raw[task], Event: expected, Accepted: accepted}
	if t.action != nil && t.action.DependencyInputVersion == 1 {
		proof.Capture, err = loadDependencyInputs(ctx, tx, e.SegmentID)
		if err != nil {
			return err
		}
	} else if len(t.dependencies) > 1 {
		return CodeError("rework_historical_capture_required")
	}
	s.Evidence[task] = proof
	var inputs []contract.AcceptedInput
	if proof.Capture != nil {
		inputs = proof.Capture.Inputs
	} else if t.action != nil {
		input, err := acceptedCandidateInput(ctx, tx, e.RunID, t.action.SourceTask)
		if err != nil {
			return err
		}
		if input.Event.EventID != a.InputEventID || input.Event.PayloadHash != a.InputSHA256 {
			return ErrConflict
		}
		inputs = []contract.AcceptedInput{input}
	}
	for _, input := range inputs {
		current, err := acceptedCandidateInput(ctx, tx, e.RunID, input.Event.TaskID)
		if err != nil || !sameJSON(current, input) {
			return ErrConflict
		}
		if err = captureDAGEvidence(ctx, tx, s, all, raw, input.Event.TaskID, reworkEventTuple(input.Event), true); err != nil {
			return err
		}
	}
	return nil
}

func historicalDAGInput(ctx context.Context, tx *sql.Tx, p dagReworkEvidence, e contract.Event) (contract.AcceptedInput, error) {
	var command string
	if !p.Accepted {
		return contract.AcceptedInput{}, ErrConflict
	}
	err := tx.QueryRowContext(ctx, `SELECT command_id FROM review_decisions WHERE task_id=? AND event_id=? AND event_revision=? AND event_hash=? AND decision='accept'`, p.TaskID, p.Event.EventID, p.Event.EventRevision, p.Event.EventHash).Scan(&command)
	return contract.AcceptedInput{Event: e, AdapterPayload: p.Adapter, DecisionCommandID: command}, err
}

func validateDAGEvidence(ctx context.Context, tx *sql.Tx, s dagReworkDecision, plan int) (map[string]reworkArtifact, error) {
	events := map[string]contract.Event{}
	artifacts := map[string]reworkArtifact{}
	parsed := map[string]adapter.InvocationPayload{}
	for id, p := range s.Evidence {
		if id != p.TaskID || p.Revision < 1 {
			return nil, ErrConflict
		}
		e, a, err := readReworkEventScope(ctx, tx, &reworkTask{id: id, revision: p.Revision}, p.Event, p.Accepted, false)
		if err != nil || e.RunID != s.Context.RunID {
			return nil, ErrConflict
		}
		var settled string
		if tx.QueryRowContext(ctx, `SELECT b.status FROM segment_runtime r JOIN budget_runtime b ON b.id=r.reservation_id AND b.segment_id=r.segment_id WHERE r.segment_id=?`, e.SegmentID).Scan(&settled) != nil || settled != "settled" {
			return nil, ErrConflict
		}
		pp, err := adapter.DecodeInvocationPayload(p.Adapter)
		if err != nil {
			return nil, err
		}
		if pp.CandidateWorkspace != nil {
			if a.Stage != "" || !reworkBindingMatches(a.Candidate.Binding, e, plan) || a.Candidate.BaseOID != pp.CandidateWorkspace.BaseOID || a.Candidate.RepoRoot != pp.CandidateWorkspace.RepoRoot || !reworkOID(a.Candidate.CandidateOID) {
				return nil, ErrConflict
			}
		} else if pp.CandidateAction == nil || a.Stage != pp.CandidateAction.Operation || !reworkBindingMatches(&a.Binding, e, plan) {
			return nil, ErrConflict
		}
		events[id] = e
		artifacts[id] = a
		parsed[id] = pp
	}
	edges := map[string][]string{}
	for id, p := range s.Evidence {
		pp, a, e := parsed[id], artifacts[id], events[id]
		if pp.CandidateAction == nil {
			if len(p.Dependencies) != 0 || p.Capture != nil {
				return nil, ErrConflict
			}
			continue
		}
		action := pp.CandidateAction
		source, ok := s.Evidence[action.SourceTask]
		se := events[action.SourceTask]
		sa := artifacts[action.SourceTask]
		if !ok || !source.Accepted || !slices.Contains(p.Dependencies, action.SourceTask) || a.InputEventID != se.EventID || a.InputSHA256 != se.PayloadHash || !reflect.DeepEqual(a.Candidate, sa.Candidate) {
			return nil, ErrConflict
		}
		if a.Stage == "validate" {
			if a.Validation == nil || !a.Validation.Passed || !validDigest(a.ValidationDigest) || sa.Stage != "" {
				return nil, ErrConflict
			}
		}
		if a.Stage == "review" {
			if sa.Stage != "validate" || sa.Validation == nil || !sa.Validation.Passed || a.ValidationDigest != sa.ValidationDigest || a.Review == nil || (a.Review.Decision != "approve" && a.Review.Decision != "reject") {
				return nil, ErrConflict
			}
		}
		if _, err := targetFromArtifact(a, action.Target, plan); err != nil {
			return nil, err
		}
		if action.DependencyInputVersion == 1 {
			capture, err := loadDependencyInputs(ctx, tx, e.SegmentID)
			if err != nil || !sameJSON(capture, p.Capture) || capture == nil || len(capture.Inputs) != len(p.Dependencies) {
				return nil, ErrConflict
			}
			input, err := historicalDAGInput(ctx, tx, source, se)
			if err != nil {
				return nil, err
			}
			var intent, slotToken string
			if tx.QueryRowContext(ctx, `SELECT launch_intent_hash,slot_token FROM segment_runtime WHERE segment_id=?`, e.SegmentID).Scan(&intent, &slotToken) != nil {
				return nil, ErrConflict
			}
			c := contract.LaunchCommand{CommandID: e.CommandID, HostID: e.ProducerID, RunID: e.RunID, TaskID: e.TaskID, AttemptID: e.AttemptID, SegmentID: e.SegmentID, WorkRevision: e.WorkRevision, PlanRevision: plan, AdapterPayload: p.Adapter, Input: &input, DependencyInputs: capture, LaunchIntentHash: intent, SlotToken: slotToken}
			// This reconstructs immutable historical evidence only. It neither
			// dispatches a grant nor creates authority for a legacy nil scope.
			if bindExistingDiagnostics(ctx, tx, &c) != nil || bindHostArtifacts(ctx, tx, &c, false) != nil {
				return nil, ErrConflict
			}
			if contract.ValidateDependencyInputs(c) != nil {
				return nil, ErrConflict
			}
			for _, captured := range capture.Inputs {
				other, ok := s.Evidence[captured.Event.TaskID]
				if !ok || !slices.Contains(p.Dependencies, other.TaskID) {
					return nil, ErrConflict
				}
				exact, err := historicalDAGInput(ctx, tx, other, events[other.TaskID])
				if err != nil || !sameJSON(exact, captured) {
					return nil, ErrConflict
				}
				edges[id] = append(edges[id], other.TaskID)
			}
		} else {
			if len(p.Dependencies) != 1 || p.Capture != nil {
				return nil, CodeError("rework_historical_capture_required")
			}
			edges[id] = []string{action.SourceTask}
		}
	}
	root, rootOK := s.Evidence[s.Spec.TaskID]
	review, reviewOK := s.Evidence[s.Spec.ReviewTaskID]
	if !rootOK || !reviewOK || root.Event != s.Spec.Candidate || review.Event != s.Spec.Review || !root.Accepted || review.Accepted || root.Revision != s.Spec.WorkRevision || review.Revision != s.Spec.ReviewWorkRevision {
		return nil, ErrConflict
	}
	old, feedback := artifacts[root.TaskID], artifacts[review.TaskID]
	if old.Candidate.CandidateOID != s.Spec.CandidateOID || feedback.Stage != "review" || feedback.Review == nil || strings.TrimSpace(feedback.Review.Summary) == "" || len(feedback.Review.Summary) > 64*1024 || feedback.Review.Summary != s.Context.ReviewSummary {
		return nil, ErrConflict
	}
	if events[review.TaskID].Kind == contract.EventFailed && feedback.Review.Decision != "reject" {
		return nil, ErrConflict
	}
	seen := map[string]bool{}
	var reaches func(string) bool
	reaches = func(id string) bool {
		if id == root.TaskID {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		for _, dep := range edges[id] {
			if reaches(dep) {
				return true
			}
		}
		return false
	}
	if !reaches(review.TaskID) {
		return nil, CodeError("rework_root_not_in_review_evidence")
	}
	return artifacts, nil
}

func gatherDAGEvidence(ctx context.Context, tx *sql.Tx, s *dagReworkDecision, chain []*reworkTask, all map[string]*reworkTask, raw map[string]json.RawMessage) error {
	if err := captureDAGEvidence(ctx, tx, s, all, raw, s.Spec.TaskID, s.Spec.Candidate, true); err != nil {
		return err
	}
	if err := captureDAGEvidence(ctx, tx, s, all, raw, s.Spec.ReviewTaskID, s.Spec.Review, false); err != nil {
		return err
	}
	members := map[string]bool{}
	for _, t := range chain {
		members[t.id] = true
	}
	for _, t := range chain {
		for _, dep := range t.dependencies {
			if !members[dep] {
				input, err := acceptedCandidateInput(ctx, tx, s.Context.RunID, dep)
				if err != nil {
					return err
				}
				s.Boundary[dep] = input
				if err = captureDAGEvidence(ctx, tx, s, all, raw, dep, reworkEventTuple(input.Event), true); err != nil {
					return err
				}
			}
		}
		if _, ok := s.Evidence[t.id]; ok {
			continue
		}
		var count int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE task_id=?`, t.id).Scan(&count) != nil {
			return ErrConflict
		}
		if count == 0 {
			continue
		}
		// A previously executed descendant needs a complete immutable Host artifact.
		var tuple ReworkEvent
		var kind string
		err := tx.QueryRowContext(ctx, `SELECT e.event_id,e.event_revision,e.payload_hash,json_extract(e.body_json,'$.kind') FROM runtime_events e WHERE e.task_id=? AND json_extract(e.body_json,'$.kind') IN ('result','failed') AND e.segment_id=(SELECT z.id FROM segments z JOIN attempts a ON a.id=z.attempt_id WHERE a.task_id=e.task_id ORDER BY a.attempt_no DESC,z.segment_no DESC LIMIT 1) ORDER BY e.sequence DESC LIMIT 1`, t.id).Scan(&tuple.EventID, &tuple.EventRevision, &tuple.EventHash, &kind)
		if err != nil {
			return CodeError("rework_descendant_evidence_missing")
		}
		if err = captureDAGEvidence(ctx, tx, s, all, raw, t.id, tuple, t.status == "completed"); err != nil {
			return err
		}
	}
	return nil
}
