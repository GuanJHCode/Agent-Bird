package store

import (
	"context"
	"crypto/subtle"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// ValidateHostHello authenticates a launch and returns its persisted owner without
// changing registration or reconciliation state. RegisterHost rechecks this binding.
func (d *DB) ValidateHostHello(ctx context.Context, hello contract.HostHello) (RunOwnerBinding, error) {
	var owner RunOwnerBinding
	if hello.LaunchID == "" || hello.LaunchToken == "" || hello.OriginContextID == "" || hello.OriginPID <= 0 || hello.OriginBirth == "" || hello.HostGeneration == "" || hello.PID <= 0 || hello.Birth == "" || hello.Executable == "" {
		return owner, ErrHostRejected
	}
	var tokenHash, generation, executable string
	err := d.sql.QueryRowContext(ctx, `SELECT h.token_hash,h.host_generation,h.executable,r.origin_context_id,r.origin_pid,r.origin_birth FROM host_launches h JOIN task_runtime tr ON tr.host_launch_id=h.id JOIN tasks t ON t.id=tr.task_id JOIN runs r ON r.id=t.run_id WHERE h.id=? AND h.origin_context_id=r.origin_context_id LIMIT 1`, hello.LaunchID).Scan(&tokenHash, &generation, &executable, &owner.OriginContextID, &owner.OriginPID, &owner.OriginBirth)
	if err != nil || subtle.ConstantTimeCompare([]byte(tokenHash), []byte(runtimeHash(hello.LaunchToken))) != 1 || generation != hello.HostGeneration || executable != hello.Executable || owner.OriginContextID != hello.OriginContextID || owner.OriginPID != hello.OriginPID || owner.OriginBirth != hello.OriginBirth {
		return RunOwnerBinding{}, ErrHostRejected
	}
	return owner, nil
}
