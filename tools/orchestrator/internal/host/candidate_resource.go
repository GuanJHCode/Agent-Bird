package host

import (
	"context"
	"encoding/json"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// registerCandidateResource is deliberately best-effort after Result and
// Exited are durable. A registration error must never retract a completed
// business result; the exact sealed source remains available to its command
// replay, which will retry this append without directory discovery.
func (h *Host) registerCandidateResource(ctx context.Context, spool *events.Spool, runID, taskID string, a store.Attempt, meta launchMetadata, content []byte) int {
	source, ok := h.candidateResourceSource(runID, taskID, a, meta, content)
	if !ok {
		return 0
	}
	header, err := gitopsworker.SealCandidateJournal(ctx, h.spoolRoot, source)
	if err != nil {
		return 0
	}
	raw, err := json.Marshal(header)
	if err != nil {
		return 0
	}
	if _, err = h.appendStructuredEvent(spool, runID, taskID, a, meta, contract.EventCandidateResourceRegistered, hashBytes(raw), nil, nil, nil, structuredEventFields{CandidateResource: raw}); err != nil {
		return 0
	}
	return 1
}

// candidateResourceScopeForGrant repeats only the immutable authority checks
// that selected registration on the original execution. Duplicate recovery
// never trusts an artifact to widen that authority.
func candidateResourceScopeForGrant(grant contract.LaunchCommand) string {
	meta := metadataForLaunch(grant)
	if meta.hasGitFilter {
		return ""
	}
	payload, err := adapter.DecodeInvocationPayload(grant.AdapterPayload)
	if err != nil {
		return ""
	}
	if action := payload.CandidateAction; action != nil {
		if action.Version == 2 && action.DependencyInputVersion == 2 && action.Operation == "compose" && action.AutoDirectory {
			return "compose"
		}
		return ""
	}
	workspace := payload.CandidateWorkspace
	if workspace != nil && workspace.Version == 1 && workspace.AutoDirectory && grant.Continuation == nil && workspace.Rework == nil && workspace.NewBaseRework == nil {
		return "producer"
	}
	return ""
}

func (h *Host) candidateResourceSource(runID, taskID string, a store.Attempt, meta launchMetadata, content []byte) (gitops.CandidateResourceSource, bool) {
	if meta.candidateResourceKind == "" {
		return gitops.CandidateResourceSource{}, false
	}
	var binding gitops.CandidateBinding
	var projection string
	switch meta.candidateResourceKind {
	case "producer":
		var wire struct {
			Version        int                     `json:"version"`
			ReceiptSHA256  string                  `json:"candidate_receipt_sha256"`
			ProviderResult string                  `json:"provider_result"`
			Candidate      gitops.CandidateReceipt `json:"candidate"`
		}
		if decodeCandidateArtifact(content, &wire) != nil || wire.Version != 1 || wire.Candidate.Binding == nil || wire.ReceiptSHA256 == "" || wire.Candidate.Filter != nil {
			return gitops.CandidateResourceSource{}, false
		}
		binding, projection = *wire.Candidate.Binding, wire.ReceiptSHA256
	case "compose":
		delivery, err := decodeCandidateDeliveryStrict(content)
		if err != nil || delivery.Stage != "compose" || delivery.Composition == nil || delivery.Candidate.Filter != nil || delivery.Composition.Binding.NewBaseSHA256 != "" || delivery.CandidateReceiptSHA256 == "" {
			return gitops.CandidateResourceSource{}, false
		}
		binding, projection = delivery.Binding, delivery.CandidateReceiptSHA256
	default:
		return gitops.CandidateResourceSource{}, false
	}
	if binding.RunID != runID || binding.TaskID != taskID || binding.AttemptID != a.ID || binding.SegmentID != a.SegmentID || binding.WorkRevision != meta.workRevision {
		return gitops.CandidateResourceSource{}, false
	}
	key := hashText(binding.RunID + "\x00" + binding.TaskID + "\x00" + binding.AttemptID + "\x00" + binding.SegmentID)
	source := gitops.CandidateResourceSource{Kind: meta.candidateResourceKind, HostID: h.producerID, CommandID: meta.commandID, Binding: binding, ProjectionSHA256: projection}
	if meta.candidateResourceKind == "producer" {
		source.JournalRelative = ".gitops-journal/" + key
	} else {
		source.JournalRelative = "gitops-journal/" + hashText(meta.commandID)
	}
	return source, true
}

func candidateResourceRegistrationMatchesGrant(raw []byte, grant contract.LaunchCommand, scope string) bool {
	if scope == "" {
		return false
	}
	var header gitops.CandidateResourceHeader
	if json.Unmarshal(raw, &header) != nil || header.Validate() != nil {
		return false
	}
	binding := header.Source.Binding
	return header.Source.Kind == scope && header.Source.HostID == grant.HostID && header.Source.CommandID == grant.CommandID && binding.RunID == grant.RunID && binding.TaskID == grant.TaskID && binding.AttemptID == grant.AttemptID && binding.SegmentID == grant.SegmentID && binding.WorkRevision == grant.WorkRevision && binding.PlanRevision == grant.PlanRevision
}

func decodeCandidateArtifact(body []byte, out any) error {
	return contract.DecodeBridgeStrict(body, out)
}
