package store

import (
	"context"
	"database/sql"
	"errors"
)

const ownerSuccessionColumns = `authorization_id,command_id,request_hash,run_id,task_id,work_revision,launch_id,predecessor_owner_id,predecessor_incarnation,predecessor_control_revision,predecessor_scope_hash,owner_pid,owner_birth,owner_exit_proof,owner_exited_at,host_pid,host_birth,host_exit_proof,host_exited_at,successor_origin_context_id,successor_origin_pid,successor_origin_birth,successor_host_generation,successor_executable,successor_executable_sha256,coordinator_epoch,ready_sequence,created_at`

func loadOwnerSuccessionCommand(ctx context.Context, tx *sql.Tx, command string) (OriginalOwnerSuccessionAuthorization, error) {
	var a OriginalOwnerSuccessionAuthorization
	var ownerProof, hostProof string
	err := tx.QueryRowContext(ctx, `SELECT `+ownerSuccessionColumns+` FROM owner_succession_authorizations WHERE command_id=?`, command).Scan(&a.AuthorizationID, &a.CommandID, &a.RequestSHA256, &a.RunID, &a.TaskID, &a.WorkRevision, &a.LaunchID, &a.PredecessorOwnerID, &a.PredecessorIncarnation, &a.PredecessorControlRevision, &a.PredecessorScopeSHA256, &a.Owner.PID, &a.Owner.Birth, &ownerProof, &a.OwnerExitedAt, &a.Host.PID, &a.Host.Birth, &hostProof, &a.HostExitedAt, &a.SuccessorOrigin.OriginContextID, &a.SuccessorOrigin.OriginPID, &a.SuccessorOrigin.OriginBirth, &a.SuccessorHostGeneration, &a.SuccessorExecutable, &a.SuccessorExecutableSHA256, &a.CoordinatorEpoch, &a.ReadySequence, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if ownerProof != "identity_exited_v1" || hostProof != "identity_exited_v1" {
		return a, originalOwnerRejected()
	}
	return a, nil
}

func insertOwnerSuccessionAuthorization(ctx context.Context, tx *sql.Tx, a OriginalOwnerSuccessionAuthorization) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO owner_succession_authorizations(`+ownerSuccessionColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.AuthorizationID, a.CommandID, a.RequestSHA256, a.RunID, a.TaskID, a.WorkRevision, a.LaunchID, a.PredecessorOwnerID, a.PredecessorIncarnation, a.PredecessorControlRevision, a.PredecessorScopeSHA256, a.Owner.PID, a.Owner.Birth, "identity_exited_v1", a.OwnerExitedAt, a.Host.PID, a.Host.Birth, "identity_exited_v1", a.HostExitedAt, a.SuccessorOrigin.OriginContextID, a.SuccessorOrigin.OriginPID, a.SuccessorOrigin.OriginBirth, a.SuccessorHostGeneration, a.SuccessorExecutable, a.SuccessorExecutableSHA256, a.CoordinatorEpoch, a.ReadySequence, a.CreatedAt)
	return mapConflict(err)
}

func ownerSuccessionAuthorizationBytes(a OriginalOwnerSuccessionAuthorization) int64 {
	values := []string{a.AuthorizationID, a.CommandID, a.RequestSHA256, a.RunID, a.TaskID, a.LaunchID, a.PredecessorOwnerID, a.PredecessorScopeSHA256, a.Owner.Birth, "identity_exited_v1", a.OwnerExitedAt, a.Host.Birth, "identity_exited_v1", a.HostExitedAt, a.SuccessorOrigin.OriginContextID, a.SuccessorOrigin.OriginBirth, a.SuccessorHostGeneration, a.SuccessorExecutable, a.SuccessorExecutableSHA256, a.CreatedAt}
	bytes := int64(64)
	for _, value := range values {
		bytes += int64(len(value))
	}
	return bytes
}
