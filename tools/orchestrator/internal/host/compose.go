package host

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

// stdout is separate from process.Output (which also contains stderr). The
// original Handle.Wait joins its output copier before the finalizer reads it.
type composeOutput struct {
	bytes.Buffer
	overflow bool
}

func (o *composeOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 1024*1024 - o.Len()
	if n > remaining {
		o.overflow = true
		p = p[:remaining]
	}
	_, _ = o.Buffer.Write(p)
	return n, nil
}
func decodeComposeJSON(body []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("candidate_compose_invalid")
	}
	return nil
}

type composeFailure struct {
	code      string
	uncertain bool
}

func (e *composeFailure) Error() string { return e.code }
func (e *composeFailure) Unwrap() error {
	if e.uncertain {
		return process.ErrProcessTreeUnknown
	}
	return nil
}
func (e *composeFailure) SafeArtifact() []byte {
	status := "failed"
	if e.uncertain {
		status = "unknown"
	}
	b, _ := json.Marshal(map[string]any{"version": 1, "status": status, "code": e.code, "operation": "compose", "recovery": "retain journal and worktree; owner reconciliation required"})
	return b
}

// Every source is authenticated before materializing a private worker request.
// The returned command goes through Host.execute's original Wait/tree lifecycle.
func (h *Host) prepareComposeAction(ctx context.Context, grant contract.LaunchCommand, inv contract.InvocationView, action *adapter.CandidateAction) (process.Command, actionFinalizer, func(), error) {
	empty, noop := invocationCommand(inv), func() {}
	if err := contract.ValidateDependencyInputs(grant); err != nil {
		return empty, nil, noop, err
	}
	if grant.Input != nil || action.SourceTask != "" || len(action.OrderedSourceTasks) < 2 || len(action.OrderedSourceTasks) > 63 || grant.DependencyInputs == nil || grant.DependencyInputs.Version != 2 || len(grant.DependencyInputs.Inputs) != len(action.OrderedSourceTasks) || grant.SessionID != "" || grant.Answer != "" || !filepath.IsAbs(inv.WorkingDirectory()) || filepath.Clean(inv.WorkingDirectory()) != inv.WorkingDirectory() {
		return empty, nil, noop, errors.New("candidate_compose_invalid")
	}
	key := hashText(grant.RunID + "\x00" + grant.TaskID + "\x00" + grant.AttemptID + "\x00" + grant.SegmentID)
	journal, err := gitopsworker.OpenJournal(filepath.Join(h.spoolRoot, ".gitops-journal", key+"-compose-auth"))
	if err != nil {
		return empty, nil, noop, err
	}
	closeJournal := func() { _ = journal.Close() }
	fail := func(err error) (process.Command, actionFinalizer, func(), error) {
		return empty, nil, closeJournal, err
	}
	ctx = gitops.WithCandidateJournal(ctx, journal)
	deliveries := make([]CandidateDelivery, len(grant.DependencyInputs.Inputs))
	for i, input := range grant.DependencyInputs.Inputs {
		if input.Event.TaskID != action.OrderedSourceTasks[i] {
			return fail(errors.New("candidate_compose_order_changed"))
		}
		source, err := adapter.DecodeInvocationPayload(input.AdapterPayload)
		if err != nil || source.Kind != "" || source.CandidateAction != nil || source.CandidateWorkspace == nil || source.CandidateWorkspace.Version != 1 || source.Profile == nil || source.Profile.Role != adapter.Implementer || source.Profile.Permission != adapter.WorkspaceWrite || validateCandidateSourceProvider(source.ProviderLock, source.Provider) != nil {
			return fail(errors.New("candidate_compose_source_invalid"))
		}
		delivery, err := readCandidateDelivery(grant, &input, filepath.Dir(h.spoolRoot))
		if err != nil {
			return fail(err)
		}
		c := delivery.Candidate
		sourceGrant := contract.LaunchCommand{RunID: grant.RunID, TaskID: input.Event.TaskID, AttemptID: input.Event.AttemptID, SegmentID: input.Event.SegmentID}
		if delivery.Stage != "" || delivery.Composition != nil || c.Binding == nil || c.Binding.TaskID != input.Event.TaskID || c.AttemptID != input.Event.AttemptID || c.PlanRevision != int64(grant.PlanRevision) || len(delivery.CandidateReceiptSHA256) != 64 || c.RepoRoot != source.CandidateWorkspace.RepoRoot || c.BaseOID != source.CandidateWorkspace.BaseOID || !compositionSourceWorktree(source.Directory, sourceGrant, source.CandidateWorkspace.AutoDirectory, c.Worktree) || c.PrivateRef == "" || c.PrivateRefExpectedOID != c.CandidateOID {
			return fail(errors.New("candidate_compose_source_invalid"))
		}
		if n := source.CandidateWorkspace.NewBaseRework; n != nil {
			if n.Validate(grant.RunID, input.Event.TaskID, source.CandidateWorkspace.BaseOID) != nil || c.Binding.NewBaseSHA256 != n.Digest() {
				return fail(errors.New("candidate_new_base_binding_invalid"))
			}
		} else if c.Binding.NewBaseSHA256 != "" {
			return fail(errors.New("candidate_new_base_binding_invalid"))
		}
		if err := gitops.ValidateCandidatePrivateRef(ctx, c); err != nil {
			return fail(err)
		}
		deliveries[i] = delivery
	}
	manifest, err := newCompositionManifest(grant, grant.DependencyInputs.Inputs, deliveries)
	if err != nil {
		return fail(err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil || !filepath.IsAbs(executable) {
		return fail(errors.New("candidate_compose_executable_invalid"))
	}
	req := gitops.CandidateCompositionRequest{RepoRoot: manifest.RepoRoot, Worktree: candidateDirectory(inv.WorkingDirectory(), grant, true), AttemptID: grant.AttemptID, BaseOID: manifest.BaseOID, OrderedInputOIDs: append([]string(nil), manifest.OrderedInputOIDs...), PlanRevision: int64(grant.PlanRevision), PrivateRef: "refs/orchestrator/g3/compose-" + key}
	for _, delivery := range deliveries {
		pin := delivery.Candidate
		pin.Files, pin.Changes, pin.Binding = nil, nil, nil
		req.SourcePins = append(req.SourcePins, pin)
	}
	payload, err := json.Marshal(struct {
		Kind      string `json:"kind"`
		Operation string `json:"operation"`
		Request   any    `json:"request"`
	}{"gitops", "compose", req})
	if err != nil {
		return fail(err)
	}
	worker, err := gitopsworker.BuildInvocation(payload, executable, h.spoolRoot, grant.CommandID)
	if err != nil {
		return fail(err)
	}
	cmd := invocationCommand(worker)
	output := &composeOutput{}
	cmd.Stdout = output
	finalize := func(finalCtx context.Context, _ string, exit int, success bool) ([]byte, error) {
		uncertain := func() ([]byte, error) { return nil, &composeFailure{code: "compose_uncertain", uncertain: true} }
		var response struct {
			Version   int             `json:"version"`
			Status    string          `json:"status"`
			Operation string          `json:"operation"`
			Result    json.RawMessage `json:"result,omitempty"`
			Code      string          `json:"code,omitempty"`
		}
		if output.overflow || decodeComposeJSON(output.Bytes(), &response) != nil || response.Version != 1 || response.Operation != "compose" {
			return uncertain()
		}
		if !success || exit != 0 {
			if response.Status != "error" || len(response.Result) != 0 {
				return uncertain()
			}
			switch response.Code {
			case "compose_uncertain":
				return uncertain()
			case "compose_conflict", "candidate_compose_failed", "invalid_input", "target_dirty", "path_collision", "target_drift", "unknown_object", "identity_changed", "git_failure", "review_mismatch":
				return nil, &composeFailure{code: response.Code}
			default:
				return uncertain()
			}
		}
		if response.Status != "ok" || response.Code != "" {
			return uncertain()
		}
		var receipt gitops.CandidateReceipt
		if decodeComposeJSON(response.Result, &receipt) != nil || receipt.Binding != nil || receipt.AttemptID != req.AttemptID || receipt.PlanRevision != req.PlanRevision || receipt.Worktree != req.Worktree || receipt.WorktreeIdentity != req.Worktree || receipt.RepoRoot != manifest.RepoRoot || receipt.CommonDir != manifest.CommonDir || receipt.RepoRootIdentity != manifest.RepoRootIdentity || receipt.CommonDirIdentity != manifest.CommonDirIdentity || receipt.BaseOID != manifest.BaseOID || !reflect.DeepEqual(receipt.OrderedInputOIDs, manifest.OrderedInputOIDs) || receipt.PrivateRef != req.PrivateRef || receipt.PrivateRefExpectedOID != receipt.CandidateOID {
			return uncertain()
		}
		if err := gitops.ValidateCandidatePrivateRef(gitops.WithCandidateJournal(finalCtx, journal), receipt); err != nil {
			return uncertain()
		}
		binding := gitops.CandidateBinding{RunID: grant.RunID, TaskID: grant.TaskID, AttemptID: grant.AttemptID, SegmentID: grant.SegmentID, WorkRevision: grant.WorkRevision, PlanRevision: grant.PlanRevision}
		binding.NewBaseSHA256 = manifest.Binding.NewBaseSHA256
		receipt.Binding = &binding
		receipt.Files = nil
		manifest.Binding = binding
		manifest.RootIdentity = receipt.RootIdentity
		manifest.CandidateOID, manifest.TreeOID, manifest.PrivateRef, manifest.PrivateRefExpectedOID = receipt.CandidateOID, receipt.TreeOID, receipt.PrivateRef, receipt.PrivateRefExpectedOID
		manifest.ManifestSHA256 = ""
		raw, err := json.Marshal(manifest)
		if err != nil {
			return uncertain()
		}
		manifest.ManifestSHA256 = hashBytes(raw)
		receiptRaw, err := json.Marshal(receipt)
		if err != nil {
			return uncertain()
		}
		out := CandidateDelivery{Version: 1, Stage: "compose", Binding: binding, Candidate: receipt, CandidateReceiptSHA256: hashBytes(receiptRaw), Composition: manifest}
		if err = validateCompositionDelivery(out); err != nil {
			return uncertain()
		}
		if err = checkDeliverySize(out); err != nil {
			return uncertain()
		}
		if err = journal.RecordCandidateAction(finalCtx, "composition_result", out); err != nil {
			return uncertain()
		}
		return json.Marshal(out)
	}
	return cmd, finalize, closeJournal, nil
}

// A continued producer keeps its original automatic workspace even though its
// accepted event belongs to a later segment. The authenticated Host receipt
// supplies the original inode; the payload constrains its workspace namespace.
func compositionSourceWorktree(base string, grant contract.LaunchCommand, automatic bool, actual string) bool {
	if !filepath.IsAbs(base) || filepath.Clean(base) != base {
		return false
	}
	if !automatic {
		return actual == base
	}
	if actual == candidateDirectory(base, grant, true) {
		return true
	}
	suffix, ok := strings.CutPrefix(actual, base+".attempt-")
	if !ok || len(suffix) != 24 {
		return false
	}
	decoded, err := hex.DecodeString(suffix)
	return err == nil && len(decoded) == 12 && hex.EncodeToString(decoded) == suffix
}
