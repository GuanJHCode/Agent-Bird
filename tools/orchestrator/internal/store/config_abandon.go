package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
)

type configAbandonQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func authenticateConfigAbandonCurrent(ctx context.Context, q configAbandonQuery, current OwnerGrant) (configOwnerEvidence, error) {
	var hash, controller, birth string
	var pid int
	e := q.QueryRowContext(ctx, `SELECT token_hash,controller_thread,origin_pid,origin_birth FROM owner_grants WHERE id=?`, current.ID).Scan(&hash, &controller, &pid, &birth)
	if e != nil || current.Version != 1 || current.Kind != "local-owner" || current.OriginContextID != current.ID || current.HostGeneration != current.ID || current.ControllerThread != controller || current.OriginPID != pid || current.OriginBirth != birth || subtle.ConstantTimeCompare([]byte(hash), []byte(runtimeHash(current.Token))) != 1 {
		return configOwnerEvidence{}, CodeError("owner_capability_mismatch")
	}
	return configOwnerEvidence{controller: controller, pid: pid, birth: birth}, nil
}

func (d *DB) authenticateConfigAbandon(ctx context.Context, q configAbandonQuery, current OwnerGrant, u install.ConfigPreparationUse) error {
	now, e := authenticateConfigAbandonCurrent(ctx, q, current)
	if e != nil {
		return e
	}
	controller, pid, birth := now.controller, now.pid, now.birth
	if _, e = install.ConfigPreparationUseBytes(u); e != nil {
		return e
	}
	if e = d.checkConfigStore(*u.Authority); e != nil {
		return e
	}
	original, e := configGrant(ctx, q, u.Authority.OwnerGrantID)
	if e != nil {
		return e
	}
	scope, e := contract.ConfigGenerationOwnerScope(original.controller, original.birth)
	if e != nil || scope != u.OwnerScopeSHA256 || original.controller != controller || original.pid != pid || original.birth != birth {
		return install.ErrConfigPreparationStoreMismatch
	}
	return nil
}
func readConfigAbandonFence(ctx context.Context, q configAbandonQuery, run string) (*install.ConfigAbandonFence, error) {
	var raw, useSHA, created, receipt string
	e := q.QueryRowContext(ctx, `SELECT use_json,use_sha256,created_at,receipt_sha256 FROM config_preparation_abandons WHERE run_id=?`, run).Scan(&raw, &useSHA, &created, &receipt)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var u install.ConfigPreparationUse
	if len(raw) > contract.ConfigGenerationRecordLimit || contract.DecodeBridgeStrict([]byte(raw), &u) != nil {
		return nil, install.ErrConfigAbandonConflict
	}
	body, e := install.ConfigPreparationUseBytes(u)
	if e != nil || string(body) != raw || u.RunID != run {
		return nil, install.ErrConfigAbandonConflict
	}
	f := install.ConfigAbandonFence{SchemaVersion: 1, Use: u, UseSHA256: useSHA, CreatedAt: created, ReceiptSHA256: receipt}
	return &f, f.Validate()
}

// This bounded read authenticates an existing receipt; it is not an absence
// proof or a new permission to unlink filesystem references.
func (d *DB) ConfigAbandonFence(ctx context.Context, current OwnerGrant, run string) (*install.ConfigAbandonFence, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, e := d.sql.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	f, e := readConfigAbandonFence(ctx, tx, run)
	if e != nil {
		return nil, e
	}
	if f != nil {
		if e = d.authenticateConfigAbandon(ctx, tx, current, f.Use); e != nil {
			return nil, e
		}
	} else {
		if _, e = authenticateConfigAbandonCurrent(ctx, tx, current); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return f, nil
}

// The install-created guard remains held through this short transaction. No FS
// cleanup occurs inside it; caller then writes terminal and complete under flock.
func (d *DB) CommitConfigAbandon(ctx context.Context, current OwnerGrant, guard *install.ConfigAbandonGuard) (out install.ConfigAbandonFence, err error) {
	err = guard.WithOriginalUse(func(u install.ConfigPreparationUse) error {
		d.mu.Lock()
		defer d.mu.Unlock()
		owned, e := d.beginOwnerTransaction(ctx)
		if e != nil {
			return e
		}
		defer owned.close()
		tx := owned.tx
		if e = d.authenticateConfigAbandon(ctx, tx, current, u); e != nil {
			return e
		}
		original, e := readConfigAbandonFence(ctx, tx, u.RunID)
		if e != nil {
			return e
		}
		if original != nil {
			if !reflect.DeepEqual(original.Use, u) {
				return install.ErrConfigAbandonConflict
			}
			out = *original
			return owned.commit()
		}
		var exists int
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=?)`, u.RunID).Scan(&exists); e != nil {
			return e
		}
		if exists != 0 {
			return CodeError("config_abandon_already_admitted")
		}
		for _, id := range u.TaskIDs {
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=?)`, id).Scan(&exists); e != nil {
				return e
			}
			if exists != 0 {
				return CodeError("config_abandon_already_admitted")
			}
		}
		out, e = install.NewConfigAbandonFence(u, time.Now().UTC().Format(time.RFC3339Nano))
		if e != nil {
			return e
		}
		raw, e := install.ConfigPreparationUseBytes(u)
		if e != nil {
			return e
		}
		if e = d.storageAdmission(ctx, tx, u.RunID, configAbandonBytes(u.RunID, string(raw), out.UseSHA256, out.CreatedAt, out.ReceiptSHA256), false); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO config_preparation_abandons(run_id,use_json,use_sha256,created_at,receipt_sha256) VALUES(?,?,?,?,?)`, u.RunID, string(raw), out.UseSHA256, out.CreatedAt, out.ReceiptSHA256); e != nil {
			return e
		}
		if e = d.checkConfigStore(*u.Authority); e != nil {
			return e
		}
		return owned.commit()
	})
	return out, err
}

func configAbandonBytes(run, raw, sha, created, receipt string) int64 {
	return 16 + int64(len(run)+len(raw)+len(sha)+len(created)+len(receipt))
}
