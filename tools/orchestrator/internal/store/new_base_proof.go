package store

import (
	"context"

	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
)

type newBaseValidation struct {
	CandidateOID     string                  `json:"candidate_oid"`
	TreeOID          string                  `json:"tree_oid"`
	Binding          gitops.CandidateBinding `json:"binding"`
	Command          []string                `json:"command"`
	ExecutableSHA256 string                  `json:"executable_sha256"`
	ExitCode         int                     `json:"exit_code"`
	OutputSHA256     string                  `json:"output_sha256"`
	Passed           bool                    `json:"passed"`
}
type newBaseReview struct {
	Decision string `json:"decision"`
	Summary  string `json:"summary"`
}
type newBaseDelivery struct {
	Version                int                         `json:"version"`
	Stage                  string                      `json:"stage"`
	Binding                gitops.CandidateBinding     `json:"binding"`
	Candidate              gitops.CandidateReceipt     `json:"candidate"`
	CandidateReceiptSHA256 string                      `json:"candidate_receipt_sha256,omitempty"`
	InputEventID           string                      `json:"input_event_id"`
	InputSHA256            string                      `json:"input_sha256"`
	OwnerDecisionID        string                      `json:"owner_decision_id"`
	Validation             *newBaseValidation          `json:"validation,omitempty"`
	ValidationDigest       string                      `json:"validation_digest,omitempty"`
	Review                 *newBaseReview              `json:"review,omitempty"`
	ProviderLock           *adapter.ProviderLock       `json:"provider_lock,omitempty"`
	Target                 gitops.ReviewBinding        `json:"target"`
	Integration            *gitops.IntegrationOutcome  `json:"integration,omitempty"`
	Composition            *gitops.CompositionManifest `json:"composition,omitempty"`
}
type newBaseReservationGate struct {
	Target dagReworkTarget
	Facts  string
}

type newBaseSource struct {
	reservations map[string]newBaseReservationGate
	run          string
	plan         int
	tasks        map[string]*reworkTask
	payloads     map[string]json.RawMessage
	inputs       map[string]contract.AcceptedInput
	bodies       map[string][]byte
}

func authorizeNewBase(ctx context.Context, tx *sql.Tx, p PlanSpec) (string, error) {
	if p.NewBaseAuthorization == nil {
		return "", CodeError("owner_mismatch")
	}
	var run, controller, token string
	err := tx.QueryRowContext(ctx, `SELECT t.run_id,r.controller_thread,c.token_hash FROM tasks t JOIN runs r ON r.id=t.run_id JOIN run_control c ON c.run_id=r.id WHERE t.id=?`, p.NewBaseRework.SourceIntegrationTaskID).Scan(&run, &controller, &token)
	if err != nil || controller != p.NewBaseAuthorization.ControllerThread || controller != p.Run.ControllerThread || subtle.ConstantTimeCompare([]byte(token), []byte(runtimeHash(p.NewBaseAuthorization.ControlToken))) != 1 {
		return "", CodeError("owner_mismatch")
	}
	if err = requirePublishedOwnerTx(ctx, tx, run); err != nil {
		return "", err
	}
	return run, nil
}

// Only this read-only gate touches artifact files. The write transaction repeats
// authorization, exact admitted payloads, current events and immutable ref checks.
func (d *DB) prepareNewBaseSource(ctx context.Context, p PlanSpec) (*newBaseSource, error) {
	s := p.NewBaseRework
	if s == nil {
		return nil, nil
	}
	if s.Version != 1 || s.SourceIntegrationTaskID == "" || len(s.SourceIntegrationTaskID) > 128 || s.SourceEvent.EventID == "" || s.SourceEvent.EventRevision < 1 || !validDigest(s.SourceEvent.EventHash) || len(s.TaskMap) < 4 || len(s.TaskMap) > 64 || strings.TrimSpace(s.Feedback) == "" || strings.TrimSpace(s.Acceptance) == "" || len(s.Feedback) > 16*1024 || len(s.Acceptance) > 16*1024 || strings.ContainsRune(s.Feedback+s.Acceptance, 0) || s.CommandID == "" || len(s.CommandID) > 128 || strings.ContainsRune(s.CommandID, 0) || p.LaunchID == "" || p.LaunchToken == "" || p.ControlToken == "" {
		return nil, CodeError("new_base_request_invalid")
	}
	tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	run, err := authorizeNewBase(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	all, raw, loaded, plan, err := loadDAGReworkTasks(ctx, tx, s.SourceIntegrationTaskID)
	if err != nil || loaded != run {
		return nil, CodeError("new_base_source_invalid")
	}
	gate := &newBaseSource{run: run, plan: plan, tasks: all, payloads: raw, inputs: map[string]contract.AcceptedInput{}, bodies: map[string][]byte{}}
	if err = validateNewBaseShape(gate, s); err != nil {
		return nil, err
	}
	for id := range all {
		input, e := acceptedCandidateInput(ctx, tx, run, id)
		if e != nil {
			return nil, e
		}
		if id == s.SourceIntegrationTaskID && reworkEventTuple(input.Event) != s.SourceEvent {
			return nil, ErrConflict
		}
		body, e := readCommittedHostArtifact(ctx, tx, input.Event, 1024*1024)
		if e != nil {
			return nil, e
		}
		gate.inputs[id] = input
		gate.bodies[id] = body
	}
	if err = validateNewBaseProof(ctx, tx, gate); err != nil {
		return nil, err
	}
	gate.reservations = map[string]newBaseReservationGate{}
	sourcePayload, e := adapter.DecodeInvocationPayload(gate.payloads[s.SourceIntegrationTaskID])
	if e != nil {
		return nil, e
	}
	ids, e := newBaseIntegrationReservations(ctx, tx, sourcePayload.CandidateAction.Target.Ref)
	if e != nil {
		return nil, e
	}
	for _, id := range ids {
		scope, e := integrationScope(ctx, tx, id, false)
		if e != nil {
			return nil, CodeError("rework_target_scope_unresolved")
		}
		facts, e := integrationReservationFacts(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		gate.reservations[id] = newBaseReservationGate{Target: scope, Facts: facts}
	}
	return gate, nil
}

func validateNewBaseShape(g *newBaseSource, s *NewBaseReworkSpec) error {
	bad := CodeError("new_base_source_unsupported")
	if len(g.tasks) != len(s.TaskMap) {
		return bad
	}
	members := map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if members[id] {
			return true
		}
		t := g.tasks[id]
		if t == nil {
			return false
		}
		members[id] = true
		for _, dep := range t.dependencies {
			if !visit(dep) {
				return false
			}
		}
		return true
	}
	if !visit(s.SourceIntegrationTaskID) || len(members) != len(g.tasks) {
		return bad
	}
	counts := map[string]int{}
	for id, t := range g.tasks {
		p, err := adapter.DecodeInvocationPayload(g.payloads[id])
		var fallbacks []json.RawMessage
		if err != nil || t.status != "completed" || t.completion != "owner_review" || json.Unmarshal([]byte(t.fallbacks), &fallbacks) != nil || len(fallbacks) > 0 || len(id) > 128 || !filepath.IsAbs(p.Directory) || filepath.Clean(p.Directory) != p.Directory || p.SessionID != "" || p.SessionKind != "" || len(p.Args) > 0 || p.ConfigGeneration != nil {
			return bad
		}
		if p.CandidateWorkspace != nil {
			if p.Kind != "" || p.CandidateAction != nil || !p.CandidateWorkspace.AutoDirectory || p.CandidateWorkspace.Rework != nil || len(t.dependencies) != 0 || p.Profile == nil || p.Profile.Role != adapter.Implementer || p.Profile.Permission != adapter.WorkspaceWrite || p.ProviderLock == nil {
				return bad
			}
			counts["producer"]++
		} else {
			a := p.CandidateAction
			if a == nil || a.ReviewIndex != nil {
				return bad
			}
			counts[a.Operation]++
			if a.Operation == "compose" {
				if !isV2Compose(g.payloads[id]) {
					return bad
				}
			} else if a.Version != 1 || (a.Operation != "validate" && a.Operation != "review" && a.Operation != "integrate") || a.DependencyInputVersion < 0 || a.DependencyInputVersion > 1 || len(t.dependencies) != 1 || t.dependencies[0] != a.SourceTask || a.Operation != "integrate" && !a.AutoDirectory {
				return bad
			}
		}
	}
	if counts["producer"] < 1 || counts["compose"] > 1 || counts["validate"] != 1 || counts["review"] != 1 || counts["integrate"] != 1 {
		return bad
	}
	root := g.tasks[s.SourceIntegrationTaskID]
	if root.action == nil || root.action.Operation != "integrate" {
		return bad
	}
	for _, payload := range g.payloads {
		p, e := adapter.DecodeInvocationPayload(payload)
		if e != nil {
			return bad
		}
		if p.CandidateWorkspace != nil || p.CandidateAction.Operation == "review" {
			if p.ProviderLock == nil {
				return bad
			}
			pin := p.ProviderLock.Binary
			_, e = adapter.BuildInvocation(adapter.Request{Provider: adapter.Provider(p.Provider), Binary: pin, CWD: p.Directory, Prompt: p.Prompt, Profile: p.Profile, Lock: p.ProviderLock, Workspace: p.CandidateWorkspace, Action: p.CandidateAction, Permission: adapter.Permission{Mode: p.PermissionMode, Allow: p.Allow, Deny: p.Deny}, ExtraArgs: p.ExtraArgs})
			if e != nil {
				return bad
			}
		}
	}
	return nil
}

func validateNewBaseProof(ctx context.Context, tx *sql.Tx, g *newBaseSource) error {
	bad := CodeError("new_base_source_proof_invalid")
	deliveries := map[string]newBaseDelivery{}
	for id, input := range g.inputs {
		e := input.Event
		t := g.tasks[id]
		var a newBaseDelivery
		if json.Unmarshal(g.bodies[id], &a) != nil || a.Version != 1 || runtimeHash(string(g.bodies[id])) != e.PayloadHash || input.Event.Artifact == nil || input.Event.Artifact.HostArtifact == nil {
			return bad
		}
		var settled string
		if tx.QueryRowContext(ctx, `SELECT b.status FROM segment_runtime s JOIN budget_runtime b ON b.id=s.reservation_id AND b.segment_id=s.segment_id WHERE s.segment_id=?`, e.SegmentID).Scan(&settled) != nil || settled != "settled" {
			return bad
		}
		if !reworkOID(a.Candidate.CandidateOID) || !reworkOID(a.Candidate.TreeOID) || a.Candidate.Binding == nil || a.Candidate.Binding.RunID != g.run || a.Candidate.Binding.PlanRevision != g.plan || a.Candidate.PlanRevision != int64(g.plan) || a.Candidate.PrivateRef == "" || a.Candidate.PrivateRefExpectedOID != a.Candidate.CandidateOID || !validDigest(a.CandidateReceiptSHA256) {
			return bad
		}
		if t.workspace != nil {
			if a.Stage != "" || !reworkBindingMatches(a.Candidate.Binding, e, g.plan) || a.Candidate.BaseOID != t.workspace.BaseOID || a.Candidate.RepoRoot != t.workspace.RepoRoot {
				return bad
			}
			if c := t.workspace.NewBaseRework; c != nil && a.Candidate.Binding.NewBaseSHA256 != c.Digest() {
				return bad
			}
		} else if a.Stage != t.action.Operation || !reworkBindingMatches(&a.Binding, e, g.plan) {
			return bad
		}
		if a.Composition != nil {
			if gitops.ValidateCompositionManifest(a.Composition, a.Candidate, a.CandidateReceiptSHA256) != nil {
				return bad
			}
		}
		deliveries[id] = a
	}
	for id, input := range g.inputs {
		p, _ := adapter.DecodeInvocationPayload(input.AdapterPayload)
		a := deliveries[id]
		e := input.Event
		c := contract.LaunchCommand{CommandID: e.CommandID, HostID: e.ProducerID, RunID: e.RunID, TaskID: e.TaskID, AttemptID: e.AttemptID, SegmentID: e.SegmentID, WorkRevision: e.WorkRevision, PlanRevision: g.plan, AdapterPayload: input.AdapterPayload}
		if tx.QueryRowContext(ctx, `SELECT launch_intent_hash,slot_token FROM segment_runtime WHERE segment_id=?`, e.SegmentID).Scan(&c.LaunchIntentHash, &c.SlotToken) != nil {
			return bad
		}
		if p.CandidateAction != nil {
			act := p.CandidateAction
			if act.DependencyInputVersion > 0 {
				v, err := loadDependencyInputs(ctx, tx, e.SegmentID)
				if err != nil {
					return bad
				}
				c.DependencyInputs = v
				if len(v.Inputs) != len(g.tasks[id].dependencies) {
					return bad
				}
				for _, in := range v.Inputs {
					if !sameJSON(in, g.inputs[in.Event.TaskID]) {
						return bad
					}
				}
			}
			if act.Operation == "compose" {
				m := a.Composition
				if m == nil || len(m.Sources) != len(act.OrderedSourceTasks) {
					return bad
				}
				for i, src := range m.Sources {
					in := g.inputs[act.OrderedSourceTasks[i]]
					d := deliveries[in.Event.TaskID]
					sp, _ := adapter.DecodeInvocationPayload(in.AdapterPayload)
					pin, _ := json.Marshal(sp.ProviderLock)
					if src.TaskID != in.Event.TaskID || src.EventID != in.Event.EventID || src.EventSHA256 != in.Event.PayloadHash || src.DecisionCommandID != in.DecisionCommandID || src.CandidateOID != d.Candidate.CandidateOID || src.CandidateReceiptSHA256 != d.CandidateReceiptSHA256 || src.ProviderLockSHA256 != runtimeHash(string(pin)) || src.AdapterPayloadSHA256 != runtimeHash(string(in.AdapterPayload)) || src.ArtifactSHA256 != in.Event.PayloadHash || d.Candidate.RepoRoot != a.Candidate.RepoRoot || d.Candidate.CommonDir != a.Candidate.CommonDir || d.Candidate.BaseOID != a.Candidate.BaseOID || d.Candidate.RepoRootIdentity != a.Candidate.RepoRootIdentity || d.Candidate.CommonDirIdentity != a.Candidate.CommonDirIdentity {
						return bad
					}
				}
			} else {
				in, ok := g.inputs[act.SourceTask]
				if !ok {
					return bad
				}
				c.Input = &in
				source := deliveries[act.SourceTask]
				if a.InputEventID != in.Event.EventID || a.InputSHA256 != in.Event.PayloadHash || a.OwnerDecisionID != in.DecisionCommandID || !reflect.DeepEqual(a.Candidate, source.Candidate) || !sameJSON(a.Composition, source.Composition) {
					return bad
				}
				if _, err := targetFromArtifact(reworkArtifact{Target: a.Target, Candidate: a.Candidate}, act.Target, g.plan); err != nil {
					return bad
				}
				switch act.Operation {
				case "validate":
					var fields struct {
						Validation json.RawMessage `json:"validation"`
					}
					json.Unmarshal(g.bodies[id], &fields)
					if a.Validation == nil || !a.Validation.Passed || a.Validation.ExitCode != 0 || a.Validation.CandidateOID != a.Candidate.CandidateOID || a.Validation.TreeOID != a.Candidate.TreeOID || a.Validation.Binding != a.Binding || !reflect.DeepEqual(a.Validation.Command, act.Command) || !validDigest(a.Validation.ExecutableSHA256) || !validDigest(a.Validation.OutputSHA256) || a.ValidationDigest != runtimeHash(string(fields.Validation)) {
						return bad
					}
				case "review":
					if source.Stage != "validate" || !reflect.DeepEqual(a.Validation, source.Validation) || a.Review == nil || a.Review.Decision != "approve" || strings.TrimSpace(a.Review.Summary) == "" || a.ValidationDigest != source.ValidationDigest || !reflect.DeepEqual(a.ProviderLock, p.ProviderLock) {
						return bad
					}
				case "integrate":
					if source.Stage != "review" || !reflect.DeepEqual(a.Validation, source.Validation) || !reflect.DeepEqual(a.Target, source.Target) || source.Review == nil || source.Review.Decision != "approve" || a.ValidationDigest != source.ValidationDigest || a.Integration == nil || a.Integration.State != gitops.IntegrationIntegrated || a.Integration.TargetHEAD != a.Candidate.CandidateOID || a.Integration.CandidateOID != a.Candidate.CandidateOID || a.Integration.TargetWorktree != act.Target.Worktree {
						return bad
					}
				}
			}
		}
		if bindHostArtifactTarget(ctx, tx, &c, true, "terminal") != nil || bindValidationTool(ctx, tx, &c, false) != nil || bindDiagnosticsForUse(ctx, tx, &c, candidateArtifactHistoricalProof) != nil || contract.ValidateDependencyInputs(c) != nil {
			return bad
		}
		h, err := contract.CandidateIntentHash(c)
		if p.CandidateWorkspace != nil && p.CandidateWorkspace.NewBaseRework == nil && p.CandidateWorkspace.Rework == nil {
			h = runtimeHash(c.RunID + "\x00" + c.TaskID + "\x00" + c.AttemptID + "\x00" + c.SegmentID + "\x00" + c.SlotToken)
		}
		if err != nil || h != c.LaunchIntentHash {
			return bad
		}
	}
	return nil
}
