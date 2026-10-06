package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
)

func parseDAGRework(raw string) (dagReworkDecision, error) {
	var s dagReworkDecision
	if strictDecisionJSON(raw, &s) != nil || s.Spec.Version != 1 || s.Receipt.Version != 1 || !validDigest(s.Receipt.ClosureSHA256) || s.Receipt.ClosureSHA256 != s.digest() {
		return s, ErrConflict
	}
	p, r, c := s.Spec, s.Receipt, s.Context
	if p.CommandID != "" || p.WorkRevision < 1 || p.WorkRevision >= 2147483647 || p.ReviewWorkRevision < 1 || p.ReviewWorkRevision >= 2147483647 || p.TaskID == "" || p.ReviewTaskID == "" || p.ActionSlot == "" || !reworkOID(p.CandidateOID) || strings.TrimSpace(p.Feedback) == "" || strings.TrimSpace(p.Acceptance) == "" || len(p.Feedback) > 16*1024 || len(p.Acceptance) > 16*1024 || strings.ContainsRune(p.Feedback+p.Acceptance, 0) || r.Status != "rework_queued" || r.TaskID != p.TaskID || r.WorkRevision != p.WorkRevision+1 || r.CandidateOID != p.CandidateOID || r.ReviewEventID != p.Review.EventID || r.RevisionSHA256 != c.Digest() || c.Version != 1 || c.TaskID != p.TaskID || c.RunID == "" || c.PreviousWorkRevision != p.WorkRevision || c.PreviousCandidateOID != p.CandidateOID || c.CandidateEventID != p.Candidate.EventID || c.CandidateSHA256 != p.Candidate.EventHash || c.ReviewEventID != p.Review.EventID || c.ReviewSHA256 != p.Review.EventHash || c.Feedback != p.Feedback || c.Acceptance != p.Acceptance || len(s.Nodes) < 3 || len(s.Nodes) > 64 || len(r.TaskRevisions) != len(s.Nodes) || len(s.Evidence) > 128 {
		return s, ErrConflict
	}
	seen := map[string]bool{}
	targets := 0
	for _, n := range s.Nodes {
		if seen[n.TaskID] || n.TaskID == "" || n.Revision < 1 || n.Revision >= 2147483647 || r.TaskRevisions[n.TaskID] != n.Revision+1 {
			return s, ErrConflict
		}
		seen[n.TaskID] = true
		p, err := validateDAGReworkPayload(n.Original, n.TaskID == s.Spec.TaskID)
		if err != nil {
			return s, err
		}
		revised, err := revisedDAGAdapter(n.Original, n.TaskID, n.Revision, c)
		if err != nil || !sameJSON(json.RawMessage(revised), n.Revised) {
			return s, ErrConflict
		}
		if n.TaskID == s.Spec.TaskID && (n.Revision != s.Spec.WorkRevision || len(n.Dependencies) != 0) {
			return s, ErrConflict
		}
		if n.TaskID == s.Spec.ReviewTaskID && n.Revision != s.Spec.ReviewWorkRevision {
			return s, ErrConflict
		}
		if p.CandidateAction != nil {
			targets++
			if _, ok := s.Targets[n.TaskID]; !ok {
				return s, ErrConflict
			}
		}
		if evidence, ok := s.Evidence[n.TaskID]; ok && (evidence.Revision != n.Revision || !sameDeps(evidence.Dependencies, n.Dependencies) || !sameJSON(evidence.Adapter, n.Original)) {
			return s, ErrConflict
		}
	}
	if !seen[s.Spec.TaskID] || !seen[s.Spec.ReviewTaskID] || len(s.Targets) != targets {
		return s, ErrConflict
	}
	return s, nil
}

func dagReworkDecisionTargets(ctx context.Context, tx *sql.Tx, r attemptDecisionRecord) ([]decisionTaskScope, error) {
	saved, err := parseDAGRework(r.payload)
	if err != nil {
		return nil, err
	}
	spec := saved.Spec
	if r.task != spec.TaskID || r.revision != spec.WorkRevision || r.event != spec.Review.EventID || r.eventRevision != spec.Review.EventRevision || r.hash != spec.Review.EventHash || r.slot != spec.ActionSlot || r.fallback {
		return nil, ErrConflict
	}
	if _, err = decisionSource(ctx, tx, r, spec.ReviewTaskID, spec.ReviewWorkRevision, false); err != nil {
		return nil, err
	}
	all, raw, run, plan, err := loadDAGReworkTasks(ctx, tx, spec.TaskID)
	if err != nil || run != saved.Context.RunID {
		return nil, ErrConflict
	}
	chain, err := dagClosure(all, spec.TaskID)
	if err != nil || len(chain) != len(saved.Nodes) {
		return nil, ErrConflict
	}
	scopes := make([]decisionTaskScope, 0, len(chain))
	members := map[string]bool{}
	for i, t := range chain {
		n := saved.Nodes[i]
		if n.TaskID != t.id || t.revision != n.Revision+1 || string(raw[t.id]) != string(n.Revised) || !sameDeps(t.dependencies, n.Dependencies) {
			return nil, ErrConflict
		}
		var revision, retired int
		var hold int64
		var attempt sql.NullString
		if tx.QueryRowContext(ctx, `SELECT target_work_revision,reserved_active_ms,attempt_id,retired FROM decision_attempt_bindings WHERE action_slot=? AND task_id=?`, r.slot, t.id).Scan(&revision, &hold, &attempt, &retired) != nil || revision != t.revision || hold <= 0 {
			return nil, ErrConflict
		}
		// Retired history is valid evidence, but can never authorize a new claim.
		if retired != 0 && attempt.Valid {
			return nil, ErrConflict
		}
		scopes = append(scopes, decisionTaskScope{id: t.id, run: run, status: t.status, payload: string(raw[t.id]), revision: t.revision})
		members[t.id] = true
	}
	artifacts, err := validateDAGEvidence(ctx, tx, saved, plan)
	if err != nil {
		return nil, err
	}
	boundary := map[string]bool{}
	for _, n := range saved.Nodes {
		for _, dep := range n.Dependencies {
			if !members[dep] {
				boundary[dep] = true
			}
		}
		p, _ := adapter.DecodeInvocationPayload(n.Original)
		if p.CandidateAction == nil {
			continue
		}
		a, ok := artifacts[n.TaskID]
		if !ok {
			reviewNode := all[saved.Spec.ReviewTaskID]
			if reviewNode == nil || reviewNode.action == nil || !reflect.DeepEqual(p.CandidateAction.Target, reviewNode.action.Target) {
				return nil, ErrConflict
			}
			a = artifacts[saved.Spec.ReviewTaskID]
		}
		target, err := targetFromArtifact(a, p.CandidateAction.Target, plan)
		if err != nil || !sameJSON(target, saved.Targets[n.TaskID]) {
			return nil, ErrConflict
		}
	}
	if len(boundary) != len(saved.Boundary) {
		return nil, ErrConflict
	}
	for id, old := range saved.Boundary {
		if !boundary[id] {
			return nil, ErrConflict
		}
		current, err := acceptedCandidateInput(ctx, tx, run, id)
		if err != nil || !sameJSON(current, old) {
			return nil, CodeError("rework_boundary_changed")
		}
	}
	return scopes, nil
}

// Discovery is by exact run and explicit membership, not the single content
// SourceTask chain. This guard cannot repair even one missing v1 binding.
func dagDecisionForTask(ctx context.Context, tx *sql.Tx, task string, revision int) (*attemptDecisionRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.action_slot,d.revision_payload FROM retry_decisions d JOIN tasks root ON root.id=d.task_id WHERE root.run_id=(SELECT run_id FROM tasks WHERE id=?) AND d.revision_payload!=''`, task)
	if err != nil {
		return nil, err
	}
	var slots []string
	for rows.Next() {
		var slot, raw string
		if err = rows.Scan(&slot, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		kind, e := decisionRecordKind(attemptDecisionRecord{payload: raw})
		if e != nil {
			rows.Close()
			return nil, e
		}
		if kind != dagReworkKind {
			continue
		}
		s, e := parseDAGRework(raw)
		if e != nil {
			rows.Close()
			return nil, e
		}
		if s.Receipt.TaskRevisions[task] == revision {
			slots = append(slots, slot)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(slots) > 1 {
		return nil, ErrConflict
	}
	if len(slots) == 0 {
		return nil, nil
	}
	r, err := readAttemptDecision(ctx, tx, slots[0])
	return &r, err
}

func guardDAGBinding(ctx context.Context, tx *sql.Tx, task string, revision int, attempt string) (bool, error) {
	r, err := dagDecisionForTask(ctx, tx, task, revision)
	if err != nil || r == nil {
		return false, err
	}
	if _, err = dagReworkDecisionTargets(ctx, tx, *r); err != nil {
		return true, err
	}
	var saved sql.NullString
	var retired int
	if tx.QueryRowContext(ctx, `SELECT attempt_id,retired FROM decision_attempt_bindings WHERE action_slot=? AND task_id=? AND target_work_revision=?`, r.slot, task, revision).Scan(&saved, &retired) != nil || retired != 0 || saved.String != attempt || saved.Valid != (attempt != "") {
		return true, ErrConflict
	}
	return true, nil
}
