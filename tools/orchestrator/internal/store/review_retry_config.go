package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
)

type retryConfigRequirement struct {
	executable, run string
	tasks           []string
	ref             contract.ConfigGenerationRef
}

func (*retryConfigRequirement) Error() string { return "review_retry_config_lock_required" }

type retryConfigProof struct {
	requirement retryConfigRequirement
	use         install.ConfigPreparationUse
	plan        string
	verify      func() error
}

func (d *DB) QueueRetry(ctx context.Context, spec RetrySpec) (RetryReceipt, error) {
	receipt, err := d.queueRetry(ctx, spec, nil)
	var required *retryConfigRequirement
	if !errors.As(err, &required) {
		return receipt, err
	}
	err = install.WithRetainedConfigTaskUses(required.executable, required.run, required.tasks, &required.ref, func(u install.ConfigPreparationUse, plan string, verify func() error) error {
		var e error
		receipt, e = d.queueRetry(ctx, spec, &retryConfigProof{*required, u, plan, verify})
		return e
	})
	return receipt, err
}

// First pass only reads. The second pass is entered under the installation
// lock and must reproduce the exact target set before any Store writes.
func (d *DB) requireReviewRetryConfig(ctx context.Context, tx *sql.Tx, root string, restored []string, proof *retryConfigProof) error {
	if len(restored) == 0 {
		if proof != nil {
			return ErrConflict
		}
		return nil
	}
	ids := append([]string{root}, restored...)
	sort.Strings(ids)
	var required retryConfigRequirement
	var paired *bool
	for _, id := range ids {
		var raw, exe, run string
		if err := tx.QueryRowContext(ctx, `SELECT r.adapter_payload,h.executable,t.run_id FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN host_launches h ON h.id=r.host_launch_id WHERE t.id=?`, id).Scan(&raw, &exe, &run); err != nil {
			return err
		}
		var p adapter.InvocationPayload
		var err error
		if raw != "" {
			p, err = adapter.DecodeInvocationPayload([]byte(raw))
			if err != nil {
				return err
			}
		}
		has := p.ConfigGeneration != nil
		if paired == nil {
			paired = &has
		} else if *paired != has {
			return install.ErrConfigPreparationStoreMismatch
		}
		if !has {
			continue
		}
		if required.run == "" {
			required.executable, required.run, required.ref = exe, run, *p.ConfigGeneration
		}
		if exe != required.executable || run != required.run || *p.ConfigGeneration != required.ref {
			return install.ErrConfigPreparationStoreMismatch
		}
		required.tasks = append(required.tasks, id)
	}
	if len(required.tasks) == 0 {
		if proof != nil {
			return ErrConflict
		}
		return nil
	}
	if proof == nil {
		return &required
	}
	if !reflect.DeepEqual(required, proof.requirement) || !validDigest(proof.plan) || proof.verify == nil {
		return ErrConflict
	}
	for _, id := range required.tasks {
		if err := d.validateConfigTaskUse(ctx, tx, required.executable, required.run, id, &required.ref, proof.use); err != nil {
			return err
		}
	}
	return proof.verify()
}
