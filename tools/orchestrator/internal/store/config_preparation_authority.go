package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/adapter"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/install"
)

// Captured at Open under the caller's existing instance ownership. No extra FD
// is retained. SQLite owns its original connection; mutable content/timestamps
// are deliberately excluded. This is not a database-copy or rollback authority.
func configStoreNodes(path string) (contract.ConfigPreparationAuthority, error) {
	var a contract.ConfigPreparationAuthority
	root, e := filepath.EvalSymlinks(filepath.Dir(path))
	if e != nil {
		return a, install.ErrConfigPreparationStoreMismatch
	}
	for i, p := range []string{root, filepath.Join(root, filepath.Base(path))} {
		info, e := os.Lstat(p)
		if e != nil || !storeOwned(info) || info.Mode()&os.ModeSymlink != 0 || (i == 0 && (!info.IsDir() || info.Mode().Perm() != 0700)) || (i == 1 && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600)) {
			return a, install.ErrConfigPreparationStoreMismatch
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || i == 1 && st.Nlink != 1 {
			return a, install.ErrConfigPreparationStoreMismatch
		}
		if i == 0 {
			a.StateDevice = uint64(st.Dev)
			a.StateInode = st.Ino
		} else {
			a.DatabaseDevice = uint64(st.Dev)
			a.DatabaseInode = st.Ino
		}
	}
	a.SchemaVersion = 1
	a.StateRoot = root
	return a, nil
}
func (d *DB) checkConfigStore(a contract.ConfigPreparationAuthority) error {
	if a.Validate() != nil || d.configIdentityError != nil {
		return install.ErrConfigPreparationStoreMismatch
	}
	current, e := configStoreNodes(d.path)
	if e != nil {
		return e
	}
	original := d.configIdentity
	original.OwnerGrantID = a.OwnerGrantID
	current.OwnerGrantID = a.OwnerGrantID
	if original != a || current != a {
		return install.ErrConfigPreparationStoreMismatch
	}
	return nil
}

type configOwnerEvidence struct {
	controller string
	pid        int
	birth      string
}

func configGrant(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (configOwnerEvidence, error) {
	var o configOwnerEvidence
	e := q.QueryRowContext(ctx, `SELECT controller_thread,origin_pid,origin_birth FROM owner_grants WHERE id=?`, id).Scan(&o.controller, &o.pid, &o.birth)
	if e != nil || o.controller == "" || o.pid <= 0 || o.birth == "" {
		return o, install.ErrConfigPreparationStoreMismatch
	}
	return o, nil
}

// Current owner is trusted output of localOwner; full persisted token validation
// is repeated here. Historical first grant is immutable and need not be current.
func (d *DB) ConfigPreparationAuthority(ctx context.Context, current OwnerGrant, original *contract.ConfigPreparationAuthority) (contract.ConfigPreparationAuthority, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var a contract.ConfigPreparationAuthority
	if e := d.ValidateOwnerGrant(ctx, current); e != nil {
		return a, e
	}
	a = d.configIdentity
	a.OwnerGrantID = current.ID
	if original != nil {
		a = *original
	}
	if e := d.checkConfigStore(a); e != nil {
		return a, e
	}
	o, e := configGrant(ctx, d.sql, a.OwnerGrantID)
	if e != nil {
		return a, e
	}
	if o.controller != current.ControllerThread || o.pid != current.OriginPID || o.birth != current.OriginBirth {
		return a, install.ErrConfigPreparationStoreMismatch
	}
	return a, nil
}

func configPlanTasks(p PlanSpec) ([]install.ConfigGenerationTaskUse, bool, error) {
	out := make([]install.ConfigGenerationTaskUse, len(p.Tasks))
	paired := false
	legacy := false
	for i, t := range p.Tasks {
		out[i] = install.ConfigGenerationTaskUse{Diagnostics: t.Diagnostics, ExecutionPolicy: t.ExecutionPolicy, TaskID: t.ID, AdapterPayload: t.AdapterPayload, Fallbacks: t.FallbackPayloads, PlanRevision: p.Run.PlanRevision, Dependencies: t.Dependencies, MaxAttempts: t.MaxAttempts, WorkRevision: t.WorkRevision, BudgetGroupID: t.BudgetGroupID, MaxActiveMS: t.MaxActiveMS, CompletionPolicy: t.CompletionPolicy, ExpectedArtifactSHA256: t.ExpectedArtifactSHA256}
		for _, raw := range append([]json.RawMessage{t.AdapterPayload}, t.FallbackPayloads...) {
			if len(raw) == 0 {
				legacy = true
				continue
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil {
				return nil, false, CodeError("invalid_adapter_payload")
			}
			has := false
			for key := range fields {
				has = has || strings.EqualFold(key, "config_generation") || strings.EqualFold(key, "config_model_source")
			}
			if has {
				v, e := adapter.DecodeInvocationPayload(raw)
				if e != nil || v.ConfigGeneration == nil {
					return nil, false, CodeError("config_generation_invalid")
				}
				paired = true
			} else {
				legacy = true
			}
		}
	}
	if paired && legacy {
		return nil, false, CodeError("config_generation_conflict")
	}
	return out, paired, nil
}
func (d *DB) validateConfigAdmission(ctx context.Context, tx *sql.Tx, p PlanSpec, a contract.ConfigPreparationAuthority) error {
	if e := d.checkConfigStore(a); e != nil {
		return e
	}
	original, e := configGrant(ctx, tx, a.OwnerGrantID)
	if e != nil {
		return e
	}
	current, e := configGrant(ctx, tx, p.Run.OriginContextID)
	if e != nil {
		return e
	}
	if original != current || current.controller != p.Run.ControllerThread || current.pid != p.Run.OriginPID || current.birth != p.Run.OriginBirth || p.Host.OriginContextID != p.Run.OriginContextID || p.Host.HostGeneration != p.Run.OriginContextID {
		return install.ErrConfigPreparationStoreMismatch
	}
	scope, e := contract.ConfigGenerationOwnerScope(original.controller, original.birth)
	if e != nil {
		return e
	}
	for _, t := range p.Tasks {
		for _, raw := range append([]json.RawMessage{t.AdapterPayload}, t.FallbackPayloads...) {
			v, e := adapter.DecodeInvocationPayload(raw)
			if e != nil || v.ConfigGeneration == nil || v.ConfigGeneration.OwnerScopeSHA256 != scope {
				return install.ErrConfigPreparationStoreMismatch
			}
		}
	}
	return nil
}

// Retains install -> Store mutex/transaction through proof and exact pin removal.
// The durable grant used to Submit may be newer than the preparation grant.
func (d *DB) ReleaseConfigTaskUse(ctx context.Context, executable, run, task string, ref *contract.ConfigGenerationRef) error {
	return install.ReleaseConfigTaskUseWithStore(executable, run, task, ref, func(u install.ConfigPreparationUse, remove func() error) error {
		d.mu.Lock()
		defer d.mu.Unlock()
		tx, e := d.sql.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if u.Authority == nil {
			return install.ErrConfigPreparationStoreUnbound
		}
		if e = d.checkConfigStore(*u.Authority); e != nil {
			return e
		}
		var eligible bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN task_runtime r ON r.task_id=t.id JOIN host_launches h ON h.id=r.host_launch_id WHERE t.id=? AND t.run_id=? AND h.executable=? AND `+releasableTaskPredicate+`)`, task, run, executable).Scan(&eligible); e != nil {
			return e
		}
		if !eligible {
			return ErrConflict
		}
		if e = d.validateConfigTaskUse(ctx, tx, executable, run, task, ref, u); e != nil {
			return e
		}
		if e = remove(); e != nil {
			return e
		}
		return tx.Commit()
	})
}

// Shared by pin retirement and retry retention while the install lock and
// Store transaction are held in that order.
func (d *DB) validateConfigTaskUse(ctx context.Context, tx *sql.Tx, executable, run, task string, ref *contract.ConfigGenerationRef, u install.ConfigPreparationUse) error {
	if u.Authority == nil {
		return install.ErrConfigPreparationStoreUnbound
	}
	if e := d.checkConfigStore(*u.Authority); e != nil {
		return e
	}
	original, e := configGrant(ctx, tx, u.Authority.OwnerGrantID)
	if e != nil {
		return e
	}
	var controller, origin, birth, generation string
	var pid int
	var payload []byte
	e = tx.QueryRowContext(ctx, `SELECT v.controller_thread,v.origin_context_id,v.origin_pid,v.origin_birth,h.host_generation,r.adapter_payload FROM runs v JOIN tasks t ON t.run_id=v.id JOIN task_runtime r ON r.task_id=t.id JOIN host_launches h ON h.id=r.host_launch_id WHERE t.id=? AND v.id=? AND h.executable=?`, task, run, executable).Scan(&controller, &origin, &pid, &birth, &generation, &payload)
	if e != nil {
		return e
	}
	submitted, e := configGrant(ctx, tx, origin)
	if e != nil || submitted != original || controller != original.controller {
		return install.ErrConfigPreparationStoreMismatch
	}
	v, e := adapter.DecodeInvocationPayload(payload)
	if e != nil || v.ConfigGeneration == nil || ref == nil || *v.ConfigGeneration != *ref {
		return install.ErrConfigPreparationStoreMismatch
	}
	scope, e := contract.ConfigGenerationOwnerScope(original.controller, original.birth)
	if e != nil || ref.OwnerScopeSHA256 != scope {
		return install.ErrConfigPreparationStoreMismatch
	}
	// An authenticated RebindOwner preserves the submit origin and writes each
	// old->new generation edge. Validate the complete bounded chain, not ordinals.
	seen := map[string]bool{}
	tail := true
	links := 0
	for generation != origin {
		if seen[generation] || links >= 64 {
			return install.ErrConfigPreparationStoreMismatch
		}
		seen[generation] = true
		links++
		var old, proof, b string
		var p int
		e = tx.QueryRowContext(ctx, `SELECT old_host_generation,origin_pid,origin_birth,attachment_proof_sha256 FROM owner_rebinds WHERE run_id=? AND host_generation=?`, run, generation).Scan(&old, &p, &b, &proof)
		if e != nil || old == "" || !validDigest(proof) || p <= 0 || b == "" || tail && (p != pid || b != birth) {
			return install.ErrConfigPreparationStoreMismatch
		}
		tail = false
		generation = old
	}
	if tail && (pid != original.pid || birth != original.birth) {
		return install.ErrConfigPreparationStoreMismatch
	}
	var extra bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owner_rebinds WHERE run_id=? AND host_generation=?)`, run, origin).Scan(&extra); e != nil {
		return e
	}
	if extra {
		return install.ErrConfigPreparationStoreMismatch
	}
	if e = d.checkConfigStore(*u.Authority); e != nil {
		return e
	}
	return nil
}
