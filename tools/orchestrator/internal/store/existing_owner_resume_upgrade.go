package store

import (
	"context"
	"database/sql"
)

// Only a still-unclaimed queue admitted for this exact predecessor may become
// a succession reservation. The original command and receipt remain immutable.
// A fresh command supplies the independently checked death/lease observations.
func ownerSuccessionQueuedResume(ctx context.Context, tx *sql.Tx, request OriginalOwnerResumeRequest, owner originalOwnerRow) (int64, error) {
	var status string
	var sequence int64
	err := tx.QueryRowContext(ctx, `SELECT t.status,tr.ready_sequence FROM tasks t JOIN task_runtime tr ON tr.task_id=t.id WHERE t.id=?`, request.TaskID).Scan(&status, &sequence)
	if err != nil {
		return 0, err
	}
	if status != "resume_queued" {
		return 0, nil
	}
	var command string
	if err = tx.QueryRowContext(ctx, `SELECT command_id FROM existing_owner_resume_commands WHERE task_id=? AND ready_sequence=?`, request.TaskID, sequence).Scan(&command); err != nil {
		return 0, ErrConflict
	}
	r, err := loadExistingOwnerResume(ctx, tx, command)
	if err != nil {
		return 0, err
	}
	s := owner.state.Intent.Scope
	if r.CommandID == request.CommandID || r.RunID != s.RunID || r.LaunchID != s.LaunchID || r.OwnerID != s.OwnerID || r.WorkRevision != request.WorkRevision || r.ControlRevision > owner.state.ControlRevision || r.ReadySequence != sequence {
		return 0, ErrConflict
	}
	count, last, err := ownerResumeSegmentSnapshot(ctx, tx, request.TaskID)
	if err != nil {
		return 0, err
	}
	if count != r.segmentCount || last != r.lastSegmentID {
		return 0, ErrConflict
	}
	if err = resumeAdmissionTx(ctx, tx, request.TaskID, request.WorkRevision, "resume_queued"); err != nil {
		return 0, err
	}
	return sequence, nil
}
