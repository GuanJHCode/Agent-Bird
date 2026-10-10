package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

func filterCandidateBinding(g contract.LaunchCommand) gitops.CandidateBinding {
	return gitops.CandidateBinding{RunID: g.RunID, TaskID: g.TaskID, AttemptID: g.AttemptID, SegmentID: g.SegmentID, WorkRevision: g.WorkRevision, PlanRevision: g.PlanRevision}
}

func (f *filterRuntime) materializeRequest(repo, directory, base, candidate string, inputs []string) gitops.MaterializeRequest {
	g := f.grant
	return gitops.MaterializeRequest{RepoRoot: repo, Worktree: candidateDirectory(directory, g, true), AttemptID: g.AttemptID, BaseOID: base, CandidateOID: candidate, OrderedInputOIDs: append([]string(nil), inputs...), PlanRevision: int64(g.PlanRevision), FilterScopeSHA256: g.GitFilter.ScopeSHA256, FilterReservationID: g.GitFilter.ReservationID}
}

func (h *Host) prepareFilteredCandidate(ctx context.Context, f *filterRuntime, p adapter.InvocationPayload) (func(context.Context, string) ([]byte, error), func(), *gitops.PreparedWorkspace, error) {
	noop := func() {}
	if err := f.openSource(ctx, p.CandidateWorkspace.RepoRoot); err != nil {
		return nil, noop, nil, err
	}
	ctx = gitops.WithCandidateJournal(ctx, f.journal)
	w := p.CandidateWorkspace
	req := f.materializeRequest(w.RepoRoot, p.Directory, w.BaseOID, w.BaseOID, []string{w.BaseOID})
	live, err := gitops.PrepareFilteredWorkspace(ctx, f.source, f.broker, req, filterCandidateBinding(f.grant), w.Paths, "refs/orchestrator/g3/candidate-"+filterJournalKey(f.grant), f.journal)
	if err != nil {
		return nil, noop, nil, err
	}
	closeCandidate := func() { _ = live.Close() }
	prepared := live.Workspace()
	prepared.OwnerContextID = h.continuationOwner
	prepared.AdapterPayloadSHA256 = hashBytes(f.grant.AdapterPayload)
	if err = f.journal.RecordWorkspacePrepared(ctx, prepared); err != nil {
		return nil, closeCandidate, nil, err
	}
	// The observation above is never read to reconstruct live. Freeze accepts
	// only this original object and the runtime's original process terminal.
	finalizeAttempted := false
	freeze := func(ctx context.Context, text string) ([]byte, error) {
		if finalizeAttempted {
			return nil, errors.New("git_filter_finalizer_reused")
		}
		finalizeAttempted = true
		if err := f.requireOriginalTerminal(); err != nil {
			return nil, err
		}
		if len(text) > 512<<10 {
			return nil, errors.New("candidate_provider_result_too_large")
		}
		if err := gitops.CheckCandidateArtifactBudget(prepared, text); err != nil {
			return nil, err
		}
		receipt, err := gitops.FreezeFilteredWorkspace(gitops.WithCandidateJournal(ctx, f.journal), live, f.journal)
		if err != nil {
			return nil, err
		}
		return gitops.CandidateArtifact(text, receipt)
	}
	return freeze, closeCandidate, &prepared, nil
}

func filteredAcceptedSource(g contract.LaunchCommand, p adapter.InvocationPayload, in CandidateDelivery) (adapter.InvocationPayload, error) {
	bad := errors.New("git_filter_candidate_source_invalid")
	if g.Input == nil || g.Input.Event.Artifact == nil || g.Input.Event.Artifact.HostArtifact == nil {
		return adapter.InvocationPayload{}, bad
	}
	source, err := adapter.DecodeInvocationPayload(g.Input.AdapterPayload)
	if err != nil || source.Kind != "provider" || adapter.ValidateGitFilterPipelinePayload(source) != nil || !reflect.DeepEqual(source.GitFilter, p.GitFilter) {
		return source, bad
	}
	c := in.Candidate
	// Reject stage/target/review/compose data attached to an original producer.
	if !reflect.DeepEqual(in, CandidateDelivery{Version: 1, Candidate: c, CandidateReceiptSHA256: in.CandidateReceiptSHA256}) || !adapter.ValidCommandSHA256(in.CandidateReceiptSHA256) || c.Filter == nil || c.Files != nil || c.Binding == nil || c.Binding.WorkRevision != 1 || c.BaseOID != source.CandidateWorkspace.BaseOID || c.RepoRoot != source.CandidateWorkspace.RepoRoot {
		return source, bad
	}
	prior := g
	prior.TaskID, prior.AttemptID, prior.SegmentID = g.Input.Event.TaskID, g.Input.Event.AttemptID, g.Input.Event.SegmentID
	if c.Worktree != candidateDirectory(source.Directory, prior, true) || c.PrivateRef != "refs/orchestrator/g3/candidate-"+filterJournalKey(prior) {
		return source, bad
	}
	for _, change := range c.Changes {
		if !slices.Contains(source.CandidateWorkspace.Paths, change.Path) {
			return source, bad
		}
	}
	return source, nil
}

func (h *Host) prepareFilteredValidation(ctx context.Context, f *filterRuntime, inv contract.InvocationView, p adapter.InvocationPayload) (process.Command, actionFinalizer, func(), error) {
	cmd, noop := invocationCommand(inv), func() {}
	g, action := f.grant, p.CandidateAction
	in, err := readAcceptedCandidate(g, filepath.Dir(h.spoolRoot))
	if err != nil {
		return cmd, nil, noop, err
	}
	if _, err = filteredAcceptedSource(g, p, in); err != nil {
		return cmd, nil, noop, err
	}
	c := in.Candidate
	if err = f.openSource(ctx, c.RepoRoot); err != nil {
		return cmd, nil, noop, err
	}
	ctx = gitops.WithCandidateJournal(ctx, f.journal)
	live, err := gitops.MaterializeFilteredCandidate(ctx, f.source, f.broker, f.materializeRequest(c.RepoRoot, p.Directory, c.BaseOID, c.CandidateOID, c.OrderedInputOIDs), c, f.journal)
	if err != nil {
		return cmd, nil, noop, err
	}
	closeCandidate := func() { _ = live.Close() }
	fail := func(err error) (process.Command, actionFinalizer, func(), error) {
		return cmd, nil, closeCandidate, err
	}
	workspace := live.Receipt()
	if err = f.journal.RecordCandidateAction(ctx, "materialized", workspace); err != nil {
		return fail(err)
	}
	if err = live.Verify(ctx); err != nil {
		return fail(err)
	}
	scratch := filepath.Join(h.spoolRoot, g.AttemptID, g.SegmentID, "scratch")
	tool, err := h.sealValidationTool(ctx, g, action.Command[0], action.CommandSHA256, workspace.Worktree, scratch)
	if err != nil {
		return fail(err)
	}
	cmd = process.Command{Path: tool, Args: append([]string(nil), action.Command[1:]...), Dir: workspace.Worktree, PinnedPath: action.Command[0], PinnedSHA256: action.CommandSHA256, SealedPath: tool, SealedSHA256: action.CommandSHA256, ExactEnv: true,
		Env: []string{"PATH=" + filepath.Dir(tool) + ":/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + scratch, "XDG_CACHE_HOME=" + filepath.Join(scratch, "cache"), "GOCACHE=" + filepath.Join(scratch, "go-build"), "GOPATH=" + filepath.Join(scratch, "go")}}
	profile := &adapter.ExecutionProfile{Version: 1, Role: adapter.Implementer, Permission: adapter.WorkspaceWrite, TimeoutMS: g.GrantedActiveMS}
	cmd, err = sandboxCommand(ctx, cmd, profile, scratch)
	if err != nil {
		return fail(err)
	}
	out := CandidateDelivery{Version: 1, Stage: "validate", Binding: filterCandidateBinding(g), InputEventID: g.Input.Event.EventID, InputSHA256: g.Input.Event.Artifact.SHA256, OwnerDecisionID: g.Input.DecisionCommandID, Candidate: c, CandidateReceiptSHA256: in.CandidateReceiptSHA256}
	out.Validation = &ValidationEvidence{CandidateOID: c.CandidateOID, TreeOID: c.TreeOID, Binding: out.Binding, Command: append([]string(nil), action.Command...), ExecutableSHA256: action.CommandSHA256}
	finalizeAttempted := false
	finalize := func(ctx context.Context, text string, exit int, success bool) ([]byte, error) {
		if finalizeAttempted {
			return nil, errors.New("git_filter_finalizer_reused")
		}
		finalizeAttempted = true
		if err := f.requireOriginalTerminal(); err != nil {
			return nil, err
		}
		if len(text) >= 1<<20 {
			return nil, errors.New("candidate_output_limit")
		}
		ctx = gitops.WithCandidateJournal(ctx, f.journal)
		if err := live.Verify(ctx); err != nil {
			return nil, err
		}
		out.Validation.ExitCode, out.Validation.OutputSHA256, out.Validation.Passed = exit, hashText(text), success && exit == 0
		raw, err := json.Marshal(out.Validation)
		if err != nil {
			return nil, err
		}
		out.ValidationDigest = hashBytes(raw)
		body, err := finishCandidateDelivery(ctx, out, success && exit == 0, f.journal)
		if verifyErr := live.Verify(ctx); verifyErr != nil {
			return nil, errors.Join(err, verifyErr)
		}
		return body, err
	}
	return cmd, finalize, closeCandidate, nil
}
