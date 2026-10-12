package host

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/events"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

// cleanupPublishedTemporaryWorkspace runs only after the immutable business
// Result and Exited records are durable. Cleanup observations are deliberately
// separate records: their failure cannot recast a completed business result.
func (h *Host) cleanupPublishedTemporaryWorkspace(ctx context.Context, spool *events.Spool, runID, taskID string, a store.Attempt, meta launchMetadata) {
	cleanup := meta.temporaryWorkspace
	if cleanup == nil || cleanup.journal == nil || cleanup.operation == "" || cleanup.receipt.Worktree == "" {
		return
	}
	raw, err := json.Marshal(cleanup.receipt)
	if err != nil {
		return
	}
	base := contract.TemporaryWorkspaceCleanup{
		Version: 1, Operation: cleanup.operation, State: "pending", Worktree: cleanup.receipt.Worktree,
		ReceiptSHA256: hashBytes(raw),
	}
	digest, err := contract.TemporaryWorkspaceCleanupDigest(base)
	if err != nil {
		return
	}
	if _, err = h.appendStructuredEvent(spool, runID, taskID, a, meta, contract.EventTemporaryWorkspaceCleanup, digest, nil, nil, nil, structuredEventFields{TemporaryWorkspaceCleanup: &base}); err != nil {
		return
	}

	// The launch context may be cancelled immediately after a natural exit.
	// Publication has already completed, so cleanup continues in this call's
	// original scope without inheriting cancellation; no detached work exists.
	cleanupCtx := context.WithoutCancel(ctx)
	outcome, cleanupErr := gitops.Cleanup(cleanupCtx, gitops.CleanupRequest{
		Root: cleanup.receipt.Worktree, Receipt: cleanup.receipt, Journal: cleanup.journal,
	})
	final := base
	if cleanupErr == nil && outcome.State == gitops.CleanupRemoved {
		final.State = "removed"
	} else {
		final.Code = temporaryWorkspaceCleanupCode(cleanupErr, outcome.Code)
	}
	digest, err = contract.TemporaryWorkspaceCleanupDigest(final)
	if err != nil {
		return
	}
	// A failed outcome is intentionally left as a durable pending observation.
	// Do not return it to the business execution path.
	_, _ = h.appendStructuredEvent(spool, runID, taskID, a, meta, contract.EventTemporaryWorkspaceCleanup, digest, nil, nil, nil, structuredEventFields{TemporaryWorkspaceCleanup: &final})
}

func temporaryWorkspaceCleanupCode(err error, outcomeCode string) string {
	if outcomeCode != "" {
		return outcomeCode
	}
	var gitErr *gitops.Error
	if errors.As(err, &gitErr) {
		return string(gitErr.Code)
	}
	if err != nil {
		return "cleanup_failed"
	}
	return "cleanup_pending"
}
