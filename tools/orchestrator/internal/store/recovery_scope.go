package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

type HostPresenceSnapshot struct {
	Status, Executable, ExecutionOwnerMode string
	PID                                    int
	Birth                                  string
	Registered                             bool
	OwnerPhase, SuccessorCommandID         string
}

// InspectHostPresence authenticates a launch; an OS observation is still
// required before its caller can treat a stored PID as a live or dead Host.
func (d *DB) InspectHostPresence(ctx context.Context, launchID, token string) (HostPresenceSnapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	owned, err := d.beginInitialOwnerTransaction(ctx, "launch", launchID, false)
	if err != nil {
		return HostPresenceSnapshot{}, err
	}
	defer owned.close()
	var r HostPresenceSnapshot
	var hash string
	var pid sql.NullInt64
	var birth sql.NullString
	err = owned.tx.QueryRowContext(ctx, `SELECT l.token_hash,l.status,l.executable,l.execution_owner_mode,h.pid,h.birth FROM host_launches l LEFT JOIN runtime_hosts h ON h.launch_id=l.id WHERE l.id=?`, launchID).Scan(&hash, &r.Status, &r.Executable, &r.ExecutionOwnerMode, &pid, &birth)
	if err != nil || token == "" || subtle.ConstantTimeCompare([]byte(hash), []byte(runtimeHash(token))) != 1 {
		return HostPresenceSnapshot{}, ErrHostRejected
	}
	if pid.Valid != birth.Valid || (pid.Valid && (pid.Int64 <= 1 || birth.String == "")) {
		return HostPresenceSnapshot{}, CodeError("host_recovery_identity_unknown")
	}
	r.Registered, r.PID, r.Birth = pid.Valid, int(pid.Int64), birth.String
	if r.ExecutionOwnerMode == "supervised-v1" {
		r.OwnerPhase, r.SuccessorCommandID, err = ownerSuccessorPresenceTx(ctx, owned.tx, launchID)
		if err != nil {
			return HostPresenceSnapshot{}, err
		}
	}
	if err = owned.finishInitialRead(); err != nil {
		return HostPresenceSnapshot{}, err
	}
	return r, nil
}

type RecoveryScopeProof struct {
	ScopeSHA256 string
	OldPID      int
	OldBirth    string
}

// AuthorizeRecoveryScope is read-only. The coordinator must authenticate the
// actual Host peer, prove this old Host exited, and recheck lifecycle fences.
// Only committed, unique same-run links can carry the retained v1 scope to
// the current owner; no missing row or legacy empty old value is repaired.
func (d *DB) AuthorizeRecoveryScope(ctx context.Context, hello contract.HostHello, priorScope, executableSHA string) (RecoveryScopeProof, error) {
	if !validDigest(priorScope) || !validDigest(executableSHA) {
		return RecoveryScopeProof{}, CodeError("host_recovery_scope_unbound")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return RecoveryScopeProof{}, err
	}
	defer tx.Rollback()
	var run, tokenHash, origin, generation, executable, originBirth, status, hostID string
	var originPID int
	var proof RecoveryScopeProof
	err = tx.QueryRowContext(ctx, `SELECT r.id,l.token_hash,l.origin_context_id,l.host_generation,l.executable,r.origin_pid,r.origin_birth,l.status,h.id,h.pid,h.birth FROM host_launches l JOIN runtime_hosts h ON h.launch_id=l.id JOIN task_runtime tr ON tr.host_launch_id=l.id JOIN tasks t ON t.id=tr.task_id JOIN runs r ON r.id=t.run_id WHERE l.id=? LIMIT 1`, hello.LaunchID).Scan(&run, &tokenHash, &origin, &generation, &executable, &originPID, &originBirth, &status, &hostID, &proof.OldPID, &proof.OldBirth)
	if err != nil || hello.LaunchToken == "" || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(runtimeHash(hello.LaunchToken))) != 1 || origin != hello.OriginContextID || generation == "" || generation != hello.HostGeneration || executable != hello.Executable || originPID != hello.OriginPID || originBirth != hello.OriginBirth || status != "offline" || proof.OldPID <= 1 || proof.OldBirth == "" {
		return RecoveryScopeProof{}, ErrHostRejected
	}
	var unsafe int
	err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM segment_runtime s JOIN attempts a ON a.id=s.attempt_id JOIN tasks t ON t.id=a.task_id WHERE t.run_id=? AND s.status NOT IN ('exited','not_started')) + (SELECT COUNT(*) FROM stop_runtime WHERE host_id=? AND status='pending')`, run, hostID).Scan(&unsafe)
	if err != nil {
		return RecoveryScopeProof{}, err
	}
	if unsafe != 0 {
		return RecoveryScopeProof{}, CodeError("host_recovery_not_quiescent")
	}
	proof.ScopeSHA256 = contract.HostRecoveryScopeSHA256(hello, executableSHA)
	if priorScope == proof.ScopeSHA256 {
		return RecoveryScopeProof{}, CodeError("host_recovery_scope_unbound")
	}
	seen := map[string]bool{generation: true}
	matched := false
	for hop := 0; ; hop++ {
		var previous, birth string
		var pid int
		err = tx.QueryRowContext(ctx, `SELECT old_host_generation,origin_pid,origin_birth FROM owner_rebinds WHERE run_id=? AND host_generation=?`, run, generation).Scan(&previous, &pid, &birth)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if matched {
					return proof, nil
				}
				return RecoveryScopeProof{}, CodeError("host_recovery_scope_unbound")
			}
			return RecoveryScopeProof{}, err
		}
		if hop >= 64 {
			return RecoveryScopeProof{}, CodeError("host_recovery_scope_chain_limit")
		}
		if previous == "" || seen[previous] || (hop == 0 && (pid != hello.OriginPID || birth != hello.OriginBirth)) {
			return RecoveryScopeProof{}, CodeError("host_recovery_scope_unbound")
		}
		oldHello := hello
		oldHello.HostGeneration = previous
		if contract.HostRecoveryScopeSHA256(oldHello, executableSHA) == priorScope {
			matched = true
		}
		seen[previous], generation = true, previous
	}
}
