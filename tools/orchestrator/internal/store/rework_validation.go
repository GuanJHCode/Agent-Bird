package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

type reworkTask struct {
	id, status, group, hostLaunch, completion, fallbacks string
	revision, maxAttempts                                int
	dependencies                                         []string
	payload                                              map[string]json.RawMessage
	kind, provider, directory, prompt                    string
	workspace                                            *adapter.CandidateWorkspace
	action                                               *adapter.CandidateAction
	profile                                              *adapter.ExecutionProfile
}

type reworkArtifact struct {
	Stage            string                  `json:"stage"`
	Binding          gitops.CandidateBinding `json:"binding"`
	Candidate        gitops.CandidateReceipt `json:"candidate"`
	InputEventID     string                  `json:"input_event_id"`
	InputSHA256      string                  `json:"input_sha256"`
	ValidationDigest string                  `json:"validation_digest"`
	Validation       *struct {
		Passed bool `json:"passed"`
	} `json:"validation"`
	Review *struct {
		Decision string `json:"decision"`
		Summary  string `json:"summary"`
	} `json:"review"`
}

func readReworkEvent(ctx context.Context, tx *sql.Tx, task *reworkTask, expected ReworkEvent, accepted bool) (contract.Event, reworkArtifact, error) {
	var e contract.Event
	var artifact reworkArtifact
	var body, slot, status, outcome, decision string
	var rev int64
	err := tx.QueryRowContext(ctx, `SELECT e.body_json,e.event_revision,e.action_slot,s.status,s.outcome,COALESCE(d.decision,'') FROM runtime_events e JOIN segment_runtime s ON s.segment_id=e.segment_id AND s.host_id=e.producer_id LEFT JOIN review_decisions d ON d.event_id=e.event_id AND d.event_revision=e.event_revision AND d.event_hash=e.payload_hash WHERE e.event_id=? AND e.task_id=? AND e.payload_hash=? AND e.segment_id=(SELECT z.id FROM segments z JOIN attempts a ON a.id=z.attempt_id WHERE a.task_id=e.task_id ORDER BY a.attempt_no DESC,z.segment_no DESC LIMIT 1)`, expected.EventID, task.id, expected.EventHash).Scan(&body, &rev, &slot, &status, &outcome, &decision)
	if err != nil || rev != expected.EventRevision || status != "exited" || json.Unmarshal([]byte(body), &e) != nil || e.WorkRevision != task.revision || e.TaskID != task.id || e.PayloadHash != expected.EventHash {
		return e, artifact, CodeError("rework_evidence_stale")
	}
	if accepted {
		if e.Kind != contract.EventResult || outcome != "result" || decision != "accept" {
			return e, artifact, CodeError("rework_evidence_not_accepted")
		}
	} else if !((e.Kind == contract.EventFailed && outcome == "failed") || (e.Kind == contract.EventResult && outcome == "result" && decision == "reject")) {
		return e, artifact, CodeError("rework_review_not_rejected")
	}
	e.EventRevision, e.ActionSlot = rev, slot
	ref := e.Artifact
	if ref == nil || !filepath.IsAbs(ref.Path) || ref.Size < 1 || ref.Size > 1024*1024 || ref.SHA256 != e.PayloadHash {
		return e, artifact, CodeError("rework_artifact_invalid")
	}
	f, err := os.OpenFile(ref.Path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return e, artifact, CodeError("rework_artifact_invalid")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != ref.Size {
		return e, artifact, CodeError("rework_artifact_invalid")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || int64(len(data)) != ref.Size || runtimeHash(string(data)) != ref.SHA256 || json.Unmarshal(data, &artifact) != nil {
		return e, artifact, CodeError("rework_artifact_invalid")
	}
	return e, artifact, nil
}

func loadReworkChain(ctx context.Context, tx *sql.Tx, taskID, reviewID string) ([]*reworkTask, string, int, error) {
	var run string
	var plan int
	if err := tx.QueryRowContext(ctx, `SELECT t.run_id,r.plan_revision FROM tasks t JOIN runs r ON r.id=t.run_id WHERE t.id=?`, taskID).Scan(&run, &plan); err != nil {
		return nil, "", 0, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.status,t.dependencies_json,t.max_attempts,r.work_revision,r.budget_group_id,r.host_launch_id,r.completion_policy,r.fallback_payloads,r.adapter_payload FROM tasks t JOIN task_runtime r ON r.task_id=t.id WHERE t.run_id=?`, run)
	if err != nil {
		return nil, "", 0, err
	}
	all := map[string]*reworkTask{}
	for rows.Next() {
		v := &reworkTask{}
		var deps, payload string
		if err = rows.Scan(&v.id, &v.status, &deps, &v.maxAttempts, &v.revision, &v.group, &v.hostLaunch, &v.completion, &v.fallbacks, &payload); err != nil {
			rows.Close()
			return nil, "", 0, err
		}
		if json.Unmarshal([]byte(deps), &v.dependencies) != nil || json.Unmarshal([]byte(payload), &v.payload) != nil {
			rows.Close()
			return nil, "", 0, ErrConflict
		}
		for key, dest := range map[string]any{"kind": &v.kind, "provider": &v.provider, "directory": &v.directory, "prompt": &v.prompt, "candidate_workspace": &v.workspace, "candidate_action": &v.action, "profile": &v.profile} {
			if raw, ok := v.payload[key]; ok && json.Unmarshal(raw, dest) != nil {
				rows.Close()
				return nil, "", 0, ErrConflict
			}
		}
		all[v.id] = v
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", 0, err
	}
	edit, review := all[taskID], all[reviewID]
	if edit == nil || review == nil || edit.status != "completed" || edit.kind != "" || edit.provider != "claude-code" || edit.workspace == nil || edit.profile == nil || edit.profile.Role != adapter.Implementer || edit.profile.Permission != adapter.WorkspaceWrite || len(edit.dependencies) != 0 || review.status != "failed" || review.provider != "claude-code" || review.action == nil || review.action.Operation != "review" || review.profile == nil || review.profile.Role != adapter.Reviewer || review.profile.Permission != adapter.ReadOnly {
		return nil, "", 0, CodeError("rework_chain_unsupported")
	}
	validate := all[review.action.SourceTask]
	if validate == nil || validate.status != "completed" || validate.kind != "candidate" || validate.action == nil || validate.action.Operation != "validate" || validate.action.SourceTask != edit.id {
		return nil, "", 0, CodeError("rework_chain_unsupported")
	}
	var integrate *reworkTask
	for _, v := range all {
		if v.action != nil && v.action.Operation == "integrate" && v.action.SourceTask == review.id {
			if integrate != nil {
				return nil, "", 0, CodeError("rework_chain_unsupported")
			}
			integrate = v
		}
	}
	if integrate == nil || integrate.kind != "candidate" || (integrate.status != "queued" && integrate.status != "blocked_dependency") {
		return nil, "", 0, CodeError("rework_chain_unsupported")
	}
	chain := []*reworkTask{edit, validate, review, integrate}
	ids := map[string]bool{}
	for i, v := range chain {
		var fallbacks []json.RawMessage
		if len(run) > 128 || len(v.id) > 128 || len(v.group) > 128 || v.revision < 1 || v.revision >= 2147483647 || v.completion != "owner_review" || json.Unmarshal([]byte(v.fallbacks), &fallbacks) != nil || len(fallbacks) != 0 || !filepath.IsAbs(v.directory) || filepath.Clean(v.directory) != v.directory {
			return nil, "", 0, CodeError("rework_chain_unsupported")
		}
		if i > 0 && (v.action.Version != 1 || len(v.dependencies) != 1 || v.dependencies[0] != chain[i-1].id || !reflect.DeepEqual(v.action.Target, validate.action.Target)) {
			return nil, "", 0, CodeError("rework_chain_unsupported")
		}
		ids[v.id] = true
	}
	for _, v := range all {
		if ids[v.id] {
			continue
		}
		for _, dep := range v.dependencies {
			if ids[dep] {
				return nil, "", 0, CodeError("rework_chain_unsupported")
			}
		}
	}
	return chain, run, plan, nil
}

func reworkBudgetsAndLifecycle(ctx context.Context, tx *sql.Tx, chain []*reworkTask) error {
	groupNeeds := map[string]int{}
	groupLimits := map[string]int{}
	for _, v := range chain {
		var active, attempts, ready, questions int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id WHERE a.task_id=? AND s.status IN ('launch_requested','prepared','spawned','running','stopping','unknown')`, v.id).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return CodeError("rework_active_segment")
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runtime_hosts WHERE launch_id=? AND status='ready'`, v.hostLaunch).Scan(&ready); err != nil {
			return err
		}
		if ready != 1 {
			return CodeError("rework_host_not_ready")
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE task_id=?`, v.id).Scan(&attempts); err != nil {
			return err
		}
		if v.action != nil && v.action.Operation == "integrate" && attempts != 0 {
			return CodeError("rework_integration_started")
		}
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM runtime_questions WHERE task_id=? AND status IN ('open','resume_queued','resume_started'))+(SELECT COUNT(*) FROM report_events e JOIN report_capabilities c ON c.capability_id=e.capability_id WHERE c.task_id=? AND e.kind='question' AND e.delivery_status='pending_session')`, v.id, v.id).Scan(&questions); err != nil {
			return err
		}
		if questions != 0 {
			return ErrConflict
		}
		groupNeeds[v.group]++
		limit := v.maxAttempts
		if limit > 3 {
			limit = 3
		}
		if old, ok := groupLimits[v.group]; !ok || limit < old {
			groupLimits[v.group] = limit
		}
	}
	for group, need := range groupNeeds {
		var attempts int
		var charged int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts a JOIN task_runtime r ON r.task_id=a.task_id WHERE r.budget_group_id=?`, group).Scan(&attempts); err != nil {
			return err
		}
		if attempts+need > groupLimits[group] {
			return ErrAttemptLimit
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status='settled' THEN used_ms ELSE granted_ms END),0) FROM budget_runtime WHERE budget_group_id=?`, group).Scan(&charged); err != nil {
			return err
		}
		if charged >= defaultGroupActiveMS {
			return CodeError("budget_exhausted")
		}
	}
	return nil
}

func reworkBindingMatches(binding *gitops.CandidateBinding, e contract.Event, plan int) bool {
	return binding != nil && binding.RunID == e.RunID && binding.TaskID == e.TaskID && binding.AttemptID == e.AttemptID && binding.SegmentID == e.SegmentID && binding.WorkRevision == e.WorkRevision && binding.PlanRevision == plan
}
func reworkOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func rejectSubmittedRework(payload json.RawMessage) error {
	if len(payload) == 0 {
		return nil
	}
	var p struct {
		Workspace *struct {
			Rework json.RawMessage `json:"rework"`
		} `json:"candidate_workspace"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return ErrInvalidDAG
	}
	if p.Workspace != nil && len(p.Workspace.Rework) > 0 && string(p.Workspace.Rework) != "null" {
		return CodeError("rework_requires_owner_decision")
	}
	return nil
}
