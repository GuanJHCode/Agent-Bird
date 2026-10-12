// Package candidatemaintenance supervises one Store-authorized cleanup child.
package candidatemaintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitops"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/gitopsworker"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/store"
)

type Store interface {
	MarkCandidateMaintenanceRunning(context.Context, string, string, store.CandidateMaintenanceGrant, process.Identity) (store.CandidateMaintenanceState, error)
	FinishCandidateMaintenance(context.Context, store.CandidateMaintenanceGrant, process.Identity, store.CandidateMaintenanceOutcome) (store.CandidateMaintenanceState, error)
}
type boundedOutput struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := 8192 - len(b.data)
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *boundedOutput) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data), b.overflow
}
func Run(ctx context.Context, db Store, controller, token string, grant store.CandidateMaintenanceGrant) (store.CandidateMaintenanceOutcome, error) {
	return run(ctx, db, controller, token, grant, []string{"candidate-maintenance-worker", "--bootstrap-fd", "3"})
}
func run(ctx context.Context, db Store, controller, token string, g store.CandidateMaintenanceGrant, args []string) (out store.CandidateMaintenanceOutcome, err error) {
	if g.Validate() != nil {
		return out, errors.New("candidate_maintenance_grant_invalid")
	}
	raw, e := json.Marshal(g)
	if e != nil || len(raw) > 24576 {
		return out, errors.New("candidate_maintenance_grant_invalid")
	}
	r, w, e := os.Pipe()
	if e != nil {
		return out, e
	}
	defer r.Close()
	defer w.Close()
	output := &boundedOutput{}
	spec := process.Command{PinnedPath: g.Executable, PinnedSHA256: g.ExecutableSHA256, Path: g.Executable, Args: args, Dir: g.Root.StateRoot, ExactEnv: true, Env: []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}, Stdout: output}
	h, e := process.StartWithCommandStarter(ctx, spec, func(c *exec.Cmd) error { c.ExtraFiles = []*os.File{r}; return c.Start() })
	if h == nil {
		return out, e
	}
	// Every return after Start retains responsibility for the original Handle.
	// A failed stop/tree observation leaves Store running; it must not authorize
	// another generation while a prior actor may still be modifying the tree.
	reaped, running, finishAttempted := false, false, false
	defer func() {
		w.Close()
		r.Close()
		if !reaped {
			stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = h.Stop(stop)
			waitErr := h.Wait(stop)
			treeErr := h.ConfirmTreeExited()
			cancel()
			reaped = waitErr == nil && treeErr == nil
			if !reaped {
				err = errors.Join(err, waitErr, treeErr)
			}
		}
		if running && reaped && !finishAttempted {
			out = store.CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: "bootstrap_delivery_failed"}
			finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, e := db.FinishCandidateMaintenance(finish, g, h.Identity(), out)
			cancel()
			err = errors.Join(err, e)
		}
	}()
	if e != nil {
		return out, e
	}
	_ = r.Close()
	id := h.Identity()
	if _, e = db.MarkCandidateMaintenanceRunning(ctx, controller, token, g, id); e != nil {
		return out, e
	}
	running = true
	// File pipe deadlines prevent a misbehaving child from stranding admission.
	deadline := time.Now().Add(5 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if e = w.SetWriteDeadline(deadline); e != nil {
		return out, e
	}
	for n := 0; n < len(raw); {
		k, e := w.Write(raw[n:])
		n += k
		if e != nil {
			return out, e
		}
		if k == 0 {
			return out, io.ErrShortWrite
		}
	}
	if e = w.Close(); e != nil {
		return out, e
	}
	waitErr := h.Wait(ctx)
	if waitErr != nil {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = h.Stop(stop)
		e = h.Wait(stop)
		cancel()
		if e != nil {
			return out, errors.Join(waitErr, e)
		}
	}
	if e = h.ConfirmTreeExited(); e != nil {
		return out, e
	}
	reaped = true
	out = store.CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: "child_failed"}
	if waitErr == nil && h.ExitCode() == 0 {
		body, overflow := output.snapshot()
		if overflow || contract.DecodeBridgeStrict(body, &out) != nil || out.Version != 1 || (out.State != "pending" && out.State != "removed") || len(out.Code) > 128 || (out.State == "removed" && (out.Cleanup == nil || out.Cleanup.State != gitops.CleanupRemoved || out.Code != "")) {
			out = store.CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: "child_outcome_invalid"}
		}
	}
	finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finishAttempted = true
	if _, e = db.FinishCandidateMaintenance(finish, g, id, out); e != nil {
		return out, e
	}
	return out, waitErr
}

// validateWorkerRoot verifies saved authority, including the database inode;
// neither a pathname nor a copied grant can authorize a replacement namespace.
func validateWorkerRoot(g store.CandidateMaintenanceGrant) error {
	bad := errors.New("candidate_maintenance_root_changed")
	for _, x := range []struct {
		path     string
		dev, ino uint64
		dir      bool
	}{{g.Root.StateRoot, g.Root.StateDevice, g.Root.StateInode, true}, {filepath.Join(g.Root.StateRoot, "state.db"), g.Root.DatabaseDevice, g.Root.DatabaseInode, false}} {
		info, e := os.Lstat(x.path)
		if e != nil {
			return bad
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSymlink != 0 || uint64(st.Dev) != x.dev || uint64(st.Ino) != x.ino || st.Uid != uint32(os.Geteuid()) {
			return bad
		}
		if x.dir {
			if !info.IsDir() || info.Mode().Perm() != 0700 {
				return bad
			}
		} else if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Nlink != 1 {
			return bad
		}
	}
	return nil
}
func Worker(ctx context.Context, bootstrap io.Reader) (store.CandidateMaintenanceOutcome, error) {
	var g store.CandidateMaintenanceGrant
	raw, e := io.ReadAll(io.LimitReader(bootstrap, 24577))
	if e != nil || len(raw) > 24576 || contract.DecodeBridgeStrict(raw, &g) != nil || g.Validate() != nil {
		return store.CandidateMaintenanceOutcome{}, errors.New("candidate_maintenance_grant_invalid")
	}
	pending := func(code string, e error) (store.CandidateMaintenanceOutcome, error) {
		return store.CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: code}, e
	}
	executable, e := os.Executable()
	if e != nil {
		return pending("executable_unverified", e)
	}
	executable, e = filepath.EvalSymlinks(executable)
	if e != nil || executable != g.Executable {
		return pending("executable_unverified", errors.New("candidate_maintenance_executable_changed"))
	}
	sha, e := process.ExecutableDigest(ctx, executable)
	if e != nil || sha != g.ExecutableSHA256 {
		return pending("executable_unverified", errors.New("candidate_maintenance_executable_changed"))
	}
	if e = validateWorkerRoot(g); e != nil {
		return pending("store_root_changed", e)
	}
	receipt, e := gitopsworker.OpenSealedCandidateResource(ctx, g.SpoolRoot, g.Header)
	if e != nil {
		return pending("sealed_open_failed", e)
	}
	j, e := gitopsworker.OpenExistingMaintenanceJournal(g.OperationJournal, g.JournalIdentity)
	if e != nil {
		return pending("journal_open_failed", e)
	}
	defer j.Close()
	if e = j.MaintenanceProcessQuiescent(ctx); e != nil {
		return pending("previous_git_process_unknown", e)
	}
	if e = validateWorkerRoot(g); e != nil {
		return pending("store_root_changed", e)
	}
	out, e := gitops.Cleanup(gitops.WithCandidateJournal(ctx, j), gitops.CleanupRequest{Root: receipt.Worktree, CandidateReceipt: &receipt, Journal: j})
	if e != nil {
		return store.CandidateMaintenanceOutcome{Version: 1, State: "pending", Code: "cleanup_failed", Cleanup: &out}, e
	}
	if out.State != gitops.CleanupRemoved {
		return pending("cleanup_not_removed", errors.New("candidate_maintenance_cleanup_incomplete"))
	}
	if e = validateWorkerRoot(g); e != nil {
		return pending("store_root_changed", e)
	}
	return store.CandidateMaintenanceOutcome{Version: 1, State: "removed", Cleanup: &out}, nil
}
