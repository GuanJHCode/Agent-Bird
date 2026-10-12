// Package candidatemaintenance supervises one Store-authorized cleanup child.
package candidatemaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
	"io"
	"os"
	"os/exec"
)

type Store interface {
	MarkCandidateMaintenanceRunning(context.Context, string, string, store.CandidateMaintenanceGrant, process.Identity) (store.CandidateMaintenanceState, error)
	FinishCandidateMaintenance(context.Context, store.CandidateMaintenanceGrant, process.Identity, store.CandidateMaintenanceOutcome) (store.CandidateMaintenanceState, error)
}

func Run(ctx context.Context, db Store, controller, token string, grant store.CandidateMaintenanceGrant) (store.CandidateMaintenanceOutcome, error) {
	if grant.Validate() != nil {
		return store.CandidateMaintenanceOutcome{}, errors.New("candidate_maintenance_grant_invalid")
	}
	r, w, e := os.Pipe()
	if e != nil {
		return store.CandidateMaintenanceOutcome{}, e
	}
	defer r.Close()
	defer w.Close()
	spec := process.Command{PinnedPath: grant.Executable, PinnedSHA256: grant.ExecutableSHA256, Path: grant.Executable, Args: []string{"candidate-maintenance-worker", "--bootstrap-fd", "3"}}
	h, e := process.StartWithCommandStarter(ctx, spec, func(c *exec.Cmd) error { c.ExtraFiles = []*os.File{r}; return c.Start() })
	if e != nil {
		if h != nil {
			_ = h.Stop(context.Background())
			_ = h.Wait(context.Background())
		}
		return store.CandidateMaintenanceOutcome{}, e
	}
	id := h.Identity()
	if _, e = db.MarkCandidateMaintenanceRunning(ctx, controller, token, grant, id); e != nil {
		_ = h.Stop(context.Background())
		_ = h.Wait(context.Background())
		return store.CandidateMaintenanceOutcome{}, e
	}
	raw, _ := json.Marshal(grant)
	if len(raw) > 24576 {
		return store.CandidateMaintenanceOutcome{}, errors.New("candidate_maintenance_grant_invalid")
	}
	if _, e = w.Write(raw); e != nil {
		return store.CandidateMaintenanceOutcome{}, e
	}
	_ = w.Close()
	e = h.Wait(ctx)
	tree := h.ConfirmTreeExited()
	out := store.CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: "child_failed"}
	if e == nil && tree == nil && h.ExitCode() == 0 {
		out.State = "removed"
		out.Code = ""
		out.Cleanup = &gitops.CleanupOutcome{State: gitops.CleanupRemoved}
	}
	if _, finish := db.FinishCandidateMaintenance(context.Background(), grant, id, out); finish != nil {
		return out, finish
	}
	return out, e
}
func Worker(ctx context.Context, bootstrap io.Reader) (store.CandidateMaintenanceOutcome, error) {
	var g store.CandidateMaintenanceGrant
	raw, e := io.ReadAll(io.LimitReader(bootstrap, 24577))
	if e != nil || len(raw) > 24576 || json.Unmarshal(raw, &g) != nil || g.Validate() != nil {
		return store.CandidateMaintenanceOutcome{}, errors.New("candidate_maintenance_grant_invalid")
	}
	receipt, e := gitopsworker.OpenSealedCandidateResource(ctx, g.SpoolRoot, g.Header)
	if e != nil {
		return store.CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: "sealed_open_failed"}, e
	}
	j, e := gitopsworker.OpenExistingMaintenanceJournal(g.OperationJournal, g.JournalIdentity)
	if e != nil {
		return store.CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: "journal_open_failed"}, e
	}
	defer j.Close()
	out, e := gitops.Cleanup(gitops.WithCandidateJournal(ctx, j), gitops.CleanupRequest{Root: receipt.Worktree, CandidateReceipt: &receipt, Journal: j})
	if e != nil {
		return store.CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: "cleanup_failed", Cleanup: &out}, e
	}
	return store.CandidateMaintenanceOutcome{Version: 1, State: "removed", Cleanup: &out}, nil
}
