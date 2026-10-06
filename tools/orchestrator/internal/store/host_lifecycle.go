package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/contract"
)

// ReleasedHostIdentity is an additional lifecycle fence after HostHello and
// OS authentication. It does not authorize a new PID or change a binding.
func (d *DB) ReleasedHostIdentity(ctx context.Context, hello contract.HostHello) (bool, error) {
	var status string
	err := d.sql.QueryRowContext(ctx, `SELECT l.status FROM host_launches l JOIN runtime_hosts h ON h.launch_id=l.id WHERE l.id=? AND h.pid=? AND h.birth=?`, hello.LaunchID, hello.PID, hello.Birth).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return status == "released", err
}

// HostLifecycleIdentities snapshots already registered identities for a
// durable coordinator shutdown fence. Tokens are deliberately not selected.
func (d *DB) HostLifecycleIdentities(ctx context.Context) ([]contract.HostHello, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT DISTINCT h.launch_id,h.origin_context_id,r.origin_pid,r.origin_birth,h.host_generation,h.pid,h.birth,h.executable FROM runtime_hosts h JOIN task_runtime tr ON tr.host_launch_id=h.launch_id JOIN tasks t ON t.id=tr.task_id JOIN runs r ON r.id=t.run_id ORDER BY h.launch_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var identities []contract.HostHello
	for rows.Next() {
		var hello contract.HostHello
		if err := rows.Scan(&hello.LaunchID, &hello.OriginContextID, &hello.OriginPID, &hello.OriginBirth, &hello.HostGeneration, &hello.PID, &hello.Birth, &hello.Executable); err != nil {
			return nil, err
		}
		identities = append(identities, hello)
	}
	return identities, rows.Err()
}
