package store

import (
	"context"
	"database/sql"
)

// RunLaunchID returns the one launch serving a run.  A run with an ambiguous
// task projection cannot be published because the bootstrap target is not
// authoritative.
func (d *DB) RunLaunchID(ctx context.Context, runID string) (string, error) {
	var launch string
	err := d.sql.QueryRowContext(ctx, `SELECT host_launch_id FROM task_runtime r JOIN tasks t ON t.id=r.task_id WHERE t.run_id=? GROUP BY host_launch_id HAVING COUNT(*)>0 AND (SELECT COUNT(DISTINCT r2.host_launch_id) FROM task_runtime r2 JOIN tasks t2 ON t2.id=r2.task_id WHERE t2.run_id=?)=1`, runID, runID).Scan(&launch)
	if err == sql.ErrNoRows {
		return "", ErrConflict
	}
	return launch, err
}
